// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package linode

import (
	"context"
	"errors"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// TestEstimateMonthlyCostUSD_UsesContextFetcher verifies the
// ADR 0016 §"Pricing seam" injection: when a StaticFetcher is on
// the context, EstimateMonthlyCostUSD must read prices from it
// (not from the live Linode catalog) and the resulting math must
// match (rate × MonthlyHours × replicas).
func TestEstimateMonthlyCostUSD_UsesContextFetcher(t *testing.T) {
	const (
		region   = "us-east"
		machine  = "g6-standard-2"
		hourly   = 0.05 // $0.05/hr → $36.50/mo
		expected = hourly * pricing.MonthlyHours
	)

	stub := pricing.StaticFetcher{
		"linode/" + region + "/" + machine: hourly,
	}
	ctx := pricing.WithFetcher(context.Background(), stub)

	cfg := &config.Config{
		ControlPlaneMachineCount: "1",
		WorkerMachineCount:       "2",
	}
	cfg.Providers.Linode.Region = region
	cfg.Providers.Linode.ControlPlaneType = machine
	cfg.Providers.Linode.NodeType = machine

	p := &Provider{}
	est, err := p.EstimateMonthlyCostUSD(ctx, cfg)
	if err != nil {
		t.Fatalf("EstimateMonthlyCostUSD: %v", err)
	}

	wantTotal := expected * 3 // 1 cp + 2 workers
	if est.TotalUSDMonthly != wantTotal {
		t.Errorf("TotalUSDMonthly = %v, want %v (=%v × 3)", est.TotalUSDMonthly, wantTotal, expected)
	}
	if len(est.Items) != 2 {
		t.Errorf("len(Items) = %d, want 2 (cp + workers)", len(est.Items))
	}
	if est.Items[0].Qty != 1 || est.Items[0].UnitUSDMonthly != expected {
		t.Errorf("cp item = %+v, want Qty=1 Unit=%v", est.Items[0], expected)
	}
	if est.Items[1].Qty != 2 || est.Items[1].UnitUSDMonthly != expected {
		t.Errorf("worker item = %+v, want Qty=2 Unit=%v", est.Items[1], expected)
	}
}

// TestEstimateMonthlyCostUSD_StaticFetcherMissReturnsErrNotApplicable
// confirms that a StaticFetcher miss (no entry for the requested SKU)
// surfaces as ErrNotApplicable from the provider — the ADR-0016
// "no fabricated numbers" guarantee carries through to test paths.
func TestEstimateMonthlyCostUSD_StaticFetcherMissReturnsErrNotApplicable(t *testing.T) {
	ctx := pricing.WithFetcher(context.Background(), pricing.StaticFetcher{})
	cfg := &config.Config{ControlPlaneMachineCount: "1"}
	cfg.Providers.Linode.Region = "us-east"
	cfg.Providers.Linode.ControlPlaneType = "g6-standard-2"

	p := &Provider{}
	_, err := p.EstimateMonthlyCostUSD(ctx, cfg)
	if err == nil {
		t.Fatal("expected error from missing StaticFetcher entry, got nil")
	}
	if !errors.Is(err, provider.ErrNotApplicable) {
		t.Errorf("err = %v, want wrap of provider.ErrNotApplicable", err)
	}
}
