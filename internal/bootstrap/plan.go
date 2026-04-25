// Package bootstrap — global dry-run plan printer.
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
package bootstrap

import (
	"fmt"
	"os"
	"strings"

	"errors"

	"github.com/lpasquali/bootstrap-capi/internal/capacity"
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/cost"
	"github.com/lpasquali/bootstrap-capi/internal/k8sclient"
	"github.com/lpasquali/bootstrap-capi/internal/pricing"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// PrintPlan writes a structured "would do" plan to stdout based on cfg.
// Mirrors Run() phase ordering so reading top-to-bottom shows the real
// run's sequence.
func PrintPlan(cfg *config.Config) {
	w := os.Stdout
	hr := strings.Repeat("─", 76)

	fmt.Fprintln(w, hr)
	fmt.Fprintln(w, "📝 DRY-RUN PLAN — bootstrap-capi would perform the following actions")
	fmt.Fprintln(w, hr)

	planStandalone(w, cfg)
	planPrePhase(w, cfg)
	planPhase1(w, cfg)
	planPhase2Identity(w, cfg)
	planPhase2Clusterctl(w, cfg)
	planPhase2Kind(w, cfg)
	planPhase2CAPI(w, cfg)
	planPhase29Workload(w, cfg)
	planPhase295Pivot(w, cfg)
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

func section(w *os.File, title string) {
	fmt.Fprintf(w, "\n▸ %s\n", title)
}

func bullet(w *os.File, format string, args ...interface{}) {
	fmt.Fprintf(w, "    • "+format+"\n", args...)
}

func skip(w *os.File, format string, args ...interface{}) {
	fmt.Fprintf(w, "    ◦ skip: "+format+"\n", args...)
}

func planStandalone(w *os.File, cfg *config.Config) {
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
	case cfg.ArgoCDPrintAccessStandalone:
		section(w, "Standalone: --argocd-print-access")
		bullet(w, "print Argo CD access info for workload %s/%s", cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
		fmt.Fprintln(w, "    (no other phases would run)")
	case cfg.ArgoCDPortForwardStandalone:
		section(w, "Standalone: --argocd-port-forward")
		bullet(w, "kubectl port-forward Argo CD on 127.0.0.1:%s", firstNonEmptyStr(cfg.ArgoCDPortForwardPort, "8443"))
		fmt.Fprintln(w, "    (no other phases would run)")
	}
}

func planPrePhase(w *os.File, cfg *config.Config) {
	if cfg.BootstrapKindStateOp != "" || cfg.WorkloadRolloutStandalone ||
		cfg.ArgoCDPrintAccessStandalone || cfg.ArgoCDPortForwardStandalone {
		return
	}
	section(w, "Pre-phase")
	if cfg.Purge {
		bullet(w, "PURGE: delete generated files + Terraform state, kind workload Cluster, capi-manifest Secret")
	}
	if cfg.RecreateProxmoxIdentities {
		bullet(w, "RECREATE_PROXMOX_IDENTITIES: tear down + reapply CAPI/CSI identity Terraform")
	}
	bullet(w, "ClusterSetID = %s (identity suffix: %s)", firstNonEmptyStr(cfg.ClusterSetID, "<generated>"), firstNonEmptyStr(cfg.ProxmoxIdentitySuffix, "<derived>"))
}

func planPhase1(w *os.File, cfg *config.Config) {
	section(w, "Phase 1 — install host dependencies (CLIs for the operator)")
	bullet(w, "system packages: git, curl, python3 (apt/dnf/yum/apk)")
	bullet(w, "Docker: install if missing; upgrade unless --no-delete-kind or kind cluster exists")
	for _, t := range []struct{ name, ver string }{
		{"kubectl", cfg.KubectlVersion},
		{"kind", cfg.KindVersion},
		{"clusterctl", cfg.ClusterctlVersion},
		{"cilium", cfg.CiliumCLIVersion},
		{"helm", "latest"},
		{"argocd", cfg.ArgoCDVersion},
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

func planPhase2Identity(w *os.File, cfg *config.Config) {
	section(w, "Phase 2.0 — Proxmox identity bootstrap (OpenTofu)")
	if cfg.ProxmoxAdminUsername == "" || cfg.ProxmoxAdminToken == "" {
		bullet(w, "interactive prompt (PROXMOX_ADMIN_USERNAME / PROXMOX_ADMIN_TOKEN unset)")
	} else {
		bullet(w, "admin: %s @ %s", cfg.ProxmoxAdminUsername, cfg.ProxmoxURL)
	}
	bullet(w, "tofu apply: create CAPI user '%s' + token prefix '%s'", cfg.ProxmoxCAPIUserID, cfg.ProxmoxCAPITokenPrefix)
	bullet(w, "tofu apply: create CSI user '%s' + token prefix '%s'", cfg.ProxmoxCSIUserID, cfg.ProxmoxCSITokenPrefix)
	bullet(w, "outputs piped into clusterctl + CSI configs")
}

func planPhase2Clusterctl(w *os.File, cfg *config.Config) {
	section(w, "Phase 2.1 — clusterctl credentials")
	if cfg.ClusterctlCfg != "" {
		bullet(w, "use explicit clusterctl config: %s", cfg.ClusterctlCfg)
	} else {
		bullet(w, "synthesize ephemeral clusterctl config from Proxmox env (URL/TOKEN/SECRET)")
	}
}

func planPhase2Kind(w *os.File, cfg *config.Config) {
	section(w, "Phase 2.4 — kind cluster")
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

func planPhase2CAPI(w *os.File, cfg *config.Config) {
	section(w, "Phase 2.8 — clusterctl init on kind")
	bullet(w, "providers: infrastructure=%s, ipam=%s, addon=helm", cfg.InfraProvider, cfg.IPAMProvider)
	bullet(w, "CAPMOX image: %s (build arm64 if needed)", firstNonEmptyStr(cfg.CAPMOXVersion, "<resolve from CAPMOX_REPO>"))
}

func planPhase29Workload(w *os.File, cfg *config.Config) {
	section(w, "Phase 2.9 — workload Cluster (Proxmox)")
	bullet(w, "Cluster: %s/%s, k8s %s", cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, cfg.WorkloadKubernetesVersion)
	bullet(w, "control plane: %s replica(s), VIP %s:%s", cfg.ControlPlaneMachineCount, cfg.ControlPlaneEndpointIP, cfg.ControlPlaneEndpointPort)
	bullet(w, "workers: %s replica(s)", cfg.WorkerMachineCount)
	bullet(w, "node IP range: %s, gateway: %s/%s, DNS: %s", cfg.NodeIPRanges, cfg.Gateway, cfg.IPPrefix, cfg.DNSServers)
	bullet(w, "CP sizing: %s sockets × %s cores, %s MiB, %s %s GB",
		cfg.ControlPlaneNumSockets, cfg.ControlPlaneNumCores, cfg.ControlPlaneMemoryMiB,
		cfg.ControlPlaneBootVolumeDevice, cfg.ControlPlaneBootVolumeSize)
	bullet(w, "Worker sizing: %s sockets × %s cores, %s MiB, %s %s GB",
		cfg.WorkerNumSockets, cfg.WorkerNumCores, cfg.WorkerMemoryMiB,
		cfg.WorkerBootVolumeDevice, cfg.WorkerBootVolumeSize)
	cpTpl := firstNonEmptyStr(cfg.WorkloadControlPlaneTemplateID, cfg.ProxmoxTemplateID)
	wkTpl := firstNonEmptyStr(cfg.WorkloadWorkerTemplateID, cfg.ProxmoxTemplateID)
	bullet(w, "Proxmox templates: control-plane=%s, worker=%s (catch-all PROXMOX_TEMPLATE_ID=%s)", cpTpl, wkTpl, cfg.ProxmoxTemplateID)
	if cfg.ProxmoxPool != "" {
		bullet(w, "Proxmox pool: %q (auto-created via admin API; tags VMs for ACLs/UI grouping)", cfg.ProxmoxPool)
	}
	bullet(w, "Cilium HCP: kpr=%s, ingress=%s, hubble=%s, LB-IPAM=%s, GatewayAPI=%s",
		cfg.CiliumKubeProxyReplacement, cfg.CiliumIngress, cfg.CiliumHubble, cfg.CiliumLBIPAM, cfg.CiliumGatewayAPIEnabled)
	if cfg.ProxmoxCSIEnabled {
		bullet(w, "Proxmox CSI on workload: chart %s/%s, namespace %s, StorageClass %s (default: %s)",
			cfg.ProxmoxCSIChartName, cfg.ProxmoxCSIChartVersion, cfg.ProxmoxCSINamespace, cfg.ProxmoxCSIStorageClassName, cfg.ProxmoxCSIDefaultClass)
	} else {
		skip(w, "Proxmox CSI disabled (--disable-proxmox-csi)")
	}
}

func planPhase295Pivot(w *os.File, cfg *config.Config) {
	section(w, "Phase 2.95 — Pivot to Proxmox-hosted management cluster")
	if !cfg.PivotEnabled {
		skip(w, "PIVOT_ENABLED=false (kind remains the management cluster)")
		return
	}
	bullet(w, "provision mgmt Cluster '%s/%s' on Proxmox (k8s %s)", cfg.MgmtClusterNamespace, cfg.MgmtClusterName, cfg.MgmtKubernetesVersion)
	bullet(w, "  CP: %s replica(s), VIP %s:%s, range %s",
		cfg.MgmtControlPlaneMachineCount, cfg.MgmtControlPlaneEndpointIP, cfg.MgmtControlPlaneEndpointPort, cfg.MgmtNodeIPRanges)
	bullet(w, "  CP sizing: %s sockets × %s cores, %s MiB",
		cfg.MgmtControlPlaneNumSockets, cfg.MgmtControlPlaneNumCores, cfg.MgmtControlPlaneMemoryMiB)
	mgmtCPTpl := firstNonEmptyStr(cfg.MgmtControlPlaneTemplateID, cfg.ProxmoxTemplateID)
	mgmtWkTpl := firstNonEmptyStr(cfg.MgmtWorkerTemplateID, cfg.ProxmoxTemplateID)
	bullet(w, "  Proxmox templates: control-plane=%s, worker=%s", mgmtCPTpl, mgmtWkTpl)
	bullet(w, "  Cilium: hubble=%s, LB-IPAM=%s; CSI: %v",
		cfg.MgmtCiliumHubble, cfg.MgmtCiliumLBIPAM, cfg.MgmtProxmoxCSIEnabled)
	if cfg.MgmtProxmoxPool != "" {
		bullet(w, "  Proxmox pool: %q (auto-created)", cfg.MgmtProxmoxPool)
	}
	bullet(w, "clusterctl init on mgmt (idempotent)")
	if cfg.PivotDryRun {
		bullet(w, "clusterctl move --dry-run (logs plan, no state moves) — exit here")
	} else {
		bullet(w, "clusterctl move kind → mgmt for namespaces: %s, %s, proxmox-bootstrap-system",
			cfg.WorkloadClusterNamespace, cfg.MgmtClusterNamespace)
		bullet(w, "handoff proxmox-bootstrap-system Secrets kind → mgmt")
		bullet(w, "VerifyParity (timeout: %s)", cfg.PivotVerifyTimeout)
		bullet(w, "rebind kind-%s context to mgmt kubeconfig (subsequent phases target mgmt)", cfg.KindClusterName)
	}
}

func planPhase210ArgoCD(w *os.File, cfg *config.Config) {
	section(w, "Phase 2.10 — Argo CD on workload")
	if !cfg.ArgoCDEnabled {
		skip(w, "ARGOCD_ENABLED=false (--disable-argocd)")
		return
	}
	bullet(w, "Argo CD Operator + ArgoCD CR (version %s, operator %s)", cfg.ArgoCDVersion, cfg.ArgoCDOperatorVersion)
	if cfg.WorkloadArgoCDEnabled {
		bullet(w, "CAAPH argocd-apps HelmChartProxy → root Application '%s'", cfg.WorkloadClusterName)
		if cfg.WorkloadAppOfAppsGitURL != "" {
			bullet(w, "  app-of-apps: %s @ %s, path %s", cfg.WorkloadAppOfAppsGitURL, cfg.WorkloadAppOfAppsGitRef, cfg.WorkloadAppOfAppsGitPath)
		}
	}
}

func planFinal(w *os.File, cfg *config.Config) {
	section(w, "Final — kind teardown")
	switch {
	case !cfg.PivotEnabled:
		skip(w, "PIVOT_ENABLED=false (kind stays — it IS the management cluster)")
	case cfg.PivotKeepKind:
		skip(w, "--pivot-keep-kind set")
	case cfg.NoDeleteKind:
		skip(w, "--no-delete-kind set")
	default:
		bullet(w, "delete kind cluster '%s'", cfg.KindClusterName)
	}
}

// planCapacity prints the resource budget summary: requested vs
// available host capacity. When Proxmox creds are present we query the
// live host; otherwise we print only the requested side.
func planCapacity(w *os.File, cfg *config.Config) {
	section(w, "Capacity budget")
	plan := capacity.PlanFor(cfg)
	threshold := cfg.ResourceBudgetFraction
	if threshold <= 0 || threshold > 1 {
		threshold = capacity.DefaultThreshold
	}
	bullet(w, "budget: %.0f%% of available Proxmox host CPU/memory/storage",
		threshold*100)
	for _, it := range plan.Items {
		bullet(w, "  %-26s %d × (%d cores, %d MiB, %d GB) = %d cores, %d MiB, %d GB",
			it.Name, it.Replicas, it.CPUCores, it.MemoryMiB, it.DiskGB,
			it.CPUCores*it.Replicas, it.MemoryMiB*int64(it.Replicas), it.DiskGB*int64(it.Replicas))
	}
	bullet(w, "TOTAL requested:  %d cores, %d MiB (%d GiB), %d GB disk",
		plan.CPUCores, plan.MemoryMiB, plan.MemoryMiB/1024, plan.StorageGB)

	hc, err := capacity.FetchHostCapacity(cfg)
	if err != nil {
		skip(w, "host-capacity query: %v (run with valid Proxmox creds for live numbers)", err)
		return
	}
	bullet(w, "host (allowed nodes %v): %d cores, %d MiB (%d GiB), %d GB disk",
		hc.Nodes, hc.CPUCores, hc.MemoryMiB, hc.MemoryMiB/1024, hc.StorageGB)
	if hc.IsSmallEnv() && cfg.BootstrapMode != "k3s" {
		bullet(w, "💡 host is small — consider --bootstrap-mode k3s for a 1 vCPU / 1 GiB-per-node footprint")
	}
	if err := capacity.Check(plan, hc, threshold); err != nil {
		bullet(w, "❌ %v", err)
		bullet(w, "(real run aborts; use --allow-resource-overcommit to override)")
		// Suggest k3s when the same machine counts under k3s sizing
		// would fit the budget.
		if cfg.BootstrapMode != "k3s" {
			if fits, k3sPlan := capacity.WouldFitAsK3s(cfg, hc, threshold); fits {
				bullet(w, "💡 same machine counts would fit under --bootstrap-mode k3s: %d cores / %d MiB / %d GB",
					k3sPlan.CPUCores, k3sPlan.MemoryMiB, k3sPlan.StorageGB)
			}
		}
	} else {
		bullet(w, "✅ plan fits within %.0f%% budget.", threshold*100)
	}
}

// planAllocations prints the workload-cluster-side allocation plan:
// total worker capacity, system-apps reserve, and the three equal
// buckets (db / observability / product) that share the remainder.
//
// Surfaces a warning when the system reserve alone exceeds total
// worker capacity (operator must either grow workers or pin add-ons
// to the control-plane node).
func planAllocations(w *os.File, cfg *config.Config) {
	section(w, "Workload allocations (per-bucket budget on workers)")
	a := capacity.AllocationsFor(cfg)
	bullet(w, "total worker capacity:    %d millicores, %d MiB (workers=%s × %s sockets × %s cores × %s MiB)",
		a.TotalCPUMillicores, a.TotalMemoryMiB,
		cfg.WorkerMachineCount, cfg.WorkerNumSockets, cfg.WorkerNumCores, cfg.WorkerMemoryMiB)
	bullet(w, "system-apps reserve:      %d millicores, %d MiB",
		a.SystemAppsCPUMillicores, a.SystemAppsMemoryMiB)
	bullet(w, "  ├─ argocd (operator + server + repo + redis), kyverno, cert-manager,")
	bullet(w, "  ├─ proxmox-csi (controller), keycloak (SSO), external-secrets, infisical")
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
func planMonthlyCost(w *os.File, cfg *config.Config) {
	p, err := provider.For(cfg)
	if err != nil {
		return
	}
	est, err := p.EstimateMonthlyCostUSD(cfg)
	if err != nil {
		if errors.Is(err, provider.ErrNotApplicable) {
			return
		}
		section(w, "Estimated monthly cost")
		bullet(w, "(estimate query failed: %v)", err)
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
func planCostCompare(w *os.File, cfg *config.Config) {
	if !cfg.CostCompare {
		return
	}
	cost.PrintComparison(w, cfg)
}

// planRetention prints how many GB of cheap-tier block storage the
// user's budget buys AFTER paying for compute on the active provider.
// No-op when --budget-usd-month is unset.
func planRetention(w *os.File, cfg *config.Config) {
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
