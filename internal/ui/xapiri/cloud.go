// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// cloud.go — fork-specific step methods for the cloud branch
// (§22.3). Asks budget + headroom at step 4; runs cost-compare +
// feasibility merge at step 5; falls into the shared tail.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/pricing"
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

// step4_cloud_budget prompts budget + headroom in the active taller
// currency and computes the budget-after-headroom that the cost-
// compare row at step 5 evaluates against (§23.4). Internally we
// store cfg.BudgetUSDMonth in USD (the canonical sort/compare unit)
// — the prompt converts taller → USD via the live FX path.
func (s *state) step4_cloud_budget() error {
	s.r.section("budget")
	taller := pricing.TallerCurrency()
	tallerSym := pricing.TallerSymbol()
	curBudget := s.cfg.BudgetUSDMonth
	if s.budgetUSDMonth > 0 {
		curBudget = s.budgetUSDMonth
	}
	curTaller := 0.0
	cur := ""
	if curBudget > 0 {
		// Show the existing default in taller terms.
		if v, _, err := pricing.ToTaller(curBudget, "USD"); err == nil {
			curTaller = v
			cur = strconv.FormatFloat(v, 'f', 2, 64)
		} else {
			cur = strconv.FormatFloat(curBudget, 'f', 2, 64)
		}
	}
	label := fmt.Sprintf("monthly budget %s", taller)
	for {
		v := s.r.promptString(label, cur)
		if v == "" && curBudget > 0 {
			s.budgetUSDMonth = curBudget
			break
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || f <= 0 {
			s.r.info("    not a positive number; try again.")
			continue
		}
		// Convert taller-amount entered by the user into USD for
		// internal storage. FX failure leaves the entered value as
		// USD (FormatTaller's USD-fallback story).
		usd := f
		if u, err := pricing.FromTaller(f); err == nil {
			usd = u
		}
		s.budgetUSDMonth = usd
		curTaller = f
		break
	}
	s.cfg.BudgetUSDMonth = s.budgetUSDMonth
	hpStr := s.r.promptString("headroom % (default 20)", "20")
	if hp, err := strconv.ParseFloat(strings.TrimSpace(hpStr), 64); err == nil && hp >= 0 && hp < 100 {
		s.headroomPct = hp / 100
	}
	s.budgetAfterHeadroom = s.budgetUSDMonth * (1 - s.headroomPct)
	s.r.info("sizing target after headroom: %s%.2f/mo",
		tallerSym, curTaller*(1-s.headroomPct))
	return nil
}

// step5_cloud_costCompare runs CompareClouds and merges the
// result with the feasibility verdict (§22.3 step 5). Renders the
// annotated table sorted by total ascending; refuses to proceed
// when AbsoluteFloor > budget; otherwise prompts the user to pick
// a provider from the feasible set and stamps cfg.InfraProvider.
func (s *state) step5_cloud_costCompare() error {
	s.r.section("cost compare")
	s.stampGeoRegions("filled blank Region/Location on every provider we can map, for live cost rows")
	rows := compareCloudsForReview(s.cfg, s.w)
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
		cheapestT := pricing.FormatTaller(cheapest, "USD")
		budgetT := pricing.FormatTaller(s.budgetUSDMonth, "USD")
		s.r.info("⚠ AbsoluteFloor: even the cheapest priced cloud (%s/mo) exceeds your budget (%s/mo).",
			cheapestT, budgetT)
		return fmt.Errorf("xapiri: AbsoluteFloor %s > budget %s", cheapestT, budgetT)
	}

	// Cloud-fork pick: ask which provider from the priced set.
	if len(feasible) == 0 {
		s.r.info("no priced cloud provider available; can't proceed on cloud fork.")
		return ErrUserExit
	}
	cur := s.cfg.InfraProvider
	if cur == "" || !contains(providerNames(feasible), cur) {
		cur = feasible[0].ProviderName
	}
	pick := s.promptCloudPick(feasible, cur)
	s.cfg.InfraProvider = pick
	s.cfg.InfraProviderDefaulted = false
	return nil
}

// promptCloudPick is the cloud-fork variant of promptChoice that
// also accepts "?N" / "?name" to drill into that provider's bill
// breakdown before committing. Reuses the Items already fetched
// during cost compare — no extra API calls.
func (s *state) promptCloudPick(feasible []cost.CloudCost, cur string) string {
	names := providerNames(feasible)
	fmt.Fprintln(s.w, "  pick a cloud provider")
	for i, r := range feasible {
		marker := " "
		if r.ProviderName == cur {
			marker = "*"
		}
		fmt.Fprintf(s.w, "    %s %d) %s\n", marker, i+1, r.ProviderName)
	}
	fmt.Fprintln(s.w, "    (prefix with '?' to see the bill breakdown — e.g. '?1' or '?hetzner')")
	for {
		hint := ""
		if cur != "" {
			hint = fmt.Sprintf(" [%s]", cur)
		}
		fmt.Fprintf(s.w, "  pick 1-%d%s: ", len(feasible), hint)
		v := strings.TrimSpace(s.r.readLine())
		if v == "" && cur != "" {
			return cur
		}
		if strings.HasPrefix(v, "?") {
			target := strings.TrimSpace(v[1:])
			if row := matchProvider(feasible, target); row != nil {
				printBillBreakdown(s.w, *row)
			} else {
				fmt.Fprintln(s.w, "    not a valid provider; try '?1' or '?hetzner'.")
			}
			continue
		}
		if row := matchProvider(feasible, v); row != nil {
			return row.ProviderName
		}
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(feasible) {
			return names[n-1]
		}
		fmt.Fprintln(s.w, "    not a valid choice; try again.")
	}
}

// matchProvider resolves a user token (1-based index or provider
// name) to a row. Returns nil when nothing matches.
func matchProvider(rows []cost.CloudCost, token string) *cost.CloudCost {
	if token == "" {
		return nil
	}
	for i := range rows {
		if rows[i].ProviderName == token {
			return &rows[i]
		}
	}
	if n, err := strconv.Atoi(token); err == nil && n >= 1 && n <= len(rows) {
		return &rows[n-1]
	}
	return nil
}

func providerNames(rows []cost.CloudCost) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ProviderName)
	}
	return out
}

// printBillBreakdown renders a provider's bill in the same shape as
// orchestrator/plan.go's planMonthlyCost: per-item Qty × unit = sub,
// total, and the provider's Note. Indented to nest under the menu.
func printBillBreakdown(w io.Writer, r cost.CloudCost) {
	fmt.Fprintf(w, "    --- %s bill breakdown ---\n", r.ProviderName)
	if r.Err != nil {
		fmt.Fprintf(w, "      (estimator error: %v)\n", r.Err)
		return
	}
	if len(r.Estimate.Items) == 0 {
		fmt.Fprintln(w, "      (no line items returned by estimator)")
		return
	}
	fmt.Fprintf(w, "      %s\n", pricing.TallerNote())
	for _, it := range r.Estimate.Items {
		unit := pricing.FormatTaller(it.UnitUSDMonthly, "USD")
		sub := pricing.FormatTaller(it.SubtotalUSD, "USD")
		fmt.Fprintf(w, "      %-40s %d × %s = %s\n", it.Name, it.Qty, unit, sub)
	}
	totalStr := pricing.FormatTaller(r.Estimate.TotalUSDMonthly, "USD")
	fmt.Fprintf(w, "      TOTAL: ~%s / month (%s)\n", totalStr, pricing.TallerCurrency())
	if r.Estimate.Note != "" {
		fmt.Fprintf(w, "      note: %s\n", r.Estimate.Note)
	}
}

// printCostCompareTable renders the per-provider cost rows with
// feasibility annotations. The format mirrors §22.4's example:
//   ✓ 1) hetzner    $34/mo   feasibility: comfortable   creds: HCLOUD_TOKEN ✓
// Free-tier hints are TODO — the per-row annotation slot is
// reserved for a future free-tier lookup. For now the symbol is
// the shim's verdict (✓ / ⚠ / ✗ / ?).
func (s *state) printCostCompareTable(rows []cost.CloudCost, verdict FeasibilityVerdict, ferr error) {
	fmt.Fprintln(s.w, "  per-provider monthly cost (live, sorted by total ascending):")
	pricingErrDebug := os.Getenv("YAGE_XAPIRI_PRICING_ERRORS") == "1" || os.Getenv("YAGE_XAPIRI_PRICING_ERRORS") == "true"
	var errLines []string
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
			totalStr = pricing.FormatTaller(r.Estimate.TotalUSDMonthly, "USD") + "/mo"
		}
		creds := credentialsHint(r.ProviderName)
		fmt.Fprintf(s.w, "    %s %d) %-12s %-18s feasibility: %s   creds: %s\n",
			sym, i+1, r.ProviderName, totalStr, state, creds)
		if pricingErrDebug && r.Err != nil {
			errLines = append(errLines, fmt.Sprintf("    %s: %v", r.ProviderName, r.Err))
		}
	}
	if ferr != nil {
		fmt.Fprintf(s.w, "  feasibility note: %v\n", ferr)
	}
	if verdict == FeasibilityUnchecked {
		fmt.Fprintln(s.w, "  (feasibility gate not wired yet — verdict shown is a placeholder.)")
	}
	if len(errLines) > 0 {
		fmt.Fprintln(s.w, "  estimator errors (YAGE_XAPIRI_PRICING_ERRORS=1):")
		for _, line := range errLines {
			fmt.Fprintln(s.w, line)
		}
	} else {
		for _, r := range rows {
			if r.Err != nil {
				fmt.Fprintln(s.w, "  hint: set YAGE_XAPIRI_PRICING_ERRORS=1 to print estimator failure details.")
				break
			}
		}
	}
	// Inline per-provider line-item splits. Operators want to see
	// what makes up each total without typing "?N" first; the prompt
	// drill-down stays available for re-displaying a single bill.
	// Skip rows with no items (estimator error / unpriced).
	hasAnyItems := false
	for _, r := range rows {
		if r.Err == nil && len(r.Estimate.Items) > 0 {
			hasAnyItems = true
			break
		}
	}
	if hasAnyItems {
		fmt.Fprintln(s.w)
		fmt.Fprintln(s.w, "  per-provider bill split:")
		for _, r := range rows {
			if r.Err != nil || len(r.Estimate.Items) == 0 {
				continue
			}
			printBillBreakdown(s.w, r)
		}
	}
}

// compareCloudsForReview wraps the cost compare and re-sorts priced
// rows ascending. Cloud-fork only runs against cloud providers —
// on-prem rows would all return (estimator error) without
// --hardware-cost-usd, so they're filtered out here.
// progress is forwarded for live-pricing progress lines (xapiri step
// 5); pass nil to stay quiet (e.g. review step).
func compareCloudsForReview(cfg *config.Config, progress io.Writer) []cost.CloudCost {
	rows := cost.CompareWithFilter(cfg, cost.ScopeCloudOnly, progress)
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

// credentialsHint reflects whether the cost-compare path has a
// pricing-grade credential for the named provider. The truth
// source is pricing.PricingCredsConfigured (which knows the actual
// env-var fallbacks each fetcher consults — e.g. YAGE_GCP_API_KEY
// for GCP, not GOOGLE_APPLICATION_CREDENTIALS). When configured,
// surface a hint of WHICH variable the fetcher picked up so the
// operator can confirm the right secret.
func credentialsHint(name string) string {
	if name == "aws" {
		return awsCredentialsHint()
	}
	// Anonymous catalogs.
	if name == "azure" || name == "linode" || name == "oci" {
		return "n/a (anonymous)"
	}
	envByVendor := map[string][]string{
		"gcp":          {"YAGE_GCP_API_KEY", "GOOGLE_BILLING_API_KEY"},
		"hetzner":      {"YAGE_HCLOUD_TOKEN", "HCLOUD_TOKEN"},
		"digitalocean": {"YAGE_DO_TOKEN", "DIGITALOCEAN_TOKEN"},
		"ibmcloud":     {"YAGE_IBMCLOUD_API_KEY", "IBMCLOUD_API_KEY"},
	}
	if !pricing.PricingCredsConfigured(name) {
		return "not detected"
	}
	for _, v := range envByVendor[name] {
		if envSet(v) {
			return v + " ✓"
		}
	}
	// Set via cfg.Cost.Credentials (kind Secret) rather than env.
	return "configured ✓"
}

// awsCredentialsHint mirrors pricing.PricingCredsConfigured("aws"):
// Bulk Pricing JSON is anonymous, but the UI should reflect the
// same “AWS identity present” signals operators use (keys, profile,
// default shared config files).
func awsCredentialsHint() string {
	if envSet("AWS_ACCESS_KEY_ID") {
		return "AWS_ACCESS_KEY_ID ✓"
	}
	if p := strings.TrimSpace(os.Getenv("AWS_PROFILE")); p != "" {
		return "AWS_PROFILE=" + p + " ✓"
	}
	home, err := os.UserHomeDir()
	if err == nil {
		for _, rel := range []string{".aws/credentials", ".aws/config"} {
			p := filepath.Join(home, rel)
			if _, e := os.Stat(p); e == nil {
				return filepath.Join("~", rel) + " ✓"
			}
		}
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