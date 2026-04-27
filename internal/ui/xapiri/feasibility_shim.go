// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// feasibility_shim.go — adapter between xapiri's simplified
// FeasibilityVerdict (✓ / ⚠ / ✗ / unchecked, displayed at step 5)
// and the richer feasibility.Verdict type.
//
// The shim owns the simplified verdict enum because xapiri does
// not need the per-provider table at the call site — step 5
// renders the rich Verdict separately for display, then asks
// runFeasibilityCheck for a single overall verdict that drives
// the loop-back / proceed decision.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/cluster/capacity"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/feasibility"
	"github.com/lpasquali/yage/internal/provider"
)

// FeasibilityVerdict mirrors the §23 vocabulary so callers do not
// need to import the feasibility package directly.
type FeasibilityVerdict int

const (
	// FeasibilityUnchecked means feasibilityCheck did not run.
	// Treated as a soft pass — the walkthrough warns at step 5
	// and lets the user proceed; the dry-run / preflight re-checks
	// later in the flow.
	FeasibilityUnchecked FeasibilityVerdict = iota
	FeasibilityComfortable
	FeasibilityTight
	FeasibilityInfeasible
)

func (v FeasibilityVerdict) symbol() string {
	switch v {
	case FeasibilityComfortable:
		return "✓"
	case FeasibilityTight:
		return "⚠"
	case FeasibilityInfeasible:
		return "✗"
	}
	return "?"
}

func (v FeasibilityVerdict) String() string {
	switch v {
	case FeasibilityComfortable:
		return "comfortable"
	case FeasibilityTight:
		return "tight"
	case FeasibilityInfeasible:
		return "infeasible"
	}
	return "unchecked"
}

// runFeasibilityCheck calls feasibility.Check and reduces the rich
// per-provider Verdict to a single overall verdict for xapiri's
// step-5 loop-back decision.
//
// Reduction: AbsoluteFloor exceeded → Infeasible. Otherwise pick
// the best PerProvider verdict (Comfortable wins over Tight wins
// over Infeasible). When PerProvider is empty (e.g., no priced
// providers in airgapped mode) → Unchecked.
func runFeasibilityCheck(cfg *config.Config) (FeasibilityVerdict, error) {
	v, err := feasibility.Check(cfg)
	if err != nil {
		if errors.Is(err, feasibility.ErrNotApplicable) {
			// No product shape on cfg yet — don't paint every provider row
			// as infeasible; the gate simply has nothing to score.
			return FeasibilityUnchecked, nil
		}
		return FeasibilityInfeasible, err
	}
	if v.AbsoluteFloor > 0 && cfg.BudgetUSDMonth > 0 && v.AbsoluteFloor > cfg.BudgetUSDMonth {
		return FeasibilityInfeasible, nil
	}
	if len(v.PerProvider) == 0 {
		return FeasibilityUnchecked, nil
	}
	best := FeasibilityInfeasible
	for _, pv := range v.PerProvider {
		switch pv.Verdict {
		case feasibility.Comfortable:
			if best > FeasibilityComfortable {
				best = FeasibilityComfortable
			}
		case feasibility.Tight:
			if best > FeasibilityTight {
				best = FeasibilityTight
			}
		}
	}
	return best, nil
}

// runFeasibilityCheckOnPrem is the on-prem analogue. Capacity-
// bound rather than budget-bound: needs a host inventory snapshot,
// fetched via Provider.Inventory then translated into the
// capacity.HostCapacity that CheckOnPrem expects.
func runFeasibilityCheckOnPrem(cfg *config.Config) (FeasibilityVerdict, error) {
	prov, err := provider.For(cfg)
	if err != nil {
		return FeasibilityUnchecked, nil // no provider resolved → caller warns
	}
	inv, err := prov.Inventory(cfg)
	if err != nil {
		// ErrNotApplicable is the common case for non-Proxmox
		// on-prem providers (their Inventory is a stub).
		// Warn-only; do not block the walkthrough.
		return FeasibilityUnchecked, nil
	}
	host := &capacity.HostCapacity{
		CPUCores:  inv.Total.Cores,
		MemoryMiB: inv.Total.MemoryMiB,
		StorageGB: inv.Total.StorageGiB,
		StorageBy: inv.Total.StorageByClass,
		Nodes:     append([]string(nil), inv.Hosts...),
	}
	v, err := feasibility.CheckOnPrem(cfg, host)
	if err != nil {
		return FeasibilityInfeasible, err
	}
	if len(v.PerProvider) == 0 {
		return FeasibilityUnchecked, nil
	}
	for _, pv := range v.PerProvider {
		switch pv.Verdict {
		case feasibility.Comfortable:
			return FeasibilityComfortable, nil
		case feasibility.Tight:
			return FeasibilityTight, nil
		}
	}
	// Infeasible — gather the reason from the active provider's verdict
	// and any hard blocking reasons (resilience, missing TCO config).
	// Without this the caller displays "✗ infeasible" with no context.
	var parts []string
	parts = append(parts, v.BlockingReasons...)
	if pv, ok := v.PerProvider[cfg.InfraProvider]; ok && pv.Reason != "" {
		parts = append(parts, pv.Reason)
	}
	if len(parts) > 0 {
		return FeasibilityInfeasible, fmt.Errorf("%s", strings.Join(parts, "; "))
	}
	return FeasibilityInfeasible, fmt.Errorf("cluster shape does not fit the available host inventory")
}

// _ keeps config imported even if all references move to other
// files in this package; harmless guard.
var _ = config.Config{}