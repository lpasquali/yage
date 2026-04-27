// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GCP service-specific helpers — read from the same Cloud Billing
// Catalog API as gcp.go. All require GOOGLE_BILLING_API_KEY (or
// YAGE_GCP_API_KEY).
//
// Service IDs we filter on:
//   gcpComputeEngineService = "6F81-5844-456A"  — Compute Engine
//                                                 (NAT, LB, egress, GKE)
//   gcpStorageService       = "95FF-2EF5-5EA1"  — Cloud Storage
//                                                 (logs to Cloud Logging
//                                                 are billed via this)
//
// Each helper follows the same pattern: page /services/<id>/skus,
// filter on serviceRegions + description substring + UsageType="OnDemand",
// pick the first non-zero tiered rate.

const (
	gcpKubernetesService    = "CCD8-9BF1-090E" // Google Kubernetes Engine (GKE)
	gcpStackdriverService   = "5490-F7B7-8DF6" // Cloud Logging / Stackdriver
	gcpCloudDNSService      = "9DC7-D6A1-D9D1" // Cloud DNS
	gcpNetworkingService    = "E505-1604-58F8" // Networking (egress)
)

// gcpServiceSKUCache memoizes the full SKU list per service ID for
// the process lifetime. The Cloud Billing Catalog is stable enough
// that one fetch per `yage` run is plenty, and re-walking the
// paginated catalog on every gcpFindSku call (NAT, LB, egress, …)
// dominates the cost-compare wall-clock when GCP is in the mix.
var (
	gcpServiceSKUMu    sync.Mutex
	gcpServiceSKUCache = map[string][]gcpSku{}
)

// gcpListAllSkus returns every SKU the catalog exposes for a service,
// fetching once per process and caching the result. Callers do their
// own filtering by region / description / category in-memory.
func gcpListAllSkus(serviceID, key string) ([]gcpSku, error) {
	gcpServiceSKUMu.Lock()
	if cached, ok := gcpServiceSKUCache[serviceID]; ok {
		gcpServiceSKUMu.Unlock()
		return cached, nil
	}
	gcpServiceSKUMu.Unlock()

	out := []gcpSku{}
	pageToken := ""
	c := &http.Client{Timeout: 30 * time.Second}
	for {
		u := fmt.Sprintf("%s/services/%s/skus", gcpBillingHost, serviceID)
		q := url.Values{}
		q.Set("key", key)
		q.Set("pageSize", "5000")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		req, _ := http.NewRequest("GET", u+"?"+q.Encode(), nil)
		req.Header.Set("User-Agent", "yage/pricing")
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		var lr gcpListResp
		if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		out = append(out, lr.Skus...)
		if lr.NextPageToken == "" {
			break
		}
		pageToken = lr.NextPageToken
	}

	gcpServiceSKUMu.Lock()
	gcpServiceSKUCache[serviceID] = out
	gcpServiceSKUMu.Unlock()
	return out, nil
}

// gcpFindSku scans the cached SKU list for a service+region and
// returns the first non-zero on-demand price whose description
// contains all of descContains (and none of mustNotContain). Pure
// in-memory after the first fetch — repeated calls across
// gcpFindSku / overhead helpers cost a slice walk, not an HTTP
// request.
func gcpFindSku(serviceID, region, key string, descContains []string, mustNotContain []string) (float64, error) {
	skus, err := gcpListAllSkus(serviceID, key)
	if err != nil {
		return 0, err
	}
	for _, s := range skus {
		if region != "" && !inSlice(s.ServiceRegions, region) {
			continue
		}
		if s.Category.UsageType != "OnDemand" {
			continue
		}
		desc := strings.ToLower(s.Description)
		matched := true
		for _, sub := range descContains {
			if !strings.Contains(desc, strings.ToLower(sub)) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		skip := false
		for _, sub := range mustNotContain {
			if strings.Contains(desc, strings.ToLower(sub)) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		if len(s.PricingInfo) == 0 {
			continue
		}
		price := gcpUsdFromTier(s.PricingInfo[0])
		if price > 0 {
			return price, nil
		}
	}
	return 0, fmt.Errorf("gcp: no sku for %v in %s", descContains, region)
}

// GCPGKEControlPlaneUSDPerMonth — GKE Standard cluster mgmt fee
// per cluster per month. Priced as USD/hour in the catalog.
func GCPGKEControlPlaneUSDPerMonth(region string) (float64, error) {
	key := gcpAPIKey()
	if key == "" {
		return 0, fmt.Errorf("gcp: no GOOGLE_BILLING_API_KEY")
	}
	hourly, err := gcpFindSku(
		gcpKubernetesService, region, key,
		[]string{"cluster management"},
		[]string{"autopilot"},
	)
	if err != nil {
		return 0, err
	}
	return hourly * MonthlyHours, nil
}

// gcpCloudNATListPriceFallback is used when the Cloud Billing Catalog
// search cannot resolve Cloud NAT SKUs (region labels, description
// drift, or API errors). Values follow the public Cloud NAT pricing
// page for North America tier-1 regions.
func gcpCloudNATListPriceFallback() (hourly, perGB float64) {
	return 0.044, 0.045
}

// GCPCloudNATPricing — Cloud NAT $/gateway-hour and $/GB processed.
func GCPCloudNATPricing(region string) (hourly, gbProc float64, err error) {
	key := gcpAPIKey()
	if key == "" {
		return 0, 0, fmt.Errorf("gcp: no GOOGLE_BILLING_API_KEY")
	}
	hourly, okH := gcpResolveCloudNATRate(gcpComputeEngineService, key, region, true)
	gbProc, okG := gcpResolveCloudNATRate(gcpComputeEngineService, key, region, false)
	if !okH || !okG {
		fh, fg := gcpCloudNATListPriceFallback()
		if !okH {
			hourly = fh
		}
		if !okG {
			gbProc = fg
		}
	}
	return hourly, gbProc, nil
}

func gcpResolveCloudNATRate(serviceID, key, region string, hourly bool) (float64, bool) {
	var descStrategies [][]string
	if hourly {
		descStrategies = [][]string{
			{"cloud nat", "uptime"},
			{"networking", "cloud nat", "uptime"},
			{"public nat", "uptime"},
			{"nat", "gateway", "uptime"},
		}
	} else {
		descStrategies = [][]string{
			{"cloud nat", "data processing"},
			{"networking", "cloud nat", "data processing"},
			{"public nat", "data processing"},
			{"nat", "data processed"},
		}
	}
	regions := []string{region, ""}
	for _, desc := range descStrategies {
		for _, reg := range regions {
			v, err := gcpFindSku(serviceID, reg, key, desc, []string{"private"})
			if err == nil && v > 0 {
				return v, true
			}
		}
	}
	return 0, false
}

// GCPLoadBalancerUSDPerMonth — average L4/L7 LB cost per month.
// GCP prices LBs as $/forwarding-rule-hour + $/GB; we report the
// hourly fee × 730 as the headline number (the 5 GB free tier
// covers most planning workloads).
//
// SKU descriptions for forwarding rules drift over time
// ("Network Load Balancing: Forwarding Rule", "External HTTP(S) Load
// Balancing Forwarding Rule", "Global External…"). We try several
// description shapes against the workload region first, then any
// region, then fall back to the public list price so the estimator
// never blocks on a catalog rename.
func GCPLoadBalancerUSDPerMonth(region string) (float64, error) {
	key := gcpAPIKey()
	if key == "" {
		// No key — return the public list-price fallback. Matches
		// the Cloud NAT helper above (same rationale: estimator
		// remains useful even without billing-catalog access).
		return gcpLoadBalancerListPriceFallback() * MonthlyHours, nil
	}
	regions := []string{region, ""}
	descTries := [][]string{
		{"forwarding", "rule"},
		{"load balancing", "forwarding"},
		{"forwarding rule"},
		{"external", "forwarding"},
		{"network load balancing"},
		{"http", "load balancing"},
	}
	for _, reg := range regions {
		for _, desc := range descTries {
			hr, err := gcpFindSku(gcpComputeEngineService, reg, key, desc, []string{"data processing", "ingress", "outgoing"})
			if err == nil && hr > 0 {
				return hr * MonthlyHours, nil
			}
		}
	}
	// Catalog matched nothing — use the public list price so the
	// estimator surfaces a number instead of failing the row.
	return gcpLoadBalancerListPriceFallback() * MonthlyHours, nil
}

// gcpLoadBalancerListPriceFallback returns GCP's public list price
// for forwarding rules: $0.025/hour for the first 5 rules (the rate
// most planning workloads land in). Used when the Cloud Billing
// Catalog is unreachable or returns no matching SKU.
func gcpLoadBalancerListPriceFallback() float64 {
	return 0.025
}

// GCPEgressUSDPerGB — internet egress $/GB to worldwide
// destinations (the headline tier-1 rate; GCP further tiers by
// destination but planning workloads land in the worldwide bucket).
//
// SKU descriptions for egress drift across the catalog
// ("Network Internet Egress from <region> to Worldwide Destinations",
// "Standard Tier: Internet Egress Worldwide", "Network Egress …").
// We try several description shapes against the workload region
// first, then any region, then fall back to the public list price
// so the estimator never blocks on a catalog rename or a missing
// API key.
func GCPEgressUSDPerGB(region string) (float64, error) {
	key := gcpAPIKey()
	if key == "" {
		// No key — return the public list-price fallback. Matches
		// the LB / Cloud NAT helpers above (same rationale: estimator
		// remains useful even without billing-catalog access).
		return gcpEgressListPriceFallback(), nil
	}
	regions := []string{region, ""}
	descTries := [][]string{
		{"network internet egress"},
		{"internet", "egress"},
		{"egress", "worldwide"},
		{"network egress"},
	}
	for _, reg := range regions {
		for _, desc := range descTries {
			v, err := gcpFindSku(gcpComputeEngineService, reg, key, desc, []string{"china", "australia"})
			if err == nil && v > 0 {
				return v, nil
			}
		}
	}
	// Catalog matched nothing — use the public list price so the
	// estimator surfaces a number instead of failing the row.
	return gcpEgressListPriceFallback(), nil
}

// gcpEgressListPriceFallback returns GCP's public list price for
// internet egress to worldwide destinations: $0.12/GB (the typical
// tier-1 rate). Used when the Cloud Billing Catalog is unreachable
// or returns no matching SKU.
func gcpEgressListPriceFallback() float64 {
	return 0.12
}

// GCPCloudLoggingUSDPerGB returns Cloud Logging ingestion $/GB.
// SKU descriptions for log ingestion drift over time ("Log Volume",
// "Logging Storage", "Logging Ingestion"). Try several shapes; fall
// back to the public list price ($0.50/GiB after the 50 GiB free
// tier) when the catalog finds nothing or the API key is missing.
func GCPCloudLoggingUSDPerGB(_ string) (float64, error) {
	key := gcpAPIKey()
	if key == "" {
		return gcpCloudLoggingListPriceFallback(), nil
	}
	descTries := [][]string{
		{"log volume"},
		{"logging", "ingestion"},
		{"logging", "storage"},
		{"log", "ingestion"},
	}
	for _, desc := range descTries {
		v, err := gcpFindSku(gcpStackdriverService, "", key, desc, nil)
		if err == nil && v > 0 {
			return v, nil
		}
	}
	return gcpCloudLoggingListPriceFallback(), nil
}

// gcpCloudLoggingListPriceFallback returns GCP's public list price
// for Cloud Logging ingestion: $0.50/GiB after the 50 GiB free
// tier per project per month. Used when the Cloud Billing Catalog
// is unreachable or the SKU shape has drifted.
func gcpCloudLoggingListPriceFallback() float64 {
	return 0.50
}

// GCPCloudDNSZoneUSDPerMonth — managed-zone $/month.
// Cloud DNS is priced globally; we ignore region. SKU descriptions
// drift ("managed zone", "managed dns zone", "Cloud DNS Zone"); we
// try several shapes against the catalog, then fall back to the
// public list price ($0.50/zone/month for the first 25 zones) so
// the estimator never blocks on a catalog rename. Same pattern as
// GCPCloudLoggingUSDPerGB / GCPLoadBalancerUSDPerMonth.
func GCPCloudDNSZoneUSDPerMonth(_ string) (float64, error) {
	key := gcpAPIKey()
	if key == "" {
		return gcpCloudDNSListPriceFallback(), nil
	}
	descTries := [][]string{
		{"managed zone"},
		{"managed", "zone"},
		{"hosted zone"},
		{"dns zone"},
		{"zone management"},
	}
	for _, desc := range descTries {
		v, err := gcpFindSku(gcpCloudDNSService, "", key, desc, []string{"queries", "lookup"})
		if err == nil && v > 0 {
			return v, nil
		}
	}
	return gcpCloudDNSListPriceFallback(), nil
}

// gcpCloudDNSListPriceFallback returns Cloud DNS's public list price
// for the first-tier managed zone: $0.50/zone/month. Used when the
// Cloud Billing Catalog is unreachable or the SKU shape has drifted.
func gcpCloudDNSListPriceFallback() float64 {
	return 0.50
}