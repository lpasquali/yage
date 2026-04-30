// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cindercsi

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"
)

func TestDriverConstants(t *testing.T) {
	d := driver{}
	if got, want := d.Name(), "openstack-cinder"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := d.K8sCSIDriverName(), "cinder.csi.openstack.org"; got != want {
		t.Errorf("K8sCSIDriverName() = %q, want %q", got, want)
	}
	if got, want := d.DefaultStorageClass(), "csi-cinder-sc-delete"; got != want {
		t.Errorf("DefaultStorageClass() = %q, want %q", got, want)
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "openstack" {
		t.Errorf("Defaults() = %v, want [openstack]", defs)
	}
}

func TestHelmChart(t *testing.T) {
	d := driver{}
	repo, chart, ver, err := d.HelmChart(nil)
	if err != nil {
		t.Fatalf("HelmChart() unexpected err: %v", err)
	}
	if chart != "openstack-cinder-csi" {
		t.Errorf("chart = %q, want openstack-cinder-csi", chart)
	}
	if ver != "2.31.2" {
		t.Errorf("version = %q, want 2.31.2", ver)
	}
	if repo == "" {
		t.Errorf("repo must not be empty")
	}
	if !strings.Contains(repo, "cloud-provider-openstack") {
		t.Errorf("repo %q does not contain cloud-provider-openstack", repo)
	}
}

func TestRenderValues(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	out, err := d.RenderValues(cfg)
	if err != nil {
		t.Fatalf("RenderValues err: %v", err)
	}
	if !strings.Contains(out, secretName) {
		t.Errorf("RenderValues output missing secret name %q: %s", secretName, out)
	}
	if !strings.Contains(out, secretNamespace) {
		t.Errorf("RenderValues output missing namespace %q: %s", secretNamespace, out)
	}
	if !strings.Contains(out, "storageClass:") {
		t.Errorf("RenderValues output missing storageClass key: %s", out)
	}
}

func TestEnsureSecretNotApplicableWhenFieldsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		cloud string
		// OS_AUTH_URL is unset in tests (env is clean)
	}{
		{name: "empty cloud name", cloud: ""},
		{name: "non-empty cloud but no OS_AUTH_URL", cloud: "devstack"},
	}
	d := driver{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Providers.OpenStack.Cloud = tc.cloud
			// OS_AUTH_URL is not set in test environment; cloud="" cases
			// also exercise the Cloud guard.
			err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig")
			if err != csi.ErrNotApplicable {
				t.Errorf("EnsureSecret(%q, no OS_AUTH_URL) = %v, want ErrNotApplicable", tc.cloud, err)
			}
		})
	}
}

func TestBuildCloudsYAML(t *testing.T) {
	cfg := &config.Config{}
	cfg.Providers.OpenStack.ProjectName = "myproject"
	cfg.Providers.OpenStack.Region = "RegionOne"

	out := buildCloudsYAML("devstack", "https://keystone.example.com:5000/v3", cfg)

	if !strings.Contains(out, "devstack:") {
		t.Errorf("missing cloud name: %s", out)
	}
	if !strings.Contains(out, "auth_url: https://keystone.example.com:5000/v3") {
		t.Errorf("missing auth_url: %s", out)
	}
	if !strings.Contains(out, "project_name: myproject") {
		t.Errorf("missing project_name: %s", out)
	}
	if !strings.Contains(out, "region_name: RegionOne") {
		t.Errorf("missing region_name: %s", out)
	}
}
