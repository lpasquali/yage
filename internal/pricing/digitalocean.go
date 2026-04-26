package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// DigitalOcean Cloud API — token-required.
//   GET https://api.digitalocean.com/v2/sizes
//
// Returns every droplet size with price_monthly (USD) and a regions
// list. We pick the size by slug ("s-2vcpu-4gb", "g-2vcpu-8gb", etc.)
// and verify the region is supported.
//
// Token: DIGITALOCEAN_TOKEN (also accepted: YAGE_DO_TOKEN).
const doSizesURL = "https://api.digitalocean.com/v2/sizes?per_page=200"

type doFetcher struct{ httpClient *http.Client }

func init() {
	Register("digitalocean", &doFetcher{httpClient: &http.Client{Timeout: 15 * time.Second}})
}

// doToken returns the DigitalOcean API token used for pricing.
// Read order: cfg.Cost.Credentials → env-var fallback. See §16.
func doToken() string {
	if creds.DigitalOceanToken != "" {
		return creds.DigitalOceanToken
	}
	if v := os.Getenv("YAGE_DO_TOKEN"); v != "" {
		return v
	}
	return os.Getenv("DIGITALOCEAN_TOKEN")
}

type doSize struct {
	Slug         string   `json:"slug"`
	PriceMonthly float64  `json:"price_monthly"`
	PriceHourly  float64  `json:"price_hourly"`
	Regions      []string `json:"regions"`
	Available    bool     `json:"available"`
}

type doSizesResp struct {
	Sizes []doSize `json:"sizes"`
}

func (d *doFetcher) Fetch(sku, region string) (Item, error) {
	token := doToken()
	if token == "" {
		return Item{}, fmt.Errorf("digitalocean: DIGITALOCEAN_TOKEN not set")
	}
	req, _ := http.NewRequest("GET", doSizesURL, nil)
	req.Header.Set("User-Agent", "yage/pricing")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("digitalocean: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Item{}, fmt.Errorf("digitalocean: HTTP %d", resp.StatusCode)
	}
	var dr doSizesResp
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return Item{}, fmt.Errorf("digitalocean decode: %w", err)
	}
	for _, s := range dr.Sizes {
		if s.Slug != sku {
			continue
		}
		if !s.Available {
			return Item{}, fmt.Errorf("digitalocean: %s not currently available", sku)
		}
		regionOK := false
		for _, r := range s.Regions {
			if r == region {
				regionOK = true
				break
			}
		}
		if !regionOK {
			return Item{}, fmt.Errorf("digitalocean: %s not in region %s", sku, region)
		}
		return Item{
			USDPerHour:  s.PriceHourly,
			USDPerMonth: s.PriceMonthly,
			FetchedAt:   time.Now(),
		}, nil
	}
	return Item{}, fmt.Errorf("digitalocean: unknown slug %q", sku)
}
