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

// TestArgoApplicationData_AllFieldsRendered exercises every ADR 0012 §3
// field on ArgoApplicationData, including a populated PostSyncs slice
// (issue #138 extends ADR 0012 §3 from a single PostSync block to a
// slice — see PR body). Fixture template references every leaf field;
// missingkey=error turns any rename into a render failure.
func TestArgoApplicationData_AllFieldsRendered(t *testing.T) {
	f := &manifests.Fetcher{MountRoot: "testdata"}

	data := templates.ArgoApplicationData{
		Cfg:         &config.Config{WorkloadClusterName: "wl-prod"},
		Name:        "metrics-server",
		DestNS:      "kube-system",
		RepoURL:     "https://kubernetes-sigs.github.io/metrics-server/",
		Chart:       "metrics-server",
		Path:        "charts/metrics-server",
		Ref:         "v0.7.2",
		SyncWave:    "5",
		ReleaseName: "metrics-server",
		ValuesYAML:  "replicas: 2",
		Annotations: map[string]string{
			"argocd.argoproj.io/compare-options": "ServerSideDiff=true",
		},
		PostSyncs: []templates.PostSyncBlock{
			{
				URL:              "https://github.com/lpasquali/workload-smoketests",
				Path:             "kustomize/proxmox-csi",
				Ref:              "main",
				KustomizePartial: "  patches:\n    - path: image-override.yaml",
			},
		},
	}

	got, err := f.Render("argoapplication.yaml.tmpl", data)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	wants := []string{
		"cluster: wl-prod",
		"name: metrics-server",
		"destNS: kube-system",
		"repoURL: https://kubernetes-sigs.github.io/metrics-server/",
		"chart: metrics-server",
		"path: charts/metrics-server",
		"ref: v0.7.2",
		"syncWave: 5",
		"releaseName: metrics-server",
		"valuesYAML: replicas: 2",
		"annotation: ServerSideDiff=true",
		"postSyncURL: https://github.com/lpasquali/workload-smoketests",
		"postSyncPath: kustomize/proxmox-csi",
		"postSyncRef: main",
		"postSyncPartial:",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\nfull output:\n%s", want, got)
		}
	}
}
