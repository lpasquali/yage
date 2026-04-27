// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

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

// doDatabaseOptionsResp models /v2/databases/options. The endpoint
// nests pricing per engine (pg, mysql, redis, …) under
// options.<engine>.layouts[]: each layout is a node-count/size
// combination with its own price_monthly and price_hourly. Single-
// node Postgres uses num_nodes=1.
type doDatabaseOptionsResp struct {
	Options map[string]struct {
		Layouts []struct {
			NumNodes int `json:"num_nodes"`
			Sizes    []struct {
				Slug         string  `json:"slug"`
				PriceMonthly float64 `json:"price_monthly"`
				PriceHourly  float64 `json:"price_hourly"`
			} `json:"sizes"`
		} `json:"layouts"`
	} `json:"options"`
}

// doManagedPostgresFallbackUSDPerMonth returns DO's published list
// price for two common managed-Postgres sizes when the API isn't
// reachable (no token, anon probes blocked). Headline rates from
// https://www.digitalocean.com/pricing/databases.
func doManagedPostgresFallbackUSDPerMonth(size string) float64 {
	switch size {
	case "db-s-1vcpu-1gb":
		return 15.0
	case "db-s-2vcpu-4gb":
		return 60.0
	}
	return 0
}

// DOManagedPostgresUSDPerMonth returns the live monthly $/instance
// rate for a DO Managed Postgres node-size slug (single-node
// layout). Falls back to the public list price when the API token
// is missing or the request fails.
func DOManagedPostgresUSDPerMonth(size string) (float64, error) {
	token := doToken()
	if token == "" {
		if v := doManagedPostgresFallbackUSDPerMonth(size); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("digitalocean db: no token and no fallback for %q", size)
	}
	c := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.digitalocean.com/v2/databases/options", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := c.Do(req)
	if err != nil {
		if v := doManagedPostgresFallbackUSDPerMonth(size); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("digitalocean db: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if v := doManagedPostgresFallbackUSDPerMonth(size); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("digitalocean db: HTTP %d", resp.StatusCode)
	}
	var dr doDatabaseOptionsResp
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		if v := doManagedPostgresFallbackUSDPerMonth(size); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("digitalocean db decode: %w", err)
	}
	pg, ok := dr.Options["pg"]
	if !ok {
		if v := doManagedPostgresFallbackUSDPerMonth(size); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("digitalocean db: no pg options")
	}
	for _, layout := range pg.Layouts {
		if layout.NumNodes != 1 {
			continue
		}
		for _, s := range layout.Sizes {
			if s.Slug == size && s.PriceMonthly > 0 {
				return s.PriceMonthly, nil
			}
		}
	}
	if v := doManagedPostgresFallbackUSDPerMonth(size); v > 0 {
		return v, nil
	}
	return 0, fmt.Errorf("digitalocean db: size %q not found", size)
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