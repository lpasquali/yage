// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package pricing fetches live monthly pricing from each cloud
// vendor's FinOps / billing API. No hardcoded money numbers — when
// a vendor's API is unreachable, callers get ErrUnavailable and the
// orchestrator surfaces "cost estimate unavailable" rather than a
// stale fabricated number.
//
// Each vendor implementation lives in its own file:
//
//	aws.go      — AWS Bulk Pricing JSON. Files are hosted on the
//	              pricing.us-east-1.amazonaws.com bucket, but the
//	              path encodes the priced region:
//	                /offers/v1.0/aws/AmazonEC2/current/<region>/index.json
//	              e.g. .../eu-west-1/index.json returns Frankfurt
//	              prices, .../us-east-1/index.json returns N.Virginia
//	              prices. The host being us-east-1 is just where the
//	              static JSON lives; it does NOT constrain priced region.
//	azure.go    — Azure Retail Prices API (unauth, region in $filter)
//	gcp.go      — GCP Cloud Billing Catalog API (needs API key)
//	hetzner.go  — Hetzner Cloud API server_types (unauth, region in
//	              prices[] array per server type)
//
// Pricing tiers (dev / prod / enterprise) describing service-overhead
// SHAPE (NAT count, LB count, egress GB, log GB) live in each
// provider's cost.go and stay constant — those are user-visible
// architecture choices, not numbers that drift with AWS/Azure list
// price updates. Only $/hour, $/GB-month, $/instance-hour values
// come from this package.
package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrUnavailable is returned when a vendor pricing API can't be
// reached, the cache is stale and refresh failed, or the SKU isn't
// in the live catalog. Callers MUST surface "cost estimate
// unavailable" rather than substitute a number.
var ErrUnavailable = errors.New("pricing: vendor API unreachable and no fresh cache")

// DefaultTTL is how long a cached price entry is considered fresh.
// Cloud list prices change infrequently (months); 24h is a sensible
// default that keeps the dry-run plan from re-fetching on every run.
const DefaultTTL = 24 * time.Hour

// Item is one priced SKU pulled from a vendor API.
//
// Currency model: every vendor has a "datacenter currency" — the
// currency in which their pricing team publishes the canonical
// number. AWS/Azure/GCP/DO/Linode/IBM/OCI publish in USD; Hetzner
// in EUR. Whatever that currency is, the Fetcher fills NativeAmount
// with the unaltered figure and NativeCurrency with the ISO code.
//
// USDPerHour / USDPerMonth are also filled with the USD-equivalent
// (computed via the live FX rate when NativeCurrency != "USD") so
// cross-vendor sort and sums work in one canonical unit. Display
// code prefers NativeAmount when NativeCurrency matches the active
// taller — that avoids a round-trip through FX and surfaces the
// vendor's published list price exactly.
type Item struct {
	Vendor         string  // "aws", "azure", "gcp", "hetzner"
	SKU            string  // "t3.medium", "Standard_D2s_v3", "n2-standard-2", "cx23"
	Region         string  // "us-east-1", "eastus", "us-central1", "fsn1"
	USDPerHour     float64 // 0 when only monthly is meaningful
	USDPerMonth    float64 // = USDPerHour × 730 unless the vendor caps differently (Hetzner)
	NativeCurrency string  // ISO-4217 code of the vendor's datacenter currency; "" treated as "USD"
	NativeAmount   float64 // monthly amount in NativeCurrency; 0 when not separately tracked
	FetchedAt      time.Time
}

// VendorFetcher fetches a single SKU's price from one vendor's
// API. Each per-vendor implementation lives in its own file
// (aws.go, azure.go, gcp.go, …) and self-registers via Register().
//
// VendorFetcher is the *vendor-level* plug, not the public ctx-scoped
// pricing seam — see the package-level [Fetcher] interface and
// [WithFetcher] / [FetcherFrom] for the ADR 0016 §"Pricing seam"
// determinism contract used by the xapiri test harness.
type VendorFetcher interface {
	Fetch(sku, region string) (Item, error)
}

var (
	mu       sync.RWMutex
	fetchers = map[string]VendorFetcher{}
	// disabled toggles all live fetches off; useful for tests + CI
	// where reaching the public APIs is undesirable.
	disabled = os.Getenv("YAGE_PRICING_DISABLED") == "true"
)

// Register makes a vendor fetcher available to Fetch().
// Implementations call this from init().
func Register(vendor string, f VendorFetcher) {
	mu.Lock()
	defer mu.Unlock()
	fetchers[vendor] = f
}

// Fetch returns the live (or cache-fresh) price for sku in region
// from vendor's API. Returns ErrUnavailable when no fetcher is
// registered, the API is unreachable, the cache is stale and the
// refresh failed, the SKU isn't in the catalog, or the orchestrator
// is in airgapped mode (cfg.Airgapped → pricing.SetAirgapped(true)).
func Fetch(vendor, sku, region string) (Item, error) {
	if disabled || airgapped {
		return Item{}, ErrUnavailable
	}
	if cached, ok := readCache(vendor, sku, region); ok {
		return cached, nil
	}
	mu.RLock()
	f, ok := fetchers[vendor]
	mu.RUnlock()
	if !ok {
		return Item{}, fmt.Errorf("%w: no fetcher for %q", ErrUnavailable, vendor)
	}
	item, err := f.Fetch(sku, region)
	if err != nil {
		return Item{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	item.Vendor = vendor
	item.SKU = sku
	item.Region = region
	if item.FetchedAt.IsZero() {
		item.FetchedAt = time.Now()
	}
	writeCache(item)
	return item, nil
}

// FetchMany batches a set of (sku, region) lookups for a single
// vendor. Preferred when a single estimate touches several SKUs —
// vendors like AWS expose bulk JSON; this lets the fetcher pull
// once and parse selectively.
func FetchMany(vendor string, queries []Query) ([]Item, error) {
	out := make([]Item, 0, len(queries))
	for _, q := range queries {
		it, err := Fetch(vendor, q.SKU, q.Region)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// Query is one (sku, region) pair for FetchMany.
type Query struct{ SKU, Region string }

// --- cache: ~/.cache/yage/pricing/<vendor>-<sku>-<region>.json ---

func cacheDir() string {
	if d := os.Getenv("YAGE_PRICING_CACHE"); d != "" {
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "yage", "pricing")
	}
	return filepath.Join(os.TempDir(), "yage-pricing")
}

func cachePath(vendor, sku, region string) string {
	// Sanitize / collapse path-unsafe chars. SKUs like "Standard_D2s_v3"
	// or "n2-standard-2" are safe; regions too. We keep the raw value.
	return filepath.Join(cacheDir(), vendor+"."+sku+"."+region+".json")
}

func readCache(vendor, sku, region string) (Item, bool) {
	path := cachePath(vendor, sku, region)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Item{}, false
	}
	var it Item
	if err := json.Unmarshal(raw, &it); err != nil {
		return Item{}, false
	}
	if time.Since(it.FetchedAt) > DefaultTTL {
		return Item{}, false
	}
	return it, true
}

func writeCache(it Item) {
	if err := os.MkdirAll(cacheDir(), 0o755); err != nil {
		return
	}
	raw, err := json.MarshalIndent(it, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(cachePath(it.Vendor, it.SKU, it.Region), raw, 0o644)
}

// MonthlyHours converts hourly to monthly using the conventional
// 730-hour month (365.25 ÷ 12). Vendors that price differently
// (Hetzner caps per month) populate USDPerMonth directly.
const MonthlyHours = 730.0
