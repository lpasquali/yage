// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/linode/linodego"
)

// Linode/Akamai catalog API — auth-free for type listing.
// Uses the official linodego SDK; the underlying HTTP client is
// &http.Client{} (nil Transport) so it inherits whatever
// http.DefaultTransport is at call time — including the airgap shim
// applied by defaulttransport_airgap_test.go.
//
// SDK docs: https://pkg.go.dev/github.com/linode/linodego

type linodeFetcher struct{}

func init() {
	Register("linode", &linodeFetcher{})
}

// newLinodeClient returns a linodego Client configured to use a
// nil-Transport HTTP client (inherits http.DefaultTransport). Linode
// type listing is anonymous (no auth token needed).
func newLinodeClient() linodego.Client {
	return linodego.NewClient(&http.Client{})
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

func (l *linodeFetcher) Fetch(typeID, region string) (Item, error) {
	client := newLinodeClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	types, err := client.ListTypes(ctx, &linodego.ListOptions{PageSize: 200})
	if err != nil {
		return Item{}, fmt.Errorf("linode: %w", err)
	}
	for _, t := range types {
		if t.ID != typeID {
			continue
		}
		var hourly, monthly float64
		if t.Price != nil {
			hourly = float64(t.Price.Hourly)
			monthly = float64(t.Price.Monthly)
		}
		// Honor per-region price overrides when the type has them.
		for _, rp := range t.RegionPrices {
			if rp.ID == region {
				hourly = float64(rp.Hourly)
				monthly = float64(rp.Monthly)
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

// LinodeManagedPostgresUSDPerMonth returns the live monthly USD
// rate for the named Linode managed-Postgres type (e.g.
// "g6-nanode-1", "g6-standard-2"), reading the single-node layout
// from /v4/databases/types. The endpoint is anonymous. Falls back
// to public list prices when the API isn't reachable.
func LinodeManagedPostgresUSDPerMonth(typeClass string) (float64, error) {
	client := newLinodeClient()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dbTypes, err := client.ListDatabaseTypes(ctx, &linodego.ListOptions{PageSize: 200})
	if err != nil {
		if v := linodeManagedPostgresFallbackUSDPerMonth(typeClass); v > 0 {
			return v, nil
		}
		return 0, fmt.Errorf("linode db: %w", err)
	}
	for _, t := range dbTypes {
		if t.ID != typeClass {
			continue
		}
		// Prefer the PostgreSQL engine's single-node price.
		for _, eng := range t.Engines.PostgreSQL {
			if eng.Quantity == 1 && eng.Price.Monthly > 0 {
				return float64(eng.Price.Monthly), nil
			}
		}
		// Fall back to MySQL single-node if no PostgreSQL entry.
		for _, eng := range t.Engines.MySQL {
			if eng.Quantity == 1 && eng.Price.Monthly > 0 {
				return float64(eng.Price.Monthly), nil
			}
		}
	}
	if v := linodeManagedPostgresFallbackUSDPerMonth(typeClass); v > 0 {
		return v, nil
	}
	return 0, fmt.Errorf("linode db: type %q not found", typeClass)
}
