// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

// DigitalOcean Cloud API — token-required.
// Uses the official godo SDK; the underlying HTTP client is built
// from &http.Client{} (nil Transport) so it inherits whatever
// http.DefaultTransport is at call time — including the airgap shim
// applied by defaulttransport_airgap_test.go.
//
// Token: DIGITALOCEAN_TOKEN (also accepted: YAGE_DO_TOKEN).

type doFetcher struct{}

func init() {
	Register("digitalocean", &doFetcher{})
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

// newGodoClient creates a godo Client authenticated with token. The
// base HTTP transport is &http.Client{} (nil Transport) so it
// inherits http.DefaultTransport — keeping the airgap shim effective.
// oauth2.Transport wraps this base client to inject the Bearer token.
func newGodoClient(token string) *godo.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	// Inject our nil-Transport HTTP client as the oauth2 base client so
	// that any request made by godo goes through http.DefaultTransport.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{})
	oauthClient := oauth2.NewClient(ctx, ts)
	return godo.NewClient(oauthClient)
}

// doDatabaseOptionsResp models /v2/databases/options. The endpoint
// nests pricing per engine (pg, mysql, redis, …) under
// options.<engine>.layouts[]: each layout is a node-count/size
// combination with its own price_monthly and price_hourly. Single-
// node Postgres uses num_nodes=1.
//
// The godo SDK's DatabaseLayout only carries slug strings (no prices),
// so we parse this response directly from the raw JSON body via the
// godo client's authenticated HTTP transport.
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
//
// /v2/databases/options returns richer pricing fields not modeled by
// godo's DatabaseLayout struct; we parse the raw response body via
// the godo-authenticated HTTP client to preserve the same JSON shape.
func DOManagedPostgresUSDPerMonth(size string) (float64, error) {
	token := doToken()
	if token == "" {
		if v := doManagedPostgresFallbackUSDPerMonth(size); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("digitalocean db: no token and no fallback for %q", size)
	}
	client := newGodoClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := client.NewRequest(ctx, http.MethodGet, "v2/databases/options", nil)
	if err != nil {
		if v := doManagedPostgresFallbackUSDPerMonth(size); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("digitalocean db: %w", err)
	}
	var rawResp doDatabaseOptionsResp
	resp, err := client.Do(ctx, req, &rawResp)
	if err != nil {
		if v := doManagedPostgresFallbackUSDPerMonth(size); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("digitalocean db: %w", err)
	}
	defer resp.Body.Close()

	// godo's Do() already decoded the JSON body into rawResp.
	// rawResp may be empty if the endpoint shape changed; check pg key.
	pg, ok := rawResp.Options["pg"]
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
	client := newGodoClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Fetch all sizes (godo handles pagination).
	sizes, _, err := client.Sizes.List(ctx, &godo.ListOptions{PerPage: 200})
	if err != nil {
		return Item{}, fmt.Errorf("digitalocean: %w", err)
	}
	for _, s := range sizes {
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
