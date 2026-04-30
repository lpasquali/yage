// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package rookceph

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/csi"
)

func TestDriverConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Name", driver{}.Name(), "rook-ceph"},
		{"K8sCSIDriverName", driver{}.K8sCSIDriverName(), "rook-ceph.csi.ceph.com"},
		{"DefaultStorageClass", driver{}.DefaultStorageClass(), "rook-ceph-block"},
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
	defs := driver{}.Defaults()
	if len(defs) != 0 {
		t.Errorf("Defaults() = %v, want empty slice (cross-provider opt-in)", defs)
	}
}

func TestHelmChart(t *testing.T) {
	repo, chart, ver, err := driver{}.HelmChart(nil)
	if err != nil {
		t.Fatalf("HelmChart() unexpected err: %v", err)
	}
	if chart != "rook-ceph" {
		t.Errorf("chart = %q, want %q", chart, "rook-ceph")
	}
	if ver != "v1.15.5" {
		t.Errorf("version = %q, want %q", ver, "v1.15.5")
	}
	if repo != "https://charts.rook.io/release" {
		t.Errorf("repo = %q, want %q", repo, "https://charts.rook.io/release")
	}
}

func TestRenderValues(t *testing.T) {
	out, err := driver{}.RenderValues(nil)
	if err != nil {
		t.Fatalf("RenderValues() unexpected err: %v", err)
	}
	checks := []string{
		"csi:",
		"cephBlockPools:",
		"replicaCount: 3",
		"operator:",
	}
	for _, needle := range checks {
		if !strings.Contains(out, needle) {
			t.Errorf("RenderValues() missing %q in output:\n%s", needle, out)
		}
	}
}

func TestEnsureSecretNotApplicable(t *testing.T) {
	err := driver{}.EnsureSecret(nil, "/nonexistent/kubeconfig")
	if err != csi.ErrNotApplicable {
		t.Errorf("EnsureSecret() = %v, want csi.ErrNotApplicable", err)
	}
}
