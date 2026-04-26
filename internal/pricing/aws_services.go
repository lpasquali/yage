// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AWS service-specific pricing helpers — all read from the same
// AWS Bulk Pricing JSON catalog as aws.go. Each service gets its
// own offer file:
//
//   AmazonEKS         — EKS control plane $/hour, region-specific
//   AmazonECS         — Fargate $/vCPU-hour and $/GB-hour
//   AmazonVPC         — NAT Gateway hourly + per-GB processed,
//                       Internet egress $/GB
//   AWSELB            — ALB / NLB hourly + LCU/NLCU $/hour
//   AmazonCloudWatch  — CloudWatch Logs ingestion + storage $/GB
//   AmazonRoute53     — Hosted zone $/month (priced under
//                       region "aws-global" in the catalog)
//
// All helpers take the workload region as input. For services that
// price globally (Route53), the helper hits the special
// pricing.us-east-1.amazonaws.com /aws-global/ index.

const awsBulkServicesCacheTTL = 7 * 24 * time.Hour

type awsServiceFetcher struct {
	mu         sync.Mutex
	httpClient *http.Client
}

var awsSvc = &awsServiceFetcher{httpClient: &http.Client{Timeout: 120 * time.Second}}

func awsBulkServiceCachePath(service, region string) string {
	d := cacheDir()
	_ = os.MkdirAll(d, 0o755)
	return fmt.Sprintf("%s/aws-svc-%s-%s.json", d, service, region)
}

// downloadServiceBulk fetches and caches the raw JSON for one
// (service, region) pair. service is the offer code (e.g.
// "AmazonEKS", "AmazonVPC"). region is either an AWS region
// short code or "aws-global" for catalogues priced globally.
func (a *awsServiceFetcher) downloadServiceBulk(service, region string) (string, error) {
	cache := awsBulkServiceCachePath(service, region)
	if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < awsBulkServicesCacheTTL {
		return cache, nil
	}
	url := fmt.Sprintf("%s/offers/v1.0/aws/%s/current/%s/index.json",
		awsPricingHost, service, region)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("aws %s/%s: %w", service, region, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("aws %s/%s: HTTP %d", service, region, resp.StatusCode)
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
	return cache, os.Rename(tmp, cache)
}

func (a *awsServiceFetcher) loadServiceBulk(service, region string) (*awsBulkPayload, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	path, err := a.downloadServiceBulk(service, region)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var pl awsBulkPayload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return nil, err
	}
	return &pl, nil
}

// awsRegionLong returns the human-readable location for a region,
// or the region itself for "aws-global".
func awsRegionLong(region string) string {
	if region == "aws-global" {
		return "Any"
	}
	if v, ok := awsRegionLongName[region]; ok {
		return v
	}
	return region
}

// findCheapestPriceUSD walks products[] looking for any product
// matching all attribute predicates. For each match, returns the
// USD price-per-unit of any OnDemand priceDimension. We pick the
// MINIMUM USD across all matches (some catalogs encode multiple
// commitment offers; the cheapest non-zero is the on-demand baseline).
func findCheapestPriceUSD(pl *awsBulkPayload, predicates map[string]string) (float64, error) {
	bestUSD := 0.0
	matched := false
	for skuID, prod := range pl.Products {
		ok := true
		for k, want := range predicates {
			got, exists := prod.Attributes[k]
			if !exists || !strings.EqualFold(got, want) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		pd, err := onDemandPriceDim(pl, skuID)
		if err != nil {
			continue
		}
		usdStr, ok := pd.PricePerUnit["USD"]
		if !ok {
			continue
		}
		usd, err := strconv.ParseFloat(usdStr, 64)
		if err != nil || usd <= 0 {
			continue
		}
		if !matched || usd < bestUSD {
			bestUSD = usd
			matched = true
		}
	}
	if !matched {
		return 0, fmt.Errorf("no priced match for %v", predicates)
	}
	return bestUSD, nil
}

// findPriceContainsUSD walks products[] matching when usagetype
// contains the given substring (case-insensitive). For services
// where attribute names vary by version this is the more reliable
// path. Returns the cheapest non-zero USD price.
func findPriceContainsUSD(pl *awsBulkPayload, usageContains string) (float64, error) {
	want := strings.ToLower(usageContains)
	bestUSD := 0.0
	matched := false
	for skuID, prod := range pl.Products {
		ut := strings.ToLower(prod.Attributes["usagetype"])
		if !strings.Contains(ut, want) {
			continue
		}
		pd, err := onDemandPriceDim(pl, skuID)
		if err != nil {
			continue
		}
		usd, err := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if err != nil || usd <= 0 {
			continue
		}
		if !matched || usd < bestUSD {
			bestUSD = usd
			matched = true
		}
	}
	if !matched {
		return 0, fmt.Errorf("no priced match for usage %q", usageContains)
	}
	return bestUSD, nil
}

// AWSEKSControlPlaneUSDPerMonth — EKS Standard Tier flat hourly
// fee, converted to monthly. Per-cluster.
func AWSEKSControlPlaneUSDPerMonth(region string) (float64, error) {
	pl, err := awsSvc.loadServiceBulk("AmazonEKS", region)
	if err != nil {
		return 0, err
	}
	loc := awsRegionLong(region)
	// Match: location=<region>, productFamily=Compute, group like Standard.
	usd, err := findCheapestPriceUSD(pl, map[string]string{
		"location":     loc,
		"tier":         "Standard",
	})
	if err != nil {
		// Older catalog may key on different attributes — fall back
		// to the usagetype-substring path.
		usd, err = findPriceContainsUSD(pl, "amazon-eks")
		if err != nil {
			return 0, fmt.Errorf("eks cp price: %w", err)
		}
	}
	return usd * MonthlyHours, nil
}

// AWSFargatePerHour returns Fargate $/vCPU-hour and $/GB-hour
// for a given region. Fargate is priced under the AmazonECS
// offer (Fargate is the launch type, not a separate service).
func AWSFargatePerHour(region string) (vcpuHour, gbHour float64, err error) {
	pl, err := awsSvc.loadServiceBulk("AmazonECS", region)
	if err != nil {
		return 0, 0, err
	}
	loc := awsRegionLong(region)
	// Two SKUs: one for vCPU-hour, one for GB-hour.
	// They differ by usagetype substring.
	for skuID, prod := range pl.Products {
		if !strings.EqualFold(prod.Attributes["location"], loc) {
			continue
		}
		ut := strings.ToLower(prod.Attributes["usagetype"])
		if !strings.Contains(ut, "fargate") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		switch {
		case strings.Contains(ut, "vcpu"):
			if vcpuHour == 0 || usd < vcpuHour {
				vcpuHour = usd
			}
		case strings.Contains(ut, "memory") || strings.Contains(ut, "gb"):
			if gbHour == 0 || usd < gbHour {
				gbHour = usd
			}
		}
	}
	if vcpuHour <= 0 || gbHour <= 0 {
		return 0, 0, fmt.Errorf("fargate: incomplete (vcpu=%v gb=%v) in %s", vcpuHour, gbHour, region)
	}
	return vcpuHour, gbHour, nil
}

// AWSNATGatewayPricing — hourly + per-GB processed.
func AWSNATGatewayPricing(region string) (hourly, gbProcessed float64, err error) {
	pl, err := awsSvc.loadServiceBulk("AmazonVPC", region)
	if err != nil {
		return 0, 0, err
	}
	for skuID, prod := range pl.Products {
		ut := strings.ToLower(prod.Attributes["usagetype"])
		if !strings.Contains(ut, "natgateway") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		switch {
		case strings.Contains(ut, "hours"):
			if hourly == 0 || usd < hourly {
				hourly = usd
			}
		case strings.Contains(ut, "bytes") || strings.Contains(ut, "gb"):
			if gbProcessed == 0 || usd < gbProcessed {
				gbProcessed = usd
			}
		}
	}
	if hourly <= 0 || gbProcessed <= 0 {
		return 0, 0, fmt.Errorf("nat gw: incomplete in %s (hr=%v gb=%v)", region, hourly, gbProcessed)
	}
	return hourly, gbProcessed, nil
}

// AWSApplicationLBPricing — ALB hourly + LCU/hour.
func AWSApplicationLBPricing(region string) (hourly, lcuHour float64, err error) {
	pl, err := awsSvc.loadServiceBulk("AWSELB", region)
	if err != nil {
		return 0, 0, err
	}
	for skuID, prod := range pl.Products {
		ut := strings.ToLower(prod.Attributes["usagetype"])
		grp := strings.ToLower(prod.Attributes["group"])
		// ALB hours: usagetype ends with "LoadBalancerUsage", productFamily "Load Balancer-Application"
		fam := strings.ToLower(prod.Attributes["productFamily"])
		if !strings.Contains(fam, "application") && !strings.Contains(grp, "application") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		switch {
		case strings.Contains(ut, "loadbalancerusage"):
			if hourly == 0 || usd < hourly {
				hourly = usd
			}
		case strings.Contains(ut, "lcu"):
			if lcuHour == 0 || usd < lcuHour {
				lcuHour = usd
			}
		}
	}
	if hourly <= 0 || lcuHour <= 0 {
		return 0, 0, fmt.Errorf("alb: incomplete in %s (hr=%v lcu=%v)", region, hourly, lcuHour)
	}
	return hourly, lcuHour, nil
}

// AWSNetworkLBPricing — NLB hourly + NLCU/hour.
func AWSNetworkLBPricing(region string) (hourly, lcuHour float64, err error) {
	pl, err := awsSvc.loadServiceBulk("AWSELB", region)
	if err != nil {
		return 0, 0, err
	}
	for skuID, prod := range pl.Products {
		ut := strings.ToLower(prod.Attributes["usagetype"])
		fam := strings.ToLower(prod.Attributes["productFamily"])
		grp := strings.ToLower(prod.Attributes["group"])
		if !strings.Contains(fam, "network") && !strings.Contains(grp, "network") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		switch {
		case strings.Contains(ut, "loadbalancerusage"):
			if hourly == 0 || usd < hourly {
				hourly = usd
			}
		case strings.Contains(ut, "lcu"):
			if lcuHour == 0 || usd < lcuHour {
				lcuHour = usd
			}
		}
	}
	if hourly <= 0 || lcuHour <= 0 {
		return 0, 0, fmt.Errorf("nlb: incomplete in %s (hr=%v lcu=%v)", region, hourly, lcuHour)
	}
	return hourly, lcuHour, nil
}

// AWSEgressUSDPerGB — internet egress $/GB above the free tier.
// Priced in AmazonVPC offer.
func AWSEgressUSDPerGB(region string) (float64, error) {
	pl, err := awsSvc.loadServiceBulk("AmazonVPC", region)
	if err != nil {
		// Try the EC2 catalog fallback — egress is sometimes priced
		// there too under DataTransfer-Out-Bytes.
		pl, err = awsSvc.loadServiceBulk("AmazonEC2", region)
		if err != nil {
			return 0, err
		}
	}
	for skuID, prod := range pl.Products {
		ut := strings.ToLower(prod.Attributes["usagetype"])
		if !strings.Contains(ut, "datatransfer-out-bytes") &&
			!strings.Contains(ut, "out-bytes") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		// First-tier above-free price; we take whatever rate the
		// catalog publishes (usually the 0–10TB tier).
		return usd, nil
	}
	return 0, fmt.Errorf("egress: no DataTransfer-Out-Bytes in %s", region)
}

// AWSCloudWatchLogsPricing — ingestion $/GB and storage $/GB-month.
func AWSCloudWatchLogsPricing(region string) (ingest, storage float64, err error) {
	pl, err := awsSvc.loadServiceBulk("AmazonCloudWatch", region)
	if err != nil {
		return 0, 0, err
	}
	for skuID, prod := range pl.Products {
		ut := strings.ToLower(prod.Attributes["usagetype"])
		fam := strings.ToLower(prod.Attributes["productFamily"])
		if !strings.Contains(fam, "logs") && !strings.Contains(ut, "logs") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		switch {
		case strings.Contains(ut, "datascanned") || strings.Contains(ut, "ingestion"):
			if ingest == 0 || usd < ingest {
				ingest = usd
			}
		case strings.Contains(ut, "datastored") || strings.Contains(ut, "archived"):
			if storage == 0 || usd < storage {
				storage = usd
			}
		}
	}
	if ingest <= 0 || storage <= 0 {
		return 0, 0, fmt.Errorf("cwl: incomplete in %s (ingest=%v storage=%v)", region, ingest, storage)
	}
	return ingest, storage, nil
}

// AWSRoute53ZoneUSDPerMonth — hosted zone $/month.
// Route53 is priced globally — the catalog uses region "aws-global".
func AWSRoute53ZoneUSDPerMonth(_ string) (float64, error) {
	pl, err := awsSvc.loadServiceBulk("AmazonRoute53", "aws-global")
	if err != nil {
		return 0, err
	}
	for skuID, prod := range pl.Products {
		ut := strings.ToLower(prod.Attributes["usagetype"])
		fam := strings.ToLower(prod.Attributes["productFamily"])
		if !strings.Contains(fam, "dns zone") && !strings.Contains(ut, "hostedzone") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		// Route53 prices the first 25 zones at one rate, additional
		// at another. Take the cheapest non-zero (first-tier).
		return usd, nil
	}
	return 0, fmt.Errorf("route53: no zone-month price")
}