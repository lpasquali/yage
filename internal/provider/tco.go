// Package provider — shared helpers used by self-hosted providers
// (Proxmox, vSphere) that don't have a vendor pricing API. The TCO
// path here is the only "monetary number" they emit, and every input
// is supplied by the operator (capex, useful life, watts, kWh rate,
// flat monthly support) — yage never invents a number.
package provider

import (
	"errors"
	"fmt"

	"github.com/lpasquali/yage/internal/config"
)

// TCOEstimate computes an amortized monthly cost for a self-hosted
// cluster from the operator-supplied capex + opex inputs. Returns
// ErrNotApplicable when no capex (HardwareCostUSD) was provided —
// without it there's nothing to amortize, and the orchestrator
// should display "estimate unavailable: supply --hardware-cost-usd".
//
// Math:
//   capex / month   = HardwareCostUSD / (UsefulLifeYears × 12)
//   power / month   = (Watts / 1000) × 24 × 30.4375 × KWHRateUSD
//   support / month = HardwareSupportUSDMonth (operator-supplied)
//   total           = sum
//
// 30.4375 is the average days/month over 4 years (730 hours / 24).
// Identical to the 730 used by cloud providers but expressed in
// days for clarity in the breakdown.
func TCOEstimate(cfg *config.Config, providerLabel string) (CostEstimate, error) {
	if cfg.HardwareCostUSD <= 0 {
		return CostEstimate{}, fmt.Errorf("%w: %s is self-hosted; pass --hardware-cost-usd (and optionally --hardware-watts / --hardware-kwh-rate-usd / --hardware-support-usd-month) to compute amortized monthly cost",
			ErrNotApplicable, providerLabel)
	}
	life := cfg.HardwareUsefulLifeYears
	if life <= 0 {
		life = 5
	}
	rate := cfg.HardwareKWHRateUSD
	if rate <= 0 {
		rate = 0.15
	}

	capexMonthly := cfg.HardwareCostUSD / (life * 12)
	powerMonthly := 0.0
	if cfg.HardwareWatts > 0 {
		powerMonthly = (cfg.HardwareWatts / 1000.0) * 24.0 * 30.4375 * rate
	}
	supportMonthly := cfg.HardwareSupportUSDMonth

	items := []CostItem{
		{
			Name:           fmt.Sprintf("Hardware amortized capex ($%.0f over %.1fy)", cfg.HardwareCostUSD, life),
			UnitUSDMonthly: capexMonthly,
			Qty:            1,
			SubtotalUSD:    capexMonthly,
		},
	}
	if powerMonthly > 0 {
		items = append(items, CostItem{
			Name:           fmt.Sprintf("Electricity (%.0fW @ $%.3f/kWh)", cfg.HardwareWatts, rate),
			UnitUSDMonthly: powerMonthly,
			Qty:            1,
			SubtotalUSD:    powerMonthly,
		})
	}
	if supportMonthly > 0 {
		items = append(items, CostItem{
			Name:           "Support / colo / licensing (operator-supplied flat)",
			UnitUSDMonthly: supportMonthly,
			Qty:            1,
			SubtotalUSD:    supportMonthly,
		})
	}
	total := capexMonthly + powerMonthly + supportMonthly
	note := fmt.Sprintf(
		"Self-hosted TCO (no vendor pricing API): capex amortized straight-line over %.1fy, "+
			"power at operator-supplied watts × kWh rate, plus any flat support figure.",
		life,
	)
	if cfg.HardwareWatts <= 0 {
		note += " (No --hardware-watts — electricity opex omitted.)"
	}
	if cfg.HardwareSupportUSDMonth <= 0 {
		note += " (No --hardware-support-usd-month — support/colo opex omitted.)"
	}
	return CostEstimate{
		TotalUSDMonthly: total,
		Items:           items,
		Note:            note,
	}, nil
}

// SelfHostedNotConfigured is the error sentinel callers can check
// for to detect "user didn't supply the TCO inputs". Returned wrapped
// from TCOEstimate when HardwareCostUSD is zero. Use errors.Is.
var SelfHostedNotConfigured = errors.New("self-hosted TCO not configured")
