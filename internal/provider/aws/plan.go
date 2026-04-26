// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package aws

// AWS provider plan-output hooks (Phase B per §8/§14.B + §13's
// AWS validation report).
//
// Minimum-bar implementation per §13.4: prints cluster shape +
// sizing + skips for parts AWS doesn't do. Cost component breakdown
// (NAT GW count, ALB hourly, etc.) lives in the central Cost
// section, not here. The AWS dry-run no longer prints Proxmox
// phases — that was the bug §8 was designed to fix.

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// DescribeIdentity emits the AWS identity-bootstrap section. yage
// doesn't mint AWS credentials — it consumes them. Both bullets are
// Skip lines explaining the operator's responsibility.
func (p *Provider) DescribeIdentity(w provider.PlanWriter, cfg *config.Config) {
	w.Section("Identity bootstrap — AWS IAM")
	w.Skip("AWS uses operator-supplied IAM (env: AWS_ACCESS_KEY_ID / _SECRET_ACCESS_KEY / AWS_PROFILE)")
	w.Skip("CAPA bootstrap stack created out-of-band — `clusterawsadm bootstrap iam create-cloudformation-stack`")
}

// DescribeWorkload emits the AWS workload-cluster section: cluster
// shape, instance types, region, mode, overhead tier. Skip line for
// CSI (handled by Helm + IRSA, not by yage).
func (p *Provider) DescribeWorkload(w provider.PlanWriter, cfg *config.Config) {
	mode := orDefault(cfg.Providers.AWS.Mode, "unmanaged")
	region := orDefault(cfg.Providers.AWS.Region, "us-east-1")

	w.Section("Workload Cluster — AWS (mode: " + mode + ")")
	w.Bullet("Cluster: %s/%s, k8s %s, region %s",
		cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName,
		cfg.WorkloadKubernetesVersion, region)

	cpType := orDefault(cfg.Providers.AWS.ControlPlaneMachineType, "t3.large")
	w.Bullet("control plane: %s × %s, %s GB gp3 root, ssh-key=%s, ami=%s",
		cfg.ControlPlaneMachineCount, cpType,
		orDefault(cfg.Providers.Proxmox.ControlPlaneBootVolumeSize, "30"),
		orDefault(cfg.Providers.AWS.SSHKeyName, "<unset>"),
		orDefault(cfg.Providers.AWS.AMIID, "<auto-resolved>"))

	if cfg.WorkerMachineCount != "" && cfg.WorkerMachineCount != "0" {
		nodeType := orDefault(cfg.Providers.AWS.NodeMachineType, "t3.medium")
		w.Bullet("workers: %s × %s, %s GB gp3 root",
			cfg.WorkerMachineCount, nodeType,
			orDefault(cfg.Providers.Proxmox.WorkerBootVolumeSize, "40"))
	}

	if tier := orDefault(cfg.Providers.AWS.OverheadTier, "prod"); tier != "" {
		w.Bullet("overhead tier: %s (NAT GW / ALB / egress / CW logs counts → see Cost section)", tier)
	}

	w.Skip("Proxmox CSI (AWS uses aws-ebs-csi-driver via Helm + IRSA — out of scope for yage)")
}

// DescribePivot for AWS: no pivot target today. ErrNotApplicable
// would skip silently; here we surface a Skip so the user knows
// why their --pivot run is a no-op on AWS.
func (p *Provider) DescribePivot(w provider.PlanWriter, cfg *config.Config) {
	w.Section("Pivot to managed mgmt cluster")
	w.Skip("AWS provider has no PivotTarget yet (kind remains the mgmt cluster)")
}