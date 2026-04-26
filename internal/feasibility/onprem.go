package feasibility

import (
	"fmt"

	"github.com/lpasquali/yage/internal/cluster/capacity"
	"github.com/lpasquali/yage/internal/config"
)

// CheckOnPrem implements the on-prem branch of §22.2 + §23: instead
// of "min_cost ≤ budget", the gate compares the projected
// minimum-viable cluster against host inventory totals (cores, mem,
// disk). Reuses internal/cluster/capacity's existing CheckCombined
// math for the soft-budget verdict; the MinCost field is the
// derived TCO line from the cfg.Hardware* knobs (§16).
//
// The host argument carries the per-host-pool inventory the caller
// already gathered (the orchestrator's hostCapacityFromInventory
// helper feeds it on real runs; tests pass a synthetic
// HostCapacity). When host is nil, every provider verdict reports
// "no inventory" instead of attempting to fit.
func CheckOnPrem(cfg *config.Config, host *capacity.HostCapacity) (Verdict, error) {
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

	// On-prem fork doesn't gate egress (intra-cluster traffic; no
	// vendor bill). Resilience-aware checks still apply.
	v.BlockingReasons = collectOnPremBlockingReasons(sh, cfg)

	tco := monthlyTCO(cfg)

	// Project the workload shape into a capacity.Plan so the
	// existing CheckCombined math can run unchanged. We model the
	// cluster as 1 PlanItem per role (control-plane + workers),
	// using the projected min-per-node footprint divided by the
	// node count (CP nodes from the resilience tier; workers from
	// the §23.3 ceil-divide once we know an instance shape).
	//
	// The on-prem path doesn't have an "instance type" the way
	// clouds do; we synthesize one using the smallest sane on-prem
	// VM (2 cores, 4 GiB) so workers come out as a count, not as
	// "however many fractional VMs would fit". This mirrors the
	// xapiri default sizing for on-prem k3s nodes.
	const nodeCores = 2
	const nodeMemMiB = int64(4096)
	const nodeDiskGB = int64(40)

	wCores := ceilDiv(sh.requiredCores, int64(nodeCores)*1000)
	wMem := ceilDiv(sh.requiredMem, nodeMemMiB)
	workers := wCores
	if wMem > workers {
		workers = wMem
	}
	if workers < 1 {
		workers = 1
	}

	plan := capacity.Plan{
		CPUCores:  (sh.cpNodes + int(workers)) * nodeCores,
		MemoryMiB: int64(sh.cpNodes+int(workers)) * nodeMemMiB,
		StorageGB: int64(sh.cpNodes+int(workers))*nodeDiskGB + int64(sh.dbGB),
		Items: []capacity.PlanItem{
			{Name: "control-plane", Replicas: sh.cpNodes,
				CPUCores: nodeCores, MemoryMiB: nodeMemMiB, DiskGB: nodeDiskGB},
			{Name: "worker", Replicas: int(workers),
				CPUCores: nodeCores, MemoryMiB: nodeMemMiB, DiskGB: nodeDiskGB + int64(sh.dbGB)},
		},
	}

	// The on-prem fork iterates the AirgapCompatible providers in
	// the §22.2 set: proxmox, vsphere, openstack, capd. We don't
	// import internal/provider here to keep the dep graph flat;
	// the names are stable per the airgap whitelist.
	onPremNames := []string{"capd", "openstack", "proxmox", "vsphere"}

	for _, name := range onPremNames {
		pv := ProviderVerdict{
			MinCost: tco,
		}
		if host == nil {
			pv.Verdict = Infeasible
			pv.Reason = "host inventory unavailable; cannot evaluate fit"
			v.PerProvider[name] = pv
			continue
		}
		verdict, msg := capacity.CheckCombined(plan, host, nil,
			cfg.ResourceBudgetFraction, cfg.OvercommitTolerancePct)
		switch verdict {
		case capacity.VerdictFits:
			pv.Verdict = Comfortable
		case capacity.VerdictTight:
			pv.Verdict = Tight
		default:
			pv.Verdict = Infeasible
		}
		pv.Reason = msg
		v.PerProvider[name] = pv
	}

	// AbsoluteFloor on the on-prem fork is the TCO line — the same
	// number across every provider (it's hardware-bound, not
	// per-cloud-priced). 0 when the user hasn't filled in the
	// hardware-cost flags.
	v.AbsoluteFloor = tco

	// Recommended = first Comfortable in deterministic order
	// (matches the on-prem default-priority list).
	for _, name := range onPremNames {
		if v.PerProvider[name].Verdict == Comfortable {
			v.Recommended = name
			break
		}
	}

	return v, nil
}

// monthlyTCO derives the TCO line from cfg.Hardware* knobs per §16:
// amortized capex + electricity + flat support. Returns 0 when the
// user hasn't set any of those flags (keeps "TCO not configured"
// behaviour intact).
func monthlyTCO(cfg *config.Config) float64 {
	if cfg == nil {
		return 0
	}
	amort := 0.0
	if cfg.HardwareCostUSD > 0 && cfg.HardwareUsefulLifeYears > 0 {
		amort = cfg.HardwareCostUSD / (cfg.HardwareUsefulLifeYears * 12.0)
	}
	// Watts × 0.001 (kW) × 720 hours/month × $/kWh
	electricity := cfg.HardwareWatts * 0.001 * 720.0 * cfg.HardwareKWHRateUSD
	support := cfg.HardwareSupportUSDMonth
	return amort + electricity + support
}

// collectOnPremBlockingReasons mirrors collectBlockingReasons but
// without the egress check (no vendor bill on-prem) and adds the
// "TCO not configured" advisory when none of the hardware-cost
// flags are set.
func collectOnPremBlockingReasons(sh shape, cfg *config.Config) []string {
	var out []string
	if sh.environment == "prod" && (sh.resilience == "" || sh.resilience == "single") {
		out = append(out,
			"prod environment requires HA resilience (3 control-plane nodes); current resilience='"+orDefault(sh.resilience, "single")+"'")
	}
	if sh.resilience == "ha" || sh.resilience == "ha-mr" {
		if got := atoiOr(cfg.ControlPlaneMachineCount, 0); got > 0 && got < 3 {
			out = append(out,
				fmt.Sprintf("HA resilience requires ≥3 control-plane nodes; cfg.ControlPlaneMachineCount=%d", got))
		}
	}
	if cfg.HardwareCostUSD == 0 && cfg.HardwareWatts == 0 && cfg.HardwareSupportUSDMonth == 0 {
		out = append(out, "TCO not configured: set --hardware-cost-usd / --hardware-watts / --hardware-support-usd-month for an amortized monthly figure")
	}
	return out
}
