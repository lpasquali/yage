// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package openebs

import (
	"errors"
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/platform/manifests"
)


// fetcher returns a Fetcher pointed at the in-package testdata fixture.
func fetcher(t *testing.T) *manifests.Fetcher {
	t.Helper()
	return &manifests.Fetcher{MountRoot: "testdata"}
}
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

func TestRender(t *testing.T) {
	d := driver{}
	vals, err := d.Render(fetcher(t), nil)
	if err != nil {
		t.Fatalf("Render() unexpected err: %v", err)
	}
	wants := []string{"engines:", "local:", "lvm:", "enabled: false", "zfs:"}
	for _, want := range wants {
		if !strings.Contains(vals, want) {
			t.Errorf("Render() missing %q in output:\n%s", want, vals)
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
