// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package openebs implements the OpenEBS CSI driver registration for
// yage's CSI add-on registry (internal/csi). OpenEBS is a
// cross-provider, cloud-native storage engine that uses local node
// storage — no cloud credentials are required. This package registers
// the hostpath-only engine profile, which is the safest default for
// generic on-prem and cloud clusters alike.
//
// The upstream OpenEBS chart (openebs/openebs) bundles three storage
// engines: hostpath (pure local-disk, zero external deps), LVM
// (requires the LVM kernel module + volume groups on nodes), and ZFS
// (requires the ZFS kernel module + zpools on nodes). This driver
// enables hostpath only; LVM and ZFS are opt-in via Helm values
// override in the operator's values file or --csi-values flag.
//
// Pinned chart version 4.1.1 — a stable release that supports K8s
// 1.25+ with the modern openebs.io/local CSIDriver object. Updating
// the pin is a follow-up commit alongside helm-up runs.
package openebs

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

func (driver) Name() string             { return "openebs" }
func (driver) K8sCSIDriverName() string { return "openebs.io/local" }

// Defaults returns an empty slice: OpenEBS is a cross-provider opt-in
// driver. No single infrastructure provider enables it by default —
// operators select it explicitly via cfg.CSI.Drivers = ["openebs"].
func (driver) Defaults() []string { return []string{} }

// HelmChart pins to chart version 4.1.1 from the OpenEBS Helm
// repository. The chart name is "openebs" under the
// openebs.github.io/openebs OCI-style HTTPS chart repo.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://openebs.github.io/openebs",
		"openebs",
		"4.1.1",
		nil
}

// RenderValues emits a minimal Helm values document that enables only
// the hostpath local engine. The LVM and ZFS engines are explicitly
// disabled to avoid requiring kernel modules on cluster nodes.
//
// Operators who need LVM or ZFS can override these values by passing
// additional Helm values:
//
//	engines.local.lvm.enabled: true   # requires LVM kernel module + VGs
//	engines.local.zfs.enabled: true   # requires ZFS kernel module + zpools
//
// The default StorageClass "openebs-hostpath" is set as the cluster
// default class with WaitForFirstConsumer binding mode so PVCs without
// an explicit storageClassName land on the local node alongside their
// consumer pod.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/openebs.\n")
	b.WriteString("# Hostpath engine only. To enable LVM or ZFS engines, override:\n")
	b.WriteString("#   engines.local.lvm.enabled: true  (requires LVM kernel module + VGs on nodes)\n")
	b.WriteString("#   engines.local.zfs.enabled: true  (requires ZFS kernel module + zpools on nodes)\n")
	b.WriteString("engines:\n")
	b.WriteString("  local:\n")
	b.WriteString("    lvm:\n")
	b.WriteString("      enabled: false\n")
	b.WriteString("    zfs:\n")
	b.WriteString("      enabled: false\n")
	return b.String(), nil
}

// EnsureSecret returns ErrNotApplicable: OpenEBS hostpath uses local
// node storage exclusively and requires no cloud credential Secret.
// The orchestrator treats this as a clean skip.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return csi.ErrNotApplicable
}

func (driver) DefaultStorageClass() string { return "openebs-hostpath" }

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("OpenEBS CSI")
	w.Bullet("driver: %s (chart openebs pinned 4.1.1)", "openebs.io/local")
	w.Bullet("engine: hostpath (local node storage — no cloud credentials needed)")
	w.Bullet("LVM and ZFS engines available via values override (engines.local.lvm.enabled / engines.local.zfs.enabled)")
	w.Bullet("default StorageClass: openebs-hostpath")
	w.Bullet("cross-provider opt-in: enable via cfg.CSI.Drivers=[\"openebs\"] or --csi-drivers openebs")
}
