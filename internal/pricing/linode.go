// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Linode/Akamai catalog API — auth-free for type listing.
//   GET https://api.linode.com/v4/linode/types
// Returns every type with price.monthly + price.hourly (USD), plus
// the per-region price overrides (region_prices[]). When the region
// has an override we honor it; otherwise the global price applies.
const linodeTypesURL = "https://api.linode.com/v4/linode/types?page_size=200"

type linodeFetcher struct{ httpClient *http.Client }

func init() {
	Register("linode", &linodeFetcher{httpClient: &http.Client{Timeout: 15 * time.Second}})
}

type linodePrice struct {
	Hourly  float64 `json:"hourly"`
	Monthly float64 `json:"monthly"`
}

type linodeRegionPrice struct {
	ID      string      `json:"id"`
	Hourly  float64     `json:"hourly"`
	Monthly float64     `json:"monthly"`
}

type linodeType struct {
	ID            string              `json:"id"`
	Label         string              `json:"label"`
	Price         linodePrice         `json:"price"`
	RegionPrices  []linodeRegionPrice `json:"region_prices"`
}

type linodeTypesResp struct {
	Data []linodeType `json:"data"`
}

func (l *linodeFetcher) Fetch(typeID, region string) (Item, error) {
	req, _ := http.NewRequest("GET", linodeTypesURL, nil)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := l.httpClient.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("linode: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Item{}, fmt.Errorf("linode: HTTP %d", resp.StatusCode)
	}
	var lr linodeTypesResp
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return Item{}, fmt.Errorf("linode decode: %w", err)
	}
	for _, t := range lr.Data {
		if t.ID != typeID {
			continue
		}
		hourly := t.Price.Hourly
		monthly := t.Price.Monthly
		// Honor per-region price overrides when the type has them.
		for _, rp := range t.RegionPrices {
			if rp.ID == region {
				hourly = rp.Hourly
				monthly = rp.Monthly
				break
			}
		}
		return Item{
			USDPerHour:  hourly,
			USDPerMonth: monthly,
			FetchedAt:   time.Now(),
		}, nil
	}
	return Item{}, fmt.Errorf("linode: unknown type %q", typeID)
}

// linodeManagedPostgresFallbackUSDPerMonth returns Linode/Akamai's
// published list price for the standard Managed Postgres node sizes
// when /v4/databases/types is unreachable.
func linodeManagedPostgresFallbackUSDPerMonth(typeClass string) float64 {
	switch typeClass {
	case "g6-nanode-1":
		return 19.0
	case "g6-standard-2":
		return 90.0
	}
	return 0
}

type linodeDBType struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Engines map[string][]struct {
		Quantity int     `json:"quantity"`
		Price    struct {
			Monthly float64 `json:"monthly"`
			Hourly  float64 `json:"hourly"`
		} `json:"price"`
	} `json:"engines"`
}

type linodeDBTypesResp struct {
	Data []linodeDBType `json:"data"`
}

// LinodeManagedPostgresUSDPerMonth returns the live monthly USD
// rate for the named Linode managed-Postgres type (e.g.
// "g6-nanode-1", "g6-standard-2"), reading the single-node layout
// from /v4/databases/types. The endpoint is anonymous. Falls back
// to public list prices when the API isn't reachable.
func LinodeManagedPostgresUSDPerMonth(typeClass string) (float64, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.linode.com/v4/databases/types?page_size=200", nil)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := c.Do(req)
	if err != nil {
		if v := linodeManagedPostgresFallbackUSDPerMonth(typeClass); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("linode db: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if v := linodeManagedPostgresFallbackUSDPerMonth(typeClass); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("linode db: HTTP %d", resp.StatusCode)
	}
	var lr linodeDBTypesResp
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		if v := linodeManagedPostgresFallbackUSDPerMonth(typeClass); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("linode db decode: %w", err)
	}
	for _, t := range lr.Data {
		if t.ID != typeClass {
			continue
		}
		// Prefer the postgresql engine's single-node price; if that
		// engine slot is missing fall back to whatever engine first
		// reports a single-node price.
		preferred := []string{"postgresql", "postgres"}
		for _, engKey := range preferred {
			for k, layouts := range t.Engines {
				if !strings.EqualFold(k, engKey) {
					continue
				}
				for _, l := range layouts {
					if l.Quantity == 1 && l.Price.Monthly > 0 {
						return l.Price.Monthly, nil
					}
				}
			}
		}
		for _, layouts := range t.Engines {
			for _, l := range layouts {
				if l.Quantity == 1 && l.Price.Monthly > 0 {
					return l.Price.Monthly, nil
				}
			}
		}
	}
	if v := linodeManagedPostgresFallbackUSDPerMonth(typeClass); v > 0 {
		return v, nil
	}
	return 0, fmt.Errorf("linode db: type %q not found", typeClass)
}