// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package rookceph implements the Rook-Ceph CSI driver registration
// for yage's CSI add-on registry (internal/csi).
//
// Rook-Ceph is a cross-provider storage operator: it turns raw block
// devices on Kubernetes nodes into a self-managed Ceph cluster and
// exposes its storage via a standard CSI driver. Unlike hyperscale
// CSI drivers (EBS, Azure Disk, GCP PD) it needs no cloud credentials
// — authentication is entirely in-cluster.
//
// Install model:
//   - The Helm chart deploys the Rook operator and registers the CSI
//     plug-in. The actual storage cluster is described by a CephCluster
//     custom resource that the operator must create post-install.
//   - Nodes must expose raw (unformatted, unpartitioned) block devices.
//     The operator will discover and use them; no manual device config
//     is required for the default bluestore backend.
//
// Pinned chart version v1.15.5 — a stable release from early 2026 that
// supports K8s 1.28+. Pin updates land alongside `helm-up` runs.
package rookceph

import (
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/ui/plan"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "rook-ceph" }
func (driver) K8sCSIDriverName() string { return "rook-ceph.csi.ceph.com" }

// Defaults returns an empty slice: Rook-Ceph is a cross-provider
// opt-in driver (baremetal, on-prem, hybrid). No infrastructure
// provider treats it as a default; operators add "rook-ceph" to
// cfg.CSI.Drivers explicitly.
func (driver) Defaults() []string { return []string{} }

// HelmChart pins to v1.15.5 of rook-ceph from charts.rook.io/release.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://charts.rook.io/release",
		"rook-ceph",
		"v1.15.5",
		nil
}

// RenderValues emits a minimal Helm values document for the Rook
// operator. The chart deploys the operator and registers the CSI
// plug-in; a CephCluster CR must be applied separately to bring up
// the actual storage cluster.
//
// csi.cephBlockPools.replicaCount is set to 3 — the recommended
// minimum for production Ceph (one replica per failure domain). Lower
// this to 1 or 2 for single-node / dev clusters by overriding the
// value out-of-band.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/rookceph.\n")
	b.WriteString("# This chart installs the Rook operator only.\n")
	b.WriteString("# Post-install: apply a CephCluster CR to create the storage cluster.\n")
	b.WriteString("# See: https://rook.io/docs/rook/latest/CRDs/Cluster/ceph-cluster-crd/\n")
	b.WriteString("csi:\n")
	b.WriteString("  cephBlockPools:\n")
	b.WriteString("    replicaCount: 3\n")
	b.WriteString("# operator.logLevel: INFO by default; set to DEBUG for troubleshooting.\n")
	b.WriteString("operator:\n")
	b.WriteString("  logLevel: INFO\n")
	return b.String(), nil
}

// EnsureSecret returns ErrNotApplicable: Rook-Ceph uses in-cluster
// Ceph for storage and requires no external cloud credential Secret.
// The orchestrator treats this as a clean skip.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return csi.ErrNotApplicable
}

func (driver) DefaultStorageClass() string { return "rook-ceph-block" }

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("Rook-Ceph CSI")
	w.Bullet("driver: %s (chart rook-ceph pinned v1.15.5)", "rook-ceph.csi.ceph.com")
	w.Bullet("auth: none — Rook-Ceph uses in-cluster Ceph; no cloud credential Secret required")
	w.Bullet("prerequisite: nodes must expose raw (unformatted) block devices for Ceph OSDs")
	w.Bullet("post-install: apply a CephCluster CR to create the storage cluster (operator only is deployed by Helm)")
	w.Bullet("default StorageClass: rook-ceph-block")
	w.Bullet("opt-in: cross-provider driver — add \"rook-ceph\" to cfg.CSI.Drivers explicitly")
}
