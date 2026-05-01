// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package templates_test

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/capi/templates"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
)

// TestKustomizePartialData_AllFieldsRendered exercises every ADR 0012
// §3 field on KustomizePartialData against an in-package fixture
// template that references each field including a representative key
// in the Extra map. missingkey=error pins the contract.
func TestKustomizePartialData_AllFieldsRendered(t *testing.T) {
	f := &manifests.Fetcher{MountRoot: "testdata"}

	data := templates.KustomizePartialData{
		Cfg:          &config.Config{WorkloadClusterName: "wl-prod"},
		Namespace:    "argocd",
		JobName:      "proxmox-csi-smoke",
		KubectlImage: "registry.k8s.io/kubectl:v1.31.0",
		Extra: map[string]string{
			"TARGET_IMAGE": "registry.local/csi-driver:v1.2.3",
		},
	}

	got, err := f.Render("kustomizepartial.yaml.tmpl", data)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	wants := []string{
		"cluster: wl-prod",
		"namespace: argocd",
		"jobName: proxmox-csi-smoke",
		"kubectlImage: registry.k8s.io/kubectl:v1.31.0",
		"extra: registry.local/csi-driver:v1.2.3",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\nfull output:\n%s", want, got)
		}
	}
}
