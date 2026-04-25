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

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/k8sclient"
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

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
