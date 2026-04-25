package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Equinix Metal API — token-required.
//   GET https://api.equinix.com/metal/v1/plans
// Returns every plan with pricing.hour (USD) and a list of regions
// expressed as deployments / facilities and metros (newer API).
//
// Token: METAL_AUTH_TOKEN (also accepted: BOOTSTRAP_CAPI_METAL_TOKEN).
const equinixPlansURL = "https://api.equinix.com/metal/v1/plans"

type equinixFetcher struct{ httpClient *http.Client }

func init() {
	Register("equinix", &equinixFetcher{httpClient: &http.Client{Timeout: 30 * time.Second}})
}

func equinixToken() string {
	if v := os.Getenv("BOOTSTRAP_CAPI_METAL_TOKEN"); v != "" {
		return v
	}
	return os.Getenv("METAL_AUTH_TOKEN")
}

type equinixPricing struct {
	Hour  float64 `json:"hour"`
	Month float64 `json:"month"`
}

type equinixMetro struct {
	Code string `json:"code"`
}

type equinixFacility struct {
	Code  string         `json:"code"`
	Metro *equinixMetro  `json:"metro"`
}

type equinixPlan struct {
	Slug              string             `json:"slug"`
	Name              string             `json:"name"`
	Pricing           equinixPricing     `json:"pricing"`
	AvailableIn       []equinixFacility  `json:"available_in"`
	AvailableInMetros []equinixMetro     `json:"available_in_metros"`
}

type equinixPlansResp struct {
	Plans []equinixPlan `json:"plans"`
}

func (e *equinixFetcher) Fetch(slug, metro string) (Item, error) {
	token := equinixToken()
	if token == "" {
		return Item{}, fmt.Errorf("equinix: METAL_AUTH_TOKEN not set")
	}
	req, _ := http.NewRequest("GET", equinixPlansURL, nil)
	req.Header.Set("X-Auth-Token", token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bootstrap-capi/pricing")
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("equinix: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Item{}, fmt.Errorf("equinix: HTTP %d", resp.StatusCode)
	}
	var er equinixPlansResp
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return Item{}, fmt.Errorf("equinix decode: %w", err)
	}
	for _, p := range er.Plans {
		if p.Slug != slug {
			continue
		}
		// Verify the metro is supported.
		want := strings.ToLower(metro)
		regionOK := false
		for _, m := range p.AvailableInMetros {
			if strings.ToLower(m.Code) == want {
				regionOK = true
				break
			}
		}
		if !regionOK {
			for _, f := range p.AvailableIn {
				if f.Metro != nil && strings.ToLower(f.Metro.Code) == want {
					regionOK = true
					break
				}
			}
		}
		if !regionOK {
			return Item{}, fmt.Errorf("equinix: %s not in metro %s", slug, metro)
		}
		hourly := p.Pricing.Hour
		monthly := p.Pricing.Month
		if monthly == 0 {
			monthly = hourly * MonthlyHours
		}
		return Item{
			USDPerHour:  hourly,
			USDPerMonth: monthly,
			FetchedAt:   time.Now(),
		}, nil
	}
	return Item{}, fmt.Errorf("equinix: unknown plan %q", slug)
}
