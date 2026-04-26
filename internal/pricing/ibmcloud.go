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

func ibmAPIKey() string {
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
	Name         string         `json:"name"`
	ID           string         `json:"id"`
	Pricing      ibmEntryPricing `json:"pricing"`
}

type ibmCatalogResp struct {
	Resources []ibmEntry `json:"resources"`
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

	// Find the catalog entry for this profile. The id pattern in the
	// catalog is "is.instance-profile.<profile>" — query via search.
	q := url.Values{}
	q.Set("q", "is.instance-profile."+profile)
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

	// Pick the deployment in our region (or fall back to "global").
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
		for _, m := range metrics {
			if !strings.Contains(strings.ToLower(m.MetricID), "instance-hour") &&
				!strings.Contains(strings.ToLower(m.PartRef), "instance-hour") {
				continue
			}
			for _, a := range m.Amounts {
				if !strings.EqualFold(a.Currency, "USD") {
					continue
				}
				for _, p := range a.Prices {
					if p.Price > 0 {
						return Item{
							USDPerHour:  p.Price,
							USDPerMonth: p.Price * MonthlyHours,
							FetchedAt:   time.Now(),
						}, nil
					}
				}
			}
		}
	}
	return Item{}, fmt.Errorf("ibmcloud: no instance-hour price for profile %q in %q", profile, region)
}
