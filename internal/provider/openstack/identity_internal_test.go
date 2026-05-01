// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package openstack

// White-box tests for buildCloudsYAML. These live in the same package (no
// _test suffix on the package name) so they can access unexported helpers.
// The external black-box tests live in identity_test.go.

import (
	"os"
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

// setenvInternal sets an env var for the duration of t and restores it via Cleanup.
func setenvInternal(t *testing.T, key, value string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("setenv %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func unsetenvInternal(t *testing.T, key string) {
	t.Helper()
	old, had := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	if had {
		t.Cleanup(func() { _ = os.Setenv(key, old) })
	}
}

// TestBuildCloudsYAML_NoCredentials verifies that missing credentials produce an error.
func TestBuildCloudsYAML_NoCredentials(t *testing.T) {
	unsetenvInternal(t, "OS_USERNAME")
	unsetenvInternal(t, "OS_PASSWORD")
	unsetenvInternal(t, "OS_APPLICATION_CREDENTIAL_ID")
	unsetenvInternal(t, "OS_APPLICATION_CREDENTIAL_SECRET")

	cfg := &config.Config{}
	_, err := buildCloudsYAML("devstack", "https://keystone.example:5000/v3", cfg)
	if err == nil {
		t.Fatal("expected error for missing credentials, got nil")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("expected 'credentials' in error message, got: %v", err)
	}
}

// TestBuildCloudsYAML_UsernamePassword verifies the password-auth path produces
// correct YAML content.
func TestBuildCloudsYAML_UsernamePassword(t *testing.T) {
	setenvInternal(t, "OS_USERNAME", "admin")
	setenvInternal(t, "OS_PASSWORD", "hunter2")
	setenvInternal(t, "OS_PROJECT_NAME", "myproject")
	setenvInternal(t, "OS_DOMAIN_NAME", "Default")
	unsetenvInternal(t, "OS_APPLICATION_CREDENTIAL_ID")
	unsetenvInternal(t, "OS_APPLICATION_CREDENTIAL_SECRET")

	cfg := &config.Config{}
	cfg.Providers.OpenStack.Region = "RegionOne"

	got, err := buildCloudsYAML("devstack", "https://keystone.example:5000/v3", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"clouds:",
		"devstack:",
		"auth:",
		"auth_url: https://keystone.example:5000/v3",
		"username: admin",
		"password: hunter2",
		"project_name: myproject",
		"user_domain_name: Default",
		"region_name: RegionOne",
		"identity_api_version: 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("clouds.yaml missing %q\nfull output:\n%s", want, got)
		}
	}

	// App-cred keys must NOT appear.
	for _, absent := range []string{"application_credential_id", "application_credential_secret"} {
		if strings.Contains(got, absent) {
			t.Errorf("clouds.yaml should not contain %q in password path\nfull output:\n%s", absent, got)
		}
	}
}

// TestBuildCloudsYAML_AppCredential verifies the application-credential path.
func TestBuildCloudsYAML_AppCredential(t *testing.T) {
	setenvInternal(t, "OS_APPLICATION_CREDENTIAL_ID", "cred-id-abc")
	setenvInternal(t, "OS_APPLICATION_CREDENTIAL_SECRET", "cred-secret-xyz")
	unsetenvInternal(t, "OS_USERNAME")
	unsetenvInternal(t, "OS_PASSWORD")

	cfg := &config.Config{}
	cfg.Providers.OpenStack.Cloud = "mycloud"

	got, err := buildCloudsYAML("mycloud", "https://keystone.example:5000/v3", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"clouds:",
		"mycloud:",
		"auth:",
		"auth_url: https://keystone.example:5000/v3",
		"application_credential_id: cred-id-abc",
		"application_credential_secret: cred-secret-xyz",
		"identity_api_version: 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("clouds.yaml missing %q\nfull output:\n%s", want, got)
		}
	}

	// Password fields must NOT appear.
	for _, absent := range []string{"username:", "password:"} {
		if strings.Contains(got, absent) {
			t.Errorf("clouds.yaml should not contain %q in app-cred path\nfull output:\n%s", absent, got)
		}
	}
}

// TestBuildCloudsYAML_RegionFromCfg verifies that the region is taken from cfg
// when the env doesn't carry it directly.
func TestBuildCloudsYAML_RegionFromCfg(t *testing.T) {
	setenvInternal(t, "OS_USERNAME", "u")
	setenvInternal(t, "OS_PASSWORD", "p")
	unsetenvInternal(t, "OS_APPLICATION_CREDENTIAL_ID")
	unsetenvInternal(t, "OS_APPLICATION_CREDENTIAL_SECRET")

	cfg := &config.Config{}
	cfg.Providers.OpenStack.Region = "eu-west-1"

	got, err := buildCloudsYAML("testcloud", "https://keystone.example:5000/v3", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "region_name: eu-west-1") {
		t.Errorf("expected region_name: eu-west-1, got:\n%s", got)
	}
}

// TestBuildCloudsYAML_ProjectFallbackFromCfg verifies that project name falls
// back to cfg when OS_PROJECT_NAME is not set.
func TestBuildCloudsYAML_ProjectFallbackFromCfg(t *testing.T) {
	setenvInternal(t, "OS_USERNAME", "u")
	setenvInternal(t, "OS_PASSWORD", "p")
	unsetenvInternal(t, "OS_PROJECT_NAME")
	unsetenvInternal(t, "OS_TENANT_NAME")
	unsetenvInternal(t, "OS_APPLICATION_CREDENTIAL_ID")
	unsetenvInternal(t, "OS_APPLICATION_CREDENTIAL_SECRET")

	cfg := &config.Config{}
	cfg.Providers.OpenStack.ProjectName = "cfg-project"

	got, err := buildCloudsYAML("testcloud", "https://keystone.example:5000/v3", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "project_name: cfg-project") {
		t.Errorf("expected project_name from cfg, got:\n%s", got)
	}
}
