// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package ociblock

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"
)

func TestDriverConstants(t *testing.T) {
	d := driver{}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Name", d.Name(), "oci-block-storage"},
		{"K8sCSIDriverName", d.K8sCSIDriverName(), "blockvolume.csi.oraclecloud.com"},
		{"DefaultStorageClass", d.DefaultStorageClass(), "oci-bv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s() = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	d := driver{}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "oci" {
		t.Errorf("Defaults() = %v, want [oci]", defs)
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
	if chart != "oci-csi-node" {
		t.Errorf("chart = %q, want %q", chart, "oci-csi-node")
	}
	if ver == "" {
		t.Error("version must not be empty")
	}
	if ver != "1.28.0" {
		t.Errorf("version = %q, want %q", ver, "1.28.0")
	}
}

func TestRenderValuesNonEmpty(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	out, err := d.RenderValues(cfg)
	if err != nil {
		t.Fatalf("RenderValues() unexpected error: %v", err)
	}
	if out == "" {
		t.Error("RenderValues() must return non-empty string")
	}
	if !strings.Contains(out, secretName) {
		t.Errorf("RenderValues() must reference secret name %q, got:\n%s", secretName, out)
	}
	if !strings.Contains(out, "oci-bv") {
		t.Errorf("RenderValues() must reference storage class oci-bv, got:\n%s", out)
	}
}

func TestEnsureSecretNoCreds(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	// OCI credentials are not set — should return ErrNotApplicable.
	err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig")
	if err != csi.ErrNotApplicable {
		t.Errorf("EnsureSecret with no creds: got %v, want csi.ErrNotApplicable", err)
	}
}

func TestEnsureSecretPartialCreds(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	// Only partial credentials — TenancyOCID set but UserOCID and Fingerprint missing.
	cfg.Providers.OCI.TenancyOCID = "ocid1.tenancy.oc1..example"
	err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig")
	if err != csi.ErrNotApplicable {
		t.Errorf("EnsureSecret with partial creds: got %v, want csi.ErrNotApplicable", err)
	}
}

func TestBuildOCIConfig(t *testing.T) {
	oci := config.OCIConfig{
		Region:         "us-ashburn-1",
		TenancyOCID:    "ocid1.tenancy.oc1..example",
		UserOCID:       "ocid1.user.oc1..example",
		Fingerprint:    "aa:bb:cc:dd",
		CompartmentOCID: "ocid1.compartment.oc1..example",
	}
	out := buildOCIConfig(oci)
	for _, want := range []string{"region:", "tenancy:", "user:", "fingerprint:", "compartment:"} {
		if !strings.Contains(out, want) {
			t.Errorf("buildOCIConfig() missing %q in output:\n%s", want, out)
		}
	}
}
