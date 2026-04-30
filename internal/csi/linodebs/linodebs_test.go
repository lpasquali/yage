// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package linodebs

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestDriverConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Name", driver{}.Name(), "linode-block-storage"},
		{"K8sCSIDriverName", driver{}.K8sCSIDriverName(), "linodebs.csi.linode.com"},
		{"DefaultStorageClass", driver{}.DefaultStorageClass(), "linode-block-storage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s() = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	d := driver{}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "linode" {
		t.Errorf("Defaults() = %v, want [linode]", defs)
	}
}

func TestHelmChart(t *testing.T) {
	d := driver{}
	repo, chart, ver, err := d.HelmChart(nil)
	if err != nil {
		t.Fatalf("HelmChart() unexpected error: %v", err)
	}
	if repo == "" {
		t.Error("repo must not be empty")
	}
	if chart != "linode-blockstorage-csi-driver" {
		t.Errorf("chart = %q, want linode-blockstorage-csi-driver", chart)
	}
	if ver != "v1.1.2" {
		t.Errorf("version = %q, want v1.1.2", ver)
	}
}

func TestRenderValues(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	out, err := d.RenderValues(cfg)
	if err != nil {
		t.Fatalf("RenderValues() unexpected error: %v", err)
	}
	if !strings.Contains(out, "linode") {
		t.Errorf("RenderValues output does not reference the linode secret: %s", out)
	}
	if !strings.Contains(out, "linode-block-storage") {
		t.Errorf("RenderValues output missing storage class name: %s", out)
	}
	if !strings.Contains(out, "WaitForFirstConsumer") {
		t.Errorf("RenderValues output missing volumeBindingMode: %s", out)
	}
}

func TestEnsureSecretEmptyToken(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	// token is empty — must return an error
	err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig")
	if err == nil {
		t.Fatal("EnsureSecret with empty token should return an error")
	}
	if !strings.Contains(err.Error(), "YAGE_LINODE_TOKEN") && !strings.Contains(err.Error(), "LINODE_TOKEN") {
		t.Errorf("error message should mention the env var, got: %v", err)
	}
}
