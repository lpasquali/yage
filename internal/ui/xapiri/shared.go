// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// shared.go — step methods that look the same in code on both
// forks (per §22.4). Steps 1, 2 (shape, options vary), 3 (shape,
// options vary), 6, 7, 8 live here. Fork-specific step 0 / 4 / 5
// live in onprem.go and cloud.go.
//
// Tone: calm, walkthrough-shaped, never an interrogation. Match
// the existing greeting style — short prompts, "press enter to
// keep [bracketed default]", quiet sectioning.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/cli"
	"github.com/lpasquali/yage/internal/ui/plan"
)

// semverRE validates Kubernetes version strings (e.g. "v1.35.0").
var semverRE = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// greet prints the opening lines. The shell-prompt style and
// cultural framing are deliberately quiet.
func (s *state) greet() {
	s.r.info("xapiri — let's shape this deployment together.")
	s.r.info("press enter to keep the value in [brackets].")
}

// stepKubernetesVersion asks for the workload Kubernetes version.
// Called first so every downstream step (manifest generation, cost
// compare, feasibility) sees the right version. Defaults to the
// value already on cfg (set by env / flag), falling back to the
// compiled-in default (v1.35.0). Re-prompts on malformed input.
func (s *state) stepKubernetesVersion() error {
	s.r.section("kubernetes version")
	cur := s.cfg.WorkloadKubernetesVersion
	if cur == "" {
		cur = "v1.35.0"
	}
	for {
		v := s.r.promptString("workload Kubernetes version (e.g. v1.35.0)", cur)
		if v == "" {
			v = cur
		}
		if !semverRE.MatchString(v) {
			s.r.errLine("not a valid version (expected vMAJOR.MINOR.PATCH, e.g. v1.35.0); try again.")
			continue
		}
		s.cfg.WorkloadKubernetesVersion = v
		return nil
	}
}

// stepKindClusterName asks the operator for a name for the kind
// management cluster. It is the first prompt because every later
// step (cost-compare's progress lines, the persist step, the
// kubectl context the orchestrator writes to) needs a name to use,
// and EnsureClusterUp errors out when cfg.KindClusterName is empty.
//
// The DNS-1123 validator is the same one the rest of yage applies
// to kubernetes-style names (see prompts.go). When the operator
// already passed --kind-cluster-name or KIND_CLUSTER_NAME, we still
// echo the value back so they can confirm or change it.
func (s *state) stepKindClusterName() error {
	s.r.section("kind management cluster")
	cur := s.cfg.KindClusterName
	if cur == "" {
		cur = "yage-mgmt"
	}
	s.cfg.KindClusterName = s.r.promptDNSLabel("kind cluster name", cur)
	return nil
}

// step0_modePick auto-detects the fork from cfg + env (§22.1) and,
// when ambiguous, asks. Sets s.fork. Returns ErrUserExit if the
// user bails at the prompt.
func (s *state) step0_modePick() error {
	s.r.section("mode")
	detected := detectFork(s.cfg)
	switch detected {
	case forkOnPrem:
		s.r.info("detected: on-prem (PROXMOX_URL set, no cloud creds, or --airgapped).")
	case forkCloud:
		s.r.info("detected: cloud (AWS/Azure/GCP creds in env, no PROXMOX_URL).")
	default:
		s.r.info("can't tell from your env whether this is on-prem or cloud — pick one.")
	}
	choices := []string{"on-prem", "cloud"}
	cur := "on-prem"
	if detected == forkCloud {
		cur = "cloud"
	}
	pick := s.r.promptChoice("which mode?", choices, cur)
	switch pick {
	case "cloud":
		// Airgapped + cloud is forbidden by the airgap allowlist
		// anyway; surface the conflict here so the user gets a
		// clear message instead of a silent ErrAirgapped at
		// provider.For() time.
		if s.cfg.Airgapped {
			s.r.info("airgapped is set — cloud fork can't reach vendor APIs. forcing on-prem.")
			s.fork = forkOnPrem
			return nil
		}
		s.fork = forkCloud
	default:
		s.fork = forkOnPrem
	}
	return nil
}

// detectFork is the auto-detection rule from §22.1. Pure function
// (no I/O) so the dispatch in step0_modePick stays trivially
// testable.
//
// Priority: (1) cfg.InfraProvider — the authoritative saved value.
// (2) airgapped flag. (3) env-var heuristics for fresh sessions.
func detectFork(cfg *config.Config) forkType {
	if cfg.InfraProvider != "" {
		if provider.AirgapCompatible(cfg.InfraProvider) {
			return forkOnPrem
		}
		return forkCloud
	}
	if cfg.Airgapped {
		return forkOnPrem
	}
	cloud := os.Getenv("AWS_ACCESS_KEY_ID") != "" ||
		os.Getenv("AZURE_SUBSCRIPTION_ID") != "" ||
		os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != ""
	if cloud {
		return forkCloud
	}
	if os.Getenv("PROXMOX_URL") != "" {
		return forkOnPrem
	}
	return forkUnknown
}

// step1_environment prompts dev / staging / prod. Identical between
// forks. Drives Argo CD / Hubble UI / monitoring add-ons; we stamp
// the relevant cfg fields here so review at step 7 reflects the
// pick.
func (s *state) step1_environment() error {
	s.r.section("environment")
	s.r.info("dev = minimal cluster; staging = + Argo; prod = + monitoring + backups.")
	cur := "staging"
	if s.env != "" {
		cur = string(s.env)
	}
	pick := s.r.promptChoice("which environment?", []string{"dev", "staging", "prod"}, cur)
	s.env = envTier(pick)
	// Stamp the on/off toggles the existing cfg already exposes —
	// these are read by add-ons rendering, dry-run plan, and the
	// orchestrator preflight. Monitoring stays untouched here (no
	// dedicated bool yet).
	switch s.env {
	case envDev:
		s.cfg.ArgoCD.Enabled = false
		s.cfg.ArgoCD.WorkloadEnabled = false
	case envStaging:
		s.cfg.ArgoCD.Enabled = true
		s.cfg.ArgoCD.WorkloadEnabled = false
	case envProd:
		s.cfg.ArgoCD.Enabled = true
		s.cfg.ArgoCD.WorkloadEnabled = true
		s.cfg.CertManagerEnabled = true
	}
	return nil
}

// step2_resilience prompts the resilience tier. The set of valid
// values differs per fork — on-prem stops at HA-across-hosts
// (single-site by definition), cloud goes up to HA-multi-region.
// Drives cp_nodes, which we stamp into cfg.ControlPlaneMachineCount.
func (s *state) step2_resilience() error {
	s.r.section("resilience")
	var choices []string
	if s.fork == forkOnPrem {
		choices = []string{"single-host", "ha-across-hosts"}
	} else {
		choices = []string{"single-az", "ha", "ha-multi-region"}
	}
	cur := choices[0]
	pick := s.r.promptChoice("how resilient should the cluster be?", choices, cur)
	switch pick {
	case "single-host", "single-az":
		s.resil = resilienceSingle
		s.cfg.ControlPlaneMachineCount = "1"
	case "ha-across-hosts", "ha":
		s.resil = resilienceHA
		s.cfg.ControlPlaneMachineCount = "3"
	case "ha-multi-region":
		s.resil = resilienceHAMulti
		s.cfg.ControlPlaneMachineCount = "3"
	}
	return nil
}

// step3_workloadShape gathers workload sizing. Both forks ask
// apps × template, database GB, optional add-ons. Cloud fork
// additionally asks egress (REQUIRED — see §23.6 sandbag defense);
// on-prem omits egress entirely.
func (s *state) step3_workloadShape() error {
	s.r.section("workload shape")
	s.r.info("apps come in templates: light (100m/128MB), medium (200m/256MB), heavy (500m/1GB).")
	s.workload.Apps = s.promptAppBuckets(s.workload.Apps)
	s.workload.DBGB = s.promptIntVal("database GB", s.workload.DBGB)
	if s.fork == forkCloud {
		// REQUIRED on cloud (§23.6). Default = db_GB × 2 — the lazy
		// catch for "I serve my DB to users" patterns.
		def := s.workload.EgressGBMo
		if def == 0 {
			def = s.workload.DBGB * 2
		}
		s.workload.EgressGBMo = s.promptIntVal("egress GB/month (default db × 2)", def)
	}
	s.workload.HasQueue = s.r.promptYesNo("add-on: message queue?", s.workload.HasQueue)
	if s.workload.HasQueue {
		def := cost.SubstituteFootprint(cost.MSMessageQueue)
		if s.workload.QueueCPUMilli == 0 {
			s.workload.QueueCPUMilli = def.CPUMillicores
		}
		if s.workload.QueueMemMiB == 0 {
			s.workload.QueueMemMiB = def.MemoryMiB
		}
		if s.workload.QueueVolGB == 0 {
			s.workload.QueueVolGB = def.PersistentGB
		}
		s.workload.QueueCPUMilli = s.promptIntVal("  queue CPU (millicores)", s.workload.QueueCPUMilli)
		s.workload.QueueMemMiB = s.promptIntVal("  queue memory (MiB)", s.workload.QueueMemMiB)
		s.workload.QueueVolGB = s.promptIntVal("  queue volume (GB)", s.workload.QueueVolGB)
	}
	s.workload.HasObjStore = s.r.promptYesNo("add-on: object storage?", s.workload.HasObjStore)
	if s.workload.HasObjStore {
		def := cost.SubstituteFootprint(cost.MSObjectStore)
		if s.workload.ObjStoreCPUMilli == 0 {
			s.workload.ObjStoreCPUMilli = def.CPUMillicores
		}
		if s.workload.ObjStoreMemMiB == 0 {
			s.workload.ObjStoreMemMiB = def.MemoryMiB
		}
		if s.workload.ObjStoreVolGB == 0 {
			s.workload.ObjStoreVolGB = def.PersistentGB
		}
		s.workload.ObjStoreCPUMilli = s.promptIntVal("  obj-storage CPU (millicores)", s.workload.ObjStoreCPUMilli)
		s.workload.ObjStoreMemMiB = s.promptIntVal("  obj-storage memory (MiB)", s.workload.ObjStoreMemMiB)
		s.workload.ObjStoreVolGB = s.promptIntVal("  obj-storage volume (GB)", s.workload.ObjStoreVolGB)
	}
	s.workload.HasCache = s.r.promptYesNo("add-on: in-memory cache?", s.workload.HasCache)
	if s.workload.HasCache {
		def := cost.SubstituteFootprint(cost.MSCache)
		if s.workload.CacheCPUMilli == 0 {
			s.workload.CacheCPUMilli = def.CPUMillicores
		}
		if s.workload.CacheMemMiB == 0 {
			s.workload.CacheMemMiB = def.MemoryMiB
		}
		s.workload.CacheCPUMilli = s.promptIntVal("  cache CPU (millicores)", s.workload.CacheCPUMilli)
		s.workload.CacheMemMiB = s.promptIntVal("  cache memory (MiB)", s.workload.CacheMemMiB)
	}
	// Stamp worker count from the workload size so orchestrator
	// code paths see a sensible number while feasibility-derived
	// sizing matures.
	totalApps := 0
	for _, b := range s.workload.Apps {
		totalApps += b.Count
	}
	if totalApps > 0 {
		// Heuristic: 1 worker per 4 medium-equivalent apps, min 1.
		w := totalApps / 4
		if w < 1 {
			w = 1
		}
		s.cfg.WorkerMachineCount = strconv.Itoa(w)
	}
	// §23 feasibility + cost paths read cfg.Workload — keep it in
	// sync with the walkthrough-local s.workload.
	syncWorkloadShapeToCfg(s.cfg, s.workload, s.resil, s.env, s.fork)
	return nil
}

// syncWorkloadShapeToCfg copies xapiri's step-3 answers onto
// cfg.Workload so feasibility.Check and any cost estimator that keys
// off the stated product shape see the same numbers the user typed.
func syncWorkloadShapeToCfg(cfg *config.Config, w workloadShape, resil resilienceTier, env envTier, fork forkType) {
	if cfg == nil {
		return
	}
	apps := make([]config.AppGroup, 0, len(w.Apps))
	for _, b := range w.Apps {
		if b.Count <= 0 {
			continue
		}
		tpl := strings.ToLower(strings.TrimSpace(b.Template))
		if tpl != "light" && tpl != "medium" && tpl != "heavy" {
			continue
		}
		apps = append(apps, config.AppGroup{Count: b.Count, Template: tpl})
	}
	var res string
	switch resil {
	case resilienceHA:
		res = "ha"
	case resilienceHAMulti:
		res = "ha-mr"
	default:
		res = "single"
	}
	var envStr string
	switch env {
	case envStaging:
		envStr = "staging"
	case envProd:
		envStr = "prod"
	default:
		envStr = "dev"
	}
	egress := w.EgressGBMo
	if fork == forkOnPrem {
		// On-prem fork never prompts egress; feasibility's §23.6
		// sandbag only applies to the cloud cost-compare path — use
		// the same lazy default as the cloud prompt so Check() doesn't
		// attach a spurious "egress unset" block when a later CLI run
		// sets BudgetUSDMonth on the same cfg.
		if egress <= 0 && w.DBGB > 0 {
			egress = w.DBGB * 2
		}
	}
	cfg.Workload = config.WorkloadShape{
		Apps:          apps,
		DatabaseGB:    w.DBGB,
		EgressGBMonth: egress,
		Resilience:    res,
		Environment:   envStr,
		HasQueue:      w.HasQueue,
		HasObjStore:   w.HasObjStore,
		HasCache:      w.HasCache,
	}
	// Stamp add-on resource overrides so cost.AddonCostItem reads them.
	// Only write non-zero values; a disabled add-on keeps any prior override
	// so re-enabling it on the next run restores the operator's sizing.
	if w.HasQueue {
		if w.QueueCPUMilli > 0 {
			cfg.MQCPUMillicoresOverride = w.QueueCPUMilli
		}
		if w.QueueMemMiB > 0 {
			cfg.MQMemoryMiBOverride = w.QueueMemMiB
		}
		if w.QueueVolGB > 0 {
			cfg.MQVolumeGBOverride = w.QueueVolGB
		}
	}
	if w.HasObjStore {
		if w.ObjStoreCPUMilli > 0 {
			cfg.ObjStoreCPUMillicoresOverride = w.ObjStoreCPUMilli
		}
		if w.ObjStoreMemMiB > 0 {
			cfg.ObjStoreMemoryMiBOverride = w.ObjStoreMemMiB
		}
		if w.ObjStoreVolGB > 0 {
			cfg.ObjStoreVolumeGBOverride = w.ObjStoreVolGB
		}
	}
	if w.HasCache {
		if w.CacheCPUMilli > 0 {
			cfg.CacheCPUMillicoresOverride = w.CacheCPUMilli
		}
		if w.CacheMemMiB > 0 {
			cfg.CacheMemoryMiBOverride = w.CacheMemMiB
		}
	}
}

// promptAppBuckets reads space-separated `count×template` pairs
// (e.g. "6 medium 2 heavy" or "6×medium,2×heavy"). Empty input
// preserves the existing buckets. Lenient on punctuation because
// the user shouldn't have to remember the exact syntax.
func (s *state) promptAppBuckets(cur []appBucket) []appBucket {
	curStr := formatAppBuckets(cur)
	v := s.r.promptString("apps (e.g. '6 medium 2 heavy')", curStr)
	if v == "" {
		if len(cur) == 0 {
			// Nothing on either side — give them a sensible default
			// so the feasibility check has something to chew on.
			return []appBucket{{Count: 4, Template: "medium"}}
		}
		return cur
	}
	parsed := parseAppBuckets(v)
	if len(parsed) == 0 {
		s.r.info("    (couldn't parse — keeping existing.)")
		if len(cur) == 0 {
			return []appBucket{{Count: 4, Template: "medium"}}
		}
		return cur
	}
	return parsed
}

// formatAppBuckets renders the apps in the same shape parseAppBuckets
// reads. Used to populate the prompt's [bracketed default].
func formatAppBuckets(b []appBucket) string {
	if len(b) == 0 {
		return ""
	}
	parts := make([]string, 0, len(b))
	for _, x := range b {
		parts = append(parts, fmt.Sprintf("%d %s", x.Count, x.Template))
	}
	return strings.Join(parts, " ")
}

// parseAppBuckets is the lenient parser. Accepts:
//   "6 medium 2 heavy"
//   "6 medium, 2 heavy"
//   "6×medium 2×heavy"
//   "6xmedium,2xheavy"
// Anything not in {light,medium,heavy} after a count is dropped on
// the floor (the prompt re-asks if the result is empty).
func parseAppBuckets(s string) []appBucket {
	clean := strings.NewReplacer(",", " ", "×", " ", "x", " ", "*", " ").Replace(s)
	tokens := strings.Fields(clean)
	out := []appBucket{}
	for i := 0; i+1 < len(tokens); i += 2 {
		n, err := strconv.Atoi(tokens[i])
		if err != nil || n < 0 {
			continue
		}
		tpl := strings.ToLower(tokens[i+1])
		if tpl != "light" && tpl != "medium" && tpl != "heavy" {
			continue
		}
		out = append(out, appBucket{Count: n, Template: tpl})
	}
	return out
}

// promptIntVal is a small int-typed wrapper around r.promptInt.
// Local rather than in prompts.go so the existing string-typed
// helpers stay untouched (the cluster-sizing fields on cfg are
// strings; the workload struct's are ints).
func (s *state) promptIntVal(label string, cur int) int {
	curStr := ""
	if cur > 0 {
		curStr = strconv.Itoa(cur)
	}
	v := s.r.promptInt(label, curStr)
	if v == "" {
		return cur
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return cur
	}
	return n
}

// step6_providerDetails surfaces the per-provider string fields
// the user hasn't filled yet. Reuses the reflection walk the prior
// implementation used in promptProviderFields so new providers
// (and new fields) light up automatically. Skips fields that are
// already non-empty so re-running xapiri after partial config
// doesn't pester the user about settings they already entered.
func (s *state) step6_providerDetails() error {
	name := s.cfg.InfraProvider
	if name == "" {
		// Should never happen — step 4 sets it on both forks. Bail
		// soft so we don't crash the walkthrough.
		return nil
	}
	sub, ok := providerSubStruct(s.cfg, name)
	if !ok {
		return nil
	}
	s.r.section(fmt.Sprintf("%s settings", name))
	s.ensureGeoLookup()
	if s.geoOK && geoHasCentroids(name) {
		if ranked := geoRankedRegions(name, s.geoLat, s.geoLon, 8); len(ranked) > 0 {
			s.r.info("nearest %s zones (great-circle): %s", name, strings.Join(ranked, ", "))
		}
	}
	t := sub.Type()
	any := false
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		if isInternalBookkeeping(f.Name) {
			continue
		}
		if isOverheadField(f.Name) {
			// Already priced into the compare row using a default.
			// Re-prompting would let the user enter a number that
			// silently disagrees with the headline they just saw.
			// Settable via --<provider>-<flag> when needed.
			continue
		}
		cur := sub.Field(i).String()
		if cur != "" {
			// Already filled — skip (per §22.4 spec). Re-running
			// xapiri shouldn't re-prompt for fields that already
			// have values.
			continue
		}
		any = true
		hint := geoBracketDefault(name, f.Name, s.geoLat, s.geoLon, s.geoOK)
		if hint == "" {
			hint = cur
		}
		var ans string
		if isSensitiveFieldName(f.Name) {
			ans = s.r.promptSecret(f.Name, cur)
		} else {
			ans = s.r.promptString(f.Name, hint)
		}
		if sub.Field(i).CanSet() {
			sub.Field(i).SetString(ans)
		}
	}
	if !any {
		s.r.info("(nothing to ask — every %s field already has a value.)", name)
	}
	return nil
}

// step7_review renders the resolved cfg via plan.NewTextWriter so
// the review reads in the same Unicode-marker style as --dry-run.
// Adds a fork-specific cost line: TCO amortization on on-prem
// (informational; "TCO not configured" when --hardware-* unset),
// monthly-bill estimate on cloud (from the chosen-provider's
// CompareClouds row).
func (s *state) step7_review() error {
	s.r.section("review")
	pw := plan.NewTextWriter(s.w)
	pw.Section("Walkthrough summary")
	pw.Bullet("fork:           %s", s.fork.String())
	pw.Bullet("environment:    %s", s.env)
	pw.Bullet("resilience:     %s (control-plane=%s)", s.resil, s.cfg.ControlPlaneMachineCount)
	pw.Bullet("provider:       %s", s.cfg.InfraProvider)
	if s.fork == forkOnPrem {
		pw.Bullet("bootstrap mode: %s", s.cfg.BootstrapMode)
		if s.cfg.Capacity.AllowOvercommit {
			pw.Bullet("overcommit:     allowed")
		}
	}
	pw.Bullet("workload apps:  %s", formatAppBuckets(s.workload.Apps))
	pw.Bullet("database GB:    %d", s.workload.DBGB)
	if s.fork == forkCloud {
		pw.Bullet("egress GB/mo:   %d", s.workload.EgressGBMo)
	}
	if s.workload.HasQueue || s.workload.HasObjStore || s.workload.HasCache {
		pw.Bullet("add-ons:        %s", addonList(s.workload))
	}
	pw.Bullet("airgapped:      %v", s.cfg.Airgapped)

	// Network / cluster identity.
	pw.Section("Network")
	pw.Bullet("workload VIP:   %s", s.cfg.ControlPlaneEndpointIP)
	pw.Bullet("node IP range:  %s", s.cfg.NodeIPRanges)
	pw.Bullet("gateway:        %s  prefix: /%s", s.cfg.Gateway, s.cfg.IPPrefix)
	pw.Bullet("DNS:            %s", s.cfg.DNSServers)
	pw.Bullet("workload name:  %s", s.cfg.WorkloadClusterName)
	if s.cfg.Pivot.Enabled {
		pw.Bullet("mgmt VIP:       %s", s.cfg.Mgmt.ControlPlaneEndpointIP)
		pw.Bullet("mgmt IP range:  %s", s.cfg.Mgmt.NodeIPRanges)
		pw.Bullet("mgmt name:      %s", s.cfg.Mgmt.ClusterName)
	}

	// Fork-specific cost line.
	if s.fork == forkOnPrem {
		s.renderTCOLine(pw)
	} else {
		s.renderCloudCostLine(pw)
	}

	// Final feasibility check before persisting (§22.2/22.3 step 7).
	pw.Section("Feasibility (final check)")
	var verdict FeasibilityVerdict
	var ferr error
	if s.fork == forkOnPrem {
		verdict, ferr = runFeasibilityCheckOnPrem(s.cfg)
	} else {
		verdict, ferr = runFeasibilityCheck(s.cfg)
	}
	if verdict == FeasibilityUnchecked {
		pw.Skip("feasibility gate not wired — proceed at own risk.")
	} else if ferr != nil {
		pw.Bullet("%s %s — %v", verdict.symbol(), verdict, ferr)
	} else {
		pw.Bullet("%s %s", verdict.symbol(), verdict)
	}

	writeDefault := verdict != FeasibilityInfeasible
	if verdict == FeasibilityInfeasible {
		s.r.info("⚠ cluster shape may not fit the host inventory — see reason above.")
		s.r.info("  You can proceed anyway if you know your hardware can handle it.")
	}
	if !s.r.promptYesNo("write to kind?", writeDefault) {
		return ErrUserExit
	}
	return nil
}

// addonList joins the optional add-on flags into a comma-separated
// list for the review summary. Empty strings filter out cleanly.
func addonList(w workloadShape) string {
	var parts []string
	if w.HasQueue {
		parts = append(parts, "queue")
	}
	if w.HasObjStore {
		parts = append(parts, "object-storage")
	}
	if w.HasCache {
		parts = append(parts, "cache")
	}
	return strings.Join(parts, ", ")
}

// renderTCOLine draws the on-prem TCO-amortization line. Pure
// derivation from --hardware-* flags (capex / years × 12 +
// electricity + support). When the flags are unset, prints the
// "TCO not configured" placeholder per §22.2 so the user knows the
// number is absent rather than zero.
func (s *state) renderTCOLine(pw plan.Writer) {
	pw.Section("TCO (on-prem)")
	c := s.cfg
	if c.HardwareCostUSD == 0 && c.HardwareWatts == 0 && c.HardwareSupportUSDMonth == 0 {
		pw.Skip("TCO not configured (set --hardware-cost-usd / --hardware-watts / --hardware-support-usd-month).")
		return
	}
	var amort float64
	if c.HardwareUsefulLifeYears > 0 {
		amort = c.HardwareCostUSD / (c.HardwareUsefulLifeYears * 12)
	}
	elec := c.HardwareWatts / 1000.0 * c.HardwareKWHRateUSD * 720
	total := amort + elec + c.HardwareSupportUSDMonth
	pw.Bullet("amortization:   %s/mo", pricing.FormatTaller(amort, "USD"))
	pw.Bullet("electricity:    %s/mo (%.0fW × %s/kWh × 720h)",
		pricing.FormatTaller(elec, "USD"), c.HardwareWatts,
		pricing.FormatTaller(c.HardwareKWHRateUSD, "USD"))
	pw.Bullet("support:        %s/mo", pricing.FormatTaller(c.HardwareSupportUSDMonth, "USD"))
	pw.Bullet("derived total:  %s/mo", pricing.FormatTaller(total, "USD"))
}

// renderCloudCostLine pulls the chosen provider's row out of
// CompareClouds and renders a one-line cost summary. Falls back
// to "(unpriced)" if the provider has no estimator wired or the
// vendor API was unreachable.
func (s *state) renderCloudCostLine(pw plan.Writer) {
	pw.Section("Monthly bill (cloud)")
	rows := compareCloudsForReview(s.cfg, nil)
	for _, r := range rows {
		if r.ProviderName != s.cfg.InfraProvider {
			continue
		}
		if r.Err != nil {
			pw.Skip("%s estimate unavailable: %v", r.ProviderName, r.Err)
			return
		}
		pw.Bullet("%s estimate: %s/mo",
			r.ProviderName, pricing.FormatTaller(r.Estimate.TotalUSDMonthly, "USD"))
		if s.budgetUSDMonth > 0 {
			pw.Bullet("budget:         %s/mo (after %.0f%% headroom: %s)",
				pricing.FormatTaller(s.budgetUSDMonth, "USD"),
				s.headroomPct*100,
				pricing.FormatTaller(s.budgetAfterHeadroom, "USD"))
		}
		if r.Estimate.Note != "" {
			pw.Bullet("note:           %s", r.Estimate.Note)
		}
		return
	}
	pw.Skip("provider %s missing from cost-compare; estimate unavailable.", s.cfg.InfraProvider)
}

// step8_persistAndDecide writes the bootstrap-config Secret to the
// kind cluster (or, with YAGE_XAPIRI_DISK_FALLBACK=1, to disk),
// echoes the equivalent `yage <flags>` invocation so the operator
// can capture it for pipelines / cost reports, and asks "deploy
// now?" Yes flips s.deployNow; no exits quietly.
func (s *state) step8_persistAndDecide() error {
	s.r.section("persist + decide")
	dest, err := persistConfig(s.w, s.cfg)
	if err != nil {
		fmt.Fprintf(s.w, "  failed to persist config: %v\n", err)
		return err
	}
	s.r.info("written to %s", dest)

	// Echo the equivalent CLI. Sensitive values render as $ENV refs
	// so the output is safe to paste into a pipeline definition or
	// runbook. `yage --print-command` reproduces this output later.
	fmt.Fprintln(s.w)
	fmt.Fprintln(s.w, "  to reproduce this configuration without the wizard:")
	fmt.Fprintln(s.w)
	for _, ln := range strings.Split(cli.RenderCommand(s.cfg, cli.SensitiveAsEnv), "\n") {
		fmt.Fprintln(s.w, "    "+ln)
	}
	fmt.Fprintln(s.w)
	fmt.Fprintln(s.w, "  (also retrievable any time via: yage --print-command)")
	fmt.Fprintln(s.w)

	s.deployNow = s.r.promptYesNo("deploy now?", false)
	if s.deployNow {
		s.cfg.XapiriDeployNow = true
	} else {
		s.r.info("nothing deployed; the next non-xapiri yage run will pick the saved config up.")
	}
	return nil
}

// runSharedTail bundles steps 6, 7, 8 — every fork ends the same
// way once the fork-specific 4 + 5 are done. Returns ErrUserExit
// if the user bailed at the review prompt.
func (s *state) runSharedTail() error {
	if err := s.step6_providerDetails(); err != nil {
		return err
	}
	if err := s.step7_review(); err != nil {
		return err
	}
	return s.step8_persistAndDecide()
}

// awsAnyCredentialsAvailable returns true when AWS credentials are available
// in any form: explicit key/secret in cfg, AWS SDK env vars (AWS_ACCESS_KEY_ID,
// AWS_PROFILE), or the standard ~/.aws/credentials / ~/.aws/config files.
// newAWSPricingClient now falls back to the SDK default chain, so any of these
// sources will allow a successful Pricing API call.
func awsAnyCredentialsAvailable(cfg *config.Config) bool {
	if cfg.Cost.Credentials.AWSAccessKeyID != "" && cfg.Cost.Credentials.AWSSecretAccessKey != "" {
		return true
	}
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, rel := range []string{".aws/credentials", ".aws/config"} {
			if _, e := os.Stat(filepath.Join(home, rel)); e == nil {
				return true
			}
		}
	}
	return false
}

// gcpAnyCredentialsAvailable mirrors awsAnyCredentialsAvailable for GCP: checks
// the explicit cfg credential first, then the same env-var fallbacks that the
// pricing fetcher uses (YAGE_GCP_API_KEY, GOOGLE_BILLING_API_KEY).
func gcpAnyCredentialsAvailable(cfg *config.Config) bool {
	if cfg.Cost.Credentials.GCPAPIKey != "" {
		return true
	}
	return os.Getenv("YAGE_GCP_API_KEY") != "" || os.Getenv("GOOGLE_BILLING_API_KEY") != ""
}

// disableProvidersMissingCredentials syncs cfg.SkipProviders with credential
// availability. Azure, Linode, and OCI use public APIs and are never touched.
// AWS is skipped only when no credentials exist in any form (explicit key/secret,
// env vars, or ~/.aws/ files) — it falls back to the SDK credential chain.
// When cfg.InfraProvider is set explicitly (non-defaulted), only that provider
// is checked; others are left alone.
//
// Critically, providers are also REMOVED from SkipProviders when credentials
// become available mid-session (e.g. after the [costs] credential form is
// submitted), so the live cost bar updates without requiring a restart.
func disableProvidersMissingCredentials(cfg *config.Config) {
	type check struct {
		name    string
		missing bool
	}
	checks := []check{
		{"aws", !awsAnyCredentialsAvailable(cfg)},
		{"gcp", !gcpAnyCredentialsAvailable(cfg)},
		{"hetzner", cfg.Cost.Credentials.HetznerToken == ""},
		{"digitalocean", cfg.Cost.Credentials.DigitalOceanToken == ""},
		{"ibmcloud", cfg.Cost.Credentials.IBMCloudAPIKey == ""},
	}
	skipped := make(map[string]struct{})
	for _, p := range strings.Split(cfg.SkipProviders, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			skipped[p] = struct{}{}
		}
	}
	for _, c := range checks {
		if cfg.InfraProvider != "" && !cfg.InfraProviderDefaulted && c.name != cfg.InfraProvider {
			continue
		}
		if c.missing {
			skipped[c.name] = struct{}{}
		} else {
			delete(skipped, c.name) // credentials now available — restore provider
		}
	}
	names := make([]string, 0, len(skipped))
	for n := range skipped {
		names = append(names, n)
	}
	cfg.SkipProviders = strings.Join(names, ",")
}

// stepCostCompareSetup prompts for API credentials of every provider that
// needs them and saves the result to the yage-system/cost-compare-config
// Secret. Runs only when cfg.CostCompareEnabled is true. Blank entry with
// no prior value causes the provider to be auto-disabled via SkipProviders.
func (s *state) stepCostCompareSetup() error {
	s.r.info("")
	s.r.info("── cost-estimation credentials ──────────────────────────────")
	s.r.info("Providers with no key will be disabled in the cost panel.")
	s.r.info("Press enter to keep an existing value; leave blank to skip a provider.")
	s.r.info("")

	type entry struct {
		label   string
		key     string // secret key name
		ptr     *string
		twoLine bool // AWS needs two prompts
		ptr2    *string
		key2    string
	}

	cfg := s.cfg
	entries := []entry{
		{label: "AWS Access Key ID", key: "aws-access-key-id", ptr: &cfg.Cost.Credentials.AWSAccessKeyID},
		{label: "AWS Secret Access Key", key: "aws-secret-access-key", ptr: &cfg.Cost.Credentials.AWSSecretAccessKey},
		{label: "GCP API Key", key: "gcp-api-key", ptr: &cfg.Cost.Credentials.GCPAPIKey},
		{label: "Hetzner Token", key: "hetzner-token", ptr: &cfg.Cost.Credentials.HetznerToken},
		{label: "DigitalOcean Token", key: "digitalocean-token", ptr: &cfg.Cost.Credentials.DigitalOceanToken},
		{label: "IBM Cloud API Key", key: "ibmcloud-api-key", ptr: &cfg.Cost.Credentials.IBMCloudAPIKey},
	}

	// Narrow to just the infra-provider's entry when set explicitly.
	infraFilter := cfg.InfraProvider != "" && !cfg.InfraProviderDefaulted
	infraKeys := map[string]bool{
		"aws":          infraFilter && cfg.InfraProvider == "aws",
		"gcp":          infraFilter && cfg.InfraProvider == "gcp",
		"hetzner":      infraFilter && cfg.InfraProvider == "hetzner",
		"digitalocean": infraFilter && cfg.InfraProvider == "digitalocean",
		"ibmcloud":     infraFilter && cfg.InfraProvider == "ibmcloud",
	}
	providerForKey := map[string]string{
		"aws-access-key-id":     "aws",
		"aws-secret-access-key": "aws",
		"gcp-api-key":           "gcp",
		"hetzner-token":         "hetzner",
		"digitalocean-token":    "digitalocean",
		"ibmcloud-api-key":      "ibmcloud",
	}

	creds := map[string]string{}
	for _, e := range entries {
		if infraFilter && !infraKeys[providerForKey[e.key]] {
			continue
		}
		val := s.r.promptSecret(e.label, *e.ptr)
		if val == "" && *e.ptr == "" {
			fmt.Fprintf(s.w, "  → %s: no key provided — provider will be disabled\n", providerForKey[e.key])
		} else if val != "" {
			*e.ptr = val
			creds[e.key] = val
		} else {
			// keep existing value
			creds[e.key] = *e.ptr
		}
	}

	disableProvidersMissingCredentials(cfg)

	// Sync new credentials to the pricing package so the rest of the
	// session (cost-compare, dashboard) can call the APIs immediately.
	pricing.SetCredentials(pricing.Credentials{
		AWSAccessKeyID:     cfg.Cost.Credentials.AWSAccessKeyID,
		AWSSecretAccessKey: cfg.Cost.Credentials.AWSSecretAccessKey,
		GCPAPIKey:          cfg.Cost.Credentials.GCPAPIKey,
		HetznerToken:       cfg.Cost.Credentials.HetznerToken,
		DigitalOceanToken:  cfg.Cost.Credentials.DigitalOceanToken,
		IBMCloudAPIKey:     cfg.Cost.Credentials.IBMCloudAPIKey,
	})

	if len(creds) > 0 {
		if err := kindsync.WriteCostCompareSecret(cfg, creds); err != nil {
			fmt.Fprintf(s.w, "  xapiri: warning — could not persist cost credentials to kind: %v\n", err)
			fmt.Fprintln(s.w, "  (credentials are active for this session; re-run --cost-compare-config to retry)")
		} else {
			fmt.Fprintln(s.w, "  ✓ credentials saved to yage-system/cost-compare-config")
		}
	}
	s.r.info("")
	return nil
}