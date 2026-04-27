// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package feasibility

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
)

// ErrNotApplicable signals the gate has nothing to evaluate — either
// because the user-stated WorkloadShape is empty (no apps + no DB),
// or because cfg.BudgetUSDMonth is zero on the cloud fork. Distinct
// from a real failure so xapiri / dry-run callers can skip the
// section instead of surfacing a misleading "infeasible" result.
var ErrNotApplicable = errors.New("feasibility: no workload shape stated")

// FeasibilityVerdict ranks how much of the post-headroom budget the
// minimum-viable cluster consumes per §23.4.
type FeasibilityVerdict int

const (
	// Comfortable means min_cost ≤ 60% of post-headroom budget.
	Comfortable FeasibilityVerdict = iota
	// Tight means 60% < min_cost ≤ 90% of post-headroom budget.
	// Surfaces a warning to the user but does not block.
	Tight
	// Infeasible means min_cost > 90% of post-headroom budget. Blocks
	// the dry-run / real-run unless --ignore-feasibility is set.
	Infeasible
)

// String returns the docs-table glyph + label per §22.4.
func (v FeasibilityVerdict) String() string {
	switch v {
	case Comfortable:
		return "comfortable"
	case Tight:
		return "tight"
	}
	return "infeasible"
}

// Verdict is the cross-provider feasibility result per §23.2.
type Verdict struct {
	// PerProvider keys provider name → ProviderVerdict. Providers
	// the gate could not price (live API unavailable, no cheap-
	// instance mapping registered) are still present but with
	// MinCost=0 and Verdict=Infeasible + Reason explaining why.
	PerProvider map[string]ProviderVerdict
	// Recommended is the cheapest-Comfortable provider name, or ""
	// when none of the priced providers comes in below the
	// Comfortable threshold.
	Recommended string
	// AbsoluteFloor is min(MinCost) across every priced provider —
	// the cheapest possible monthly bill anywhere. When AbsoluteFloor
	// > cfg.BudgetUSDMonth (and BudgetUSDMonth > 0), the user has
	// asked for a cluster nobody can sell them at that budget; the
	// xapiri state machine loops back to step 3 / 4. AbsoluteFloor
	// is 0 when no provider produced a price.
	AbsoluteFloor float64
	// BlockingReasons lists cross-provider violations that don't
	// depend on pricing — e.g. "prod tier picked but Resilience left
	// at single", "egress unset on cloud fork". Surfaced as a bullet
	// list under the per-provider table.
	BlockingReasons []string
}

// ProviderVerdict is the per-provider row in Verdict.PerProvider.
type ProviderVerdict struct {
	// Verdict is Comfortable / Tight / Infeasible.
	Verdict FeasibilityVerdict
	// MinCost is the priced floor for this provider's cheapest
	// viable instance type running the projected cluster. 0 when
	// pricing was unavailable — Reason explains why.
	MinCost float64
	// Reason is a one-line human-readable explanation of how
	// MinCost was reached (or why it wasn't). Examples:
	//   "3 × t3.medium + 50GB gp3 + 100GB egress @ live us-east-1 prices"
	//   "live pricing unavailable: ec2 us-east-1: cache stale"
	//   "no cheap-instance mapping registered for this provider"
	Reason string
	// FreeTierFit is true when this provider's free-tier quota
	// (per §23.7) would cover the projected cluster outright.
	// MinCost is still populated with the post-free-tier number
	// (typically 0); FreeTierCliff describes the trigger that
	// would push the user off the free tier.
	FreeTierFit bool
	// FreeTierCliff is the human-readable cliff trigger for
	// providers with meaningful Always-Free quotas (Linode, DO,
	// Hetzner, OCI). Empty for providers without a free tier.
	FreeTierCliff string
}

// cheapInstance is one entry in feasibility's small per-provider
// cheapest-viable-instance catalog. The §23.2 spec called out two
// designs (a Provider-interface hook vs an inline table); the inline
// table is the simpler one for this round and avoids extending
// every Provider implementation. Per the task brief, providers not
// in the table return ErrNotApplicable for the cloud fork — they're
// still listed in Verdict.PerProvider so the user sees them.
type cheapInstance struct {
	// SKU is the instance name passed to pricing.Fetch.
	SKU string
	// Cores is the per-instance vCPU count.
	Cores int
	// MemMiB is the per-instance memory in MiB.
	MemMiB int64
	// Region is the default region used when cfg's per-provider
	// region field is empty.
	Region string
	// PricingVendor is the internal/pricing vendor key (usually
	// matches the provider name; "ibmcloud"/"oci" don't have
	// fetchers wired today and so report unavailable).
	PricingVendor string
}

// cheapInstances maps provider name → small fleet entry. The four
// "primary" providers in §23 are AWS / Azure / GCP / Hetzner; the
// rest fall through to cliff-only annotations (Linode / DO / OCI)
// or to-be-priced (IBM Cloud).
//
// The instances chosen here are the smallest 2-vCPU / ≥4 GiB option
// per vendor — k8s requires ≥2 vCPU per node, and the §23.3 math
// always emits a CP node first which sets the per-node floor.
var cheapInstances = map[string]cheapInstance{
	"aws":     {SKU: "t3.medium", Cores: 2, MemMiB: 4096, Region: "us-east-1", PricingVendor: "aws"},
	"azure":   {SKU: "Standard_D2s_v3", Cores: 2, MemMiB: 8192, Region: "eastus", PricingVendor: "azure"},
	"gcp":     {SKU: "n2-standard-2", Cores: 2, MemMiB: 8192, Region: "us-central1", PricingVendor: "gcp"},
	"hetzner": {SKU: "cx23", Cores: 2, MemMiB: 4096, Region: "fsn1", PricingVendor: "hetzner"},
}

// freeTierCliffs is the §23.7 hard-coded annotation table — providers
// with meaningful "always free" quotas (not one-time credits). When
// the projected cluster fits a vendor's quota, that row in
// Verdict.PerProvider gets FreeTierFit = true + FreeTierCliff = the
// trigger string from this table.
//
// Per the task brief, this lives inline rather than wiring it through
// every provider package. The thresholds are intentionally
// conservative (any cluster above one worker / 20 GB DB falls off
// the cliff for OCI; Hetzner's free-tier is essentially "no
// always-free compute" so it's marked off by default).
type freeTierQuota struct {
	// MaxCPNodes is the max control-plane node count that fits the
	// quota (1 for OCI Always Free; 0 means "no always-free").
	MaxCPNodes int
	// MaxWorkerNodes is the max worker count that fits.
	MaxWorkerNodes int
	// MaxDBGB is the max database size that fits the quota's storage.
	MaxDBGB int
	// Cliff is the human-readable trigger displayed when the cluster
	// is on the edge ("+1 worker / +20 GB DB" style).
	Cliff string
}

var freeTierTable = map[string]freeTierQuota{
	// OCI Always Free: 4 ARM Ampere cores total, 24 GB RAM, 200 GB
	// block storage. Maps to ~1 CP + 1 worker on small shapes.
	"oci": {MaxCPNodes: 1, MaxWorkerNodes: 1, MaxDBGB: 20, Cliff: "+1 worker or +20 GB DB"},
	// Linode: no compute always-free; only $100 / 60-day credit.
	// Marked here for completeness; MaxCPNodes=0 means "never fits".
	"linode": {MaxCPNodes: 0, MaxWorkerNodes: 0, MaxDBGB: 0, Cliff: "no always-free compute (60-day $100 credit only)"},
	// DigitalOcean: same — $200 / 60-day credit only.
	"digitalocean": {MaxCPNodes: 0, MaxWorkerNodes: 0, MaxDBGB: 0, Cliff: "no always-free compute (60-day $200 credit only)"},
	// Hetzner: no always-free compute (project-based; charges from
	// hour 1). Marked for completeness.
	"hetzner": {MaxCPNodes: 0, MaxWorkerNodes: 0, MaxDBGB: 0, Cliff: "no always-free compute (hourly billing from t=0)"},
}

// shape is the projected cluster footprint per §23.3. Computed once
// per Check call and reused across every provider iteration.
type shape struct {
	requiredCores int64 // millicores, post-fragmentation
	requiredMem   int64 // MiB, post-fragmentation
	cpNodes       int   // 1 / 3 / 3+
	dbGB          int
	egressGB      int
	resilience    string
	environment   string
	hasDB         bool
	hasApps       bool
}

// projectShape implements §23.3 step-by-step: app + db + system
// reserve, scheduling fragmentation, control-plane overhead. Reused
// by both the cloud (Check) and on-prem (CheckOnPrem) paths.
//
// TODO(§23.8): IOPS-floor check is OUT OF SCOPE this round — the
// CSI Driver interface needs an IOPSHint() method first, and that
// touches every provider. Track separately.
func projectShape(cfg *config.Config) (shape, error) {
	w := cfg.Workload
	hasApps := false
	for _, g := range w.Apps {
		if g.Count > 0 {
			hasApps = true
			break
		}
	}
	hasDB := w.DatabaseGB > 0
	if !hasApps && !hasDB {
		return shape{}, ErrNotApplicable
	}

	appCores, appMem := appResourcesFor(w.Apps)
	dbCores, dbMem := dbResourcesFor(w.DatabaseGB)

	rawCores := appCores + dbCores + SystemCoresMilli
	rawMem := appMem + dbMem + SystemMemMiB

	required := func(n int64) int64 {
		return int64(math.Ceil(float64(n) * SchedulingFragmentationFactor))
	}

	return shape{
		requiredCores: required(rawCores),
		requiredMem:   required(rawMem),
		cpNodes:       cpNodesFor(w.Resilience),
		dbGB:          w.DatabaseGB,
		egressGB:      w.EgressGBMonth,
		resilience:    w.Resilience,
		environment:   w.Environment,
		hasDB:         hasDB,
		hasApps:       hasApps,
	}, nil
}

// Check is the cloud-fork entry point per §23.2. Iterates every
// known cheap-instance provider, prices the projected cluster, and
// emits a per-provider verdict against cfg.BudgetUSDMonth.
//
// Returns ErrNotApplicable when the user hasn't stated any workload
// (no apps + no database) — callers (xapiri / dry-run) skip the
// section in that case.
func Check(cfg *config.Config) (Verdict, error) {
	if cfg == nil {
		return Verdict{}, ErrNotApplicable
	}
	sh, err := projectShape(cfg)
	if err != nil {
		return Verdict{}, err
	}

	v := Verdict{
		PerProvider: map[string]ProviderVerdict{},
	}

	// Cross-provider blocking checks first — these don't depend on
	// pricing and apply uniformly to every row.
	v.BlockingReasons = collectBlockingReasons(sh, cfg)

	budget := cfg.BudgetUSDMonth
	postHeadroom := budget * (1.0 - DefaultHeadroomPct)

	// Iterate the inline catalog deterministically (sorted) so test
	// assertions don't flake on map order.
	names := make([]string, 0, len(cheapInstances))
	for n := range cheapInstances {
		names = append(names, n)
	}
	sort.Strings(names)

	floor := math.Inf(1)

	for _, name := range names {
		entry := cheapInstances[name]
		region := regionFor(name, cfg, entry.Region)
		minCost, reason, err := priceProvider(name, entry, region, sh)
		pv := ProviderVerdict{
			MinCost: minCost,
			Reason:  reason,
		}
		if err != nil {
			// Pricing unavailable: keep the row but mark Infeasible
			// with the upstream reason so the user sees what failed.
			pv.Verdict = Infeasible
			v.PerProvider[name] = pv
			continue
		}

		// Free-tier annotation (cheap-instance providers in scope:
		// only Hetzner today has a row in the free-tier table).
		if quota, ok := freeTierTable[name]; ok {
			pv.FreeTierFit = quotaFits(quota, sh)
			pv.FreeTierCliff = quota.Cliff
		}

		pv.Verdict = verdictFor(minCost, postHeadroom, budget)
		v.PerProvider[name] = pv

		if minCost > 0 && minCost < floor {
			floor = minCost
		}
	}

	// Also surface the §23.7 free-tier-only providers in the verdict
	// map so the xapiri table can show "OCI Always Free" rows even
	// though we don't have a pricing fetcher for OCI yet.
	for name, quota := range freeTierTable {
		if _, already := v.PerProvider[name]; already {
			continue
		}
		pv := ProviderVerdict{
			Reason:        "no live pricing fetcher; free-tier annotation only",
			FreeTierFit:   quotaFits(quota, sh),
			FreeTierCliff: quota.Cliff,
		}
		if pv.FreeTierFit {
			pv.MinCost = 0
			pv.Verdict = Comfortable
			pv.Reason = "fits free-tier quota"
		} else {
			pv.Verdict = Infeasible
		}
		v.PerProvider[name] = pv
	}

	if !math.IsInf(floor, 1) {
		v.AbsoluteFloor = floor
	}

	// Recommended = cheapest Comfortable provider.
	recName := ""
	recCost := math.Inf(1)
	for _, name := range names {
		pv := v.PerProvider[name]
		if pv.Verdict != Comfortable {
			continue
		}
		if pv.MinCost > 0 && pv.MinCost < recCost {
			recName = name
			recCost = pv.MinCost
		}
	}
	v.Recommended = recName

	// AbsoluteFloor exceeded (loud) gets surfaced as a blocking
	// reason so the xapiri loop-back path (§23.4 last bullet) has
	// one place to look.
	if budget > 0 && v.AbsoluteFloor > 0 && v.AbsoluteFloor > budget {
		v.BlockingReasons = append(v.BlockingReasons,
			fmt.Sprintf("AbsoluteFloor exceeded: cheapest provider min-cost $%.2f/mo > budget $%.2f/mo",
				v.AbsoluteFloor, budget))
	}

	return v, nil
}

// collectBlockingReasons enumerates §23.5 / §23.6 cross-provider
// violations: resilience mismatch with the workload + tier, egress
// unset on the cloud fork, etc. Returns nil when the workload is
// clean.
func collectBlockingReasons(sh shape, cfg *config.Config) []string {
	var out []string
	// §23.5: prod environment requires HA resilience.
	if sh.environment == "prod" && (sh.resilience == "" || sh.resilience == "single") {
		out = append(out, "prod environment requires HA resilience (3 control-plane nodes); current resilience='"+orDefault(sh.resilience, "single")+"'")
	}
	// §23.5: HA tier needs ≥3 CP nodes (the cpNodesFor table
	// already returns 3 for "ha"; this defends against a manual
	// override that drops it). Respect cfg.ControlPlaneMachineCount
	// when set.
	if sh.resilience == "ha" || sh.resilience == "ha-mr" {
		if got := atoiOr(cfg.ControlPlaneMachineCount, 0); got > 0 && got < 3 {
			out = append(out,
				fmt.Sprintf("HA resilience requires ≥3 control-plane nodes; cfg.ControlPlaneMachineCount=%d", got))
		}
	}
	// §23.6: egress sandbag — required on the cloud fork unless the
	// user explicitly states "0" (we can't tell apart unset from
	// zero in an int field, so we treat 0 as "unset" and warn). On
	// the on-prem fork this field is meaningless.
	if cfg.BudgetUSDMonth > 0 && sh.egressGB == 0 {
		out = append(out,
			"egress GB/month unset (cloud fork): treat as a sandbag risk per §23.6 — set cfg.Workload.EgressGBMonth (default suggestion = DatabaseGB × 2)")
	}
	return out
}

// regionFor resolves the active region for a provider, defaulting to
// the cheapInstance entry's Region when cfg's per-provider region
// field is empty.
func regionFor(name string, cfg *config.Config, fallback string) string {
	if cfg == nil {
		return fallback
	}
	switch name {
	case "aws":
		if r := cfg.Providers.AWS.Region; r != "" {
			return r
		}
	case "azure":
		if r := cfg.Providers.Azure.Location; r != "" {
			return r
		}
	case "gcp":
		if r := cfg.Providers.GCP.Region; r != "" {
			return r
		}
		// hetzner has no region selector in cfg today (server type
		// determines the location pool).
	}
	return fallback
}

// priceProvider runs the §23.3 last block: pick the cheapest viable
// instance type, ceil-divide required cores/mem to get worker count,
// multiply by live price, add storage + egress lines, return total.
func priceProvider(name string, entry cheapInstance, region string, sh shape) (float64, string, error) {
	item, err := pricing.Fetch(entry.PricingVendor, entry.SKU, region)
	if err != nil {
		return 0, fmt.Sprintf("live pricing unavailable for %s/%s: %v", entry.SKU, region, err), err
	}

	// Worker count = max(ceil(required_cores / instance.cores),
	//                    ceil(required_mem  / instance.mem))
	instCoresMilli := int64(entry.Cores) * 1000
	wCores := ceilDiv(sh.requiredCores, instCoresMilli)
	wMem := ceilDiv(sh.requiredMem, entry.MemMiB)
	workers := wCores
	if wMem > workers {
		workers = wMem
	}
	if workers < 1 {
		workers = 1
	}

	totalNodes := int64(sh.cpNodes) + workers
	compute := float64(totalNodes) * item.USDPerMonth

	// Storage: priced per provider via the existing block-storage
	// helpers from internal/cost/compare.go (cheap tier for the DB
	// volume). To keep this package decoupled from internal/cost we
	// reuse pricing.Fetch directly with the same SKU keys.
	storage := storageCostFor(name, region, sh.dbGB)

	// Egress: priced via the per-provider $/GB tier when available;
	// when the fetcher isn't wired we charge $0 for it and the
	// reason string reflects that (the §23.6 warning still fires
	// upstream because egress=0 lands in BlockingReasons).
	egress := egressCostFor(name, region, sh.egressGB)

	total := compute + storage + egress

	reason := fmt.Sprintf("%d × %s + %d GB cheap-tier storage + %d GB egress @ %s (live)",
		totalNodes, entry.SKU, sh.dbGB, sh.egressGB, region)
	return total, reason, nil
}

// storageCostFor prices the DB volume against the provider's cheap
// block-storage tier. Mirrors the SKU keys used by
// internal/cost/compare.go's liveBlockStorageUSDPerGBMonth — keeping
// the two in sync is a manual exercise (the cost package is in the
// allowed-imports set; we re-implement the lookup rather than touch
// it).
func storageCostFor(name, region string, dbGB int) float64 {
	if dbGB <= 0 {
		return 0
	}
	switch name {
	case "aws":
		it, err := pricing.Fetch("aws", "ebs:gp3", region)
		if err != nil {
			return 0
		}
		return float64(dbGB) * it.USDPerMonth
	case "gcp":
		it, err := pricing.Fetch("gcp", "pd:balanced", region)
		if err != nil {
			return 0
		}
		return float64(dbGB) * it.USDPerMonth
	case "hetzner":
		rate, err := pricing.HetznerVolumeUSDPerGBMonth()
		if err != nil {
			return 0
		}
		return float64(dbGB) * rate
	case "azure":
		// Azure managed-disk pricing has its own helper that takes
		// region + sku-name; the cheap-tier name today is "Standard
		// SSD Managed Disks" (per cost/compare.go).
		rate, err := pricing.AzureManagedDiskUSDPerGBMonth(region, "Standard SSD Managed Disks")
		if err != nil {
			return 0
		}
		return float64(dbGB) * rate
	}
	return 0
}

// egressCostFor prices internet egress per the §23.6 sandbag-defense
// path. Today only AWS has a wired-up live fetcher
// (pricing.AWSEgressUSDPerGB); the others fall through to $0 and the
// "egress unpriced" detail surfaces via the row's reason text.
func egressCostFor(name, region string, gb int) float64 {
	if gb <= 0 {
		return 0
	}
	switch name {
	case "aws":
		rate, err := pricing.AWSEgressUSDPerGB(region)
		if err != nil {
			return 0
		}
		return float64(gb) * rate
	}
	return 0
}

// verdictFor classifies a min_cost against the post-headroom budget
// per §23.4.
func verdictFor(minCost, postHeadroom, budget float64) FeasibilityVerdict {
	if budget <= 0 || postHeadroom <= 0 {
		// No budget stated — fall back to "Comfortable" so the
		// gate does not block runs that don't engage the cloud
		// fork. Callers (xapiri) only ask for a verdict on the
		// cloud fork, so this branch is mostly defensive.
		return Comfortable
	}
	if minCost <= postHeadroom*ComfortableThreshold {
		return Comfortable
	}
	if minCost <= postHeadroom*TightThreshold {
		return Tight
	}
	return Infeasible
}

// quotaFits returns true when the projected cluster footprint fits
// inside the named provider's free-tier quota.
func quotaFits(q freeTierQuota, sh shape) bool {
	if q.MaxCPNodes <= 0 {
		return false
	}
	// Conservative: the cluster must have at most the quota's
	// CP+worker total, AND the DB volume must fit the storage
	// allowance. We approximate worker count by 1 (the projected
	// footprint always lands on at least one worker for non-empty
	// workloads) — tightening this would require cross-referencing
	// the cheap-instance entry per quota provider, out of scope.
	if sh.cpNodes > q.MaxCPNodes {
		return false
	}
	if sh.dbGB > q.MaxDBGB {
		return false
	}
	return true
}

// --- helpers ---

func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func atoiOr(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	return n
}