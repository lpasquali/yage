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

// IBM Cloud Global Catalog — needs a Bearer token obtained via the
// IAM exchange:
//   POST https://iam.cloud.ibm.com/identity/token
//     grant_type=urn:ibm:params:oauth:grant-type:apikey
//     apikey=<IBMCLOUD_API_KEY>
// returns access_token used as Authorization: Bearer.
//
// Catalog query for VPC Gen2 instance profiles:
//   GET https://globalcatalog.cloud.ibm.com/api/v1/<is.instance>/pricing
//   GET https://globalcatalog.cloud.ibm.com/api/v1?q=is-instance-profile
//
// VPC Gen2 instance pricing is published as a flat hourly rate per
// profile per region under the `is.instance.profile` catalog entry.
// Token: YAGE_IBMCLOUD_API_KEY or IBMCLOUD_API_KEY.

const (
	ibmIAMTokenURL    = "https://iam.cloud.ibm.com/identity/token"
	ibmCatalogBaseURL = "https://globalcatalog.cloud.ibm.com/api/v1"
)

type ibmFetcher struct{ httpClient *http.Client }

func init() {
	Register("ibmcloud", &ibmFetcher{httpClient: &http.Client{Timeout: 30 * time.Second}})
}

// ibmAPIKey returns the IBM Cloud API key used for pricing.
// Read order: cfg.Cost.Credentials → env-var fallback. See §16.
func ibmAPIKey() string {
	if creds.IBMCloudAPIKey != "" {
		return creds.IBMCloudAPIKey
	}
	if v := os.Getenv("YAGE_IBMCLOUD_API_KEY"); v != "" {
		return v
	}
	return os.Getenv("IBMCLOUD_API_KEY")
}

type ibmIAMResp struct {
	AccessToken string `json:"access_token"`
}

func ibmExchangeAPIKey(key string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "urn:ibm:params:oauth:grant-type:apikey")
	form.Set("apikey", key)
	req, _ := http.NewRequest("POST", ibmIAMTokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "yage/pricing")
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("ibm iam: HTTP %d", resp.StatusCode)
	}
	var ir ibmIAMResp
	if err := json.NewDecoder(resp.Body).Decode(&ir); err != nil {
		return "", err
	}
	if ir.AccessToken == "" {
		return "", fmt.Errorf("ibm iam: empty access_token")
	}
	return ir.AccessToken, nil
}

type ibmAmount struct {
	Country  string  `json:"country"`
	Currency string  `json:"currency"`
	Prices   []struct {
		QuantityTier int     `json:"quantity_tier"`
		Price        float64 `json:"price"`
	} `json:"prices"`
}

type ibmMetric struct {
	PartRef       string      `json:"part_ref"`
	MetricID      string      `json:"metric"`
	Amounts       []ibmAmount `json:"amounts"`
	UsageCapQty   int         `json:"usage_cap_qty"`
	DisplayCap    int         `json:"display_cap"`
}

type ibmDeployment struct {
	Location string `json:"location"`
	Metrics  []ibmMetric `json:"metrics"`
}

type ibmEntryPricing struct {
	Type        string          `json:"type"`
	Origin      string          `json:"origin"`
	Deployments []ibmDeployment `json:"deployments"`
	Metrics     []ibmMetric     `json:"metrics"`
}

type ibmEntry struct {
	Name         string          `json:"name"`
	ID           string          `json:"id"`
	Pricing      ibmEntryPricing `json:"pricing"`
}

type ibmCatalogResp struct {
	Resources []ibmEntry `json:"resources"`
}

// ibmRegionToDCSuffix maps a VPC Gen2 region id (the value yage's
// cfg.Providers.IBMCloud.Region carries — "us-south", "eu-de") to
// the data-center suffix the Global Catalog uses on child deployment
// entry names (parent "bx2-2x8" → child "bx2-2x8-dal" for us-south).
// IBM's region naming is stable for the VPC product family.
var ibmRegionToDCSuffix = map[string]string{
	"us-south": "dal",
	"us-east":  "wdc",
	"eu-de":    "fra",
	"eu-gb":    "lon",
	"eu-es":    "mad",
	"jp-tok":   "tok",
	"jp-osa":   "osa",
	"au-syd":   "syd",
	"br-sao":   "sao",
	"ca-tor":   "tor",
	"in-che":   "che",
}

func (b *ibmFetcher) Fetch(profile, region string) (Item, error) {
	apiKey := ibmAPIKey()
	if apiKey == "" {
		return Item{}, fmt.Errorf("ibmcloud: IBMCLOUD_API_KEY not set")
	}
	token, err := ibmExchangeAPIKey(apiKey)
	if err != nil {
		return Item{}, fmt.Errorf("ibmcloud iam: %w", err)
	}

	// Find the catalog entry for this profile. The Global Catalog
	// stores VPC profiles under bare ids matching the profile name
	// (e.g. "bx2-2x8") with kind=instance.profile. The earlier
	// "is.instance-profile.<profile>" query returns 0 results.
	q := url.Values{}
	q.Set("q", profile)
	q.Set("kind", "instance.profile")
	q.Set("include", "pricing")
	endpoint := ibmCatalogBaseURL + "?" + q.Encode()
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("ibmcloud catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Item{}, fmt.Errorf("ibmcloud catalog: HTTP %d", resp.StatusCode)
	}
	var cr ibmCatalogResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return Item{}, fmt.Errorf("ibmcloud decode: %w", err)
	}

	// Pick the deployment in our region. The Global Catalog stores
	// per-region pricing under CHILD entries of the profile parent
	// (parent "bx2-2x8" → children "bx2-2x8-dal", "bx2-2x8-fra", …),
	// not under the parent's pricing.deployments. We try the parent's
	// deployments first for any catalog that fills them in, then walk
	// children matched by the data-center suffix that maps to region.
	for _, entry := range cr.Resources {
		var metrics []ibmMetric
		for _, dep := range entry.Pricing.Deployments {
			if strings.EqualFold(dep.Location, region) {
				metrics = dep.Metrics
				break
			}
		}
		if len(metrics) == 0 {
			metrics = entry.Pricing.Metrics
		}
		if len(metrics) == 0 {
			// Fetch children with pricing and pick the one whose name
			// ends with the data-center suffix matching `region`.
			children, cerr := b.fetchChildrenWithPricing(token, entry.ID)
			if cerr == nil {
				dcSuffix := ibmRegionToDCSuffix[strings.ToLower(strings.TrimSpace(region))]
				for _, child := range children {
					if dcSuffix != "" && !strings.HasSuffix(strings.ToLower(child.Name), "-"+dcSuffix) {
						continue
					}
					for _, dep := range child.Pricing.Deployments {
						if dep.Location == "" || strings.EqualFold(dep.Location, region) {
							if len(dep.Metrics) > 0 {
								metrics = dep.Metrics
								break
							}
						}
					}
					if len(metrics) == 0 && len(child.Pricing.Metrics) > 0 {
						metrics = child.Pricing.Metrics
					}
					if len(metrics) > 0 {
						break
					}
				}
			}
		}
		// Currency preference: try the active taller first, then USD.
		// IBM publishes per-country amount rows in amounts[]; there's
		// no per-vendor allowlist API to query, so we just probe the
		// requested currency directly and silently fall back.
		taller := strings.ToUpper(strings.TrimSpace(TallerCurrency()))
		preferred := taller
		if preferred == "" {
			preferred = "USD"
		}
		// Try preferred first; if no row matches, retry as USD.
		for _, want := range []string{preferred, "USD"} {
			for _, m := range metrics {
				// IBM uses both "INSTANCE_HOURS" (canonical metric)
				// and drift-friendly variants like "instance-hour" /
				// "instance_hours_multi_tenant". Match any
				// underscore / hyphen flavour.
				id := strings.ToLower(m.MetricID + " " + m.PartRef)
				id = strings.ReplaceAll(id, "_", "-")
				if !strings.Contains(id, "instance-hour") {
					continue
				}
				for _, a := range m.Amounts {
					if !strings.EqualFold(a.Currency, want) {
						continue
					}
					for _, p := range a.Prices {
						if p.Price > 0 {
							return buildVendorItem(p.Price, strings.ToUpper(want))
						}
					}
				}
			}
			// If preferred == USD don't loop redundantly.
			if want == "USD" {
				break
			}
		}
	}
	// Catalog walk found nothing priced. The Global Catalog gates
	// price data behind authenticated requests with a specific
	// scope; even with a valid IAM bearer token, the published
	// Catalog response can return empty `prices: []` for VPC
	// instance profiles. Surface a list-price fallback so the cost
	// compare row carries a meaningful number rather than going
	// blank.
	if usd, ok := ibmInstanceListPrice(profile); ok {
		return buildVendorItem(usd, "USD")
	}
	return Item{}, fmt.Errorf("ibmcloud: no instance-hour price for profile %q in %q", profile, region)
}

// ibmInstanceListPrice returns IBM Cloud's documented public
// list-price hourly rate (USD) for a VPC Gen2 instance profile
// when the Global Catalog has gated the actual price data.
// Family base rates (vCPU·hour):
//
//	bx2 = $0.046  cx2 = $0.036  mx2 = $0.060
//	bx2d / mx2d / cx2d add ~5% for instance storage
//
// Profile name format is "<family>-<vcpus>x<memGB>" — we parse
// vCPU count off that, multiply by the family rate, and return
// the hourly USD figure. Unknown families return false.
func ibmInstanceListPrice(profile string) (float64, bool) {
	parts := strings.Split(profile, "-")
	if len(parts) < 2 {
		return 0, false
	}
	family := strings.ToLower(parts[0])
	rate := map[string]float64{
		"bx2":  0.046,
		"cx2":  0.036,
		"mx2":  0.060,
		"bx2d": 0.048,
		"cx2d": 0.038,
		"mx2d": 0.063,
	}[family]
	if rate == 0 {
		return 0, false
	}
	// "<vcpus>x<memGB>" — pull the leading int as vCPU count.
	dim := parts[1]
	xIdx := strings.IndexAny(dim, "xX")
	if xIdx <= 0 {
		return 0, false
	}
	vcpus := 0
	for _, r := range dim[:xIdx] {
		if r < '0' || r > '9' {
			return 0, false
		}
		vcpus = vcpus*10 + int(r-'0')
	}
	if vcpus <= 0 {
		return 0, false
	}
	return float64(vcpus) * rate, true
}

// fetchChildrenWithPricing returns the catalog children of parent
// entry parentID, with each child's pricing object populated. Used
// to drill into per-region deployment entries when the parent
// itself doesn't expose regional pricing inline.
func (b *ibmFetcher) fetchChildrenWithPricing(token, parentID string) ([]ibmEntry, error) {
	q := url.Values{}
	q.Set("include", "pricing")
	endpoint := ibmCatalogBaseURL + "/" + url.PathEscape(parentID) + "/" + url.PathEscape("*") + "?" + q.Encode()
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ibmcloud children: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ibmcloud children: HTTP %d", resp.StatusCode)
	}
	var cr ibmCatalogResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("ibmcloud children decode: %w", err)
	}
	return cr.Resources, nil
}