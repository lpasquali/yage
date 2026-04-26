// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// cloud.go — fork-specific step methods for the cloud branch
// (§22.3). Asks budget + headroom at step 4; runs cost-compare +
// feasibility merge at step 5; falls into the shared tail.

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
)

// runCloudFork is the cloud-fork driver. Steps 1-3 are shared; 4
// and 5 are local; 6-8 fall into runSharedTail.
func (s *state) runCloudFork() int {
	if err := s.step1_environment(); err != nil {
		return s.exit(err)
	}
	if err := s.step2_resilience(); err != nil {
		return s.exit(err)
	}
	if err := s.step3_workloadShape(); err != nil {
		return s.exit(err)
	}
	for {
		if err := s.step4_cloud_budget(); err != nil {
			return s.exit(err)
		}
		err := s.step5_cloud_costCompare()
		if err == nil {
			break
		}
		if err == ErrUserExit {
			return 0
		}
		// AbsoluteFloor exceeded or every provider infeasible —
		// loop back per §23.4. The user can shrink workload at
		// step 3 by hitting "back" / re-running, or grow budget
		// at step 4 here. We loop step 4 + 5 in place; the user
		// can ^D to bail.
		s.r.info("looping back to step 4 — adjust budget or shrink workload via env / re-run xapiri.")
	}
	if err := s.runSharedTail(); err != nil {
		return s.exit(err)
	}
	return 0
}

// step4_cloud_budget prompts budget + headroom and computes the
// budget-after-headroom that the cost-compare row at step 5 will
// be evaluated against (§23.4). Both values land on cfg so the
// rest of yage (--dry-run, real run preflight) sees the same
// number.
func (s *state) step4_cloud_budget() error {
	s.r.section("budget")
	curBudget := s.cfg.BudgetUSDMonth
	if s.budgetUSDMonth > 0 {
		curBudget = s.budgetUSDMonth
	}
	cur := ""
	if curBudget > 0 {
		cur = strconv.FormatFloat(curBudget, 'f', -1, 64)
	}
	for {
		v := s.r.promptString("monthly budget USD", cur)
		if v == "" && curBudget > 0 {
			s.budgetUSDMonth = curBudget
			break
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f <= 0 {
			s.r.info("    not a positive number; try again.")
			continue
		}
		s.budgetUSDMonth = f
		break
	}
	s.cfg.BudgetUSDMonth = s.budgetUSDMonth
	hpStr := s.r.promptString("headroom % (default 20)", "20")
	if hp, err := strconv.ParseFloat(strings.TrimSpace(hpStr), 64); err == nil && hp >= 0 && hp < 100 {
		s.headroomPct = hp / 100
	}
	s.budgetAfterHeadroom = s.budgetUSDMonth * (1 - s.headroomPct)
	s.r.info("sizing target after headroom: $%.2f/mo", s.budgetAfterHeadroom)
	return nil
}

// step5_cloud_costCompare runs CompareClouds and merges the
// result with the feasibility verdict (§22.3 step 5). Renders the
// annotated table sorted by total ascending; refuses to proceed
// when AbsoluteFloor > budget; otherwise prompts the user to pick
// a provider from the feasible set and stamps cfg.InfraProvider.
func (s *state) step5_cloud_costCompare() error {
	s.r.section("cost compare")
	rows := compareCloudsForReview(s.cfg)
	if len(rows) == 0 {
		s.r.info("no cloud providers registered with a working cost estimator.")
		return ErrUserExit
	}

	feasibility, ferr := runFeasibilityCheck(s.cfg)
	feasible := []cost.CloudCost{}
	for _, r := range rows {
		// Hide priced-but-broken estimators from the choice list,
		// but keep them visible in the table so the user sees what
		// happened.
		if r.Err == nil {
			feasible = append(feasible, r)
		}
	}

	// Render the table.
	s.printCostCompareTable(rows, feasibility, ferr)

	// AbsoluteFloor check: does the cheapest priced provider fit
	// the budget at all?
	cheapest := -1.0
	for _, r := range rows {
		if r.Err != nil || r.Estimate.TotalUSDMonthly <= 0 {
			continue
		}
		if cheapest < 0 || r.Estimate.TotalUSDMonthly < cheapest {
			cheapest = r.Estimate.TotalUSDMonthly
		}
	}
	if cheapest > 0 && cheapest > s.budgetUSDMonth {
		s.r.info("⚠ AbsoluteFloor: even the cheapest priced cloud (%s$%.2f/mo) exceeds your budget ($%.2f/mo).",
			"$", cheapest, s.budgetUSDMonth)
		return fmt.Errorf("xapiri: AbsoluteFloor $%.2f > budget $%.2f", cheapest, s.budgetUSDMonth)
	}

	// Cloud-fork pick: ask which provider from the priced set.
	if len(feasible) == 0 {
		s.r.info("no priced cloud provider available; can't proceed on cloud fork.")
		return ErrUserExit
	}
	names := make([]string, 0, len(feasible))
	for _, r := range feasible {
		names = append(names, r.ProviderName)
	}
	cur := s.cfg.InfraProvider
	if cur == "" || !contains(names, cur) {
		cur = names[0]
	}
	pick := s.r.promptChoice("pick a cloud provider", names, cur)
	s.cfg.InfraProvider = pick
	s.cfg.InfraProviderDefaulted = false
	return nil
}

// printCostCompareTable renders the per-provider cost rows with
// feasibility annotations. The format mirrors §22.4's example:
//   ✓ 1) hetzner    $34/mo   feasibility: comfortable   creds: HCLOUD_TOKEN ✓
// Free-tier hints are TODO — once Track α's free-tier table lands
// the per-row annotation goes here. For now the symbol is the
// shim's verdict (✓ / ⚠ / ✗ / ?).
func (s *state) printCostCompareTable(rows []cost.CloudCost, verdict FeasibilityVerdict, ferr error) {
	fmt.Fprintln(s.w, "  per-provider monthly cost (live, sorted by total ascending):")
	for i, r := range rows {
		sym := "?"
		state := "unchecked"
		switch verdict {
		case FeasibilityComfortable:
			sym = "✓"
			state = "comfortable"
		case FeasibilityTight:
			sym = "⚠"
			state = "tight"
		case FeasibilityInfeasible:
			sym = "✗"
			state = "infeasible"
		}
		var totalStr string
		switch {
		case r.Err != nil:
			totalStr = "(estimator error)"
		case r.Estimate.TotalUSDMonthly <= 0:
			totalStr = "(unpriced)"
		default:
			totalStr = fmt.Sprintf("$%.2f/mo", r.Estimate.TotalUSDMonthly)
		}
		creds := credentialsHint(r.ProviderName)
		fmt.Fprintf(s.w, "    %s %d) %-12s %-18s feasibility: %s   creds: %s\n",
			sym, i+1, r.ProviderName, totalStr, state, creds)
	}
	if ferr != nil {
		fmt.Fprintf(s.w, "  feasibility note: %v\n", ferr)
	}
	if verdict == FeasibilityUnchecked {
		fmt.Fprintln(s.w, "  (feasibility gate not wired yet — verdict shown is a placeholder.)")
	}
}

// compareCloudsForReview wraps cost.CompareClouds and re-sorts
// priced rows ascending. The cost package already does this; we
// keep the wrapper so future filtering (free-tier first, creds
// detected first, …) lives in one place.
func compareCloudsForReview(cfg *config.Config) []cost.CloudCost {
	rows := cost.CompareClouds(cfg)
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := rows[i], rows[j]
		ipriced := ri.Err == nil && ri.Estimate.TotalUSDMonthly > 0
		jpriced := rj.Err == nil && rj.Estimate.TotalUSDMonthly > 0
		if ipriced != jpriced {
			return ipriced
		}
		if ipriced {
			return ri.Estimate.TotalUSDMonthly < rj.Estimate.TotalUSDMonthly
		}
		return ri.ProviderName < rj.ProviderName
	})
	return rows
}

// credentialsHint reads the well-known per-provider env var and
// surfaces "<VAR> ✓" or "not detected" for the cost-compare row.
// The list mirrors §22.4 — extend as new providers land.
func credentialsHint(name string) string {
	checks := map[string]string{
		"aws":          "AWS_ACCESS_KEY_ID",
		"azure":        "AZURE_SUBSCRIPTION_ID",
		"gcp":          "GOOGLE_APPLICATION_CREDENTIALS",
		"hetzner":      "HCLOUD_TOKEN",
		"digitalocean": "DIGITALOCEAN_TOKEN",
		"linode":       "LINODE_TOKEN",
		"oci":          "OCI_CONFIG_FILE",
		"ibmcloud":     "IBMCLOUD_API_KEY",
	}
	v, ok := checks[name]
	if !ok {
		return "n/a"
	}
	if envSet(v) {
		return v + " ✓"
	}
	return "not detected"
}

func envSet(name string) bool {
	if name == "" {
		return false
	}
	return os.Getenv(name) != ""
}

// contains is a tiny generic-shaped helper for the InfraProvider
// reset check. The provider package's own filter helpers take
// slices so we don't need to drag a third party in for this.
func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}