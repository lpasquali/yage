package hetzner

// Hetzner provider plan-output hooks (Phase B per §8/§14.B + §13's
// Hetzner validation report).
//
// Hetzner is the simpler hyperscale: project-scoped HCLOUD_TOKEN
// (operator-supplied), per-project server quota (count-based, not
// per-resource), single VPC. The plan output reflects that
// simplicity.

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// DescribeIdentity for Hetzner: skip (operator-supplied token).
func (p *Provider) DescribeIdentity(w provider.PlanWriter, cfg *config.Config) {
	w.Section("Identity bootstrap — Hetzner Cloud")
	w.Skip("HCLOUD_TOKEN supplied via env (operator-managed, not minted by yage)")
}

// DescribeWorkload emits the Hetzner workload section.
func (p *Provider) DescribeWorkload(w provider.PlanWriter, cfg *config.Config) {
	location := orDefault(cfg.Providers.Hetzner.Location, "fsn1")

	w.Section("Workload Cluster — Hetzner Cloud")
	w.Bullet("Cluster: %s/%s, k8s %s, region %s",
		cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName,
		cfg.WorkloadKubernetesVersion, location)

	cpType := orDefault(cfg.Providers.Hetzner.ControlPlaneMachineType, "cx22")
	w.Bullet("control plane: %s × %s",
		cfg.ControlPlaneMachineCount, cpType)

	if cfg.WorkerMachineCount != "" && cfg.WorkerMachineCount != "0" {
		nodeType := orDefault(cfg.Providers.Hetzner.NodeMachineType, "cx22")
		w.Bullet("workers: %s × %s",
			cfg.WorkerMachineCount, nodeType)
	}

	if tier := orDefault(cfg.Providers.Hetzner.OverheadTier, "prod"); tier != "" {
		w.Bullet("overhead tier: %s (LB / floating IP / volume counts → see Cost section)", tier)
	}

	w.Skip("Hetzner CSI installed by operator via Helm (hcloud-csi-driver) — out of scope for yage")
}

// DescribePivot for Hetzner: no pivot today (the Hetzner K3s
// template exists but the mgmt-cluster bootstrap flow isn't wired).
func (p *Provider) DescribePivot(w provider.PlanWriter, cfg *config.Config) {
	w.Section("Pivot to managed mgmt cluster")
	w.Skip("Hetzner provider has no PivotTarget yet (kind remains the mgmt cluster)")
}

