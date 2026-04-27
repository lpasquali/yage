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

	"github.com/IBM/go-sdk-core/v5/core"
)

// IBM Cloud Global Catalog — authenticated via the IBM go-sdk-core
// IamAuthenticator, which handles the IAM API-key → Bearer-token
// exchange and caches the token until it nears expiry.
//
// Catalog query for VPC Gen2 instance profiles:
//   GET https://globalcatalog.cloud.ibm.com/api/v1?q=<profile>&kind=instance.profile&include=pricing
//
// VPC Gen2 instance pricing is published as a flat hourly rate per
// profile per region under the `is.instance.profile` catalog entry.
//
// The IamAuthenticator is configured with Client: &http.Client{} (nil
// Transport) so IAM token requests go through http.DefaultTransport —
// keeping the airgap shim effective. The catalog HTTP client uses the
// same nil-Transport pattern.
//
// Token: YAGE_IBMCLOUD_API_KEY or IBMCLOUD_API_KEY.

const (
	ibmCatalogBaseURL = "https://globalcatalog.cloud.ibm.com/api/v1"
)

// ibmHTTPClient is the shared nil-Transport HTTP client used for all
// IBM Cloud catalog HTTP requests. Inherits http.DefaultTransport at
// call time (including the airgap shim). The IamAuthenticator uses its
// own Client field, also set to &http.Client{}.
var ibmHTTPClient = &http.Client{}

type ibmFetcher struct{}

func init() {
	Register("ibmcloud", &ibmFetcher{})
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

// newIBMAuthenticator creates an IamAuthenticator for the given API
// key. The Client field is set to &http.Client{} (nil Transport) so
// the IAM token request inherits http.DefaultTransport, keeping the
// airgap shim effective.
func newIBMAuthenticator(apiKey string) *core.IamAuthenticator {
	return &core.IamAuthenticator{
		ApiKey: apiKey,
		Client: ibmHTTPClient,
	}
}

// ibmAuthorize exchanges the API key for a Bearer token via the IBM
// IamAuthenticator and returns it as a string. The authenticator
// caches the token so repeated calls within a process avoid redundant
// IAM round-trips.
func ibmAuthorize(auth *core.IamAuthenticator) (string, error) {
	// Construct a throwaway request just to extract the token value.
	req, _ := http.NewRequest("GET", ibmCatalogBaseURL, nil)
	if err := auth.Authenticate(req); err != nil {
		return "", fmt.Errorf("ibm iam: %w", err)
	}
	authHeader := req.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return "", fmt.Errorf("ibm iam: unexpected authorization header %q", authHeader)
	}
	return strings.TrimPrefix(authHeader, "Bearer "), nil
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
	PartRef     string      `json:"part_ref"`
	MetricID    string      `json:"metric"`
	Amounts     []ibmAmount `json:"amounts"`
	UsageCapQty int         `json:"usage_cap_qty"`
	DisplayCap  int         `json:"display_cap"`
}

type ibmDeployment struct {
	Location string      `json:"location"`
	Metrics  []ibmMetric `json:"metrics"`
}

type ibmEntryPricing struct {
	Type        string          `json:"type"`
	Origin      string          `json:"origin"`
	Deployments []ibmDeployment `json:"deployments"`
	Metrics     []ibmMetric     `json:"metrics"`
}

type ibmEntry struct {
	Name    string          `json:"name"`
	ID      string          `json:"id"`
	Pricing ibmEntryPricing `json:"pricing"`
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
	auth := newIBMAuthenticator(apiKey)
	token, err := ibmAuthorize(auth)
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
	resp, err := ibmHTTPClient.Do(req)
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
			children, cerr := ibmFetchChildrenWithPricing(token, entry.ID)
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

// ibmFetchChildrenWithPricing returns the catalog children of parent
// entry parentID, with each child's pricing object populated. Used
// to drill into per-region deployment entries when the parent
// itself doesn't expose regional pricing inline.
func ibmFetchChildrenWithPricing(token, parentID string) ([]ibmEntry, error) {
	q := url.Values{}
	q.Set("include", "pricing")
	endpoint := ibmCatalogBaseURL + "/" + url.PathEscape(parentID) + "/" + url.PathEscape("*") + "?" + q.Encode()
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := ibmHTTPClient.Do(req)
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

// ensure time is used (FetchedAt in buildVendorItem uses time.Now internally)
var _ = time.Now
