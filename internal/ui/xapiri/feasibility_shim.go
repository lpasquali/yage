package xapiri

// feasibility_shim.go — function-pointer indirection for the
// §23 feasibility gate.
//
// Track α is shipping `internal/feasibility/` in parallel and may
// or may not be in main when this code lands. Rather than couple
// the build to its merge timing, we mirror the pattern persist.go
// already uses for kindsync.WriteBootstrapConfigSecret: a package-
// level function variable, nil at first, wired up by a one-line
// edit (or by an `init()` if the integrator wants the flip
// automatic) once Track α merges.
//
// Until then, calls return nil — the walkthrough proceeds without
// blocking on a check the rest of yage can't perform yet, and the
// step-5 display labels every provider as "feasibility: unchecked"
// so the user knows the gate hasn't been wired.
//
// Wire-up after Track α merges (one of two equivalents):
//
//   1. The integrator flips the imports here:
//
//        import "github.com/lpasquali/yage/internal/feasibility"
//        var feasibilityCheck = feasibility.Check
//        var feasibilityCheckOnPrem = feasibility.CheckOnPrem
//
//   2. Or, the integrator drops a one-line `init()` in this file
//      that assigns the pointers from the feasibility package.
//
// The shim deliberately avoids any reference to internal/feasibility
// here so the build doesn't depend on Track α's merge.

import "github.com/lpasquali/yage/internal/config"

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

// feasibilityCheck is the cloud-fork hook (§22 step 5 / §23.2).
// nil while Track α is unmerged. Called with the resolved cfg;
// returning a non-nil error means "block; loop back."
var feasibilityCheck func(cfg *config.Config) error = nil

// feasibilityCheckOnPrem is the on-prem-fork hook (§22.2 step 5).
// Same shape; same nil semantics.
var feasibilityCheckOnPrem func(cfg *config.Config) error = nil

// runFeasibilityCheck dispatches to the cloud-fork hook, treating
// a nil hook as the unchecked path (warn-only). Returns the
// verdict + the underlying error. Step 5 inspects the verdict; if
// FeasibilityInfeasible the walkthrough loops back.
func runFeasibilityCheck(cfg *config.Config) (FeasibilityVerdict, error) {
	if feasibilityCheck == nil {
		return FeasibilityUnchecked, nil
	}
	if err := feasibilityCheck(cfg); err != nil {
		return FeasibilityInfeasible, err
	}
	return FeasibilityComfortable, nil
}

// runFeasibilityCheckOnPrem is the on-prem analogue. Same
// dispatch shape; capacity-bound rather than budget-bound.
func runFeasibilityCheckOnPrem(cfg *config.Config) (FeasibilityVerdict, error) {
	if feasibilityCheckOnPrem == nil {
		return FeasibilityUnchecked, nil
	}
	if err := feasibilityCheckOnPrem(cfg); err != nil {
		return FeasibilityInfeasible, err
	}
	return FeasibilityComfortable, nil
}
