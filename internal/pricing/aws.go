package pricing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AWS Bulk Pricing JSON — auth-free.
// Files are hosted on the pricing.us-east-1.amazonaws.com bucket
// regardless of the priced region; the path encodes the region:
//
//   https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/<region>/index.json
//
// EBS volume pricing lives INSIDE the AmazonEC2 offer (storage is
// modeled as a productFamily="Storage" attribute on the EC2 catalog,
// not as a separate AmazonEBS service file). One catalog covers
// both compute and storage SKUs for the region.
//
// EC2 regional indexes are large (50–300 MB depending on region),
// so we cache the *raw* JSON to disk and invalidate weekly. The
// per-SKU lookup then reads from disk and parses just enough.
//
// SKU resolution: we walk products[] looking for a product whose
// attributes match (instanceType=<sku>, location=<long region>,
// operatingSystem=Linux, tenancy=Shared, capacityStatus=Used,
// preInstalledSw=NA). We then take terms.OnDemand.<offerTerm>
// .priceDimensions.<rateCode>.pricePerUnit.USD as USD/hour.
const (
	awsPricingHost     = "https://pricing.us-east-1.amazonaws.com"
	awsEC2PathTemplate = "/offers/v1.0/aws/AmazonEC2/current/%s/index.json"
	// The bulk JSON files don't change often. Default catalog
	// cache TTL is 7 days; the per-SKU price cache (parent's
	// readCache/writeCache, 24h) sits on top.
	awsBulkCacheTTL = 7 * 24 * time.Hour
)

type awsFetcher struct {
	mu         sync.Mutex
	httpClient *http.Client
}

func init() {
	Register("aws", &awsFetcher{httpClient: &http.Client{Timeout: 120 * time.Second}})
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
	Products map[string]awsBulkProduct                  `json:"products"`
	Terms    map[string]map[string]map[string]awsOnDemandTerm `json:"terms"`
	// Terms structure: terms[OnDemand][sku][offerCode] = term
}

func awsBulkCachePath(service, region string) string {
	d := cacheDir()
	_ = os.MkdirAll(d, 0o755)
	return filepath.Join(d, fmt.Sprintf("aws-bulk-%s-%s.json", service, region))
}

// downloadBulk fetches and caches the raw bulk JSON for one
// service+region pair.
func (a *awsFetcher) downloadBulk(service, region string) (string, error) {
	cache := awsBulkCachePath(service, region)
	if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < awsBulkCacheTTL {
		return cache, nil
	}
	var pathTpl string
	switch service {
	case "ec2", "ebs":
		// EBS volumes are priced inside the EC2 catalog; both
		// services share the same regional index.
		pathTpl = awsEC2PathTemplate
	default:
		return "", fmt.Errorf("aws: unknown bulk service %q", service)
	}
	url := awsPricingHost + fmt.Sprintf(pathTpl, region)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("aws bulk: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("aws bulk %s/%s: HTTP %d", service, region, resp.StatusCode)
	}
	tmp := cache + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	f.Close()
	if err := os.Rename(tmp, cache); err != nil {
		return "", err
	}
	return cache, nil
}

func (a *awsFetcher) loadBulk(service, region string) (*awsBulkPayload, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	path, err := a.downloadBulk(service, region)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pl awsBulkPayload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return nil, fmt.Errorf("aws bulk parse: %w", err)
	}
	return &pl, nil
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
	pl, err := a.loadBulk("ec2", region)
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
	pl, err := a.loadBulk("ebs", region)
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
