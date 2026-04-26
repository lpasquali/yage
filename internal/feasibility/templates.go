// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package feasibility implements the §23 feasibility gate — the
// "anti-scrooge" cluster sizing check that prevents yage from
// provisioning a cluster that physically can't run the user's stated
// workload. It projects a config.WorkloadShape (apps × template + db
// GB + egress + resilience + environment) into a minimum-viable
// (cores, mem, instance count, storage GB, egress GB) footprint, then
// either:
//
//   - prices that footprint against every cloud provider via live
//     internal/pricing fetches and assigns a Comfortable / Tight /
//     Infeasible verdict per provider against cfg.BudgetUSDMonth, OR
//   - on the on-prem fork (§22.2), compares the footprint against
//     internal/cluster/capacity host inventory totals.
//
// The cloud + on-prem entry points live in feasibility.go (Check) and
// onprem.go (CheckOnPrem); this file holds the shared template /
// constants table that both entry points consume.
package feasibility

import "github.com/lpasquali/yage/internal/config"

// Template defines the (cores, memMiB) request for a named app
// sizing preset. Three presets exist in the §22.2 table; "custom"
// reads CustomCores + CustomMemMiB off the AppGroup directly.
type Template struct {
	// Name is the user-visible label ("light", "medium", "heavy").
	Name string
	// CoresMilli is the per-pod CPU request in millicores
	// (1000 = 1 full core).
	CoresMilli int
	// MemMiB is the per-pod memory request in MiB.
	MemMiB int64
}

// Named templates per §22.2. Order matches the docs table.
var (
	TemplateLight  = Template{Name: "light", CoresMilli: 100, MemMiB: 128}
	TemplateMedium = Template{Name: "medium", CoresMilli: 200, MemMiB: 256}
	TemplateHeavy  = Template{Name: "heavy", CoresMilli: 500, MemMiB: 1024}
)

// templateOf returns the named template for the given AppGroup, or
// the resolved (custom) values when Template == "custom". Unknown
// names fall back to "medium" — the §22.2 default for "I didn't
// say".
func templateOf(g config.AppGroup) Template {
	switch g.Template {
	case "light":
		return TemplateLight
	case "heavy":
		return TemplateHeavy
	case "custom":
		t := Template{Name: "custom", CoresMilli: g.CustomCores, MemMiB: g.CustomMemMiB}
		// Defensive floors: a "custom" group with zero resources is a
		// programmer error elsewhere (TUI / YAML loader bug); rather
		// than divide-by-zero downstream, fall back to medium.
		if t.CoresMilli <= 0 {
			t.CoresMilli = TemplateMedium.CoresMilli
		}
		if t.MemMiB <= 0 {
			t.MemMiB = TemplateMedium.MemMiB
		}
		return t
	default:
		// "medium" or empty / unknown.
		return TemplateMedium
	}
}

// System reserve per §23.3 — the cluster-wide overhead for cilium +
// argo + coredns + cert-manager + the rest of yage's bundled add-on
// stack. These values are the floor the cluster needs before the
// user's first app pod can schedule.
const (
	SystemCoresMilli int64 = 2000 // 2 full cores
	SystemMemMiB     int64 = 4096 // 4 GiB
)

// K8s scheduling fragmentation factor per §23.3 — multiply raw
// (app + db + system) cores/mem by this to leave room for pods that
// don't pack perfectly into nodes.
const SchedulingFragmentationFactor = 1.33

// Per-control-plane node overhead per §23.3 — the apiserver + etcd +
// controller-manager + scheduler footprint that one CP node carries.
const (
	CPCoresMilli int64 = 2000
	CPMemMiB     int64 = 4096
)

// Default headroom percent per §23.4. The verdict thresholds are
// applied to (budget × (1 − headroom)) — i.e. the gate intentionally
// reserves a slice of the user's budget for the unstated overhead
// (price changes, surge traffic, the next month's usage growth).
const DefaultHeadroomPct = 0.20

// Verdict thresholds per §23.4, expressed as fractions of the
// post-headroom budget.
const (
	ComfortableThreshold = 0.60
	TightThreshold       = 0.90
)

// cpNodesFor returns the §23.3 control-plane node count keyed on the
// resilience tier. Empty / "single" maps to 1; "ha" → 3; "ha-mr" → 3
// (multi-region replication is at the worker / data-plane layer for
// CAPI infra providers we ship — three CP nodes spread across regions
// is enough for quorum).
func cpNodesFor(resilience string) int {
	switch resilience {
	case "ha":
		return 3
	case "ha-mr":
		return 3
	default:
		return 1
	}
}

// dbResourcesFor projects DatabaseGB into (cores, memMiB) per §23.3.
// Heuristics:
//
//   - db_cores = max(2, db_GB / 50) full cores, expressed in millis
//   - db_mem   = max(2 GiB, db_GB × 100 MiB)
//
// A zero DatabaseGB returns zero — feasibility treats it as "no
// database" and skips the DB compute reserve.
func dbResourcesFor(dbGB int) (coresMilli int64, memMiB int64) {
	if dbGB <= 0 {
		return 0, 0
	}
	cores := int64(dbGB / 50)
	if cores < 2 {
		cores = 2
	}
	mem := int64(dbGB) * 100
	if mem < 2048 {
		mem = 2048
	}
	return cores * 1000, mem
}

// appResourcesFor sums every AppGroup's (count × template) into total
// (cores, memMiB) for the workload. Returns zeros when the slice is
// empty — feasibility treats that as ErrNotApplicable upstream.
func appResourcesFor(apps []config.AppGroup) (coresMilli int64, memMiB int64) {
	for _, g := range apps {
		if g.Count <= 0 {
			continue
		}
		t := templateOf(g)
		coresMilli += int64(g.Count) * int64(t.CoresMilli)
		memMiB += int64(g.Count) * t.MemMiB
	}
	return
}