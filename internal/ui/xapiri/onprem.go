package xapiri

// onprem.go — fork-specific step methods for the on-prem branch
// (§22.2). Picks from the airgap-compatible provider set at step 4;
// runs the §23 capacity-bound feasibility check at step 5; falls
// into the shared tail.

import (
	"fmt"

	"github.com/lpasquali/yage/internal/provider"
)

// runOnPremFork is the on-prem-fork driver. Steps 1-3 are shared;
// 4 and 5 are local; 6-8 fall into runSharedTail.
func (s *state) runOnPremFork() int {
	if err := s.step1_environment(); err != nil {
		return s.exit(err)
	}
	if err := s.step2_resilience(); err != nil {
		return s.exit(err)
	}
	if err := s.step3_workloadShape(); err != nil {
		return s.exit(err)
	}
	if err := s.step4_onprem_providerPick(); err != nil {
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
// When the gate isn't wired (Track α pending) we soft-pass with a
// note. When it returns "infeasible" we surface the reason and
// loop back to step 3.
func (s *state) step5_onprem_capacity() error {
	s.r.section("capacity")
	verdict, err := runFeasibilityCheckOnPrem(s.cfg)
	switch verdict {
	case FeasibilityUnchecked:
		s.r.info("on-prem feasibility gate not wired (Track α pending).")
		s.r.info("proceeding without the host-pool capacity check — re-run when --dry-run lands the gate.")
		return nil
	case FeasibilityComfortable:
		s.r.info("✓ comfortable on the configured host pool.")
		return nil
	case FeasibilityTight:
		s.r.info("⚠ tight on the configured host pool — proceeding anyway.")
		if err != nil {
			s.r.info("  detail: %v", err)
		}
		return nil
	case FeasibilityInfeasible:
		s.r.info("✗ infeasible: %v", err)
		return fmt.Errorf("xapiri: on-prem capacity infeasible: %w", err)
	}
	return nil
}
