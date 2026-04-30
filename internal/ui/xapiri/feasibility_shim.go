// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// feasibility_shim.go — FeasibilityVerdict type used by xapiri's
// dashboard for displaying feasibility states.

// FeasibilityVerdict mirrors the §23 vocabulary so callers do not
// need to import the feasibility package directly.
type FeasibilityVerdict int

const (
	// FeasibilityUnchecked means the feasibility check did not run.
	// Treated as a soft pass — the dashboard warns and lets the user
	// proceed; the dry-run / preflight re-checks later in the flow.
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
