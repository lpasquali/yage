// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package longhorn implements the Longhorn CSI driver registration
// for yage's CSI add-on registry (internal/csi). Longhorn is a
// cloud-native distributed block storage system built on local node
// storage — no cloud credentials are required. It operates entirely
// within the cluster, replicating data across nodes via their local
// disks.
//
// Longhorn is a cross-provider opt-in: it works on any infrastructure
// where worker nodes have dedicated local disks and open-iscsi is
// installed. Operators enable it by setting cfg.CSI.Drivers =
// ["longhorn"] (or appending it alongside a cloud CSI driver for
// mixed workloads).
//
// Pinned chart version v1.7.2 — a stable release as of early 2025
// that supports K8s 1.25+. Chart updates are a follow-up commit
// (bumped alongside `helm-up` runs, not silently).
package longhorn

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

func (driver) Name() string             { return "longhorn" }
func (driver) K8sCSIDriverName() string { return "driver.longhorn.io" }

// Defaults returns an empty slice — Longhorn is a cross-provider
// opt-in, not the default choice for any single infrastructure
// provider. Operators enable it explicitly via cfg.CSI.Drivers.
func (driver) Defaults() []string { return []string{} }

func (driver) DefaultStorageClass() string { return "longhorn" }

// EnsureManagementInstall returns ErrNotApplicable: Longhorn is a
// cross-provider opt-in driver and does not have a management-cluster
// install path via yage's pivot path.
func (driver) EnsureManagementInstall(_ *config.Config, _ string) error {
	return csi.ErrNotApplicable
}

// HelmChart pins to v1.7.2 of the Longhorn chart from the official
// Longhorn Helm repository.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://charts.longhorn.io",
		"longhorn",
		"v1.7.2",
		nil
}

// RenderValues emits a minimal Helm values document. Longhorn's
// defaults are production-ready out of the box; no required overrides
// exist for a standard install. The only tunable emitted here is
// defaultSettings.defaultReplicaCount — set to 3, which is Longhorn's
// own default and gives one replica per worker node in a three-node
// cluster. Operators reduce it to 2 on two-node setups or 1 for
// single-node dev environments via a Helm CLI override.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/longhorn.\n")
	b.WriteString("# Longhorn defaults are production-ready; no required overrides.\n")
	b.WriteString("# Adjust defaultReplicaCount to match your cluster's worker node count.\n")
	b.WriteString("defaultSettings:\n")
	b.WriteString("  defaultReplicaCount: 3\n")
	return b.String(), nil
}

// EnsureSecret returns ErrNotApplicable: Longhorn uses local node
// storage and requires no cloud credential Secret. The orchestrator
// treats this as a clean skip.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return csi.ErrNotApplicable
}

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("Longhorn CSI")
	w.Bullet("driver: %s (chart longhorn pinned v1.7.2)", "driver.longhorn.io")
	w.Bullet("prerequisite: open-iscsi must be installed and running on every worker node")
	w.Bullet("replica count: 3 (adjust defaultSettings.defaultReplicaCount for non-3-node clusters)")
	w.Bullet("cross-provider opt-in: works on any infrastructure with local node disks — not a provider default")
	w.Bullet("default StorageClass: longhorn (no cloud credential Secret required)")
}
