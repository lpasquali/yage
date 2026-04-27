// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyYAMLFile_basic(t *testing.T) {
	content := `
PROXMOX_URL: "https://pve.example.com:8006"
PROXMOX_TOKEN: "root@pam!yage=secret123"
INFRA_PROVIDER: "proxmox"
KIND_CLUSTER_NAME: "test-mgmt"
ARGOCD_ENABLED: "false"
`
	f := filepath.Join(t.TempDir(), "yage.yaml")
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Load()
	if err := ApplyYAMLFile(cfg, f); err != nil {
		t.Fatalf("ApplyYAMLFile: %v", err)
	}

	if cfg.Providers.Proxmox.URL != "https://pve.example.com:8006" {
		t.Errorf("URL = %q, want pve.example.com", cfg.Providers.Proxmox.URL)
	}
	if cfg.Providers.Proxmox.CAPIToken != "root@pam!yage=secret123" {
		t.Errorf("Token = %q", cfg.Providers.Proxmox.CAPIToken)
	}
	if cfg.InfraProvider != "proxmox" {
		t.Errorf("InfraProvider = %q", cfg.InfraProvider)
	}
	if cfg.KindClusterName != "test-mgmt" {
		t.Errorf("KindClusterName = %q", cfg.KindClusterName)
	}
	if cfg.ArgoCD.Enabled {
		t.Error("ArgoCD.Enabled should be false")
	}
}

func TestApplyYAMLFile_blankPath(t *testing.T) {
	cfg := Load()
	orig := cfg.KindClusterName
	if err := ApplyYAMLFile(cfg, ""); err != nil {
		t.Fatalf("blank path should be no-op, got: %v", err)
	}
	if cfg.KindClusterName != orig {
		t.Error("blank path mutated config")
	}
}

func TestApplyYAMLFile_missingFile(t *testing.T) {
	cfg := Load()
	err := ApplyYAMLFile(cfg, "/nonexistent/yage.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestConfigFilePath_argv(t *testing.T) {
	if p := ConfigFilePath([]string{"--config", "/etc/yage.yaml"}); p != "/etc/yage.yaml" {
		t.Errorf("got %q", p)
	}
	if p := ConfigFilePath([]string{"--config=/etc/yage.yaml"}); p != "/etc/yage.yaml" {
		t.Errorf("got %q", p)
	}
	if p := ConfigFilePath([]string{"--dry-run"}); p != "" {
		t.Errorf("expected empty, got %q", p)
	}
}

func TestConfigFilePath_env(t *testing.T) {
	t.Setenv("YAGE_CONFIG_FILE", "/tmp/cfg.yaml")
	if p := ConfigFilePath(nil); p != "/tmp/cfg.yaml" {
		t.Errorf("got %q", p)
	}
}
