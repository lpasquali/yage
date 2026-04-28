// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// onprem.go — fork-specific step methods for the on-prem branch.
// Picks from the airgap-compatible provider set at step 4; runs
// the on-prem capacity-bound feasibility check at step 5; offers
// the optional TCO estimator at step 5.5; then falls into the
// shared tail.

import (
	"fmt"
	"strconv"

	"github.com/lpasquali/yage/internal/provider"
)

// runOnPremFork is the on-prem-fork driver. Steps 1-3 are shared;
// 4 and 5 are local; 6-8 fall into runSharedTail.
func (s *state) runOnPremFork() int {
	if err := s.step4_onprem_providerPick(); err != nil {
		return s.exit(err)
	}
	if err := s.step1_environment(); err != nil {
		return s.exit(err)
	}
	if err := s.step2_resilience(); err != nil {
		return s.exit(err)
	}
	if err := s.step3_workloadShape(); err != nil {
		return s.exit(err)
	}
	if err := s.stepBootstrapMode(); err != nil {
		return s.exit(err)
	}
	for {
		err := s.step5_onprem_capacity()
		if err == nil {
			break
		}
		if err == ErrUserExit {
			return 0
		}
		// Capacity-infeasible — loop back to step 3 (workload).
		s.r.info("capacity infeasible — re-pick workload size.")
		if err := s.step3_workloadShape(); err != nil {
			return s.exit(err)
		}
	}
	if err := s.step5_5_onprem_tco(); err != nil {
		return s.exit(err)
	}
	// Provider-specific credential + network steps.
	// Proxmox gets a structured flow; other on-prem providers use
	// the generic reflection walk.
	if s.cfg.InfraProvider == "proxmox" {
		if err := s.step6_proxmox(); err != nil {
			return s.exit(err)
		}
		if err := s.step6_5_proxmox_network(); err != nil {
			return s.exit(err)
		}
		if err := s.step7_review(); err != nil {
			return s.exit(err)
		}
		return s.exit(s.step8_persistAndDecide())
	}
	if err := s.runSharedTail(); err != nil {
		return s.exit(err)
	}
	return 0
}

// step4_onprem_providerPick picks from the on-prem-compatible
// registered providers (§22.2 step 4). When --airgapped is set we
// run the same airgap filter the orchestrator uses everywhere
// else; in non-airgapped mode we still narrow to the on-prem set
// because picking AWS on the on-prem fork makes no sense.
func (s *state) step4_onprem_providerPick() error {
	s.r.section("provider pick")
	all := provider.Registered()
	// Filter to on-prem-compatible regardless of cfg.Airgapped:
	// the on-prem fork is by definition the on-prem-compatible
	// set, even when the operator hasn't set --airgapped (e.g.
	// they're running yage from a workstation that has internet
	// but the target deployment is air-isolated).
	var names []string
	for _, n := range all {
		if provider.AirgapCompatible(n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("xapiri: no on-prem-compatible providers registered")
	}
	cur := s.cfg.InfraProvider
	if cur == "" || !provider.AirgapCompatible(cur) {
		cur = names[0]
	}
	s.cfg.InfraProvider = s.r.promptChoice("which on-prem provider?", names, cur)
	s.cfg.InfraProviderDefaulted = false
	return nil
}

// step5_onprem_capacity calls the §23 on-prem feasibility hook.
// When the gate is not wired we soft-pass with a note. When it
// returns "infeasible" we surface the reason and loop back to
// step 3.
func (s *state) step5_onprem_capacity() error {
	s.r.section("capacity")
	verdict, err := runFeasibilityCheckOnPrem(s.cfg)
	switch verdict {
	case FeasibilityUnchecked:
		s.r.info("on-prem feasibility gate not wired.")
		s.r.info("proceeding without the host-pool capacity check.")
		return nil
	case FeasibilityComfortable:
		s.r.info("✓ comfortable on the configured host pool.")
		return nil
	case FeasibilityTight:
		s.r.info("⚠ tight on the configured host pool.")
		if err != nil {
			s.r.info("  detail: %v", err)
		}
		s.r.info("  Resource overcommit lets VMs exceed the soft capacity budget;")
		s.r.info("  the hard ceiling (memory/disk) still applies.")
		if s.r.promptYesNo("allow resource overcommit?", s.cfg.Capacity.AllowOvercommit) {
			s.cfg.Capacity.AllowOvercommit = true
			s.r.info("  ✓ overcommit enabled (equivalent to --allow-resource-overcommit)")
		}
		return nil
	case FeasibilityInfeasible:
		s.r.info("✗ infeasible: %v", err)
		return fmt.Errorf("xapiri: on-prem capacity infeasible: %w", err)
	}
	return nil
}

// step5_5_onprem_tco prompts the operator for the inputs the TCO
// estimator needs (capex, useful life, electricity, support). Every
// prompt is skippable by hitting enter on an empty default — and
// the whole step is skippable via the leading yes/no — because the
// on-prem cost path is genuinely optional: operators who already
// know their hardware costs by heart, or who don't want to surface
// them at all, can move straight to review and persist.
//
// When the operator opts in we stamp cfg.HardwareCostUSD /
// HardwareUsefulLifeYears / HardwareWatts / HardwareKWHRateUSD /
// HardwareSupportUSDMonth so the existing renderTCOLine in shared.go
// (and the orchestrator dry-run) pick up the values without any
// further plumbing.
func (s *state) step5_5_onprem_tco() error {
	s.r.section("on-prem cost estimator (optional)")
	curHas := s.cfg.HardwareCostUSD > 0 || s.cfg.HardwareWatts > 0 ||
		s.cfg.HardwareSupportUSDMonth > 0
	if !s.r.promptYesNo("estimate monthly TCO?", curHas) {
		s.r.info("skipped — review and persist will show 'TCO not configured'.")
		return nil
	}
	s.cfg.HardwareCostUSD = s.promptFloat(
		"hardware capex (one-time, in your local currency)",
		s.cfg.HardwareCostUSD)
	s.cfg.HardwareUsefulLifeYears = s.promptFloat(
		"useful life (years; capex amortizes straight-line)",
		orFloat(s.cfg.HardwareUsefulLifeYears, 5))
	s.cfg.HardwareWatts = s.promptFloat(
		"sustained power draw (W)",
		s.cfg.HardwareWatts)
	s.cfg.HardwareKWHRateUSD = s.promptFloat(
		"electricity rate (per kWh, in your local currency)",
		s.cfg.HardwareKWHRateUSD)
	s.cfg.HardwareSupportUSDMonth = s.promptFloat(
		"flat monthly support / licensing (0 to skip)",
		s.cfg.HardwareSupportUSDMonth)
	return nil
}

// promptFloat is a small wrapper around promptString that parses a
// non-negative float. Empty input keeps cur. Reject-and-retry on
// negative or unparsable input (zero is allowed — operators
// legitimately have $0 support contracts).
func (s *state) promptFloat(label string, cur float64) float64 {
	curStr := ""
	if cur > 0 {
		curStr = strconv.FormatFloat(cur, 'f', -1, 64)
	}
	for {
		v := s.r.promptString(label, curStr)
		if v == "" {
			return cur
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 {
			s.r.info("    not a non-negative number; try again.")
			continue
		}
		return f
	}
}

// orFloat returns cur when non-zero, else def. Compact helper for
// step prompts that have a static fallback (e.g. 5-year useful life).
func orFloat(cur, def float64) float64 {
	if cur > 0 {
		return cur
	}
	return def
}

// stepBootstrapMode prompts for the Kubernetes distribution:
// kubeadm (upstream CAPI) or k3s (lightweight single-binary). Runs
// only on the on-prem fork, after workload shape and before the
// capacity feasibility gate — so the capacity check uses the right
// per-node footprint (k3s uses ~1 vCPU / 1 GiB per node vs
// kubeadm's higher baseline).
//
// Pre-selection heuristic: when the configured control-plane and
// worker machine counts are all going on a single host, and that
// host appears small (≤ SmallEnvCPUCores / SmallEnvMemoryGiB), k3s
// is pre-selected and the reason is shown. In the xapiri context we
// don't have live inventory, so we use cfg.ControlPlaneMachineCount
// + cfg.WorkerMachineCount to estimate total VM count and compare
// against the small-env thresholds via the provider's capacity
// sizing constants.
func (s *state) stepBootstrapMode() error {
	s.r.section("bootstrap mode")

	cur := s.cfg.BootstrapMode
	if cur == "" {
		cur = "kubeadm"
	}

	// Suggest k3s when the workload is small (≤4 total VMs or the
	// user configured small machine types). This is a best-effort
	// heuristic — the authoritative check is in the capacity gate.
	cpCount := parseIntOrKeep(s.cfg.ControlPlaneMachineCount, 1)
	wkCount := parseIntOrKeep(s.cfg.WorkerMachineCount, 0)
	totalVMs := cpCount + wkCount
	suggestK3s := totalVMs <= 4 && cur == "kubeadm"
	if suggestK3s {
		s.r.info("💡 small cluster (%d VM(s) total) — k3s uses ~1 vCPU / 1 GiB per node", totalVMs)
		s.r.info("   vs kubeadm's higher control-plane baseline. Consider k3s.")
		cur = "k3s"
	}

	chosen := s.r.promptChoice("bootstrap mode", []string{"kubeadm", "k3s"}, cur)
	s.cfg.BootstrapMode = chosen

	switch chosen {
	case "k3s":
		s.r.info("  k3s selected — CAPI will use KCP-K3s + CABK3s providers.")
	default:
		s.r.info("  kubeadm selected — standard upstream CAPI control-plane.")
	}
	return nil
}