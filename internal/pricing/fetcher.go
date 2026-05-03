// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"context"
	"fmt"
	"sync"
)

// Fetcher is the context-scoped pricing seam introduced by ADR 0016
// §"Pricing seam". It abstracts the package-global catalog lookup so
// parallel test scenarios can pin a frozen catalog (StaticFetcher)
// instead of racing on the per-vendor live fetchers registered via
// Register().
//
// Production code receives the package's defaultFetcher (which routes
// through the existing live fetchers + 24h disk cache). Tests inject
// a StaticFetcher (or any Fetcher implementation) on the context they
// pass to cost.Stream*/cost.Compare* and Provider.EstimateMonthlyCostUSD.
//
// The interface is intentionally narrow: Fetch covers every existing
// pricing.Fetch(vendor, sku, region) call site (most provider cost.go
// paths), and USDPerHour is a convenience for callers that only need
// the hourly rate. Bespoke per-vendor helpers (AWSEKSControlPlaneUSDPerMonth,
// AzureManagedDiskUSDPerGBMonth, …) remain package-level functions in
// this PR; their full migration onto Fetcher is tracked as follow-up
// per the issue #197 DoD scope.
type Fetcher interface {
	// Fetch returns the live (or cache-fresh) Item for (vendor, sku,
	// region). Mirrors the package-level pricing.Fetch contract:
	// returns ErrUnavailable wrapped with detail when no fetcher is
	// registered for the vendor, the API is unreachable, the cache
	// is stale and refresh failed, or the orchestrator is in
	// airgapped mode.
	Fetch(ctx context.Context, vendor, sku, region string) (Item, error)

	// USDPerHour returns the live USD-per-hour rate for (vendor, region,
	// sku). Convenience wrapper over Fetch for callers that only need
	// the hourly figure (the harness DSL's primary consumer). Returns
	// ErrUnavailable on the same conditions as Fetch.
	USDPerHour(ctx context.Context, vendor, region, sku string) (float64, error)
}

// StaticFetcher is a deterministic in-memory catalog used by tests.
// Keys take the form "vendor/region/sku"; values are the USD-per-hour
// rate for that triple. Fetch synthesises an Item with USDPerHour and
// USDPerMonth = USDPerHour × MonthlyHours so existing call sites that
// read it.USDPerMonth keep working unchanged.
//
// Lookup is exact: the same (vendor, region, sku) triple a provider's
// cost path passes must be present in the map, otherwise StaticFetcher
// returns ErrUnavailable wrapped with the missing key. This is by
// design — silent fallback would mask test/setup mismatches.
type StaticFetcher map[string]float64

// USDPerHour returns the rate stored for (vendor, region, sku) or
// ErrUnavailable if absent.
func (s StaticFetcher) USDPerHour(_ context.Context, vendor, region, sku string) (float64, error) {
	if rate, ok := s[staticKey(vendor, region, sku)]; ok {
		return rate, nil
	}
	return 0, fmt.Errorf("%w: StaticFetcher: no entry for %s/%s/%s", ErrUnavailable, vendor, region, sku)
}

// Fetch returns an Item synthesised from the StaticFetcher's stored
// hourly rate. USDPerMonth is computed via MonthlyHours so existing
// call sites that read .USDPerMonth keep working.
func (s StaticFetcher) Fetch(_ context.Context, vendor, sku, region string) (Item, error) {
	if rate, ok := s[staticKey(vendor, region, sku)]; ok {
		return Item{
			Vendor:      vendor,
			SKU:         sku,
			Region:      region,
			USDPerHour:  rate,
			USDPerMonth: rate * MonthlyHours,
		}, nil
	}
	return Item{}, fmt.Errorf("%w: StaticFetcher: no entry for %s/%s/%s", ErrUnavailable, vendor, region, sku)
}

// staticKey is the canonical map key shape for StaticFetcher. Kept
// private so callers cannot construct keys with a different separator
// and miss lookups.
func staticKey(vendor, region, sku string) string {
	return vendor + "/" + region + "/" + sku
}

// liveFetcher is the production Fetcher implementation. It delegates
// to the package-level Fetch function so the existing live fetchers,
// 24h disk cache, airgap short-circuit, and Register-based routing
// all keep working unchanged. Stateless; the singleton lives in
// defaultFetcher.
type liveFetcher struct{}

// Fetch routes through the existing package-level pricing.Fetch path
// (per-vendor live fetcher + 24h disk cache). Context is accepted for
// the interface contract but not yet propagated — the underlying
// vendor fetchers do not currently take a context. Future work tracked
// as a separate issue.
func (liveFetcher) Fetch(_ context.Context, vendor, sku, region string) (Item, error) {
	return Fetch(vendor, sku, region)
}

// USDPerHour delegates to Fetch and returns the hourly rate.
func (l liveFetcher) USDPerHour(ctx context.Context, vendor, region, sku string) (float64, error) {
	it, err := l.Fetch(ctx, vendor, sku, region)
	if err != nil {
		return 0, err
	}
	return it.USDPerHour, nil
}

// defaultFetcher is the singleton returned by FetcherFrom when the
// context carries no override. Allocated once via sync.Once so repeated
// FetcherFrom calls on a context without a fetcher are zero-allocation
// after the first.
var (
	defaultFetcherOnce sync.Once
	defaultFetcherInst Fetcher
)

// DefaultFetcher returns the production Fetcher singleton (live
// per-vendor catalog + 24h disk cache). Exported for callers that need
// to wrap or compose it (e.g. a test harness that wants to fall back to
// live data when a Static map misses).
func DefaultFetcher() Fetcher {
	defaultFetcherOnce.Do(func() {
		defaultFetcherInst = liveFetcher{}
	})
	return defaultFetcherInst
}

// fetcherCtxKey is the unexported context key under which a Fetcher
// override is stored. The unexported key prevents accidental
// cross-package clashes — only WithFetcher can write it.
type fetcherCtxKey struct{}

// WithFetcher returns a derived context that carries f as the active
// Fetcher. Callers (typically test harnesses, but also future per-call
// production overrides) pass the returned context through to
// cost.Stream*/cost.Compare* / Provider.EstimateMonthlyCostUSD.
//
// Passing a nil Fetcher returns the parent context unchanged so callers
// can `ctx = pricing.WithFetcher(ctx, maybeNil)` without a guard.
func WithFetcher(ctx context.Context, f Fetcher) context.Context {
	if f == nil {
		return ctx
	}
	return context.WithValue(ctx, fetcherCtxKey{}, f)
}

// FetcherFrom returns the Fetcher carried by ctx, or the production
// DefaultFetcher() singleton when ctx carries no override (or ctx is
// nil). Hot-path safe: the absent-override branch is one map lookup +
// type assertion + one sync.Once first-call cost.
func FetcherFrom(ctx context.Context) Fetcher {
	if ctx == nil {
		return DefaultFetcher()
	}
	if f, ok := ctx.Value(fetcherCtxKey{}).(Fetcher); ok && f != nil {
		return f
	}
	return DefaultFetcher()
}
