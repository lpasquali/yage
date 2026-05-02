// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package proxmoxcsi

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
)

// fetcher returns a Fetcher pointed at the in-package testdata fixture.
func fetcher(t *testing.T) *manifests.Fetcher {
	t.Helper()
	return &manifests.Fetcher{MountRoot: "testdata"}
}

func TestDriverConstants(t *testing.T) {
	d := driver{}
	if got, want := d.Name(), "proxmox-csi"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := d.K8sCSIDriverName(), "csi.proxmox.sinextra.dev"; got != want {
		t.Errorf("K8sCSIDriverName() = %q, want %q", got, want)
	}
	if got, want := d.DefaultStorageClass(), "proxmox-data-xfs"; got != want {
		t.Errorf("DefaultStorageClass() = %q, want %q", got, want)
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "proxmox" {
		t.Errorf("Defaults() = %v, want [proxmox]", defs)
	}
}

func TestHelmChart(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	cfg.Providers.Proxmox.CSIChartRepoURL = "oci://ghcr.io/sergelogvinov/charts"
	cfg.Providers.Proxmox.CSIChartName = "proxmox-csi-plugin"
	cfg.Providers.Proxmox.CSIChartVersion = "0.2.10"
	repo, chart, ver, err := d.HelmChart(cfg)
	if err != nil {
		t.Fatalf("HelmChart() unexpected err: %v", err)
	}
	if chart != "proxmox-csi-plugin" {
		t.Errorf("chart = %q, want proxmox-csi-plugin", chart)
	}
	if ver != "0.2.10" {
		t.Errorf("version = %q, want 0.2.10", ver)
	}
	if repo != "oci://ghcr.io/sergelogvinov/charts" {
		t.Errorf("repo = %q", repo)
	}
}

func TestRender(t *testing.T) {
	cfg := &config.Config{}
	cfg.Providers.Proxmox.CSIStorageClassName = "proxmox-xfs"
	out, err := driver{}.Render(fetcher(t), cfg)
	if err != nil {
		t.Fatalf("Render() unexpected err: %v", err)
	}
	checks := []string{
		"proxmox-xfs",
		"storageClass:",
		"reclaimPolicy: Delete",
		"fstype: xfs",
	}
	for _, c := range checks {
		if !strings.Contains(out, c) {
			t.Errorf("Render() missing %q in output:\n%s", c, out)
		}
	}
}
