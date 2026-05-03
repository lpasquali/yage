// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cost_test

import (
	"context"
	"sync"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/pricing"

	// Side-effect registration: cost.Stream*/Compare* iterates the
	// provider registry, so the test needs at least one registered
	// implementation. linode is the smallest cloud provider whose cost
	// path is fully Fetcher-routed (no bespoke per-vendor helpers in
	// the priced rows), making it the cleanest fixture for the
	// context-isolation property.
	_ "github.com/lpasquali/yage/internal/provider/linode"
)

// TestStreamWithFilter_PropagatesContextFetcher verifies that two
// concurrent StreamWithFilter calls carrying different StaticFetchers
// on their contexts do not see each other's data — the parallel-test
// safety property gated by issue #197 / ADR 0016 §"Pricing seam".
//
// Construction: each goroutine builds a StaticFetcher with a unique
// hourly rate for the same (vendor, region, sku) triple, drives
// StreamWithFilter with cost.ScopeCloudOnly, and asserts every priced row's
// totals were computed from its own rate (never the sibling's).
//
// Linode is the single cost.ScopeCloudOnly provider whose EstimateMonthlyCostUSD
// is mostly Fetcher-routed (no bespoke per-vendor helpers in the hot
// path), so it is the only row guaranteed to be priced from the stub.
// Other providers are allowed to error out with ErrNotApplicable when
// their bespoke helpers (AWSEKSControlPlaneUSDPerMonth, …) hit the
// real network and fail; what matters is that the priced rows reflect
// the per-context rate, not a leak from the sibling context.
func TestStreamWithFilter_PropagatesContextFetcher(t *testing.T) {
	const (
		region = "us-east"
		sku    = "g6-standard-2"
	)

	cfg := func() *config.Config {
		c := &config.Config{
			ControlPlaneMachineCount: "1",
			WorkerMachineCount:       "0",
			InfraProvider:            "linode",
			SkipProviders:            "aws,azure,gcp,hetzner,digitalocean,oci,ibmcloud",
		}
		c.Providers.Linode.Region = region
		c.Providers.Linode.ControlPlaneType = sku
		c.Providers.Linode.NodeType = sku
		return c
	}

	run := func(rate float64) float64 {
		stub := pricing.StaticFetcher{"linode/" + region + "/" + sku: rate}
		ctx := pricing.WithFetcher(context.Background(), stub)
		ch := make(chan cost.CloudCost, 16)
		cost.StreamWithFilter(ctx, cfg(), cost.ScopeCloudOnly, nil, ch)
		var got float64
		for row := range ch {
			if row.ProviderName != "linode" {
				continue
			}
			if row.Err != nil {
				t.Errorf("rate=%v linode row err: %v", rate, row.Err)
				continue
			}
			got = row.Estimate.TotalUSDMonthly
		}
		return got
	}

	var wg sync.WaitGroup
	type result struct {
		rate, total float64
	}
	results := make(chan result, 2)

	for _, rate := range []float64{0.05, 0.99} {
		rate := rate
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- result{rate: rate, total: run(rate)}
		}()
	}
	wg.Wait()
	close(results)

	for r := range results {
		want := r.rate * pricing.MonthlyHours // 1 cp × hours × rate
		if r.total != want {
			t.Errorf("rate=%v: total=%v want %v (cross-context leak?)", r.rate, r.total, want)
		}
	}
}

// TestCompareWithFilter_AcceptsContext is a smoke test confirming the
// signature accepts a ctx and propagates it. With a StaticFetcher on
// the context, the linode row is priced from the stub while skipped
// providers are filtered out.
func TestCompareWithFilter_AcceptsContext(t *testing.T) {
	const region = "us-east"
	const sku = "g6-standard-2"
	stub := pricing.StaticFetcher{"linode/" + region + "/" + sku: 0.10}
	ctx := pricing.WithFetcher(context.Background(), stub)

	cfg := &config.Config{
		ControlPlaneMachineCount: "1",
		InfraProvider:            "linode",
		SkipProviders:            "aws,azure,gcp,hetzner,digitalocean,oci,ibmcloud",
	}
	cfg.Providers.Linode.Region = region
	cfg.Providers.Linode.ControlPlaneType = sku
	cfg.Providers.Linode.NodeType = sku

	rows := cost.CompareWithFilter(ctx, cfg, cost.ScopeCloudOnly, nil)
	var found bool
	for _, r := range rows {
		if r.ProviderName == "linode" && r.Err == nil {
			found = true
			want := 0.10 * pricing.MonthlyHours
			if r.Estimate.TotalUSDMonthly != want {
				t.Errorf("linode total = %v, want %v", r.Estimate.TotalUSDMonthly, want)
			}
		}
	}
	if !found {
		t.Errorf("no priced linode row in %d results: %+v", len(rows), rows)
	}
}
