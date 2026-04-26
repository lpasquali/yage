// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package provider

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

// approxEqual reports whether two USD figures match to within
// 0.005 USD (half a cent). The TCO math uses constants like
// 30.4375 days/month so exact equality on floats would be brittle
// against compiler-version drift; half-a-cent tolerance is far
// finer than any rounding the orchestrator displays.
func approxEqual(a, b float64) bool { return math.Abs(a-b) < 0.005 }

// TestTCOEstimate_NoHardwareCost_ReturnsErrNotApplicable: without
// --hardware-cost-usd there's nothing to amortize. TCOEstimate
// must wrap ErrNotApplicable so the orchestrator's existing
// "estimate unavailable" path catches it.
func TestTCOEstimate_NoHardwareCost_ReturnsErrNotApplicable(t *testing.T) {
	for _, capex := range []float64{0, -100} {
		cfg := &config.Config{HardwareCostUSD: capex}
		_, err := TCOEstimate(cfg, "proxmox")
		if err == nil {
			t.Errorf("TCOEstimate(capex=%v) returned nil err, want wrapped ErrNotApplicable", capex)
			continue
		}
		if !errors.Is(err, ErrNotApplicable) {
			t.Errorf("TCOEstimate(capex=%v) err = %v, doesn't wrap ErrNotApplicable", capex, err)
		}
		if !strings.Contains(err.Error(), "proxmox") {
			t.Errorf("TCOEstimate(capex=%v) err = %v, missing provider label", capex, err)
		}
	}
}

// TestTCOEstimate_CapexOnly: with capex but no watts/support, the
// only line item is the amortized hardware capex. Default useful
// life is 5 years per the implementation.
func TestTCOEstimate_CapexOnly(t *testing.T) {
	cfg := &config.Config{HardwareCostUSD: 6000} // $6000 / (5y × 12) = $100/mo
	got, err := TCOEstimate(cfg, "proxmox")
	if err != nil {
		t.Fatalf("TCOEstimate err = %v", err)
	}
	if !approxEqual(got.TotalUSDMonthly, 100) {
		t.Errorf("TotalUSDMonthly = %v, want ~100", got.TotalUSDMonthly)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1 (capex only)", len(got.Items))
	}
	if !strings.Contains(got.Items[0].Name, "amortized capex") {
		t.Errorf("Items[0].Name = %q, want one mentioning amortized capex", got.Items[0].Name)
	}
	// Note must surface the "no watts" branch since we didn't pass any.
	if !strings.Contains(got.Note, "No --hardware-watts") {
		t.Errorf("Note doesn't mention missing watts: %q", got.Note)
	}
	if !strings.Contains(got.Note, "No --hardware-support-usd-month") {
		t.Errorf("Note doesn't mention missing support: %q", got.Note)
	}
}

// TestTCOEstimate_CustomUsefulLife: --hardware-useful-life-years
// changes the amortization denominator.
func TestTCOEstimate_CustomUsefulLife(t *testing.T) {
	cfg := &config.Config{HardwareCostUSD: 12000, HardwareUsefulLifeYears: 10}
	// 12000 / (10y × 12) = $100/mo
	got, _ := TCOEstimate(cfg, "vsphere")
	if !approxEqual(got.TotalUSDMonthly, 100) {
		t.Errorf("10y amortization total = %v, want ~100", got.TotalUSDMonthly)
	}
}

// TestTCOEstimate_FullStack: capex + watts + support all wired.
// Three line items, sum matches per-item math.
func TestTCOEstimate_FullStack(t *testing.T) {
	cfg := &config.Config{
		HardwareCostUSD:         6000, // $100/mo capex (5y default)
		HardwareWatts:           400,  // 0.4kW × 24 × 30.4375 × $0.15 = ~$43.83/mo
		HardwareKWHRateUSD:      0.15,
		HardwareSupportUSDMonth: 25, // operator flat
	}
	got, err := TCOEstimate(cfg, "proxmox")
	if err != nil {
		t.Fatalf("TCOEstimate err = %v", err)
	}
	if len(got.Items) != 3 {
		t.Fatalf("len(Items) = %d, want 3 (capex + power + support)", len(got.Items))
	}

	// Power line: 0.4 × 24 × 30.4375 × 0.15
	wantPower := 0.4 * 24.0 * 30.4375 * 0.15
	wantTotal := 100.0 + wantPower + 25.0
	if !approxEqual(got.TotalUSDMonthly, wantTotal) {
		t.Errorf("TotalUSDMonthly = %v, want ~%v", got.TotalUSDMonthly, wantTotal)
	}

	// Item 1 should be capex
	if !strings.Contains(got.Items[0].Name, "amortized capex") {
		t.Errorf("Items[0] = %q, expected capex first", got.Items[0].Name)
	}
	if !strings.Contains(got.Items[1].Name, "Electricity") {
		t.Errorf("Items[1] = %q, expected electricity second", got.Items[1].Name)
	}
	if !strings.Contains(got.Items[2].Name, "Support") {
		t.Errorf("Items[2] = %q, expected support third", got.Items[2].Name)
	}

	// Note should NOT mention missing watts/support since both are present.
	if strings.Contains(got.Note, "No --hardware-watts") {
		t.Errorf("Note shouldn't mention missing watts when watts > 0: %q", got.Note)
	}
	if strings.Contains(got.Note, "No --hardware-support") {
		t.Errorf("Note shouldn't mention missing support when support > 0: %q", got.Note)
	}
}

// TestTCOEstimate_DefaultKWHRate: when --hardware-kwh-rate-usd is
// omitted, the implementation falls back to $0.15/kWh.
func TestTCOEstimate_DefaultKWHRate(t *testing.T) {
	cfg := &config.Config{
		HardwareCostUSD: 6000,
		HardwareWatts:   1000, // 1kW
		// HardwareKWHRateUSD intentionally zero — should default to 0.15
	}
	got, _ := TCOEstimate(cfg, "proxmox")
	// Power: 1.0 × 24 × 30.4375 × 0.15 = ~109.575
	wantPower := 1.0 * 24.0 * 30.4375 * 0.15
	wantTotal := 100.0 + wantPower
	if !approxEqual(got.TotalUSDMonthly, wantTotal) {
		t.Errorf("default kWh rate total = %v, want ~%v", got.TotalUSDMonthly, wantTotal)
	}
}

// TestTCOEstimate_NegativeWattsTreatedAsZero: a negative HardwareWatts
// shouldn't produce a negative power line. The implementation guards
// with `if cfg.HardwareWatts > 0` so the line is omitted, not
// negative.
func TestTCOEstimate_NegativeWattsTreatedAsZero(t *testing.T) {
	cfg := &config.Config{
		HardwareCostUSD: 6000,
		HardwareWatts:   -100, // garbage input
	}
	got, _ := TCOEstimate(cfg, "proxmox")
	if len(got.Items) != 1 {
		t.Errorf("negative watts produced %d items, want 1 (capex only)", len(got.Items))
	}
	if !approxEqual(got.TotalUSDMonthly, 100) {
		t.Errorf("negative watts total = %v, want ~100 (capex only)", got.TotalUSDMonthly)
	}
}
