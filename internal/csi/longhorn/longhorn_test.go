// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package longhorn

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

func TestRender(t *testing.T) {
	d := driver{}
	out, err := d.Render(fetcher(t), nil)
	if err != nil {
		t.Fatalf("Render() unexpected err: %v", err)
	}
	if !strings.Contains(out, "defaultReplicaCount: 3") {
		t.Errorf("Render() missing defaultReplicaCount: 3\n%s", out)
	}
	if !strings.Contains(out, "no required overrides") {
		t.Errorf("Render() missing comment about no required overrides\n%s", out)
	}
}

func TestEnsureSecretNotApplicable(t *testing.T) {
	d := driver{}
	err := d.EnsureSecret(nil, "/nonexistent/kubeconfig")
	if !errors.Is(err, csi.ErrNotApplicable) {
		t.Errorf("EnsureSecret() = %v, want csi.ErrNotApplicable", err)
	}
}
