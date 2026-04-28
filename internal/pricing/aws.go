// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awspricingsdk "github.com/aws/aws-sdk-go-v2/service/pricing"
	awspricingtypes "github.com/aws/aws-sdk-go-v2/service/pricing/types"
)

// AWS Pricing API — uses the official aws-sdk-go-v2/service/pricing SDK.
// The Pricing API requires AWS credentials (IAM user, role, env vars,
// or ~/.aws/credentials). Without credentials, Fetch returns an error.
//
// GetProducts is used to page all products for a given service code and
// region. Results are cached to disk for 7 days (awsBulkCacheTTL) in
// the same awsBulkPayload format used by the in-memory walkers, so all
// existing fetchEC2/fetchEBS/findCheapestPriceUSD logic stays unchanged.
//
// SKU resolution: we walk products[] looking for a product whose
// attributes match (instanceType=<sku>, location=<long region>,
// operatingSystem=Linux, tenancy=Shared, capacityStatus=Used,
// preInstalledSw=NA). We then take terms.OnDemand.<offerTerm>
// .priceDimensions.<rateCode>.pricePerUnit.USD as USD/hour.
//
// HTTP transport: the AWS SDK config is loaded with config.WithHTTPClient
// (&http.Client{}) — a nil-Transport client that inherits
// http.DefaultTransport at request time, keeping the airgap shim effective.
const (
	awsBulkCacheTTL = 7 * 24 * time.Hour
)

type awsFetcher struct {
	mu sync.Mutex
}

func init() {
	Register("aws", &awsFetcher{})
}

// awsRegionLongName maps short region codes to the human-readable
// "location" attribute the bulk JSON uses to filter products.
// Bulk JSON identifies a product by its long location name, not
// its API region code, so we have to translate.
var awsRegionLongName = map[string]string{
	"us-east-1":      "US East (N. Virginia)",
	"us-east-2":      "US East (Ohio)",
	"us-west-1":      "US West (N. California)",
	"us-west-2":      "US West (Oregon)",
	"ca-central-1":   "Canada (Central)",
	"eu-west-1":      "EU (Ireland)",
	"eu-west-2":      "EU (London)",
	"eu-west-3":      "EU (Paris)",
	"eu-central-1":   "EU (Frankfurt)",
	"eu-north-1":     "EU (Stockholm)",
	"eu-south-1":     "EU (Milan)",
	"ap-northeast-1": "Asia Pacific (Tokyo)",
	"ap-northeast-2": "Asia Pacific (Seoul)",
	"ap-southeast-1": "Asia Pacific (Singapore)",
	"ap-southeast-2": "Asia Pacific (Sydney)",
	"ap-south-1":     "Asia Pacific (Mumbai)",
	"sa-east-1":      "South America (Sao Paulo)",
}

type awsBulkProduct struct {
	SKU           string            `json:"sku"`
	ProductFamily string            `json:"productFamily"`
	Attributes    map[string]string `json:"attributes"`
}

type awsPriceDim struct {
	Unit         string            `json:"unit"`
	PricePerUnit map[string]string `json:"pricePerUnit"`
	Description  string            `json:"description"`
}

type awsOnDemandTerm struct {
	PriceDimensions map[string]awsPriceDim `json:"priceDimensions"`
}

type awsBulkPayload struct {
	Products map[string]awsBulkProduct                         `json:"products"`
	Terms    map[string]map[string]map[string]awsOnDemandTerm `json:"terms"`
	// Terms structure: terms[OnDemand][sku][offerCode] = term
}

// awsPriceListItem is the per-item shape returned by GetProducts.PriceList.
// Each string in PriceList is a JSON object with this structure.
type awsPriceListItem struct {
	Product struct {
		SKU           string            `json:"sku"`
		ProductFamily string            `json:"productFamily"`
		Attributes    map[string]string `json:"attributes"`
	} `json:"product"`
	Terms struct {
		OnDemand map[string]struct {
			PriceDimensions map[string]awsPriceDim `json:"priceDimensions"`
		} `json:"OnDemand"`
	} `json:"terms"`
}

func awsBulkCachePath(service, region string) string {
	d := cacheDir()
	_ = os.MkdirAll(d, 0o755)
	return filepath.Join(d, fmt.Sprintf("aws-bulk-%s-%s.json", service, region))
}

// newAWSPricingClient creates a Pricing API client.
// If explicit key+secret have been set via pricing.SetCredentials those are
// used as a static credential provider. Otherwise the SDK's default
// credential chain is tried (AWS_ACCESS_KEY_ID env vars, ~/.aws/credentials,
// IAM instance profile, etc.). The AWS Pricing API returns the same public
// catalog regardless of which account authenticates, so ambient credentials
// are safe to use here — there is no "wrong account" risk for a read-only
// price query.
// The Pricing API endpoint is always us-east-1.
func newAWSPricingClient(ctx context.Context) (*awspricingsdk.Client, error) {
	keyID := creds.AWSAccessKeyID
	secret := creds.AWSSecretAccessKey
	opts := []func(*config.LoadOptions) error{
		config.WithRegion("us-east-1"),
		config.WithHTTPClient(&http.Client{}),
	}
	if keyID != "" && secret != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(keyID, secret, ""),
		))
	}
	// If no explicit credentials, LoadDefaultConfig uses the standard chain
	// (env → shared-credentials-file → IAM role). If that chain has nothing,
	// the first real API call will fail and we surface the error then.
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: aws config: %v", ErrUnavailable, err)
	}
	return awspricingsdk.NewFromConfig(cfg), nil
}

// awsFetchAndBuildPayload calls GetProducts for the given service and
// optional region filter, pages through all results, and builds an
// awsBulkPayload from the per-item JSON strings. The result is written
// to disk (7-day TTL cache). Service-specific filters (e.g. location=)
// can be supplied via extraFilters.
func awsFetchAndBuildPayload(ctx context.Context, client *awspricingsdk.Client, serviceCode, region string, extraFilters []awspricingtypes.Filter) (*awsBulkPayload, error) {
	pl := &awsBulkPayload{
		Products: make(map[string]awsBulkProduct),
		Terms:    make(map[string]map[string]map[string]awsOnDemandTerm),
	}
	pl.Terms["OnDemand"] = make(map[string]map[string]awsOnDemandTerm)

	paginator := awspricingsdk.NewGetProductsPaginator(client, &awspricingsdk.GetProductsInput{
		ServiceCode:   &serviceCode,
		Filters:       extraFilters,
		FormatVersion: strPtr("aws_v1"),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("aws pricing %s: %w", serviceCode, err)
		}
		for _, item := range page.PriceList {
			var pli awsPriceListItem
			if err := json.Unmarshal([]byte(item), &pli); err != nil {
				continue
			}
			sku := pli.Product.SKU
			if sku == "" {
				continue
			}
			pl.Products[sku] = awsBulkProduct{
				SKU:           sku,
				ProductFamily: pli.Product.ProductFamily,
				Attributes:    pli.Product.Attributes,
			}
			if len(pli.Terms.OnDemand) > 0 {
				terms := make(map[string]awsOnDemandTerm)
				for k, t := range pli.Terms.OnDemand {
					terms[k] = awsOnDemandTerm{PriceDimensions: t.PriceDimensions}
				}
				pl.Terms["OnDemand"][sku] = terms
			}
		}
	}
	// Persist to disk for reuse within the 7-day TTL window.
	_ = awsWriteBulkCache(awsBulkCachePath(serviceCode, region), pl)
	return pl, nil
}

func strPtr(s string) *string { return &s }

func awsWriteBulkCache(path string, pl *awsBulkPayload) error {
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (a *awsFetcher) loadBulk(service, region string) (*awsBulkPayload, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return awsLoadOrFetch(service, region, nil)
}

// awsLoadOrFetch checks the disk cache, returning cached data if
// fresh (< 7 days old). On a cache miss it calls the AWS Pricing SDK
// to fetch and cache the payload.
func awsLoadOrFetch(service, region string, extraFilters []awspricingtypes.Filter) (*awsBulkPayload, error) {
	cache := awsBulkCachePath(service, region)
	if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < awsBulkCacheTTL {
		raw, err := os.ReadFile(cache)
		if err == nil {
			var pl awsBulkPayload
			if err := json.Unmarshal(raw, &pl); err == nil {
				return &pl, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	client, err := newAWSPricingClient(ctx)
	if err != nil {
		return nil, err
	}
	return awsFetchAndBuildPayload(ctx, client, service, region, extraFilters)
}

// Fetch routes by SKU shape:
//   - "<family>.<size>"      → EC2 Compute Instance (t3.medium, m5.large)
//   - "ebs:<volType>"        → EBS volume per-GB-month (ebs:gp3, ebs:io2)
//   - "ebs:<volType>:iops"   → EBS provisioned IOPS price
//   - "ebs:<volType>:throughput" → EBS provisioned throughput price
func (a *awsFetcher) Fetch(sku, region string) (Item, error) {
	loc, ok := awsRegionLongName[region]
	if !ok {
		return Item{}, fmt.Errorf("aws: unknown region %q (add to awsRegionLongName)", region)
	}
	if strings.HasPrefix(sku, "ebs:") {
		return a.fetchEBS(strings.TrimPrefix(sku, "ebs:"), region, loc)
	}
	return a.fetchEC2(sku, region, loc)
}

func (a *awsFetcher) fetchEC2(instanceType, region, loc string) (Item, error) {
	pl, err := a.loadBulk("AmazonEC2", region)
	if err != nil {
		return Item{}, err
	}
	var matchedSKU string
	for skuID, p := range pl.Products {
		attr := p.Attributes
		if attr["instanceType"] != instanceType {
			continue
		}
		if attr["location"] != loc {
			continue
		}
		if attr["operatingSystem"] != "Linux" {
			continue
		}
		if attr["tenancy"] != "Shared" {
			continue
		}
		if attr["preInstalledSw"] != "NA" {
			continue
		}
		if v, ok := attr["capacitystatus"]; ok && v != "Used" {
			continue
		}
		if v, ok := attr["capacityStatus"]; ok && v != "Used" {
			continue
		}
		matchedSKU = skuID
		break
	}
	if matchedSKU == "" {
		return Item{}, fmt.Errorf("aws ec2: no match for %q in %q (Linux/Shared/NA/Used)", instanceType, region)
	}
	return a.priceFromTerms(pl, matchedSKU, "Hrs")
}

func (a *awsFetcher) fetchEBS(volType, region, loc string) (Item, error) {
	// volType may be "gp3", "gp2", "io1", "io2", "st1", "sc1",
	// or sub-form "gp3:iops", "gp3:throughput".
	parts := strings.SplitN(volType, ":", 2)
	base := parts[0]
	wantSub := ""
	if len(parts) == 2 {
		wantSub = parts[1] // "iops" | "throughput"
	}
	pl, err := a.loadBulk("AmazonEC2", region)
	if err != nil {
		return Item{}, err
	}
	awsVolName := ebsVolumeAPIName(base)
	var matchedSKU string
	for skuID, p := range pl.Products {
		attr := p.Attributes
		if attr["location"] != loc {
			continue
		}
		switch wantSub {
		case "":
			if !strings.EqualFold(attr["volumeApiName"], awsVolName) {
				continue
			}
			if !strings.Contains(strings.ToLower(attr["usagetype"]), "ebs:volumeusage") {
				continue
			}
			matchedSKU = skuID
		case "iops":
			if !strings.EqualFold(attr["volumeApiName"], awsVolName) {
				continue
			}
			if !strings.Contains(strings.ToLower(attr["usagetype"]), "iops") {
				continue
			}
			matchedSKU = skuID
		case "throughput":
			if !strings.EqualFold(attr["volumeApiName"], awsVolName) {
				continue
			}
			if !strings.Contains(strings.ToLower(attr["usagetype"]), "throughput") {
				continue
			}
			matchedSKU = skuID
		}
		if matchedSKU != "" {
			break
		}
	}
	if matchedSKU == "" {
		return Item{}, fmt.Errorf("aws ebs: no match for %q in %q", volType, region)
	}
	// EBS is priced per GB-Month — the result is a USDPerMonth-per-GB,
	// not USDPerHour. Caller multiplies by GB.
	pd, err := onDemandPriceDim(pl, matchedSKU)
	if err != nil {
		return Item{}, err
	}
	priceUSD, err := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
	if err != nil {
		return Item{}, fmt.Errorf("aws ebs: parse: %w", err)
	}
	return Item{
		USDPerHour:  0,
		USDPerMonth: priceUSD,
		FetchedAt:   time.Now(),
	}, nil
}

func ebsVolumeAPIName(base string) string {
	switch strings.ToLower(base) {
	case "gp3":
		return "gp3"
	case "gp2":
		return "gp2"
	case "io1":
		return "io1"
	case "io2":
		return "io2"
	case "st1":
		return "st1"
	case "sc1":
		return "sc1"
	case "standard":
		return "standard"
	default:
		return base
	}
}

func onDemandPriceDim(pl *awsBulkPayload, skuID string) (awsPriceDim, error) {
	od, ok := pl.Terms["OnDemand"]
	if !ok {
		return awsPriceDim{}, fmt.Errorf("aws: no OnDemand terms")
	}
	skuTerms, ok := od[skuID]
	if !ok {
		return awsPriceDim{}, fmt.Errorf("aws: no OnDemand for sku %s", skuID)
	}
	for _, term := range skuTerms {
		for _, dim := range term.PriceDimensions {
			return dim, nil
		}
	}
	return awsPriceDim{}, fmt.Errorf("aws: empty priceDimensions for %s", skuID)
}

func (a *awsFetcher) priceFromTerms(pl *awsBulkPayload, skuID, expectedUnit string) (Item, error) {
	pd, err := onDemandPriceDim(pl, skuID)
	if err != nil {
		return Item{}, err
	}
	priceUSD, err := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
	if err != nil {
		return Item{}, fmt.Errorf("aws: parse price: %w", err)
	}
	if expectedUnit != "" && !strings.EqualFold(pd.Unit, expectedUnit) {
		// Don't reject — different SKUs use Hrs/Hour/Hourly; just record what we got.
		_ = expectedUnit
	}
	return Item{
		USDPerHour:  priceUSD,
		USDPerMonth: priceUSD * MonthlyHours,
		FetchedAt:   time.Now(),
	}, nil
}
