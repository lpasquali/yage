// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package vspherecsi

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
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Name", d.Name(), "vsphere-csi"},
		{"K8sCSIDriverName", d.K8sCSIDriverName(), "csi.vsphere.volume"},
		{"DefaultStorageClass", d.DefaultStorageClass(), "vsphere-sc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s() = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "vsphere" {
		t.Errorf("Defaults() = %v, want [vsphere]", defs)
	}
}

func TestHelmChart(t *testing.T) {
	d := driver{}
	repo, chart, version, err := d.HelmChart(&config.Config{})
	if err != nil {
		t.Fatalf("HelmChart() error: %v", err)
	}
	if repo != "https://kubernetes.github.io/cloud-provider-vsphere" {
		t.Errorf("repo = %q", repo)
	}
	if chart != "vsphere-csi-driver" {
		t.Errorf("chart = %q", chart)
	}
	if version != "3.3.1" {
		t.Errorf("version = %q", version)
	}
}

func TestRender(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	cfg.Providers.Vsphere.Server = "vcenter.example.com"
	out, err := d.Render(fetcher(t), cfg)
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	wants := []string{"vsphere-config-secret", "vsphere-sc", "WaitForFirstConsumer", "storageClass:"}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("Render() missing %q in:\n%s", want, out)
		}
	}
}

func TestEnsureSecretMissingFields(t *testing.T) {
	d := driver{}
	tests := []struct {
		name     string
		server   string
		username string
		password string
		wantErr  string
	}{
		{
			name:     "server empty",
			server:   "",
			username: "admin",
			password: "secret",
			wantErr:  "Server must be set",
		},
		{
			name:     "username empty",
			server:   "vcenter.example.com",
			username: "",
			password: "secret",
			wantErr:  "Username must be set",
		},
		{
			name:     "password empty",
			server:   "vcenter.example.com",
			username: "admin",
			password: "",
			wantErr:  "Password must be set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Providers.Vsphere.Server = tt.server
			cfg.Providers.Vsphere.Username = tt.username
			cfg.Providers.Vsphere.Password = tt.password
			// Pass empty kubeconfig path — validation runs first so it
			// never reaches the kubeconfig load.
			err := d.EnsureSecret(cfg, "")
			if err == nil {
				t.Fatalf("EnsureSecret() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("EnsureSecret() error = %q, want containing %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestBuildINI(t *testing.T) {
	cfg := &config.Config{}
	cfg.WorkloadClusterName = "my-cluster"
	cfg.Providers.Vsphere.Server = "vcenter.example.com"
	cfg.Providers.Vsphere.Username = "administrator@vsphere.local"
	cfg.Providers.Vsphere.Password = "P@ssw0rd!"
	cfg.Providers.Vsphere.Datacenter = "DC0"
	cfg.Providers.Vsphere.TLSThumbprint = "AA:BB:CC:DD:EE:FF"

	ini := buildINI(cfg)

	checks := []string{
		"cluster-id = \"my-cluster\"",
		`[VirtualCenter "vcenter.example.com"]`,
		"user = \"administrator@vsphere.local\"",
		"datacenters = \"DC0\"",
		"thumbprint = \"AA:BB:CC:DD:EE:FF\"",
		"insecure-flag = \"false\"",
	}
	for _, want := range checks {
		if !strings.Contains(ini, want) {
			t.Errorf("buildINI() missing %q in:\n%s", want, ini)
		}
	}
}

func TestBuildINIInsecureWhenNoThumbprint(t *testing.T) {
	cfg := &config.Config{}
	cfg.Providers.Vsphere.Server = "vcenter.example.com"
	cfg.Providers.Vsphere.Username = "admin"
	cfg.Providers.Vsphere.Password = "pass"

	ini := buildINI(cfg)
	if !strings.Contains(ini, "insecure-flag = \"true\"") {
		t.Errorf("buildINI() should set insecure-flag=true when TLSThumbprint is empty:\n%s", ini)
	}
	if strings.Contains(ini, "thumbprint") {
		t.Errorf("buildINI() should not emit thumbprint when empty:\n%s", ini)
	}
}
