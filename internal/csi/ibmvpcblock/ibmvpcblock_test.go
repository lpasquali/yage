// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package ibmvpcblock

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestDriverConstants(t *testing.T) {
	d := driver{}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Name", d.Name(), "ibm-vpc-block"},
		{"K8sCSIDriverName", d.K8sCSIDriverName(), "vpc.block.csi.ibm.io"},
		{"DefaultStorageClass", d.DefaultStorageClass(), "ibmc-vpc-block-10iops-tier"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s() = %q, want %q", tt.name, tt.got, tt.want)
			}
		})
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "ibmcloud" {
		t.Errorf("Defaults() = %v, want [ibmcloud]", defs)
	}
}

func TestHelmChart(t *testing.T) {
	d := driver{}
	repo, chart, ver, err := d.HelmChart(nil)
	if err != nil {
		t.Fatalf("HelmChart() unexpected err: %v", err)
	}
	if chart != "ibm-vpc-block-csi-driver" {
		t.Errorf("chart = %q, want ibm-vpc-block-csi-driver", chart)
	}
	if ver != "5.2.0" {
		t.Errorf("version = %q, want 5.2.0", ver)
	}
	if repo == "" {
		t.Errorf("repo must not be empty")
	}
	if !strings.Contains(repo, "icr.io") {
		t.Errorf("repo %q should reference icr.io", repo)
	}
}

func TestRenderValues(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	out, err := d.RenderValues(cfg)
	if err != nil {
		t.Fatalf("RenderValues() unexpected err: %v", err)
	}
	if !strings.Contains(out, "clusterInfo.clusterID") {
		t.Errorf("RenderValues missing clusterInfo.clusterID operator note: %s", out)
	}
	if !strings.Contains(out, secretName) {
		t.Errorf("RenderValues missing Secret name %q: %s", secretName, out)
	}
}

func TestEnsureSecretEmptyAPIKey(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	// IBMCloudAPIKey is zero-value (empty)
	err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig")
	if err == nil {
		t.Fatal("EnsureSecret should return an error when IBMCloudAPIKey is empty")
	}
	if !strings.Contains(err.Error(), "IBMCloudAPIKey") {
		t.Errorf("error message should mention IBMCloudAPIKey, got: %v", err)
	}
}
