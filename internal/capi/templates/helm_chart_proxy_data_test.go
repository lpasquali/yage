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

// TestHelmChartProxyData_AllFieldsRendered exercises every ADR 0012 §3
// field on HelmChartProxyData. Fixture template references each field
// exactly once; missingkey=error turns rename or removal into a hard
// render error.
func TestHelmChartProxyData_AllFieldsRendered(t *testing.T) {
	f := &manifests.Fetcher{MountRoot: "testdata"}

	data := templates.HelmChartProxyData{
		Cfg:       &config.Config{WorkloadClusterName: "wl-prod"},
		Name:      "cilium",
		Namespace: "default",
		ClusterSelector: map[string]string{
			"cluster.x-k8s.io/cluster-name": "wl-prod",
		},
		ChartName:      "cilium",
		RepoURL:        "https://helm.cilium.io/",
		Version:        "1.16.4",
		ChartNamespace: "kube-system",
		ValuesTemplate: "kubeProxyReplacement: true",
	}

	got, err := f.Render("helmchartproxy.yaml.tmpl", data)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	wants := []string{
		"cluster: wl-prod",
		"name: cilium",
		"namespace: default",
		"selector: wl-prod",
		"chartName: cilium",
		"repoURL: https://helm.cilium.io/",
		"version: 1.16.4",
		"chartNamespace: kube-system",
		"valuesTemplate: kubeProxyReplacement: true",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\nfull output:\n%s", want, got)
		}
	}
}
