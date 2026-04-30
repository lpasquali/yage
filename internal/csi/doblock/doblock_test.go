// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package doblock

import (
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestDriverConstants(t *testing.T) {
	d := driver{}
	if got, want := d.Name(), "do-block-storage"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := d.K8sCSIDriverName(), "dobs.csi.digitalocean.com"; got != want {
		t.Errorf("K8sCSIDriverName() = %q, want %q", got, want)
	}
	if got, want := d.DefaultStorageClass(), "do-block-storage"; got != want {
		t.Errorf("DefaultStorageClass() = %q, want %q", got, want)
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "digitalocean" {
		t.Errorf("Defaults() = %v, want [digitalocean]", defs)
	}
}

func TestHelmChart(t *testing.T) {
	d := driver{}
	repo, chart, ver, err := d.HelmChart(nil)
	if err != nil {
		t.Fatalf("HelmChart() unexpected err: %v", err)
	}
	if chart != "do-csi-driver" {
		t.Errorf("chart = %q, want %q", chart, "do-csi-driver")
	}
	if ver != "v4.14.0" {
		t.Errorf("version = %q, want %q", ver, "v4.14.0")
	}
	if repo == "" {
		t.Errorf("repo must not be empty")
	}
}

func TestRenderValues(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	out, err := d.RenderValues(cfg)
	if err != nil {
		t.Fatalf("RenderValues() unexpected err: %v", err)
	}
	if out == "" {
		t.Error("RenderValues() returned empty string")
	}
}

func TestEnsureSecretEmptyTokenError(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	// Token is empty — EnsureSecret must return an error.
	err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig")
	if err == nil {
		t.Error("EnsureSecret() with empty token should return error, got nil")
	}
}
