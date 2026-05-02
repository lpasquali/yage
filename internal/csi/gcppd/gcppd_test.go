// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package gcppd

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
	if got, want := d.Name(), "gcp-pd"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := d.K8sCSIDriverName(), "pd.csi.storage.gke.io"; got != want {
		t.Errorf("K8sCSIDriverName() = %q, want %q", got, want)
	}
	if got, want := d.DefaultStorageClass(), "pd-balanced"; got != want {
		t.Errorf("DefaultStorageClass() = %q, want %q", got, want)
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "gcp" {
		t.Errorf("Defaults() = %v, want [gcp]", defs)
	}
}

func TestRender(t *testing.T) {
	d := driver{}
	t.Run("project", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Providers.GCP.Project = "my-gcp-project"
		out, err := d.Render(fetcher(t), cfg)
		if err != nil {
			t.Fatalf("Render err: %v", err)
		}
		if !strings.Contains(out, "my-gcp-project") {
			t.Errorf("Render missing project: %s", out)
		}
		if !strings.Contains(out, "gce-conf") {
			t.Errorf("Render SA mode should reference gce-conf Secret: %s", out)
		}
	})
	t.Run("workload-identity", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Providers.GCP.IdentityModel = "workload-identity"
		cfg.Providers.GCP.Project = "my-gcp-project"
		out, err := d.Render(fetcher(t), cfg)
		if err != nil {
			t.Fatalf("Render WI err: %v", err)
		}
		if !strings.Contains(out, "iam.gke.io/gcp-service-account") {
			t.Errorf("Render WI missing SA annotation: %s", out)
		}
		if !strings.Contains(out, "my-gcp-project") {
			t.Errorf("Render WI missing project: %s", out)
		}
	})
}



func TestEnsureSecretWorkloadIdentityNoOp(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	cfg.Providers.GCP.IdentityModel = "workload-identity"
	if err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig"); err != nil {
		t.Errorf("EnsureSecret on workload-identity should be no-op, got %v", err)
	}
}