package xapiri

// feasibility_shim.go — adapter between xapiri's simplified
// FeasibilityVerdict (✓ / ⚠ / ✗ / unchecked, displayed at step 5)
// and Track α's richer feasibility.Verdict type. Track α landed
// in commit e84d62e; this file flipped from the original
// nil-function-pointer indirection to the real wire-up below.
//
// The shim still owns the simplified verdict enum because xapiri
// doesn't need the per-provider table at the call site — step 5
// renders the rich Verdict separately for display, then asks
// runFeasibilityCheck for a single overall verdict that drives
// the loop-back / proceed decision.

import (
	"github.com/lpasquali/yage/internal/cluster/capacity"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/feasibility"
	"github.com/lpasquali/yage/internal/provider"
)

// FeasibilityVerdict mirrors the §23 vocabulary so callers don't
// need to import the feasibility package while the shim is live.
// Once Track α lands, this type can be removed (or kept as an
// alias) — the shape is the same on both sides.
type FeasibilityVerdict int

const (
	// FeasibilityUnchecked means feasibilityCheck wasn't wired.
	// Treated as a soft pass — the walkthrough warns at step 5
	// and lets the user proceed; the dry-run / preflight will
	// re-check once Track α is wired.
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

// runFeasibilityCheck calls Track α's feasibility.Check and
// reduces the rich per-provider Verdict to a single overall
// verdict for xapiri's step-5 loop-back decision.
//
// Reduction: AbsoluteFloor exceeded → Infeasible. Otherwise pick
// the best PerProvider verdict (Comfortable wins over Tight wins
// over Infeasible). When PerProvider is empty (e.g., no priced
// providers in airgapped mode) → Unchecked.
func runFeasibilityCheck(cfg *config.Config) (FeasibilityVerdict, error) {
	v, err := feasibility.Check(cfg)
	if err != nil {
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
// capacity.HostCapacity Track α's CheckOnPrem expects.
func runFeasibilityCheckOnPrem(cfg *config.Config) (FeasibilityVerdict, error) {
	prov, err := provider.For(cfg)
	if err != nil {
		return FeasibilityUnchecked, nil // no provider resolved → caller warns
	}
	inv, err := prov.Inventory(cfg)
	if err != nil {
		// ErrNotApplicable is the common case for non-Proxmox
		// on-prem providers today (their Inventory is a stub).
		// Warn-only; don't block the walkthrough.
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
	return FeasibilityInfeasible, nil
}

// _ keeps config imported even if all references move to other
// files in this package; harmless guard.
var _ = config.Config{}
