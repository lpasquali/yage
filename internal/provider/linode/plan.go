// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package linode

// Linode provider plan-output hooks (PlanDescriber).
//
// LINODE_TOKEN is operator-supplied and consumed directly by the CAPL
// controller — yage never mints or rotates it. The identity section
// therefore emits a Skip line explaining the operator's responsibility,
// matching Hetzner's HCLOUD_TOKEN precedent.

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// DescribeIdentity for Linode: skip (operator-supplied token).
func (p *Provider) DescribeIdentity(w provider.PlanWriter, cfg *config.Config) {
	w.Section("Identity bootstrap — Linode")
	w.Skip("LINODE_TOKEN supplied via env (operator-managed, not minted by yage)")
	w.Skip("CAPL controller reads LINODE_TOKEN directly from its own Secret/env")
}

// DescribeWorkload emits the Linode workload-cluster section.
func (p *Provider) DescribeWorkload(w provider.PlanWriter, cfg *config.Config) {
	region := orDefault(cfg.Providers.Linode.Region, "us-east")

	w.Section("Workload Cluster — Linode")
	w.Bullet("Cluster: %s/%s, k8s %s, region %s",
		cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName,
		cfg.WorkloadKubernetesVersion, region)

	cpType := orDefault(cfg.Providers.Linode.ControlPlaneType, "g6-standard-2")
	w.Bullet("control plane: %s × %s (LinodeMachineTemplate)",
		cfg.ControlPlaneMachineCount, cpType)

	if cfg.WorkerMachineCount != "" && cfg.WorkerMachineCount != "0" {
		nodeType := orDefault(cfg.Providers.Linode.NodeType, "g6-standard-2")
		w.Bullet("workers: %s × %s (LinodeMachineTemplate)",
			cfg.WorkerMachineCount, nodeType)
	}

	if tier := orDefault(cfg.Providers.Linode.OverheadTier, "prod"); tier != "" {
		w.Bullet("overhead tier: %s (NodeBalancer / volume / transfer counts → see Cost section)", tier)
	}

	w.Skip("Linode CSI (linode-blockstorage-csi) installed by operator via Helm — out of scope for yage")
}

// DescribePivot for Linode: no pivot target (kind remains the mgmt cluster).
func (p *Provider) DescribePivot(w provider.PlanWriter, cfg *config.Config) {
	w.Section("Pivot to managed mgmt cluster")
	w.Skip("Linode provider has no PivotTarget yet (kind remains the mgmt cluster)")
}
