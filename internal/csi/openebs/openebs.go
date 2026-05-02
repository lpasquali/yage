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

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/capi/templates"
	"github.com/lpasquali/yage/internal/platform/manifests"
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

func (driver) Render(f *manifests.Fetcher, cfg *config.Config) (string, error) {
	return f.Render("csi/openebs/values.yaml.tmpl", templates.HelmValuesData{Cfg: cfg})
}

// EnsureSecret returns ErrNotApplicable: OpenEBS hostpath uses local
// node storage exclusively and requires no cloud credential Secret.
// The orchestrator treats this as a clean skip.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return csi.ErrNotApplicable
}

func (driver) DefaultStorageClass() string { return "openebs-hostpath" }

// EnsureManagementInstall returns ErrNotApplicable: OpenEBS is a
// cross-provider opt-in driver and does not have a management-cluster
// install path via yage's pivot path.
func (driver) EnsureManagementInstall(_ *config.Config, _ string) error {
	return csi.ErrNotApplicable
}

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("OpenEBS CSI")
	w.Bullet("driver: %s (chart openebs pinned 4.1.1)", "openebs.io/local")
	w.Bullet("engine: hostpath (local node storage — no cloud credentials needed)")
	w.Bullet("LVM and ZFS engines available via values override (engines.local.lvm.enabled / engines.local.zfs.enabled)")
	w.Bullet("default StorageClass: openebs-hostpath")
	w.Bullet("cross-provider opt-in: enable via cfg.CSI.Drivers=[\"openebs\"] or --csi-drivers openebs")
}
