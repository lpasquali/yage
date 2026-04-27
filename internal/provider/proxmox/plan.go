// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package proxmox

// Proxmox provider plan-output hooks.
//
// Each provider owns its own plan text; the orchestrator owns the
// cross-cutting sections (Capacity, Cost, Allocations, Retention).
// Section titles use named phases rather than numeric labels.

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// DescribeIdentity emits the Proxmox identity-bootstrap plan
// section: which admin credentials yage uses to talk to Proxmox,
// which CAPI + CSI users get minted by `tofu apply`, and what's
// piped back into clusterctl + CSI configs.
func (p *Provider) DescribeIdentity(w provider.PlanWriter, cfg *config.Config) {
	w.Section("Identity bootstrap — Proxmox (OpenTofu)")
	if cfg.Providers.Proxmox.RecreateIdentities {
		w.Bullet("RECREATE_PROXMOX_IDENTITIES: tear down + reapply CAPI/CSI identity Terraform")
	}
	if cfg.Providers.Proxmox.AdminUsername == "" || cfg.Providers.Proxmox.AdminToken == "" {
		w.Bullet("interactive prompt (PROXMOX_ADMIN_USERNAME / PROXMOX_ADMIN_TOKEN unset)")
	} else {
		w.Bullet("admin: %s @ %s", cfg.Providers.Proxmox.AdminUsername, cfg.Providers.Proxmox.URL)
	}
	w.Bullet("tofu apply: create CAPI user '%s' + token prefix '%s'",
		cfg.Providers.Proxmox.CAPIUserID, cfg.Providers.Proxmox.CAPITokenPrefix)
	w.Bullet("tofu apply: create CSI user '%s' + token prefix '%s'",
		cfg.Providers.Proxmox.CSIUserID, cfg.Providers.Proxmox.CSITokenPrefix)
	w.Bullet("outputs piped into clusterctl + CSI configs")
}

// DescribeClusterctlInit emits the Proxmox-specific bullet inside
// the "clusterctl init on kind" section (the orchestrator has already
// written the section header — only w.Bullet / w.Skip here).
func (p *Provider) DescribeClusterctlInit(w provider.PlanWriter, cfg *config.Config) {
	w.Bullet("CAPMOX image: %s (build arm64 if needed)", firstNonEmptyStr(cfg.CAPMOXVersion, "<resolve from CAPMOX_REPO>"))
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// DescribeWorkload emits the Proxmox workload-cluster plan section:
// cluster name / k8s version, CP and worker replica + sizing,
// templates, pool, Cilium HCP, CSI install.
func (p *Provider) DescribeWorkload(w provider.PlanWriter, cfg *config.Config) {
	w.Section("Workload Cluster — Proxmox")
	w.Bullet("Cluster: %s/%s, k8s %s",
		cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, cfg.WorkloadKubernetesVersion)
	w.Bullet("control plane: %s replica(s), VIP %s:%s",
		cfg.ControlPlaneMachineCount, cfg.ControlPlaneEndpointIP, cfg.ControlPlaneEndpointPort)
	w.Bullet("workers: %s replica(s)", cfg.WorkerMachineCount)
	w.Bullet("node IP range: %s, gateway: %s/%s, DNS: %s",
		cfg.NodeIPRanges, cfg.Gateway, cfg.IPPrefix, cfg.DNSServers)
	w.Bullet("CP sizing: %s sockets × %s cores, %s MiB, %s %s GB",
		cfg.Providers.Proxmox.ControlPlaneNumSockets,
		cfg.Providers.Proxmox.ControlPlaneNumCores,
		cfg.Providers.Proxmox.ControlPlaneMemoryMiB,
		cfg.Providers.Proxmox.ControlPlaneBootVolumeDevice,
		cfg.Providers.Proxmox.ControlPlaneBootVolumeSize)
	w.Bullet("Worker sizing: %s sockets × %s cores, %s MiB, %s %s GB",
		cfg.Providers.Proxmox.WorkerNumSockets,
		cfg.Providers.Proxmox.WorkerNumCores,
		cfg.Providers.Proxmox.WorkerMemoryMiB,
		cfg.Providers.Proxmox.WorkerBootVolumeDevice,
		cfg.Providers.Proxmox.WorkerBootVolumeSize)
	cpTpl := orDefault(cfg.WorkloadControlPlaneTemplateID, cfg.Providers.Proxmox.TemplateID)
	wkTpl := orDefault(cfg.WorkloadWorkerTemplateID, cfg.Providers.Proxmox.TemplateID)
	w.Bullet("Proxmox templates: control-plane=%s, worker=%s (catch-all PROXMOX_TEMPLATE_ID=%s)",
		cpTpl, wkTpl, cfg.Providers.Proxmox.TemplateID)
	if cfg.Providers.Proxmox.Pool != "" {
		w.Bullet("Proxmox pool: %q (auto-created via admin API; tags VMs for ACLs/UI grouping)",
			cfg.Providers.Proxmox.Pool)
	}
	w.Bullet("Cilium HCP: kpr=%s, ingress=%s, hubble=%s, LB-IPAM=%s, GatewayAPI=%s",
		cfg.CiliumKubeProxyReplacement, cfg.CiliumIngress, cfg.CiliumHubble,
		cfg.CiliumLBIPAM, cfg.CiliumGatewayAPIEnabled)
	if cfg.Providers.Proxmox.CSIEnabled {
		w.Bullet("Proxmox CSI on workload: chart %s/%s, namespace %s, StorageClass %s (default: %s)",
			cfg.Providers.Proxmox.CSIChartName, cfg.Providers.Proxmox.CSIChartVersion,
			cfg.Providers.Proxmox.CSINamespace, cfg.Providers.Proxmox.CSIStorageClassName,
			cfg.Providers.Proxmox.CSIDefaultClass)
	} else {
		w.Skip("Proxmox CSI disabled (--disable-proxmox-csi)")
	}
}

// DescribePivot emits the Proxmox pivot section: provisioning a
// Proxmox-hosted management cluster and clusterctl-moving CAPI
// state from kind into it.
func (p *Provider) DescribePivot(w provider.PlanWriter, cfg *config.Config) {
	w.Section("Pivot to managed mgmt cluster — Proxmox")
	if !cfg.Pivot.Enabled {
		w.Skip("PIVOT_ENABLED=false (kind remains the management cluster)")
		return
	}
	w.Bullet("provision mgmt Cluster '%s/%s' on Proxmox (k8s %s)",
		cfg.Mgmt.ClusterNamespace, cfg.Mgmt.ClusterName, cfg.Mgmt.KubernetesVersion)
	w.Bullet("  CP: %s replica(s), VIP %s:%s, range %s",
		cfg.Mgmt.ControlPlaneMachineCount, cfg.Mgmt.ControlPlaneEndpointIP,
		cfg.Mgmt.ControlPlaneEndpointPort, cfg.Mgmt.NodeIPRanges)
	w.Bullet("  CP sizing: %s sockets × %s cores, %s MiB",
		cfg.Providers.Proxmox.Mgmt.ControlPlaneNumSockets,
		cfg.Providers.Proxmox.Mgmt.ControlPlaneNumCores,
		cfg.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB)
	mgmtCPTpl := orDefault(cfg.Providers.Proxmox.Mgmt.ControlPlaneTemplateID, cfg.Providers.Proxmox.TemplateID)
	mgmtWkTpl := orDefault(cfg.Providers.Proxmox.Mgmt.WorkerTemplateID, cfg.Providers.Proxmox.TemplateID)
	w.Bullet("  Proxmox templates: control-plane=%s, worker=%s", mgmtCPTpl, mgmtWkTpl)
	w.Bullet("  Cilium: hubble=%s, LB-IPAM=%s; CSI: %v",
		cfg.Mgmt.CiliumHubble, cfg.Mgmt.CiliumLBIPAM, cfg.Providers.Proxmox.Mgmt.CSIEnabled)
	if cfg.Providers.Proxmox.Mgmt.Pool != "" {
		w.Bullet("  Proxmox pool: %q (auto-created)", cfg.Providers.Proxmox.Mgmt.Pool)
	}
	w.Bullet("clusterctl init on mgmt (idempotent)")
	if cfg.Pivot.DryRun {
		w.Bullet("clusterctl move --dry-run (logs plan, no state moves) — exit here")
	} else {
		w.Bullet("clusterctl move kind → mgmt for namespaces: %s, %s, yage-system",
			cfg.WorkloadClusterNamespace, cfg.Mgmt.ClusterNamespace)
		w.Bullet("handoff yage-system Secrets kind → mgmt")
		w.Bullet("VerifyParity (timeout: %s)", cfg.Pivot.VerifyTimeout)
		w.Bullet("rebind kind-%s context to mgmt kubeconfig (subsequent phases target mgmt)",
			cfg.KindClusterName)
	}
}

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}