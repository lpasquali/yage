// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package azuredisk

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
	if got, want := d.Name(), "azure-disk"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := d.K8sCSIDriverName(), "disk.csi.azure.com"; got != want {
		t.Errorf("K8sCSIDriverName() = %q, want %q", got, want)
	}
	if got, want := d.DefaultStorageClass(), "azuredisk-standard-ssd"; got != want {
		t.Errorf("DefaultStorageClass() = %q, want %q", got, want)
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "azure" {
		t.Errorf("Defaults() = %v, want [azure]", defs)
	}
}

func TestRender(t *testing.T) {
	d := driver{}
	t.Run("SP", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Providers.Azure.IdentityModel = "service-principal"
		out, err := d.Render(fetcher(t), cfg)
		if err != nil {
			t.Fatalf("Render SP err: %v", err)
		}
		if !strings.Contains(out, "azuredisk-standard-ssd") {
			t.Errorf("Render SP missing storage class: %s", out)
		}
		if !strings.Contains(out, "azure-cloud-config") {
			t.Errorf("Render SP missing cloud-config Secret: %s", out)
		}
	})
	t.Run("WI", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Providers.Azure.IdentityModel = "workload-identity"
		cfg.Providers.Azure.ClientID = "test-client-id"
		out, err := d.Render(fetcher(t), cfg)
		if err != nil {
			t.Fatalf("Render WI err: %v", err)
		}
		if !strings.Contains(out, "azure.workload.identity/client-id") {
			t.Errorf("Render WI missing client-id annotation: %s", out)
		}
		if !strings.Contains(out, "test-client-id") {
			t.Errorf("Render WI missing client-id: %s", out)
		}
	})
}

func TestEnsureSecretWorkloadIdentityNoOp(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	cfg.Providers.Azure.IdentityModel = "workload-identity"
	if err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig"); err != nil {
		t.Errorf("EnsureSecret on workload-identity should be no-op, got %v", err)
	}
}

func TestEnsureSecretEmptyConfigNoOp(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	// IdentityModel unset → treated as service-principal, but with
	// no creds populated EnsureSecret returns nil silently.
	if err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig"); err != nil {
		t.Errorf("EnsureSecret with empty Azure cfg should be no-op, got %v", err)
	}
}