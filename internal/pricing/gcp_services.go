package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// GCP service-specific helpers — read from the same Cloud Billing
// Catalog API as gcp.go. All require GOOGLE_BILLING_API_KEY (or
// BOOTSTRAP_CAPI_GCP_API_KEY).
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

// findFirstSku iterates skus for a service+region matching all
// description substrings (lowercased). Returns the first non-zero
// tiered USD rate.
func gcpFindSku(serviceID, region, key string, descContains []string, mustNotContain []string) (float64, error) {
	pageToken := ""
	for {
		u := fmt.Sprintf("%s/services/%s/skus", gcpBillingHost, serviceID)
		q := url.Values{}
		q.Set("key", key)
		q.Set("pageSize", "5000")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		req, _ := http.NewRequest("GET", u+"?"+q.Encode(), nil)
		req.Header.Set("User-Agent", "bootstrap-capi/pricing")
		c := &http.Client{Timeout: 30 * 1e9} // 30s
		resp, err := c.Do(req)
		if err != nil {
			return 0, err
		}
		var lr gcpListResp
		if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
			resp.Body.Close()
			return 0, err
		}
		resp.Body.Close()
		for _, s := range lr.Skus {
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
		if lr.NextPageToken == "" {
			break
		}
		pageToken = lr.NextPageToken
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

// GCPCloudNATPricing — Cloud NAT $/gateway-hour and $/GB processed.
func GCPCloudNATPricing(region string) (hourly, gbProc float64, err error) {
	key := gcpAPIKey()
	if key == "" {
		return 0, 0, fmt.Errorf("gcp: no GOOGLE_BILLING_API_KEY")
	}
	hourly, err = gcpFindSku(gcpComputeEngineService, region, key,
		[]string{"nat gateway"},
		nil)
	if err != nil {
		return 0, 0, fmt.Errorf("nat hourly: %w", err)
	}
	gbProc, err = gcpFindSku(gcpComputeEngineService, region, key,
		[]string{"nat", "data processing"},
		nil)
	if err != nil {
		return 0, 0, fmt.Errorf("nat gb: %w", err)
	}
	return hourly, gbProc, nil
}

// GCPLoadBalancerUSDPerMonth — average L4/L7 LB cost per month.
// GCP prices LBs as $/forwarding-rule-hour + $/GB; we report the
// hourly fee × 730 as the headline number (the 5 GB free tier
// covers most planning workloads).
func GCPLoadBalancerUSDPerMonth(region string) (float64, error) {
	key := gcpAPIKey()
	if key == "" {
		return 0, fmt.Errorf("gcp: no GOOGLE_BILLING_API_KEY")
	}
	hr, err := gcpFindSku(gcpComputeEngineService, region, key,
		[]string{"forwarding rule"},
		[]string{"global"}) // skip cross-region if possible
	if err != nil {
		// Fall back to global forwarding rule
		hr, err = gcpFindSku(gcpComputeEngineService, region, key,
			[]string{"forwarding rule"},
			nil)
		if err != nil {
			return 0, err
		}
	}
	return hr * MonthlyHours, nil
}

// GCPEgressUSDPerGB — internet egress $/GB (cheap-tier worldwide
// rate; GCP has tiered destinations but we model the headline rate).
func GCPEgressUSDPerGB(region string) (float64, error) {
	key := gcpAPIKey()
	if key == "" {
		return 0, fmt.Errorf("gcp: no GOOGLE_BILLING_API_KEY")
	}
	// Compute Engine catalog has the per-region egress SKUs.
	return gcpFindSku(gcpComputeEngineService, region, key,
		[]string{"network internet egress"},
		[]string{"china", "australia"})
}

// GCPCloudLoggingUSDPerGB — Cloud Logging ingestion $/GB.
func GCPCloudLoggingUSDPerGB(region string) (float64, error) {
	key := gcpAPIKey()
	if key == "" {
		return 0, fmt.Errorf("gcp: no GOOGLE_BILLING_API_KEY")
	}
	return gcpFindSku(gcpStackdriverService, "", key,
		[]string{"log volume"},
		nil)
}

// GCPCloudDNSZoneUSDPerMonth — managed-zone $/month.
// Cloud DNS is priced globally; we ignore region.
func GCPCloudDNSZoneUSDPerMonth(_ string) (float64, error) {
	key := gcpAPIKey()
	if key == "" {
		return 0, fmt.Errorf("gcp: no GOOGLE_BILLING_API_KEY")
	}
	// Cloud DNS managed-zone pricing tiers by zone count; first 25
	// zones at one rate, more at another. Pick the first-tier rate.
	return gcpFindSku(gcpCloudDNSService, "", key,
		[]string{"managed zone"},
		[]string{"queries"})
}
