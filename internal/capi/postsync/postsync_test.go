// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package postsync_test

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/capi/postsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
)

// testFetcher returns a Fetcher pointed at the in-package testdata fixtures.
func testFetcher() *manifests.Fetcher {
	return &manifests.Fetcher{MountRoot: "testdata"}
}

func testCfg() *config.Config {
	cfg := &config.Config{}
	cfg.WorkloadPostsyncNamespace = "workload-smoke"
	cfg.Providers.Proxmox.CSINamespace = "csi-namespace"
	cfg.Providers.Proxmox.CSIStorageClassName = "proxmox-csi"
	return cfg
}

// --- KustomizeBlockForJobTemplate ---

func TestKustomizeBlockForJobTemplate_MetricsServerSmoketest(t *testing.T) {
	f := testFetcher()
	cfg := testCfg()

	got, err := postsync.KustomizeBlockForJobTemplate(f, cfg, "metrics-server-smoketest")
	if err != nil {
		t.Fatalf("KustomizeBlockForJobTemplate error: %v", err)
	}

	wants := []string{
		"kustomize:",
		"namespace: workload-smoke",
		"kind: Job",
		"name: metrics-server-smoketest",
		"/spec/template/spec/containers/0/image",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("KustomizeBlockForJobTemplate missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestKustomizeBlockForJobTemplate_ProxmoxCSIRolloutSmoketest(t *testing.T) {
	f := testFetcher()
	cfg := testCfg()

	got, err := postsync.KustomizeBlockForJobTemplate(f, cfg, "proxmox-csi-rollout-smoketest")
	if err != nil {
		t.Fatalf("KustomizeBlockForJobTemplate error: %v", err)
	}

	if !strings.Contains(got, "name: proxmox-csi-rollout-smoketest") {
		t.Errorf("KustomizeBlockForJobTemplate missing job name; got:\n%s", got)
	}
}

func TestKustomizeBlockForJobTemplate_KyvernoSmoketest(t *testing.T) {
	f := testFetcher()
	cfg := testCfg()

	got, err := postsync.KustomizeBlockForJobTemplate(f, cfg, "kyverno-smoketest")
	if err != nil {
		t.Fatalf("KustomizeBlockForJobTemplate error: %v", err)
	}

	if !strings.Contains(got, "name: kyverno-smoketest") {
		t.Errorf("KustomizeBlockForJobTemplate missing kyverno-smoketest job name; got:\n%s", got)
	}
}

func TestKustomizeBlockForJobTemplate_DefaultNamespace(t *testing.T) {
	f := testFetcher()
	cfg := &config.Config{}
	// WorkloadPostsyncNamespace is intentionally empty — must default to workload-smoke.

	got, err := postsync.KustomizeBlockForJobTemplate(f, cfg, "some-smoketest")
	if err != nil {
		t.Fatalf("KustomizeBlockForJobTemplate error: %v", err)
	}

	if !strings.Contains(got, "namespace: workload-smoke") {
		t.Errorf("KustomizeBlockForJobTemplate did not apply default namespace; got:\n%s", got)
	}
}

func TestKustomizeBlockForJobTemplate_KubectlImageEmbedded(t *testing.T) {
	f := testFetcher()
	cfg := testCfg()
	cfg.ArgoCD.PostsyncHooksKubectlImg = "registry.local/kubectl:v1.31.0"

	got, err := postsync.KustomizeBlockForJobTemplate(f, cfg, "some-smoketest")
	if err != nil {
		t.Fatalf("KustomizeBlockForJobTemplate error: %v", err)
	}

	if !strings.Contains(got, "registry.local/kubectl:v1.31.0") {
		t.Errorf("KustomizeBlockForJobTemplate missing kubectl image; got:\n%s", got)
	}
}

// --- SmokeRenderKustomizeBlockTemplate ---

func TestSmokeRenderKustomizeBlockTemplate_Basic(t *testing.T) {
	f := testFetcher()
	cfg := testCfg()

	got, err := postsync.SmokeRenderKustomizeBlockTemplate(f, cfg)
	if err != nil {
		t.Fatalf("SmokeRenderKustomizeBlockTemplate error: %v", err)
	}

	wants := []string{
		"kustomize:",
		"namespace: csi-namespace",
		"name: proxmox-csi-smoke",
		"/spec/template/spec/containers/0/image",
		"/spec/template/spec/containers/0/env/0/value",
		"/spec/template/spec/containers/0/env/1/value",
		"proxmox-csi",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("SmokeRenderKustomizeBlockTemplate missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestSmokeRenderKustomizeBlockTemplate_StorageClassEmbedded(t *testing.T) {
	f := testFetcher()
	cfg := testCfg()
	cfg.Providers.Proxmox.CSIStorageClassName = "fast-storage"

	got, err := postsync.SmokeRenderKustomizeBlockTemplate(f, cfg)
	if err != nil {
		t.Fatalf("SmokeRenderKustomizeBlockTemplate error: %v", err)
	}

	if !strings.Contains(got, "fast-storage") {
		t.Errorf("SmokeRenderKustomizeBlockTemplate missing storage class; got:\n%s", got)
	}
}

func TestSmokeRenderKustomizeBlockTemplate_NamespaceInBothPatches(t *testing.T) {
	f := testFetcher()
	cfg := testCfg()
	cfg.Providers.Proxmox.CSINamespace = "my-csi-ns"

	got, err := postsync.SmokeRenderKustomizeBlockTemplate(f, cfg)
	if err != nil {
		t.Fatalf("SmokeRenderKustomizeBlockTemplate error: %v", err)
	}

	count := strings.Count(got, "my-csi-ns")
	if count < 2 {
		t.Errorf("SmokeRenderKustomizeBlockTemplate: expected namespace at least 2 times (kustomize.namespace + env/0/value), got %d\nfull:\n%s", count, got)
	}
}
