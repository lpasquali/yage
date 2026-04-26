// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// GCP Cloud Billing Catalog API — needs an API key.
// Set GOOGLE_BILLING_API_KEY (or YAGE_GCP_API_KEY) to
// enable; otherwise this fetcher returns ErrUnavailable and
// the cost path surfaces "GCP estimate unavailable".
//
// Endpoint:
//   GET https://cloudbilling.googleapis.com/v1/services/<svcID>/skus?key=<API_KEY>
//
// Compute Engine service ID is "6F81-5844-456A". We page
// through skus[], finding ones whose category.resourceFamily
// is "Compute" and whose serviceRegions includes the requested
// region and whose description matches the machine type.
//
// The catalog is large (>10k SKUs for Compute alone). We don't
// pre-cache the full dump; instead, we filter server-side using
// pageToken and skip rows quickly.
const (
	gcpBillingHost          = "https://cloudbilling.googleapis.com/v1"
	gcpComputeEngineService = "6F81-5844-456A"
	gcpStorageService       = "95FF-2EF5-5EA1"
)

type gcpFetcher struct{ httpClient *http.Client }

func init() {
	Register("gcp", &gcpFetcher{httpClient: &http.Client{Timeout: 30 * time.Second}})
}

// gcpAPIKey returns the configured Google Cloud Billing Catalog
// key. Read order: cfg.Cost.Credentials (set by main from
// config.Load) → env-var fallback for cases where the orchestrator
// hasn't called SetCredentials yet (e.g. test setups, --xapiri).
func gcpAPIKey() string {
	if creds.GCPAPIKey != "" {
		return creds.GCPAPIKey
	}
	if k := os.Getenv("YAGE_GCP_API_KEY"); k != "" {
		return k
	}
	return os.Getenv("GOOGLE_BILLING_API_KEY")
}

type gcpPricingExpression struct {
	UsageUnit               string `json:"usageUnit"`
	UsageUnitDescription    string `json:"usageUnitDescription"`
	BaseUnit                string `json:"baseUnit"`
	DisplayQuantity         float64 `json:"displayQuantity"`
	TieredRates             []struct {
		StartUsageAmount float64 `json:"startUsageAmount"`
		UnitPrice        struct {
			CurrencyCode string `json:"currencyCode"`
			Units        string `json:"units"`
			Nanos        int64  `json:"nanos"`
		} `json:"unitPrice"`
	} `json:"tieredRates"`
}

type gcpPricingInfo struct {
	EffectiveTime      string               `json:"effectiveTime"`
	PricingExpression  gcpPricingExpression `json:"pricingExpression"`
	CurrencyConversionRate float64          `json:"currencyConversionRate"`
}

type gcpSku struct {
	Name        string `json:"name"`
	SkuId       string `json:"skuId"`
	Description string `json:"description"`
	Category    struct {
		ServiceDisplayName string `json:"serviceDisplayName"`
		ResourceFamily     string `json:"resourceFamily"`
		ResourceGroup      string `json:"resourceGroup"`
		UsageType          string `json:"usageType"`
	} `json:"category"`
	ServiceRegions []string         `json:"serviceRegions"`
	PricingInfo    []gcpPricingInfo `json:"pricingInfo"`
}

type gcpListResp struct {
	Skus          []gcpSku `json:"skus"`
	NextPageToken string   `json:"nextPageToken"`
}

func gcpUsdFromTier(p gcpPricingInfo) float64 {
	if len(p.PricingExpression.TieredRates) == 0 {
		return 0
	}
	// Pick the first non-zero tiered rate (commonly tier 0 is
	// the headline on-demand price).
	for _, t := range p.PricingExpression.TieredRates {
		usd := 0.0
		// units is a string ("0"), nanos is int (e.g. 23000000 = 0.023)
		var u float64
		fmt.Sscanf(t.UnitPrice.Units, "%f", &u)
		usd = u + float64(t.UnitPrice.Nanos)/1e9
		if usd > 0 {
			return usd
		}
	}
	return 0
}

func (g *gcpFetcher) Fetch(sku, region string) (Item, error) {
	key := gcpAPIKey()
	if key == "" {
		return Item{}, fmt.Errorf("gcp: no GOOGLE_BILLING_API_KEY (or YAGE_GCP_API_KEY)")
	}

	// SKU forms:
	//   "<machineType>"        e.g. "n2-standard-2", "e2-medium"
	//   "pd:balanced"          PD Balanced GB-month
	//   "pd:ssd"               PD SSD GB-month
	//   "pd:standard"          PD Standard GB-month
	if strings.HasPrefix(sku, "pd:") {
		return g.fetchPD(strings.TrimPrefix(sku, "pd:"), region, key)
	}
	return g.fetchCompute(sku, region, key)
}

func (g *gcpFetcher) fetchCompute(machineType, region, key string) (Item, error) {
	// Compute pricing in GCP is split into core (CPU) + RAM (GB)
	// SKUs per machine family. For the headline number we want
	// the explicit "<MachineType> Instance Core" + "RAM" pair.
	// As a simpler conservative path, we sum core + ram for the
	// machine family. machineType like "n2-standard-2" parses to
	// family=n2, predefined=standard, vCPU=2, RAM via convention.
	family, vcpu, ramGB, err := parseGCPMachineType(machineType)
	if err != nil {
		return Item{}, err
	}
	corePrice, err := g.findCoreRam(family, region, key, true /*core*/)
	if err != nil {
		return Item{}, err
	}
	ramPrice, err := g.findCoreRam(family, region, key, false /*ram*/)
	if err != nil {
		return Item{}, err
	}
	hourly := corePrice*float64(vcpu) + ramPrice*ramGB
	if hourly <= 0 {
		return Item{}, fmt.Errorf("gcp: zero price for %s in %s", machineType, region)
	}
	return Item{
		USDPerHour:  hourly,
		USDPerMonth: hourly * MonthlyHours,
		FetchedAt:   time.Now(),
	}, nil
}

// findCoreRam searches Compute Engine SKUs for a single
// "Core/Ram running in <region>" entry of the requested family.
// Returns USD/vCPU-hour (when wantCore) or USD/GB-hour (when !wantCore).
func (g *gcpFetcher) findCoreRam(family, region, key string, wantCore bool) (float64, error) {
	pageToken := ""
	wantGroup := "CPU"
	if !wantCore {
		wantGroup = "RAM"
	}
	familyUpper := strings.ToUpper(family)
	for {
		u := fmt.Sprintf("%s/services/%s/skus", gcpBillingHost, gcpComputeEngineService)
		q := url.Values{}
		q.Set("key", key)
		q.Set("pageSize", "5000")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		req, _ := http.NewRequest("GET", u+"?"+q.Encode(), nil)
		req.Header.Set("User-Agent", "yage/pricing")
		resp, err := g.httpClient.Do(req)
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
			if !inSlice(s.ServiceRegions, region) {
				continue
			}
			if s.Category.ResourceFamily != "Compute" {
				continue
			}
			if !strings.Contains(s.Category.ResourceGroup, wantGroup) {
				continue
			}
			if s.Category.UsageType != "OnDemand" {
				continue
			}
			desc := strings.ToUpper(s.Description)
			if !strings.Contains(desc, familyUpper) {
				continue
			}
			if strings.Contains(desc, "PREEMPTIBLE") || strings.Contains(desc, "SPOT") {
				continue
			}
			if strings.Contains(desc, "COMMITMENT") || strings.Contains(desc, "RESERVED") {
				continue
			}
			if strings.Contains(desc, "SOLE TENANT") || strings.Contains(desc, "CUSTOM") {
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
	return 0, fmt.Errorf("gcp: no %s sku for family %s in %s", wantGroup, family, region)
}

func (g *gcpFetcher) fetchPD(kind, region, key string) (Item, error) {
	wantDesc := "Balanced PD"
	switch kind {
	case "ssd":
		wantDesc = "SSD backed PD Capacity"
	case "standard":
		wantDesc = "Storage PD Capacity"
	case "balanced":
		wantDesc = "Balanced PD Capacity"
	default:
		return Item{}, fmt.Errorf("gcp pd: unknown kind %q", kind)
	}
	pageToken := ""
	for {
		u := fmt.Sprintf("%s/services/%s/skus", gcpBillingHost, gcpComputeEngineService)
		q := url.Values{}
		q.Set("key", key)
		q.Set("pageSize", "5000")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		req, _ := http.NewRequest("GET", u+"?"+q.Encode(), nil)
		req.Header.Set("User-Agent", "yage/pricing")
		resp, err := g.httpClient.Do(req)
		if err != nil {
			return Item{}, err
		}
		var lr gcpListResp
		if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
			resp.Body.Close()
			return Item{}, err
		}
		resp.Body.Close()
		for _, s := range lr.Skus {
			if !inSlice(s.ServiceRegions, region) {
				continue
			}
			if !strings.Contains(s.Description, wantDesc) {
				continue
			}
			if s.Category.UsageType != "OnDemand" {
				continue
			}
			if len(s.PricingInfo) == 0 {
				continue
			}
			price := gcpUsdFromTier(s.PricingInfo[0])
			if price > 0 {
				return Item{
					USDPerHour:  0,
					USDPerMonth: price,
					FetchedAt:   time.Now(),
				}, nil
			}
		}
		if lr.NextPageToken == "" {
			break
		}
		pageToken = lr.NextPageToken
	}
	return Item{}, fmt.Errorf("gcp pd: no %q sku in %s", wantDesc, region)
}

func parseGCPMachineType(mt string) (family string, vcpu int, ramGB float64, err error) {
	parts := strings.Split(mt, "-")
	if len(parts) < 3 {
		// e.g. "e2-medium" — predefined shared-core
		return "", 0, 0, fmt.Errorf("gcp: unsupported machine type form %q", mt)
	}
	family = parts[0]
	predef := parts[1]
	var n int
	if _, e := fmt.Sscanf(parts[2], "%d", &n); e != nil {
		return "", 0, 0, fmt.Errorf("gcp: bad vcpu in %q", mt)
	}
	vcpu = n
	switch predef {
	case "standard":
		ramGB = float64(n) * 4
	case "highmem":
		ramGB = float64(n) * 8
	case "highcpu":
		ramGB = float64(n) * 1
	default:
		return "", 0, 0, fmt.Errorf("gcp: unknown predefined shape %q", predef)
	}
	return family, vcpu, ramGB, nil
}

func inSlice(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}