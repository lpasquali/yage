// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

// AWS service-specific pricing helpers — all read from the same
// AWS Pricing API as aws.go. Each service uses its own service code
// (AmazonEKS, AmazonECS, AmazonEC2, AmazonVPC, AWSELB,
// AmazonCloudWatch, AmazonRoute53). Results are cached to disk for
// 7 days (awsBulkCacheTTL, defined in aws.go).
//
// Uses aws-sdk-go-v2/service/pricing (GetProducts) via
// awsLoadOrFetch (defined in aws.go). All helpers share the same
// disk-cache key structure: aws-bulk-<serviceCode>-<region>.json.
//
// All helpers take the workload region as input. Route53 is priced
// globally — its helper ignores the input region and reads the
// top-level (no-region) offer file by passing an empty region.

const awsBulkServicesCacheTTL = awsBulkCacheTTL

// awsBulkProductFamily returns the lowercased product family from
// AWS Price List bulk JSON. Modern catalogs put productFamily on the
// product object; older rows sometimes nested it under
// attributes["productFamily"].
func awsBulkProductFamily(p awsBulkProduct) string {
	f := strings.TrimSpace(p.ProductFamily)
	if f == "" {
		f = p.Attributes["productFamily"]
	}
	return strings.ToLower(f)
}

func awsIsAWSRegionProduct(p awsBulkProduct) bool {
	lt := strings.TrimSpace(p.Attributes["locationType"])
	return lt == "" || lt == "AWS Region"
}

// awsServiceFetcher serializes service-bulk loads (one mutex per
// process, no httpClient field — HTTP is handled by the AWS SDK).
type awsServiceFetcher struct {
	mu sync.Mutex
}

var awsSvc = &awsServiceFetcher{}

func awsBulkServiceCachePath(service, region string) string {
	d := cacheDir()
	_ = os.MkdirAll(d, 0o755)
	tag := region
	if tag == "" {
		tag = "global"
	}
	return fmt.Sprintf("%s/aws-svc-%s-%s.json", d, service, tag)
}

func (a *awsServiceFetcher) loadServiceBulk(service, region string) (*awsBulkPayload, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return awsLoadOrFetch(service, region, nil)
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
		"location": loc,
		"tier":     "Standard",
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
	// NAT Gateway SKUs live under AmazonEC2 (NGW:NatGateway group) in
	// current AWS Price List Bulk API JSON; AmazonVPC no longer lists them.
	pl, err := awsSvc.loadServiceBulk("AmazonEC2", region)
	if err != nil {
		return 0, 0, err
	}
	for skuID, prod := range pl.Products {
		ut := strings.ToLower(prod.Attributes["usagetype"])
		grp := strings.ToLower(prod.Attributes["group"])
		if !strings.Contains(ut, "natgateway") && !strings.Contains(grp, "ngw:natgateway") {
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
		if !awsIsAWSRegionProduct(prod) {
			continue
		}
		op := strings.ToLower(prod.Attributes["operation"])
		if !strings.Contains(op, "application") {
			continue
		}
		ut := strings.ToLower(prod.Attributes["usagetype"])
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
		if !awsIsAWSRegionProduct(prod) {
			continue
		}
		op := strings.ToLower(prod.Attributes["operation"])
		if !strings.Contains(op, "network") {
			continue
		}
		ut := strings.ToLower(prod.Attributes["usagetype"])
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
		fam := awsBulkProductFamily(prod)
		// Modern catalogs price log ingest/storage under productFamily
		// "Data Payload" with usagetype like USE1-VendedLog-Bytes
		// (ingest) and USE1-VendedLogIA-Bytes (IA storage tier).
		// Older rows used "Logs" / "ingestion" / "DataStored" tokens.
		isLog := strings.Contains(fam, "log") || strings.Contains(ut, "log") ||
			strings.Contains(ut, "vendedlog")
		if !isLog {
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
		case strings.Contains(ut, "vendedlogia"):
			if storage == 0 || usd < storage {
				storage = usd
			}
		case strings.Contains(ut, "vendedlog"):
			if ingest == 0 || usd < ingest {
				ingest = usd
			}
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

// AWSRDSPostgresHourly returns the on-demand $/hour for a managed
// Postgres instance type in region. engineFlavor is "postgres" for
// stock RDS Postgres or "aurora-postgresql" for Aurora Postgres
// (the AWS Bulk JSON uses the human label "Aurora PostgreSQL" but
// we accept the API form too). Caller multiplies by MonthlyHours
// to get the monthly compute number; storage is priced separately
// via AWSRDSStorageUSDPerGBMonth.
func AWSRDSPostgresHourly(instanceType, region, engineFlavor string) (float64, error) {
	pl, err := awsSvc.loadServiceBulk("AmazonRDS", region)
	if err != nil {
		return 0, err
	}
	loc := awsRegionLong(region)
	wantEngine := awsRDSEngineLabel(engineFlavor)
	wantDeploy := "Single-AZ"
	if strings.Contains(strings.ToLower(engineFlavor), "aurora") {
		// Aurora is always multi-AZ behind the scenes; the bulk JSON
		// uses an empty deploymentOption or "Multi-AZ" depending on
		// the row. Don't constrain by deployment for Aurora.
		wantDeploy = ""
	}
	best := 0.0
	matched := false
	for skuID, prod := range pl.Products {
		attr := prod.Attributes
		if !strings.EqualFold(attr["instanceType"], instanceType) {
			continue
		}
		if !strings.EqualFold(attr["location"], loc) {
			continue
		}
		if !strings.EqualFold(attr["databaseEngine"], wantEngine) {
			continue
		}
		if wantDeploy != "" {
			if !strings.EqualFold(attr["deploymentOption"], wantDeploy) {
				continue
			}
		}
		// Skip BYOL / SQL-licensed rows; we want the bundled
		// (no-license) on-demand SKU.
		lic := strings.ToLower(attr["licenseModel"])
		if lic != "" && !strings.Contains(lic, "no license") && !strings.Contains(lic, "included") {
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
		if !matched || usd < best {
			best = usd
			matched = true
		}
	}
	if !matched {
		return 0, fmt.Errorf("aws rds: no on-demand price for %s/%s in %s",
			instanceType, wantEngine, region)
	}
	return best, nil
}

// awsRDSEngineLabel normalizes engineFlavor into the value AWS Bulk
// JSON uses on the databaseEngine attribute.
func awsRDSEngineLabel(engineFlavor string) string {
	s := strings.ToLower(strings.TrimSpace(engineFlavor))
	switch s {
	case "aurora", "aurora-postgresql", "aurora postgres", "aurora postgresql":
		return "Aurora PostgreSQL"
	case "", "postgres", "postgresql":
		return "PostgreSQL"
	}
	return engineFlavor
}

// AWSRDSStorageUSDPerGBMonth returns RDS general-purpose (gp2)
// storage $/GB-month in region. The AmazonRDS bulk catalog encodes
// storage SKUs under productFamily="Database Storage" with
// volumeType containing "General Purpose" and a usagetype like
// "RDS:GP2-Storage".
func AWSRDSStorageUSDPerGBMonth(region string) (float64, error) {
	pl, err := awsSvc.loadServiceBulk("AmazonRDS", region)
	if err != nil {
		return 0, err
	}
	loc := awsRegionLong(region)
	best := 0.0
	matched := false
	for skuID, prod := range pl.Products {
		attr := prod.Attributes
		if !strings.EqualFold(attr["location"], loc) {
			continue
		}
		fam := awsBulkProductFamily(prod)
		if !strings.Contains(fam, "database storage") {
			continue
		}
		vt := strings.ToLower(attr["volumeType"])
		ut := strings.ToLower(attr["usagetype"])
		isGP := strings.Contains(vt, "general purpose") ||
			strings.Contains(ut, "gp2-storage") ||
			strings.Contains(ut, "gp3-storage")
		if !isGP {
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
		if !matched || usd < best {
			best = usd
			matched = true
		}
	}
	if !matched {
		return 0, fmt.Errorf("aws rds storage: no GP $/GB-month in %s", region)
	}
	return best, nil
}

// AWSRoute53ZoneUSDPerMonth — hosted zone $/month.
// Route53 is priced globally; the hosted-zone SKUs
// (productFamily "DNS Zone", usagetype "HostedZone…") live only
// in the top-level AmazonRoute53 bulk index — the regional offer
// files contain DNS Query / Profile / Outpost SKUs but no zone
// pricing. We read the no-region offer file by passing an empty
// region to loadServiceBulk.
func AWSRoute53ZoneUSDPerMonth(_ string) (float64, error) {
	pl, err := awsSvc.loadServiceBulk("AmazonRoute53", "")
	if err != nil {
		return 0, err
	}
	for skuID, prod := range pl.Products {
		ut := strings.ToLower(prod.Attributes["usagetype"])
		fam := awsBulkProductFamily(prod)
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
