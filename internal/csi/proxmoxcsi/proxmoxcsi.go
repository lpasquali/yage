// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package proxmoxcsi registers the proxmox-csi-plugin driver with
// yage's CSI add-on registry (internal/csi). The upstream chart is
// proxmox-csi-plugin published by sergelogvinov on GHCR; auth is via
// a Proxmox API token, materialised as a Kubernetes Secret on the
// workload cluster (the registry pre-§20 path that lives in
// internal/capi/csi.ApplyConfigSecretToWorkload — kept here so the
// migration is purely additive: same Secret shape, same Helm chart,
// new registration seam).
package proxmoxcsi

import (
	"strings"

	capicsi "github.com/lpasquali/yage/internal/capi/csi"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/ui/plan"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "proxmox-csi" }
func (driver) K8sCSIDriverName() string { return "csi.proxmox.sinextra.dev" }
func (driver) Defaults() []string       { return []string{"proxmox"} }

// HelmChart returns the chart coordinates from cfg.Providers.Proxmox
// rather than hardcoding them — Proxmox operators commonly mirror
// the chart internally and yage already exposes the override knobs
// (PROXMOX_CSI_CHART_REPO_URL / _NAME / _VERSION).
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return cfg.Providers.Proxmox.CSIChartRepoURL,
		cfg.Providers.Proxmox.CSIChartName,
		cfg.Providers.Proxmox.CSIChartVersion,
		nil
}

// RenderValues emits a thin Helm values document. The bulk of the
// driver's runtime config (Proxmox URL / token / region / cluster
// alias) flows in via the Secret applied by EnsureSecret below, not
// via Helm values — that's how the upstream chart is wired and why
// yage doesn't need to enumerate fields here.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/proxmoxcsi.\n")
	b.WriteString("# Auth: Kubernetes Secret applied by EnsureSecret\n")
	b.WriteString("# (see internal/capi/csi.ApplyConfigSecretToWorkload).\n")
	b.WriteString("storageClass:\n")
	b.WriteString("  - name: ")
	b.WriteString(cfg.Providers.Proxmox.CSIStorageClassName)
	b.WriteString("\n")
	b.WriteString("    storage: data\n")
	b.WriteString("    reclaimPolicy: Delete\n")
	b.WriteString("    fstype: xfs\n")
	return b.String(), nil
}

// EnsureSecret delegates to the existing Secret-apply path. The
// Secret name, namespace, and aliasing
// (<cluster>-proxmox-csi-config plus the short proxmox-csi-config
// mirror) are an upstream-chart contract, so the §20 integration
// is "register the driver" rather than "rewrite the apply logic".
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	capicsi.ApplyConfigSecretToWorkload(cfg, func() (string, error) {
		return workloadKubeconfigPath, nil
	})
	return nil
}

func (driver) DefaultStorageClass() string {
	// Read from cfg via DescribeInstall / orchestrator; this hook
	// is the static fallback when no cfg is available (e.g. dry-run
	// driver listings). Matches the env default.
	return "proxmox-data-xfs"
}

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("Proxmox CSI")
	w.Bullet("driver: %s (chart %s pinned %s)",
		"csi.proxmox.sinextra.dev",
		cfg.Providers.Proxmox.CSIChartName,
		cfg.Providers.Proxmox.CSIChartVersion)
	w.Bullet("auth: Kubernetes Secret <cluster>-proxmox-csi-config in %s",
		cfg.Providers.Proxmox.CSINamespace)
	w.Bullet("default StorageClass: %s",
		cfg.Providers.Proxmox.CSIStorageClassName)
}