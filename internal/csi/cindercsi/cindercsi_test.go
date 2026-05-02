// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cindercsi

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
	"github.com/lpasquali/yage/internal/csi"
)


// fetcher returns a Fetcher pointed at the in-package testdata fixture.
func fetcher(t *testing.T) *manifests.Fetcher {
	t.Helper()
	return &manifests.Fetcher{MountRoot: "testdata"}
}
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

func TestRender(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	out, err := d.Render(fetcher(t), cfg)
	if err != nil {
		t.Fatalf("Render err: %v", err)
	}
	if !strings.Contains(out, secretName) {
		t.Errorf("Render output missing secret name %q: %s", secretName, out)
	}
	if !strings.Contains(out, "secret:") {
		t.Errorf("Render output missing secret: key: %s", out)
	}
	if !strings.Contains(out, "storageClass:") {
		t.Errorf("Render output missing storageClass key: %s", out)
	}
	if !strings.Contains(out, "hostMount: false") {
		t.Errorf("Render should disable hostMount: %s", out)
	}
}

func TestEnsureSecretNotApplicableWhenFieldsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		cloud string
	}{
		{name: "empty cloud name", cloud: ""},
		{name: "non-empty cloud but no OS_AUTH_URL", cloud: "devstack"},
	}
	d := driver{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Ensure OS_AUTH_URL is unset for deterministic behaviour
			// regardless of the developer's environment.
			t.Setenv("OS_AUTH_URL", "")
			cfg := &config.Config{}
			cfg.Providers.OpenStack.Cloud = tc.cloud
			err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig")
			if err != csi.ErrNotApplicable {
				t.Errorf("EnsureSecret(%q, OS_AUTH_URL='') = %v, want ErrNotApplicable", tc.cloud, err)
			}
		})
	}
}

func TestBuildCloudConf(t *testing.T) {
	// Set env vars for this test, restore on exit.
	t.Setenv("OS_USERNAME", "testuser")
	t.Setenv("OS_PASSWORD", "testpass")
	t.Setenv("OS_PROJECT_NAME", "")
	t.Setenv("OS_USER_DOMAIN_NAME", "Default")
	t.Setenv("OS_DOMAIN_NAME", "")

	cfg := &config.Config{}
	cfg.Providers.OpenStack.ProjectName = "myproject"
	cfg.Providers.OpenStack.Region = "RegionOne"

	out := buildCloudConf("https://keystone.example.com:5000/v3", cfg)

	if !strings.HasPrefix(out, "[Global]\n") {
		t.Errorf("cloud.conf must start with [Global]: %s", out)
	}
	if !strings.Contains(out, "auth-url=https://keystone.example.com:5000/v3") {
		t.Errorf("missing auth-url: %s", out)
	}
	if !strings.Contains(out, "username=testuser") {
		t.Errorf("missing username: %s", out)
	}
	if !strings.Contains(out, "password=testpass") {
		t.Errorf("missing password: %s", out)
	}
	// OS_PROJECT_NAME empty, falls back to cfg field.
	if !strings.Contains(out, "tenant-name=myproject") {
		t.Errorf("missing tenant-name: %s", out)
	}
	if !strings.Contains(out, "domain-name=Default") {
		t.Errorf("missing domain-name: %s", out)
	}
	if !strings.Contains(out, "region=RegionOne") {
		t.Errorf("missing region: %s", out)
	}
	// YAML keys (clouds.yaml format) must NOT appear in INI output.
	if strings.Contains(out, "auth_url") || strings.Contains(out, "clouds:") {
		t.Errorf("cloud.conf must be INI format, not YAML: %s", out)
	}
}
