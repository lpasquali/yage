// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

// Unit tests for EnsureRepoSync helpers (ADR 0010 §1–2, issue #144).
//
// All tests use a fake k8s clientset (no real cluster required).
// We verify:
//   - PVC is created with the correct spec (size, storage class, labels)
//   - Job is created with the correct init containers (images, env vars, order)
//   - Image mirror prefix is applied correctly when cfg.ImageRegistryMirror is set
//   - reposPVCSize falls back to "500Mi" when the config field is empty

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// fakeCli returns a k8sclient.Client backed by a fake clientset with the
// yage-system namespace pre-created (so EnsureNamespace is a no-op).
func fakeCli(t *testing.T) *k8sclient.Client {
	t.Helper()
	cs := k8sfake.NewClientset()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: yageNamespace},
	}
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("pre-create %s namespace: %v", yageNamespace, err)
	}
	return &k8sclient.Client{Typed: cs}
}

// minCfg returns a minimal config with required repo fields populated.
// Storage-class and image-mirror fields are zeroed so tests that check
// defaults are not polluted by environment variables on the test host.
func minCfg() *config.Config {
	cfg := config.Load()
	// Override repo fields to known values for assertions.
	cfg.TofuRepo = "https://github.com/lpasquali/yage-tofu"
	cfg.TofuRef = "v1.2.3"
	cfg.ManifestsRepo = "https://github.com/lpasquali/yage-manifests"
	cfg.ManifestsRef = "v0.5.0"
	cfg.ReposPVCSize = "500Mi"
	cfg.ImageRegistryMirror = ""
	// Clear storage-class overrides so repoSyncStorageClass returns "standard".
	cfg.CSI.DefaultClass = ""
	cfg.Providers.Proxmox.CSIStorageClassName = ""
	return cfg
}

// --- reposPVCSize tests ---

func TestReposPVCSizeDefault(t *testing.T) {
	cfg := config.Load()
	cfg.ReposPVCSize = ""
	if got := reposPVCSize(cfg); got != "500Mi" {
		t.Errorf("reposPVCSize: got %q, want %q", got, "500Mi")
	}
}

func TestReposPVCSizeFromConfig(t *testing.T) {
	cfg := config.Load()
	cfg.ReposPVCSize = "2Gi"
	if got := reposPVCSize(cfg); got != "2Gi" {
		t.Errorf("reposPVCSize: got %q, want %q", got, "2Gi")
	}
}

// --- repoSyncStorageClass tests ---

func TestRepoSyncStorageClassDefault(t *testing.T) {
	cfg := config.Load()
	cfg.CSI.DefaultClass = ""
	cfg.Providers.Proxmox.CSIStorageClassName = ""
	if got := repoSyncStorageClass(cfg); got != "standard" {
		t.Errorf("repoSyncStorageClass: got %q, want %q", got, "standard")
	}
}

func TestRepoSyncStorageClassCSIDefault(t *testing.T) {
	cfg := config.Load()
	cfg.CSI.DefaultClass = "fast-ssd"
	cfg.Providers.Proxmox.CSIStorageClassName = "proxmox-csi"
	if got := repoSyncStorageClass(cfg); got != "fast-ssd" {
		t.Errorf("repoSyncStorageClass: CSI default should win, got %q", got)
	}
}

func TestRepoSyncStorageClassProxmoxFallback(t *testing.T) {
	cfg := config.Load()
	cfg.CSI.DefaultClass = ""
	cfg.Providers.Proxmox.CSIStorageClassName = "proxmox-csi"
	if got := repoSyncStorageClass(cfg); got != "proxmox-csi" {
		t.Errorf("repoSyncStorageClass: Proxmox fallback, got %q, want %q", got, "proxmox-csi")
	}
}

// --- repoSyncImageRef tests ---

func TestRepoSyncImageRefNoMirror(t *testing.T) {
	cfg := config.Load()
	cfg.ImageRegistryMirror = ""
	if got := repoSyncImageRef(cfg, "alpine/git:2"); got != "alpine/git:2" {
		t.Errorf("repoSyncImageRef (no mirror): got %q, want %q", got, "alpine/git:2")
	}
}

func TestRepoSyncImageRefWithMirror(t *testing.T) {
	cfg := config.Load()
	cfg.ImageRegistryMirror = "harbor.internal/mirror"
	tests := []struct {
		image string
		want  string
	}{
		{"alpine/git:2", "harbor.internal/mirror/alpine/git:2"},
		{"busybox:stable", "harbor.internal/mirror/busybox:stable"},
	}
	for _, tt := range tests {
		got := repoSyncImageRef(cfg, tt.image)
		if got != tt.want {
			t.Errorf("repoSyncImageRef(%q): got %q, want %q", tt.image, got, tt.want)
		}
	}
}

// --- ensureReposPVC tests ---

func TestEnsureReposPVCCreated(t *testing.T) {
	cli := fakeCli(t)
	cfg := minCfg()
	ctx := context.Background()

	if err := ensureReposPVC(ctx, cli, cfg, yageNamespace); err != nil {
		t.Fatalf("ensureReposPVC: %v", err)
	}

	pvc, err := cli.Typed.CoreV1().PersistentVolumeClaims(yageNamespace).
		Get(ctx, reposPVCName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get PVC: %v", err)
	}

	// Check name and namespace.
	if pvc.Name != reposPVCName {
		t.Errorf("PVC name: got %q, want %q", pvc.Name, reposPVCName)
	}

	// Check managed-by label.
	if pvc.Labels["app.kubernetes.io/managed-by"] != "yage" {
		t.Errorf("PVC label managed-by: %v", pvc.Labels)
	}

	// Check access mode.
	if len(pvc.Spec.AccessModes) != 1 || pvc.Spec.AccessModes[0] != corev1.ReadWriteOnce {
		t.Errorf("PVC access mode: %v", pvc.Spec.AccessModes)
	}

	// Check storage size.
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.String() != "500Mi" {
		t.Errorf("PVC storage: got %q, want %q", storage.String(), "500Mi")
	}

	// Check storage class (default = "standard").
	if pvc.Spec.StorageClassName == nil {
		t.Errorf("PVC storageClassName: got nil, want standard")
	} else if *pvc.Spec.StorageClassName != "standard" {
		t.Errorf("PVC storageClassName: got %q, want standard", *pvc.Spec.StorageClassName)
	}
}

func TestEnsureReposPVCCustomSize(t *testing.T) {
	cli := fakeCli(t)
	cfg := minCfg()
	cfg.ReposPVCSize = "2Gi"
	ctx := context.Background()

	if err := ensureReposPVC(ctx, cli, cfg, yageNamespace); err != nil {
		t.Fatalf("ensureReposPVC: %v", err)
	}
	pvc, err := cli.Typed.CoreV1().PersistentVolumeClaims(yageNamespace).
		Get(ctx, reposPVCName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get PVC: %v", err)
	}
	storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if storage.String() != "2Gi" {
		t.Errorf("PVC storage: got %q, want %q", storage.String(), "2Gi")
	}
}

// --- submitRepoSyncJob tests ---

func TestSubmitRepoSyncJobCreated(t *testing.T) {
	cli := fakeCli(t)
	cfg := minCfg()
	ctx := context.Background()

	if err := submitRepoSyncJob(ctx, cli, cfg, yageNamespace); err != nil {
		t.Fatalf("submitRepoSyncJob: %v", err)
	}

	job, err := cli.Typed.BatchV1().Jobs(yageNamespace).
		Get(ctx, repoSyncJobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Job: %v", err)
	}

	// Check job name.
	if job.Name != repoSyncJobName {
		t.Errorf("Job name: got %q, want %q", job.Name, repoSyncJobName)
	}

	// Check service account.
	if job.Spec.Template.Spec.ServiceAccountName != "yage-job-runner" {
		t.Errorf("ServiceAccountName: got %q, want yage-job-runner",
			job.Spec.Template.Spec.ServiceAccountName)
	}

	// Check restart policy.
	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("RestartPolicy: got %q, want Never", job.Spec.Template.Spec.RestartPolicy)
	}

	// Check exactly 2 init containers.
	initContainers := job.Spec.Template.Spec.InitContainers
	if len(initContainers) != 2 {
		t.Fatalf("InitContainers count: got %d, want 2", len(initContainers))
	}

	// First init container: sync-yage-tofu.
	ic0 := initContainers[0]
	if ic0.Name != "sync-yage-tofu" {
		t.Errorf("InitContainer[0].Name: got %q, want sync-yage-tofu", ic0.Name)
	}
	if ic0.Image != "alpine/git:2" {
		t.Errorf("InitContainer[0].Image: got %q, want alpine/git:2", ic0.Image)
	}
	assertEnvVar(t, ic0.Env, "REPO", cfg.TofuRepo)
	assertEnvVar(t, ic0.Env, "REF", cfg.TofuRef)

	// Second init container: sync-yage-manifests.
	ic1 := initContainers[1]
	if ic1.Name != "sync-yage-manifests" {
		t.Errorf("InitContainer[1].Name: got %q, want sync-yage-manifests", ic1.Name)
	}
	if ic1.Image != "alpine/git:2" {
		t.Errorf("InitContainer[1].Image: got %q, want alpine/git:2", ic1.Image)
	}
	assertEnvVar(t, ic1.Env, "REPO", cfg.ManifestsRepo)
	assertEnvVar(t, ic1.Env, "REF", cfg.ManifestsRef)

	// Check the main "done" container.
	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("Containers count: got %d, want 1", len(containers))
	}
	if containers[0].Name != "done" {
		t.Errorf("Container[0].Name: got %q, want done", containers[0].Name)
	}
	if containers[0].Image != "busybox:stable" {
		t.Errorf("Container[0].Image: got %q, want busybox:stable", containers[0].Image)
	}

	// Check the repos volume references the correct PVC.
	volumes := job.Spec.Template.Spec.Volumes
	if len(volumes) != 1 || volumes[0].Name != "repos" {
		t.Fatalf("Volumes: got %+v, want single repos volume", volumes)
	}
	if volumes[0].PersistentVolumeClaim == nil ||
		volumes[0].PersistentVolumeClaim.ClaimName != reposPVCName {
		t.Errorf("Volume repos PVC claim: got %v, want %q",
			volumes[0].PersistentVolumeClaim, reposPVCName)
	}

	// Check TTL is set to 0.
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 0 {
		t.Errorf("TTLSecondsAfterFinished: got %v, want 0", job.Spec.TTLSecondsAfterFinished)
	}
}

func TestSubmitRepoSyncJobWithMirror(t *testing.T) {
	cli := fakeCli(t)
	cfg := minCfg()
	cfg.ImageRegistryMirror = "registry.internal"
	ctx := context.Background()

	if err := submitRepoSyncJob(ctx, cli, cfg, yageNamespace); err != nil {
		t.Fatalf("submitRepoSyncJob: %v", err)
	}

	job, err := cli.Typed.BatchV1().Jobs(yageNamespace).
		Get(ctx, repoSyncJobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Job: %v", err)
	}

	initContainers := job.Spec.Template.Spec.InitContainers
	if len(initContainers) < 2 {
		t.Fatalf("expected 2 init containers, got %d", len(initContainers))
	}

	wantGitImage := "registry.internal/alpine/git:2"
	for i, ic := range initContainers {
		if ic.Image != wantGitImage {
			t.Errorf("InitContainer[%d].Image: got %q, want %q", i, ic.Image, wantGitImage)
		}
	}

	wantBusybox := "registry.internal/busybox:stable"
	containers := job.Spec.Template.Spec.Containers
	if len(containers) > 0 && containers[0].Image != wantBusybox {
		t.Errorf("Container[0].Image: got %q, want %q", containers[0].Image, wantBusybox)
	}
}

func TestSubmitRepoSyncJobNoMirror(t *testing.T) {
	cli := fakeCli(t)
	cfg := minCfg()
	cfg.ImageRegistryMirror = ""
	ctx := context.Background()

	if err := submitRepoSyncJob(ctx, cli, cfg, yageNamespace); err != nil {
		t.Fatalf("submitRepoSyncJob: %v", err)
	}
	job, err := cli.Typed.BatchV1().Jobs(yageNamespace).
		Get(ctx, repoSyncJobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Job: %v", err)
	}
	// Without mirror, images should be plain Docker Hub references.
	for i, ic := range job.Spec.Template.Spec.InitContainers {
		if ic.Image != "alpine/git:2" {
			t.Errorf("InitContainer[%d].Image (no mirror): got %q, want alpine/git:2", i, ic.Image)
		}
	}
	if len(job.Spec.Template.Spec.Containers) > 0 {
		if job.Spec.Template.Spec.Containers[0].Image != "busybox:stable" {
			t.Errorf("Container[0].Image (no mirror): got %q, want busybox:stable",
				job.Spec.Template.Spec.Containers[0].Image)
		}
	}
}

// assertEnvVar asserts that the named env var exists with the expected value.
func assertEnvVar(t *testing.T, envVars []corev1.EnvVar, name, want string) {
	t.Helper()
	for _, ev := range envVars {
		if ev.Name == name {
			if ev.Value != want {
				t.Errorf("env var %q: got %q, want %q", name, ev.Value, want)
			}
			return
		}
	}
	t.Errorf("env var %q not found in %+v", name, envVars)
}
