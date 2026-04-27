// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Global dry-run plan printer for the orchestrator package.
//
// PrintPlan is invoked from Run() when cfg.DryRun is true. It walks the
// orchestrator's phases and prints a structured "this is what the next
// real run would do" report based on the resolved Config, without
// executing anything. The output is one section per phase plus a final
// "no state was changed" footer.
//
// Distinct from cfg.PivotDryRun: that flag actually provisions the mgmt
// cluster on Proxmox and runs `clusterctl move --dry-run`, while
// cfg.DryRun runs zero phases.
package orchestrator

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lpasquali/yage/internal/cluster/capacity"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/plan"
	"github.com/lpasquali/yage/internal/platform/shell"
)

// PrintPlan writes a structured "would do" plan to stdout based on cfg.
// Mirrors Run() phase ordering so reading top-to-bottom shows the real
// run's sequence.
func PrintPlan(cfg *config.Config) {
	PrintPlanTo(os.Stdout, cfg)
}

// PrintPlanTo is the same as PrintPlan but writes to an arbitrary
// io.Writer. Used by the snapshot-golden tests in
// plan_golden_test.go so the full dry-run output can be captured into
// a buffer and compared against an on-disk fixture.
func PrintPlanTo(w io.Writer, cfg *config.Config) {
	pw := plan.NewTextWriter(w)
	hr := strings.Repeat("─", 76)

	fmt.Fprintln(w, hr)
	fmt.Fprintln(w, "📝 DRY-RUN PLAN — yage would perform the following actions")
	fmt.Fprintln(w, hr)

	// Provider-specific Describe* sections live behind the Provider
	// interface. Resolve once; if the provider is unknown or refused
	// (airgapped + cloud), the three hooks below become no-op Skip
	// lines. Cross-cutting sections (capacity, cost, allocations,
	// retention) stay in the orchestrator.
	prov, perr := provider.For(cfg)

	planStandalone(w, cfg)
	planPrePhase(w, cfg)
	planPhase1(w, cfg)
	describeIdentity(pw, cfg, prov, perr)
	planPhase2Clusterctl(w, cfg)
	planPhase2Kind(w, cfg)
	planPhase2CAPI(pw, w, cfg, prov, perr)
	describeWorkload(pw, cfg, prov, perr)
	describePivot(pw, cfg, prov, perr)
	planPhase210ArgoCD(w, cfg)
	planFinal(w, cfg)
	planCapacity(w, cfg)
	planAllocations(w, cfg)
	planMonthlyCost(w, cfg)
	planCostCompare(w, cfg)
	planRetention(w, cfg)

	fmt.Fprintln(w, hr)
	fmt.Fprintln(w, "✅ Dry run complete — NO state was changed.")
	fmt.Fprintln(w, "   Re-run without --dry-run to execute the plan above.")
	fmt.Fprintln(w, hr)
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n▸ %s\n", title)
}

func bullet(w io.Writer, format string, args ...interface{}) {
	fmt.Fprintf(w, "    • "+format+"\n", args...)
}

func skip(w io.Writer, format string, args ...interface{}) {
	fmt.Fprintf(w, "    ◦ skip: "+format+"\n", args...)
}

func planStandalone(w io.Writer, cfg *config.Config) {
	switch {
	case cfg.BootstrapKindStateOp == "backup":
		section(w, "Standalone: kind backup")
		bullet(w, "snapshot kind cluster '%s' to %s", cfg.KindClusterName, cfg.BootstrapKindBackupOut)
		fmt.Fprintln(w, "    (no other phases would run)")
	case cfg.BootstrapKindStateOp == "restore":
		section(w, "Standalone: kind restore")
		bullet(w, "restore kind cluster '%s' from %s", cfg.KindClusterName, cfg.BootstrapKindStatePath)
		fmt.Fprintln(w, "    (no other phases would run)")
	case cfg.WorkloadRolloutStandalone:
		section(w, "Standalone: --workload-rollout")
		bullet(w, "mode=%s, workload=%s/%s", cfg.WorkloadRolloutMode, cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
		fmt.Fprintln(w, "    (no other phases would run)")
	case cfg.ArgoCD.PrintAccessStandalone:
		section(w, "Standalone: --argocd-print-access")
		bullet(w, "print Argo CD access info for workload %s/%s", cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
		fmt.Fprintln(w, "    (no other phases would run)")
	case cfg.ArgoCD.PortForwardStandalone:
		section(w, "Standalone: --argocd-port-forward")
		bullet(w, "kubectl port-forward Argo CD on 127.0.0.1:%s", firstNonEmptyStr(cfg.ArgoCD.PortForwardPort, "8443"))
		fmt.Fprintln(w, "    (no other phases would run)")
	}
}

func planPrePhase(w io.Writer, cfg *config.Config) {
	if cfg.BootstrapKindStateOp != "" || cfg.WorkloadRolloutStandalone ||
		cfg.ArgoCD.PrintAccessStandalone || cfg.ArgoCD.PortForwardStandalone {
		return
	}
	section(w, "Pre-phase")
	if cfg.Purge {
		bullet(w, "PURGE: delete generated files + Terraform state, kind workload Cluster, capi-manifest Secret")
	}
	bullet(w, "ClusterSetID = %s", firstNonEmptyStr(cfg.ClusterSetID, "<generated>"))
}

func planPhase1(w io.Writer, cfg *config.Config) {
	section(w, "Dependency install — host CLIs for the operator")
	bullet(w, "system packages: git, curl, python3 (apt/dnf/yum/apk)")
	bullet(w, "Docker: install if missing; upgrade unless --no-delete-kind or kind cluster exists")
	for _, t := range []struct{ name, ver string }{
		{"kubectl", cfg.KubectlVersion},
		{"kind", cfg.KindVersion},
		{"clusterctl", cfg.ClusterctlVersion},
		{"cilium", cfg.CiliumCLIVersion},
		{"helm", "latest"},
		{"argocd", cfg.ArgoCD.Version},
		{"kyverno", cfg.KyvernoCLIVersion},
		{"cmctl", cfg.CmctlVersion},
		{"tofu", cfg.OpenTofuVersion},
	} {
		have := "(install)"
		if shell.CommandExists(t.name) {
			have = "(present — reinstall if version drift detected)"
		}
		bullet(w, "%-12s %-10s %s", t.name, t.ver, have)
	}
}

// describeIdentity delegates to the provider's
// PlanDescriber.DescribeIdentity hook. If no provider resolved
// (unknown name, airgapped + cloud), surface a Skip explaining why.
func describeIdentity(pw plan.Writer, cfg *config.Config, prov provider.Provider, perr error) {
	if perr != nil {
		pw.Section("Identity bootstrap")
		pw.Skip("provider %q: %v", cfg.InfraProvider, perr)
		return
	}
	prov.DescribeIdentity(pw, cfg)
}

func planPhase2Clusterctl(w io.Writer, cfg *config.Config) {
	section(w, "clusterctl credentials")
	if cfg.ClusterctlCfg != "" {
		bullet(w, "use explicit clusterctl config: %s", cfg.ClusterctlCfg)
	} else {
		bullet(w, "synthesize ephemeral clusterctl config from provider env vars")
	}
}

func planPhase2Kind(w io.Writer, cfg *config.Config) {
	section(w, "kind cluster")
	want := "kind-" + cfg.KindClusterName
	if k8sclient.ContextExists(want) {
		if cfg.Force && !cfg.NoDeleteKind {
			bullet(w, "force-delete + recreate kind cluster '%s'", cfg.KindClusterName)
		} else {
			bullet(w, "reuse existing kind cluster '%s'", cfg.KindClusterName)
		}
	} else {
		bullet(w, "create kind cluster '%s' (config: %s)", cfg.KindClusterName, firstNonEmptyStr(cfg.KindConfig, "<ephemeral default>"))
	}
}

func planPhase2CAPI(pw plan.Writer, w io.Writer, cfg *config.Config, prov provider.Provider, perr error) {
	section(w, "clusterctl init on kind")
	bullet(w, "providers: infrastructure=%s, ipam=%s, addon=helm", cfg.InfraProvider, cfg.IPAMProvider)
	bullet(w, "infra provider image pinned/built if needed")
	if perr == nil {
		prov.DescribeClusterctlInit(pw, cfg)
	}
}

// describeWorkload delegates to the provider's
// PlanDescriber.DescribeWorkload hook. Provider-agnostic; per-cloud
// bullet text lives in internal/provider/<name>/plan.go.
func describeWorkload(pw plan.Writer, cfg *config.Config, prov provider.Provider, perr error) {
	if perr != nil {
		pw.Section("Workload Cluster")
		pw.Skip("provider %q: %v", cfg.InfraProvider, perr)
		return
	}
	prov.DescribeWorkload(pw, cfg)
}

// describePivot delegates to the provider's
// PlanDescriber.DescribePivot hook. When pivot is not applicable
// the provider emits a Skip; the orchestrator does not hardcode
// Proxmox-specific pivot bullets.
func describePivot(pw plan.Writer, cfg *config.Config, prov provider.Provider, perr error) {
	if perr != nil {
		pw.Section("Pivot to managed mgmt cluster")
		pw.Skip("provider %q: %v", cfg.InfraProvider, perr)
		return
	}
	prov.DescribePivot(pw, cfg)
}

func planPhase210ArgoCD(w io.Writer, cfg *config.Config) {
	section(w, "Argo CD on workload")
	if !cfg.ArgoCD.Enabled {
		skip(w, "ARGOCD_ENABLED=false (--disable-argocd)")
		return
	}
	bullet(w, "Argo CD Operator + ArgoCD CR (version %s, operator %s)", cfg.ArgoCD.Version, cfg.ArgoCD.OperatorVersion)
	if cfg.ArgoCD.WorkloadEnabled {
		bullet(w, "CAAPH argocd-apps HelmChartProxy → root Application '%s'", cfg.WorkloadClusterName)
		if cfg.ArgoCD.AppOfAppsGitURL != "" {
			bullet(w, "  app-of-apps: %s @ %s, path %s", cfg.ArgoCD.AppOfAppsGitURL, cfg.ArgoCD.AppOfAppsGitRef, cfg.ArgoCD.AppOfAppsGitPath)
		}
	}
}

func planFinal(w io.Writer, cfg *config.Config) {
	section(w, "Final — kind teardown")
	switch {
	case !cfg.Pivot.Enabled:
		skip(w, "PIVOT_ENABLED=false (kind stays — it IS the management cluster)")
	case cfg.Pivot.KeepKind:
		skip(w, "--pivot-keep-kind set")
	case cfg.NoDeleteKind:
		skip(w, "--no-delete-kind set")
	default:
		bullet(w, "delete kind cluster '%s'", cfg.KindClusterName)
	}
}

// planCapacity prints the resource budget summary: requested vs
// available host capacity. The active provider's Inventory drives
// the live-host side; providers that return ErrNotApplicable
// (per §13.4 #1: AWS/Azure/GCP/Hetzner/vSphere) skip the section.
func planCapacity(w io.Writer, cfg *config.Config) {
	section(w, "Capacity budget")
	plan := capacity.PlanFor(cfg)
	threshold := cfg.Capacity.ResourceBudgetFraction
	if threshold <= 0 || threshold > 1 {
		threshold = capacity.DefaultThreshold
	}
	bullet(w, "budget: %.0f%% of available host CPU/memory/storage",
		threshold*100)
	for _, it := range plan.Items {
		bullet(w, "  %-26s %d × (%d cores, %d MiB, %d GB) = %d cores, %d MiB, %d GB",
			it.Name, it.Replicas, it.CPUCores, it.MemoryMiB, it.DiskGB,
			it.CPUCores*it.Replicas, it.MemoryMiB*int64(it.Replicas), it.DiskGB*int64(it.Replicas))
	}
	bullet(w, "TOTAL requested:  %d cores, %d MiB (%d GiB), %d GB disk",
		plan.CPUCores, plan.MemoryMiB, plan.MemoryMiB/1024, plan.StorageGB)

	prov, err := provider.For(cfg)
	if err != nil {
		skip(w, "provider lookup: %v", err)
		return
	}
	inv, err := prov.Inventory(cfg)
	if errors.Is(err, provider.ErrNotApplicable) {
		skip(w, "provider %q has no flat-pool inventory model — preflight skipped (§13.4 #1)", prov.Name())
		return
	}
	if err != nil {
		skip(w, "host-capacity query: %v (run with valid provider creds for live numbers)", err)
		return
	}
	hc := hostCapacityFromInventory(inv)
	used := existingUsageFromInventory(inv)

	bullet(w, "host (allowed nodes %v): %d cores, %d MiB (%d GiB), %d GB disk",
		hc.Nodes, hc.CPUCores, hc.MemoryMiB, hc.MemoryMiB/1024, hc.StorageGB)
	if hc.IsSmallEnv() && cfg.BootstrapMode != "k3s" {
		bullet(w, "💡 host is small — consider --bootstrap-mode k3s for a 1 vCPU / 1 GiB-per-node footprint")
	}

	// Existing-VM awareness: provider.Inventory.Used carries the
	// structured aggregate (cores/mem/disk); the human-readable
	// "existing VMs: N" + "VMs by pool: …" lines come from
	// inv.Notes (provider-defined, Proxmox-shaped). Render both.
	if used.CPUCores > 0 || used.MemoryMiB > 0 || used.StorageGB > 0 {
		bullet(w, "existing usage on host: CPU %d cores, mem %d MiB, disk %d GB",
			used.CPUCores, used.MemoryMiB, used.StorageGB)
	} else {
		bullet(w, "existing usage on host: none (fresh host)")
	}
	for _, n := range inv.Notes {
		// The "allowed nodes" Note is already reflected in the
		// host bullet above; skip duplicate output. Surface the
		// rest verbatim (existing VMs / by-pool / advisories).
		if strings.HasPrefix(n, "allowed nodes:") {
			continue
		}
		bullet(w, "  %s", n)
	}

	tolerancePct := cfg.Capacity.OvercommitTolerancePct
	if tolerancePct <= 0 {
		tolerancePct = capacity.DefaultOvercommitTolerancePct
	}
	verdict, msg := capacity.CheckCombined(plan, hc, used, threshold, tolerancePct)
	switch verdict {
	case capacity.VerdictFits:
		bullet(w, "✅ verdict: fits within %.0f%% soft budget", threshold*100)
		bullet(w, "   %s", msg)
	case capacity.VerdictTight:
		bullet(w, "⚠️  verdict: tight — above soft budget but inside %.0f%% overcommit tolerance",
			tolerancePct)
		bullet(w, "   %s", msg)
		bullet(w, "   real run will proceed with a warning; raise --resource-budget-fraction or shrink the plan if you want headroom.")
	case capacity.VerdictAbort:
		bullet(w, "❌ verdict: abort — over %.0f%% overcommit ceiling", tolerancePct)
		bullet(w, "   %s", msg)
		bullet(w, "   real run aborts; --allow-resource-overcommit forces, --overcommit-tolerance-pct raises the ceiling.")
		if cfg.BootstrapMode != "k3s" {
			if fits, k3sPlan := capacity.WouldFitAsK3s(cfg, hc, threshold); fits {
				bullet(w, "💡 same machine counts would fit under --bootstrap-mode k3s: %d cores / %d MiB / %d GB",
					k3sPlan.CPUCores, k3sPlan.MemoryMiB, k3sPlan.StorageGB)
			}
		}
	}
}

// planAllocations prints the workload-cluster-side allocation plan:
// total worker capacity, system-apps reserve, and the three equal
// buckets (db / observability / product) that share the remainder.
//
// Surfaces a warning when the system reserve alone exceeds total
// worker capacity (operator must either grow workers or pin add-ons
// to the control-plane node).
func planAllocations(w io.Writer, cfg *config.Config) {
	section(w, "Workload allocations (per-bucket budget on workers)")
	a := capacity.AllocationsFor(cfg)
	bullet(w, "total worker capacity:    %d millicores, %d MiB (workers=%s)",
		a.TotalCPUMillicores, a.TotalMemoryMiB, cfg.WorkerMachineCount)
	bullet(w, "system-apps reserve:      %d millicores, %d MiB",
		a.SystemAppsCPUMillicores, a.SystemAppsMemoryMiB)
	bullet(w, "  ├─ argocd (operator + server + repo + redis), kyverno, cert-manager,")
	bullet(w, "  ├─ CSI controller (provider-specific), keycloak (SSO), external-secrets, infisical")
	bullet(w, "  └─ tune via SYSTEM_APPS_CPU_MILLICORES / SYSTEM_APPS_MEMORY_MIB")
	if a.IsOverReserved() {
		bullet(w, "❌ system reserve (%d mCPU / %d MiB) exceeds total worker capacity (%d / %d)",
			a.SystemAppsCPUMillicores, a.SystemAppsMemoryMiB, a.TotalCPUMillicores, a.TotalMemoryMiB)
		bullet(w, "   grow worker count / sizing, OR untaint the control-plane node and let it host system pods")
		return
	}
	bullet(w, "remaining after reserve:  %d millicores, %d MiB", a.RemainCPUMillicores, a.RemainMemoryMiB)
	bullet(w, "  ├─ database:           %d millicores, %d MiB  (cnpg, postgres, redis, …)", a.BucketCPUMillicores, a.BucketMemoryMiB)
	bullet(w, "  ├─ observability:      %d millicores, %d MiB  (vmsingle, OTel, Grafana, Loki, …)", a.BucketCPUMillicores, a.BucketMemoryMiB)
	bullet(w, "  └─ product apps:       %d millicores, %d MiB  (Argo-deployed user workloads)", a.BucketCPUMillicores, a.BucketMemoryMiB)
}

// planMonthlyCost prints the provider-supplied monthly cost
// estimate when one is available (currently AWS only). Self-hosted
// providers (Proxmox, vSphere, CAPD) and ones with too-variable
// pricing (OpenStack) return ErrNotApplicable and the section is
// skipped silently — see docs/aws-cost-tiers.md for the AWS table.
func planMonthlyCost(w io.Writer, cfg *config.Config) {
	p, err := provider.For(cfg)
	if err != nil {
		return
	}
	est, err := p.EstimateMonthlyCostUSD(cfg)
	if err != nil {
		if errors.Is(err, provider.ErrNotApplicable) {
			// Even when the provider opted out, surface the
			// onboarding hint ONCE if the failure was credentials.
			if !pricing.PricingCredsConfigured(p.Name()) {
				section(w, "Estimated monthly cost (provider: "+p.Name()+")")
				bullet(w, "(skipped: %v)", err)
				pricing.MaybePrintOnboarding(w, p.Name())
			}
			return
		}
		section(w, "Estimated monthly cost")
		bullet(w, "(estimate query failed: %v)", err)
		pricing.MaybePrintOnboarding(w, p.Name())
		return
	}
	section(w, "Estimated monthly cost (provider: "+p.Name()+")")
	bullet(w, "%s", pricing.TallerNote())
	for _, it := range est.Items {
		unit := pricing.FormatTaller(it.UnitUSDMonthly, "USD")
		sub := pricing.FormatTaller(it.SubtotalUSD, "USD")
		bullet(w, "%-40s %d × %s = %s", it.Name, it.Qty, unit, sub)
	}
	totalStr := pricing.FormatTaller(est.TotalUSDMonthly, "USD")
	bullet(w, "TOTAL: ~%s / month (%s)", totalStr, pricing.TallerCurrency())
	if est.Note != "" {
		bullet(w, "note: %s", est.Note)
	}
}

// planCostCompare emits a side-by-side cost table when --cost-compare
// is set. Runs every registered provider's EstimateMonthlyCostUSD
// against the same cfg; sorts ascending by total monthly. The
// rightmost column shows what the same total budget would buy as
// persistent block storage on each cloud — handy for picking where
// observability + DB go when storage is the dominant cost driver.
func planCostCompare(w io.Writer, cfg *config.Config) {
	if !cfg.CostCompare {
		return
	}
	cost.PrintComparison(w, cfg)
}

// planRetention prints how many GB of cheap-tier block storage the
// user's budget buys AFTER paying for compute on the active provider.
// No-op when --budget-usd-month is unset.
func planRetention(w io.Writer, cfg *config.Config) {
	if cfg.BudgetUSDMonth <= 0 {
		return
	}
	p, err := provider.For(cfg)
	if err != nil {
		return
	}
	est, err := p.EstimateMonthlyCostUSD(cfg)
	if err != nil {
		return
	}
	budgetStr := pricing.FormatTaller(cfg.BudgetUSDMonth, "USD")
	section(w, fmt.Sprintf("Storage retention at %s / month budget (%s)", budgetStr, p.Name()))
	gb, note := cost.RetentionAtBudget(p.Name(), cfg, cfg.BudgetUSDMonth, est.TotalUSDMonthly)
	if note != "" {
		bullet(w, "❌ %s", note)
		bullet(w, "   compute estimate (%s): %s / month", p.Name(), pricing.FormatTaller(est.TotalUSDMonthly, "USD"))
		bullet(w, "   shrink the cluster, raise the budget, or pick a cheaper cloud (--cost-compare)")
		return
	}
	leftover := cfg.BudgetUSDMonth - est.TotalUSDMonthly
	label := cost.LiveBlockStorageLabel(p.Name(), cfg)
	bullet(w, "compute estimate:        %s / month", pricing.FormatTaller(est.TotalUSDMonthly, "USD"))
	bullet(w, "leftover for storage:    %s / month", pricing.FormatTaller(leftover, "USD"))
	if gb >= 1024 {
		bullet(w, "max persistent storage:  %.1f TiB at the cloud's cheap-tier price (%s)", gb/1024, label)
	} else {
		bullet(w, "max persistent storage:  %.0f GB at the cloud's cheap-tier price (%s)", gb, label)
	}
	bullet(w, "split this across observability + DB + product buckets via cluster-side ResourceQuota")
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}