// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package longhorn

import (
	"errors"
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/csi"
)

func TestDriverConstants(t *testing.T) {
	d := driver{}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Name", d.Name(), "longhorn"},
		{"K8sCSIDriverName", d.K8sCSIDriverName(), "driver.longhorn.io"},
		{"DefaultStorageClass", d.DefaultStorageClass(), "longhorn"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.got != c.want {
				t.Errorf("%s() = %q, want %q", c.name, c.got, c.want)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	d := driver{}
	defs := d.Defaults()
	if len(defs) != 0 {
		t.Errorf("Defaults() = %v, want empty slice (cross-provider opt-in)", defs)
	}
}

func TestHelmChart(t *testing.T) {
	d := driver{}
	cases := []struct {
		name      string
		checkFunc func(repo, chart, version string, err error)
	}{
		{
			name: "no error",
			checkFunc: func(repo, chart, version string, err error) {
				if err != nil {
					t.Fatalf("HelmChart() unexpected err: %v", err)
				}
			},
		},
		{
			name: "chart name",
			checkFunc: func(repo, chart, version string, err error) {
				if chart != "longhorn" {
					t.Errorf("chart = %q, want %q", chart, "longhorn")
				}
			},
		},
		{
			name: "version pinned",
			checkFunc: func(repo, chart, version string, err error) {
				if version != "v1.7.2" {
					t.Errorf("version = %q, want %q", version, "v1.7.2")
				}
			},
		},
		{
			name: "repo not empty",
			checkFunc: func(repo, chart, version string, err error) {
				if repo == "" {
					t.Errorf("repo must not be empty")
				}
			},
		},
		{
			name: "repo url correct",
			checkFunc: func(repo, chart, version string, err error) {
				if repo != "https://charts.longhorn.io" {
					t.Errorf("repo = %q, want %q", repo, "https://charts.longhorn.io")
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, chart, version, err := d.HelmChart(nil)
			c.checkFunc(repo, chart, version, err)
		})
	}
}

func TestRenderValues(t *testing.T) {
	d := driver{}
	cases := []struct {
		name    string
		checkFn func(out string, err error)
	}{
		{
			name: "no error",
			checkFn: func(out string, err error) {
				if err != nil {
					t.Fatalf("RenderValues() unexpected err: %v", err)
				}
			},
		},
		{
			name: "contains defaultReplicaCount 3",
			checkFn: func(out string, err error) {
				if !strings.Contains(out, "defaultReplicaCount: 3") {
					t.Errorf("RenderValues() missing defaultReplicaCount: 3\n%s", out)
				}
			},
		},
		{
			name: "contains no required overrides comment",
			checkFn: func(out string, err error) {
				if !strings.Contains(out, "no required overrides") {
					t.Errorf("RenderValues() missing explanatory comment about no required overrides\n%s", out)
				}
			},
		},
		{
			name: "non-empty output",
			checkFn: func(out string, err error) {
				if out == "" {
					t.Errorf("RenderValues() returned empty string")
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := d.RenderValues(nil)
			c.checkFn(out, err)
		})
	}
}

func TestEnsureSecretNotApplicable(t *testing.T) {
	d := driver{}
	err := d.EnsureSecret(nil, "/nonexistent/kubeconfig")
	if !errors.Is(err, csi.ErrNotApplicable) {
		t.Errorf("EnsureSecret() = %v, want csi.ErrNotApplicable", err)
	}
}
