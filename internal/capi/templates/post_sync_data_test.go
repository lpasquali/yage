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

// TestPostSyncData_AllFieldsRendered exercises every ADR 0012 §3 field
// on PostSyncData against an in-package fixture template that
// references each field. missingkey=error pins the contract.
func TestPostSyncData_AllFieldsRendered(t *testing.T) {
	f := &manifests.Fetcher{MountRoot: "testdata"}

	data := templates.PostSyncData{
		Cfg:           &config.Config{WorkloadClusterName: "wl-prod"},
		Namespace:     "argocd",
		StorageClass:  "proxmox-csi",
		KubectlImage:  "registry.k8s.io/kubectl:v1.31.0",
		K8sVersionTag: "v1.31.0",
	}

	got, err := f.Render("postsyncdata.yaml.tmpl", data)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	wants := []string{
		"cluster: wl-prod",
		"namespace: argocd",
		"storageClass: proxmox-csi",
		"kubectlImage: registry.k8s.io/kubectl:v1.31.0",
		"k8sVersionTag: v1.31.0",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\nfull output:\n%s", want, got)
		}
	}
}
