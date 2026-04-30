// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package openebs

import (
	"errors"
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
		{"Name", driver{}.Name(), "openebs"},
		{"K8sCSIDriverName", driver{}.K8sCSIDriverName(), "openebs.io/local"},
		{"DefaultStorageClass", driver{}.DefaultStorageClass(), "openebs-hostpath"},
	}
	for _, tc := range tests {
		tc := tc
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
	d := driver{}
	repo, chart, ver, err := d.HelmChart(nil)
	if err != nil {
		t.Fatalf("HelmChart() unexpected err: %v", err)
	}
	if chart != "openebs" {
		t.Errorf("chart = %q, want %q", chart, "openebs")
	}
	if ver != "4.1.1" {
		t.Errorf("version = %q, want %q", ver, "4.1.1")
	}
	if repo == "" {
		t.Errorf("repo must not be empty")
	}
	if !strings.HasPrefix(repo, "https://") {
		t.Errorf("repo = %q, want https:// prefix", repo)
	}
}

func TestRenderValues(t *testing.T) {
	d := driver{}
	vals, err := d.RenderValues(nil)
	if err != nil {
		t.Fatalf("RenderValues() unexpected err: %v", err)
	}
	for _, want := range []string{
		"engines:",
		"lvm:",
		"enabled: false",
		"zfs:",
	} {
		if !strings.Contains(vals, want) {
			t.Errorf("RenderValues() missing %q in output:\n%s", want, vals)
		}
	}
}

func TestEnsureSecret(t *testing.T) {
	d := driver{}
	err := d.EnsureSecret(nil, "")
	if !errors.Is(err, csi.ErrNotApplicable) {
		t.Errorf("EnsureSecret() = %v, want csi.ErrNotApplicable", err)
	}
}
