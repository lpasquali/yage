// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// huh_form.go — opt-in TUI spike that drives steps 1..4 of the
// cloud fork through a charmbracelet/huh form. Each step is a
// huh.Group; Tab/Shift+Tab moves between groups so the operator can
// revisit prior answers without re-running the whole walkthrough.
//
// Activation: YAGE_XAPIRI_TUI=huh. Anything else falls through to
// the line-oriented prompts. The cloud-fork tail (cost compare,
// provider details, review, persist) runs unchanged after the form
// completes; the on-prem fork is out of scope here and falls back
// to the legacy path.
//
// Workload shape (apps × template, DB GB, egress, queue/cache/
// objstore booleans) is captured in a dedicated group between the
// env/resilience and location/budget groups so the cost estimator
// has the same inputs as the legacy text walkthrough.

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// huhDNS1123 mirrors prompts.go's dns1123label regexp. Local copy
// so the validator is self-contained for the spike — keeps the
// legacy reader's regexp untouched.
var huhDNS1123 = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// huhAlpha2 matches an ISO-3166 alpha-2 country code candidate.
var huhAlpha2 = regexp.MustCompile(`^[A-Za-z]{2}$`)

// runHuhForm drives steps 1..4 of the cloud fork (kind name, mode,
// environment, resilience, location, budget, skip-providers, infra
// pick) through a single huh.Form. Mutates cfg + s in place; the
// caller dispatches the rest of the walkthrough.
//
// Returns 0 on a clean confirm, the s.exit() code if huh itself
// errors out, or 0 after delegating to runOnPremFork when the user
// flips to on-prem at the mode group.
func runHuhForm(w io.Writer, cfg *config.Config, s *state) int {
	_ = w // form draws on its own; keep w in the signature for parity with Run.
	// Local working copies so Shift+Tab can safely revise values
	// before commit. We stamp cfg/s only after the form returns.
	kindName := cfg.KindClusterName
	if kindName == "" {
		kindName = "yage-mgmt"
	}
	mode := "cloud"
	if s.fork == forkOnPrem {
		mode = "on-prem"
	}
	env := "staging"
	if s.env != "" {
		env = string(s.env)
	}
	resil := "single-az"
	switch s.resil {
	case resilienceHA:
		resil = "ha"
	case resilienceHAMulti:
		resil = "ha-multi-region"
	}
	dcLoc := cfg.Cost.Currency.DataCenterLocation
	budgetStr := ""
	if cfg.BudgetUSDMonth > 0 {
		// Show the existing default in taller terms when we can.
		if v, _, err := pricing.ToTaller(cfg.BudgetUSDMonth, "USD"); err == nil {
			budgetStr = strconv.FormatFloat(v, 'f', 2, 64)
		} else {
			budgetStr = strconv.FormatFloat(cfg.BudgetUSDMonth, 'f', 2, 64)
		}
	}
	skip := splitCSV(cfg.SkipProviders)
	cloudNames := huhCloudProviders()
	infraPick := cfg.InfraProvider
	if infraPick == "" || !contains(cloudNames, infraPick) {
		if len(cloudNames) > 0 {
			infraPick = cloudNames[0]
		}
	}

	// Workload shape locals — pre-seed from any prior s.workload (so
	// Shift+Tab re-entries preserve user input) or from sensible
	// defaults when empty.
	appsStr := formatAppBuckets(s.workload.Apps)
	if appsStr == "" {
		appsStr = "4 medium"
	}
	dbGBStr := ""
	if s.workload.DBGB > 0 {
		dbGBStr = strconv.Itoa(s.workload.DBGB)
	}
	egressStr := ""
	if s.workload.EgressGBMo > 0 {
		egressStr = strconv.Itoa(s.workload.EgressGBMo)
	}
	hasQueue := s.workload.HasQueue
	hasObjStore := s.workload.HasObjStore
	hasCache := s.workload.HasCache

	confirm := false

	tallerSym := pricing.TallerSymbol()
	taller := pricing.TallerCurrency()
	budgetTitle := fmt.Sprintf("monthly budget (%s, symbol %s)", taller, tallerSym)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("kind management cluster name").
				Description("DNS-1123 label: lowercase alphanumerics + hyphens, max 63 chars.").
				Value(&kindName).
				Validate(huhValidateDNSLabel),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("which mode?").
				Description("the huh spike covers the cloud fork end-to-end; on-prem falls back to the legacy TUI.").
				Options(
					huh.NewOption("cloud", "cloud"),
					huh.NewOption("on-prem", "on-prem"),
				).
				Value(&mode),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("environment").
				Description("dev = minimal; staging = + Argo; prod = + monitoring + cert-manager.").
				Options(
					huh.NewOption("dev", "dev"),
					huh.NewOption("staging", "staging"),
					huh.NewOption("prod", "prod"),
				).
				Value(&env),
			huh.NewSelect[string]().
				Title("resilience tier").
				Description("single-az = one AZ; ha = multi-AZ in one region; ha-multi-region = cross-region.").
				Options(
					huh.NewOption("single-az", "single-az"),
					huh.NewOption("ha", "ha"),
					huh.NewOption("ha-multi-region", "ha-multi-region"),
				).
				Value(&resil),
		).WithHideFunc(func() bool { return mode != "cloud" }),
		huh.NewGroup(
			huh.NewInput().
				Title("apps (count × template)").
				Description("templates: light (100m/128MB), medium (200m/256MB), heavy (500m/1GB). e.g. '6 medium 2 heavy'.").
				Value(&appsStr).
				Validate(huhValidateAppBuckets),
			huh.NewInput().
				Title("database GB").
				Description("size of the primary database volume.").
				Value(&dbGBStr).
				Validate(huhValidateNonNegativeInt),
			huh.NewInput().
				Title("egress GB / month").
				Description("required on cloud (§23.6 sandbag defense). leave blank to default to db × 2.").
				Value(&egressStr).
				Validate(huhValidateNonNegativeIntOptional),
			huh.NewConfirm().
				Title("add-on: message queue?").
				Description("RabbitMQ / Kafka / managed equivalent.").
				Value(&hasQueue),
			huh.NewConfirm().
				Title("add-on: object storage?").
				Description("MinIO / S3 / GCS / managed equivalent.").
				Value(&hasObjStore),
			huh.NewConfirm().
				Title("add-on: in-memory cache?").
				Description("Redis / Valkey / managed equivalent.").
				Value(&hasCache),
		).WithHideFunc(func() bool { return mode != "cloud" }),
		huh.NewGroup(
			huh.NewInput().
				Title("data-center location (ISO-3166 alpha-2)").
				Description("optional — leave blank to fall back to geo-IP / USD.").
				Value(&dcLoc).
				Validate(huhValidateAlpha2Optional),
			huh.NewInput().
				Title(budgetTitle).
				Description("numeric, > 0; entered in the active taller — converted to USD internally.").
				Value(&budgetStr).
				Validate(huhValidatePositiveFloat),
		).WithHideFunc(func() bool { return mode != "cloud" }),
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("--skip-providers").
				Description("toggle providers to drop from cost-compare; space to select.").
				Options(huhMultiOptions(cloudNames)...).
				Value(&skip),
			huh.NewSelect[string]().
				Title("infra provider (final pick)").
				Description("the cloud cost-compare runs against the registered cloud set minus skips.").
				Options(huhSelectOptions(cloudNames)...).
				Value(&infraPick),
		).WithHideFunc(func() bool { return mode != "cloud" }),
		huh.NewGroup(
			huh.NewNote().
				Title("on-prem fallback").
				Description("the huh spike covers the cloud fork only — the on-prem flow falls back to the legacy TUI; re-run without YAGE_XAPIRI_TUI=huh for the full on-prem walkthrough."),
		).WithHideFunc(func() bool { return mode != "on-prem" }),
		huh.NewGroup(
			huh.NewConfirm().
				Title("ready to run cost-compare?").
				Description("yes proceeds into step 5; no aborts the spike (Shift+Tab to revise instead).").
				Affirmative("yes, proceed").
				Negative("no, revise").
				Value(&confirm),
		),
	)

	if err := form.Run(); err != nil {
		fmt.Fprintf(s.w, "xapiri (huh): %v\n", err)
		return 1
	}
	if !confirm {
		// Operator declined at the final confirm — exit cleanly.
		// huh's per-group Shift+Tab already lets them revise prior
		// answers before the confirm group, so a hard "no" here is
		// the operator saying "abort the spike entirely".
		s.r.info("nothing written. the spirits rest.")
		return 0
	}

	// Stamp answers onto cfg + state.
	cfg.KindClusterName = kindName
	if mode == "on-prem" {
		// Spike scope: hand off to the legacy on-prem walkthrough so
		// the user still gets a complete experience. step1..3 there
		// will re-prompt for env/resilience under the legacy reader,
		// which is fine — those weren't shown in this run.
		s.fork = forkOnPrem
		return s.runOnPremFork()
	}
	s.fork = forkCloud
	s.env = envTier(env)
	switch resil {
	case "ha":
		s.resil = resilienceHA
		s.cfg.ControlPlaneMachineCount = "3"
	case "ha-multi-region":
		s.resil = resilienceHAMulti
		s.cfg.ControlPlaneMachineCount = "3"
	default:
		s.resil = resilienceSingle
		s.cfg.ControlPlaneMachineCount = "1"
	}
	switch s.env {
	case envDev:
		s.cfg.ArgoCDEnabled = false
		s.cfg.WorkloadArgoCDEnabled = false
	case envStaging:
		s.cfg.ArgoCDEnabled = true
		s.cfg.WorkloadArgoCDEnabled = false
	case envProd:
		s.cfg.ArgoCDEnabled = true
		s.cfg.WorkloadArgoCDEnabled = true
		s.cfg.CertManagerEnabled = true
	}
	cfg.Cost.Currency.DataCenterLocation = strings.ToUpper(strings.TrimSpace(dcLoc))

	// Budget is entered in taller terms; convert to USD for storage.
	if f, err := strconv.ParseFloat(strings.TrimSpace(budgetStr), 64); err == nil && f > 0 {
		usd := f
		if u, ferr := pricing.FromTaller(f); ferr == nil {
			usd = u
		}
		cfg.BudgetUSDMonth = usd
		s.budgetUSDMonth = usd
		s.budgetAfterHeadroom = usd * (1 - s.headroomPct)
	}
	cfg.SkipProviders = strings.Join(skip, ",")
	cfg.InfraProvider = infraPick
	cfg.InfraProviderDefaulted = false

	// Workload shape — parse the free-form bucket string + numeric
	// inputs onto s.workload before sync. The validators above already
	// rejected garbage; the parsers below tolerate "" and fall back to
	// existing values. Egress defaults to db × 2 when blank, matching
	// the legacy text walkthrough's lazy-default rule.
	if parsed := parseAppBuckets(appsStr); len(parsed) > 0 {
		s.workload.Apps = parsed
	} else if len(s.workload.Apps) == 0 {
		s.workload.Apps = []appBucket{{Count: 4, Template: "medium"}}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(dbGBStr)); err == nil && n >= 0 {
		s.workload.DBGB = n
	}
	if e := strings.TrimSpace(egressStr); e != "" {
		if n, err := strconv.Atoi(e); err == nil && n >= 0 {
			s.workload.EgressGBMo = n
		}
	} else if s.workload.EgressGBMo == 0 && s.workload.DBGB > 0 {
		s.workload.EgressGBMo = s.workload.DBGB * 2
	}
	s.workload.HasQueue = hasQueue
	s.workload.HasObjStore = hasObjStore
	s.workload.HasCache = hasCache

	// Worker count heuristic: 1 worker per 4 medium-equivalent apps,
	// minimum 1. Mirrors step3_workloadShape so the orchestrator sees
	// the same stamped value regardless of which UI ran.
	totalApps := 0
	for _, b := range s.workload.Apps {
		totalApps += b.Count
	}
	if totalApps > 0 {
		w := totalApps / 4
		if w < 1 {
			w = 1
		}
		s.cfg.WorkerMachineCount = strconv.Itoa(w)
	}
	syncWorkloadShapeToCfg(cfg, s.workload, s.resil, s.env, s.fork)
	return 0
}

// huhValidateDNSLabel mirrors promptDNSLabel's rules: max 63 chars
// + DNS-1123 label regex.
func huhValidateDNSLabel(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("kind cluster name is required")
	}
	if len(v) > 63 {
		return fmt.Errorf("too long: %d chars (max 63)", len(v))
	}
	if !huhDNS1123.MatchString(v) {
		return fmt.Errorf("not a DNS-1123 label (lowercase alphanumeric + hyphens)")
	}
	return nil
}

// huhValidatePositiveFloat enforces "> 0" on the budget input.
func huhValidatePositiveFloat(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("budget is required")
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("not a number: %q", v)
	}
	if f <= 0 {
		return fmt.Errorf("must be greater than zero")
	}
	return nil
}

// huhValidateAlpha2Optional accepts empty OR exactly two ASCII
// letters — matches what cfg.Cost.Currency.DataCenterLocation
// expects after normalisation.
func huhValidateAlpha2Optional(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if !huhAlpha2.MatchString(v) {
		return fmt.Errorf("expected ISO-3166 alpha-2 (two letters) or blank")
	}
	return nil
}

// huhValidateAppBuckets accepts the same lenient syntax as
// parseAppBuckets ("6 medium 2 heavy", "6×medium,2×heavy", …) and
// rejects only when the result has zero usable buckets — that's the
// signal the operator typed a count without a known template, or a
// template without a count.
func huhValidateAppBuckets(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("at least one bucket is required (e.g. '4 medium')")
	}
	if len(parseAppBuckets(v)) == 0 {
		return fmt.Errorf("couldn't parse — expected pairs like '6 medium 2 heavy'")
	}
	return nil
}

// huhValidateNonNegativeInt rejects empty / non-numeric / negative
// inputs. Used for required numeric fields like database GB.
func huhValidateNonNegativeInt(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("value is required")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("not an integer: %q", v)
	}
	if n < 0 {
		return fmt.Errorf("must be zero or positive")
	}
	return nil
}

// huhValidateNonNegativeIntOptional permits empty (the run-time path
// fills in db × 2 when egress is blank) but rejects non-numeric or
// negative inputs.
func huhValidateNonNegativeIntOptional(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("not an integer: %q", v)
	}
	if n < 0 {
		return fmt.Errorf("must be zero or positive")
	}
	return nil
}

// huhCloudProviders returns the registered cloud providers — the
// inverse of provider.AirgapCompatible. Cost.CompareWithFilter uses
// the same classification, so the menu lines up with the rows the
// cost compare will actually iterate.
func huhCloudProviders() []string {
	all := provider.Registered()
	out := make([]string, 0, len(all))
	for _, n := range all {
		if !provider.AirgapCompatible(n) {
			out = append(out, n)
		}
	}
	return out
}

// huhSelectOptions is a tiny adapter that turns []string into
// []huh.Option[string] for huh.Select.
func huhSelectOptions(values []string) []huh.Option[string] {
	out := make([]huh.Option[string], 0, len(values))
	for _, v := range values {
		out = append(out, huh.NewOption(v, v))
	}
	return out
}

// huhMultiOptions is the MultiSelect variant. Same shape, different
// generic instantiation site — keeps callers readable.
func huhMultiOptions(values []string) []huh.Option[string] {
	return huhSelectOptions(values)
}

// splitCSV splits a comma-separated list, dropping blanks and
// trimming surrounding spaces. Used to seed the multi-select from
// cfg.SkipProviders.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
