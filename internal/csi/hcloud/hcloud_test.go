// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package hcloud

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestDriverConstants(t *testing.T) {
	d := driver{}
	if got, want := d.Name(), "hcloud-csi"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := d.K8sCSIDriverName(), "csi.hetzner.cloud"; got != want {
		t.Errorf("K8sCSIDriverName() = %q, want %q", got, want)
	}
	if got, want := d.DefaultStorageClass(), "hcloud-volumes"; got != want {
		t.Errorf("DefaultStorageClass() = %q, want %q", got, want)
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "hetzner" {
		t.Errorf("Defaults() = %v, want [hetzner]", defs)
	}
}

func TestHelmChart(t *testing.T) {
	d := driver{}
	repo, chart, ver, err := d.HelmChart(nil)
	if err != nil {
		t.Fatalf("HelmChart() unexpected err: %v", err)
	}
	if repo == "" {
		t.Errorf("repo must not be empty")
	}
	if chart != "hcloud-csi" {
		t.Errorf("chart = %q, want hcloud-csi", chart)
	}
	if ver != "v2.6.0" {
		t.Errorf("version = %q, want v2.6.0", ver)
	}
}

func TestRenderValues(t *testing.T) {
	d := driver{}
	out, err := d.RenderValues(&config.Config{})
	if err != nil {
		t.Fatalf("RenderValues() unexpected err: %v", err)
	}
	if out == "" {
		t.Error("RenderValues() returned empty string")
	}
	if !strings.Contains(out, "hcloud-volumes") {
		t.Errorf("RenderValues() missing hcloud-volumes: %s", out)
	}
	if !strings.Contains(out, "secret") {
		t.Errorf("RenderValues() missing secret reference: %s", out)
	}
}

func TestEnsureSecretEmptyToken(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	// Ensure no token is set.
	cfg.Providers.Hetzner.Token = ""
	err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig")
	if err == nil {
		t.Fatal("EnsureSecret() with empty token should return an error")
	}
	if !strings.Contains(err.Error(), "HCLOUD_TOKEN") {
		t.Errorf("EnsureSecret() error should mention HCLOUD_TOKEN, got: %v", err)
	}
}
