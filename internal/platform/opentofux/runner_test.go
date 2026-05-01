// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
)

// fakeClient builds a k8sclient.Client whose Typed field is backed by the
// given fake objects (no real cluster required).
func fakeClient(objs ...runtime.Object) *k8sclient.Client {
	return &k8sclient.Client{
		Typed: fake.NewClientset(objs...),
	}
}

// --- Runner interface satisfaction tests ---

func TestLocalRunnerImplementsRunner(t *testing.T) {
	var _ Runner = (*LocalRunner)(nil)
}

func TestJobRunnerImplementsRunner(t *testing.T) {
	var _ Runner = (*JobRunner)(nil)
}

// --- LocalRunner unit tests (no tofu binary required) ---

func TestModuleDirFor(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := moduleDirFor("registry")
	want := filepath.Join(home, ".yage", "tofu-registry")
	if got != want {
		t.Errorf("moduleDirFor: got %q, want %q", got, want)
	}
}

func TestLocalRunnerDestroyNoopWhenMissing(t *testing.T) {
	// Override home so we don't accidentally touch the real ~/.yage.
	t.Setenv("HOME", t.TempDir())
	r := NewLocalRunner(nil)
	// Destroy on a non-existent module dir must return nil (no-op).
	if err := r.Destroy(context.Background(), "nonexistent-module-99"); err != nil {
		t.Fatalf("Destroy on missing dir returned error: %v", err)
	}
}

// --- Fetcher unit tests ---

func TestFetcherModulePathDefault(t *testing.T) {
	f := &Fetcher{}
	got := f.ModulePath("proxmox")
	want := "/repos/yage-tofu/proxmox"
	if got != want {
		t.Errorf("ModulePath (default root): got %q, want %q", got, want)
	}
}

func TestFetcherModulePathCustomRoot(t *testing.T) {
	f := &Fetcher{MountRoot: "/mnt/repos"}
	got := f.ModulePath("registry")
	want := "/mnt/repos/yage-tofu/registry"
	if got != want {
		t.Errorf("ModulePath (custom root): got %q, want %q", got, want)
	}
}

// --- JobRunner unit tests (fake client, no cluster) ---

func TestJobRunnerTofuImageRef(t *testing.T) {
	tests := []struct {
		name   string
		mirror string
		want   string
	}{
		{"no-mirror", "", "ghcr.io/opentofu/opentofu:latest"},
		{"with-mirror", "harbor.internal/yage-mirror", "harbor.internal/yage-mirror/opentofu/opentofu:latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Load()
			cfg.ImageRegistryMirror = tt.mirror
			j := &JobRunner{cfg: cfg, client: fakeClient()}
			if got := j.tofuImageRef(); got != tt.want {
				t.Errorf("tofuImageRef: got %q, want %q", got, tt.want)
			}
		})
	}
}


func TestJobRunnerBuildCommand(t *testing.T) {
	j := &JobRunner{cfg: config.Load(), client: fakeClient()}
	tests := []struct {
		op      string
		contain string
	}{
		{"apply", "apply -auto-approve"},
		{"destroy", "destroy -auto-approve"},
		{"output", "output -json"},
	}
	for _, tt := range tests {
		cmd := j.buildCommand("proxmox", tt.op)
		if !containsStr(cmd, tt.contain) {
			t.Errorf("buildCommand(%q): %q not found in %q", tt.op, tt.contain, cmd)
		}
	}
}

func TestJobRunnerBuildCommandNoStateFlag(t *testing.T) {
	j := &JobRunner{cfg: config.Load(), client: fakeClient()}
	for _, op := range []string{"apply", "destroy", "output"} {
		cmd := j.buildCommand("proxmox", op)
		if containsStr(cmd, "-state=") {
			t.Errorf("buildCommand(%q): unexpected -state= flag in %q (state is kept in kubernetes backend)", op, cmd)
		}
	}
}

func TestJobRunnerBuildCommandInClusterBackend(t *testing.T) {
	j := &JobRunner{cfg: config.Load(), client: fakeClient()}
	// apply and destroy must pass in_cluster_config to tofu init.
	for _, op := range []string{"apply", "destroy"} {
		cmd := j.buildCommand("proxmox", op)
		if !containsStr(cmd, "in_cluster_config=true") {
			t.Errorf("buildCommand(%q): -backend-config=in_cluster_config=true not found in %q", op, cmd)
		}
	}
}


func TestEnsureCredsSecretEncodesTFVars(t *testing.T) {
	cfg := config.Load()
	cl := fakeClient()
	j := &JobRunner{cfg: cfg, client: cl}
	ctx := context.Background()

	vars := map[string]string{
		"my_token":  "s3cr3t",
		"my_region": "eu-west-1",
	}
	if err := j.ensureCredsSecret(ctx, "yage-system", "tofu-creds-test", vars); err != nil {
		t.Fatalf("ensureCredsSecret: %v", err)
	}

	secret, err := cl.Typed.CoreV1().Secrets("yage-system").Get(ctx, "tofu-creds-test", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	for _, k := range []string{"TF_VAR_my_token", "TF_VAR_my_region"} {
		if _, ok := secret.Data[k]; !ok {
			t.Errorf("secret missing key %q; data keys: %v", k, secretDataKeys(secret))
		}
	}
}

func TestEnsureCredsSecretUpdate(t *testing.T) {
	cfg := config.Load()
	cl := fakeClient()
	j := &JobRunner{cfg: cfg, client: cl}
	ctx := context.Background()

	// Create initial secret.
	if err := j.ensureCredsSecret(ctx, "yage-system", "tofu-creds-update", map[string]string{"key": "v1"}); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	// Update it.
	if err := j.ensureCredsSecret(ctx, "yage-system", "tofu-creds-update", map[string]string{"key": "v2"}); err != nil {
		t.Fatalf("update secret: %v", err)
	}
	secret, err := cl.Typed.CoreV1().Secrets("yage-system").Get(ctx, "tofu-creds-update", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret: %v", err)
	}
	if got := string(secret.Data["TF_VAR_key"]); got != "v2" {
		t.Errorf("updated secret data: got %q, want %q", got, "v2")
	}
}

func TestYageLabels(t *testing.T) {
	labels := yageLabels()
	if labels["app.kubernetes.io/managed-by"] != "yage" {
		t.Errorf("labels missing managed-by=yage: %v", labels)
	}
	if labels["app.kubernetes.io/component"] != "tofu-runner" {
		t.Errorf("labels missing component=tofu-runner: %v", labels)
	}
}

func TestEnsureModuleConfigMapWithRealDir(t *testing.T) {
	// Create a temp dir as a fake yage-repos PVC mount (mimics /repos inside a Job pod).
	// After the ADR 0010 refactor, Fetcher.ModulePath resolves to <MountRoot>/yage-tofu/<module>.
	// We pass a custom MountRoot via the fetcher field on JobRunner so the test is hermetic.
	tempMount := t.TempDir()

	// Simulate the PVC layout: <tempMount>/yage-tofu/mymod/
	modDir := filepath.Join(tempMount, "yage-tofu", "mymod")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "main.tf"), []byte(`resource "null_resource" "x" {}`), 0o644); err != nil {
		t.Fatalf("write main.tf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "variables.tf"), []byte(`variable "x" {}`), 0o644); err != nil {
		t.Fatalf("write variables.tf: %v", err)
	}
	// Non-.tf file — should be excluded.
	if err := os.WriteFile(filepath.Join(modDir, "README.md"), []byte("# readme"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}

	cfg := config.Load()
	cl := fakeClient()
	j := &JobRunner{cfg: cfg, client: cl, fetcher: &Fetcher{MountRoot: tempMount}}
	ctx := context.Background()

	if err := j.ensureModuleConfigMap(ctx, "yage-system", "tofu-module-mymod", "mymod"); err != nil {
		t.Fatalf("ensureModuleConfigMap: %v", err)
	}
	cm, err := cl.Typed.CoreV1().ConfigMaps("yage-system").Get(ctx, "tofu-module-mymod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if _, ok := cm.Data["main.tf"]; !ok {
		t.Error("configmap missing main.tf")
	}
	if _, ok := cm.Data["variables.tf"]; !ok {
		t.Error("configmap missing variables.tf")
	}
	if _, ok := cm.Data["README.md"]; ok {
		t.Error("configmap should not contain README.md")
	}
}

func TestBuildJobSpec(t *testing.T) {
	cfg := config.Load()
	cfg.ImageRegistryMirror = ""
	j := &JobRunner{cfg: cfg, client: fakeClient()}

	job := j.buildJob("yage-system", "tofu-proxmox-apply", "tofu-module-proxmox", "tofu-creds-proxmox", "proxmox", "apply")

	if job.Name != "tofu-proxmox-apply" {
		t.Errorf("job name: %q", job.Name)
	}
	if job.Namespace != "yage-system" {
		t.Errorf("job namespace: %q", job.Namespace)
	}
	podSpec := job.Spec.Template.Spec
	// ServiceAccountName must be yage-job-runner for kubernetes backend RBAC.
	if podSpec.ServiceAccountName != "yage-job-runner" {
		t.Errorf("ServiceAccountName: got %q, want %q", podSpec.ServiceAccountName, "yage-job-runner")
	}
	containers := podSpec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	if containers[0].Image != "ghcr.io/opentofu/opentofu:latest" {
		t.Errorf("container image: %q", containers[0].Image)
	}
	// Check envFrom references the creds secret.
	if len(containers[0].EnvFrom) != 1 || containers[0].EnvFrom[0].SecretRef.Name != "tofu-creds-proxmox" {
		t.Errorf("envFrom: %+v", containers[0].EnvFrom)
	}
	// KUBE_NAMESPACE must be set so the kubernetes backend targets yage-system.
	// TF_DATA_DIR must redirect tofu's plugin cache off the read-only ConfigMap mount.
	wantEnvs := map[string]string{
		"KUBE_NAMESPACE": "yage-system",
		"TF_DATA_DIR":    "/tmp/.terraform",
	}
	for _, e := range containers[0].Env {
		delete(wantEnvs, e.Name)
	}
	if len(wantEnvs) > 0 {
		t.Errorf("container missing required env vars: %v; got: %+v", wantEnvs, containers[0].Env)
	}
	// No state volume — state lives in a Kubernetes Secret via the backend.
	for _, vol := range podSpec.Volumes {
		if vol.Name == "state" {
			t.Errorf("unexpected 'state' volume in pod spec (state is in kubernetes backend Secret)")
		}
	}
	for _, mount := range containers[0].VolumeMounts {
		if mount.Name == "state" {
			t.Errorf("unexpected 'state' volume mount in container")
		}
	}
}

func TestJobRunnerEnsureCredsSecretWithNamespace(t *testing.T) {
	cfg := config.Load()

	// Pre-create the namespace so EnsureNamespace won't fail with the fake client.
	cl := fakeClient(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "yage-system"},
	})
	j := &JobRunner{cfg: cfg, client: cl}

	// Verify that ensureCredsSecret works when the namespace already exists.
	ctx := context.Background()
	if err := j.ensureCredsSecret(ctx, "yage-system", "tofu-creds-nstest", map[string]string{"key": "val"}); err != nil {
		t.Fatalf("ensureCredsSecret with existing namespace: %v", err)
	}
}

// --- helper functions ---

func containsStr(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		(haystack == needle || len(haystack) > 0 && strings.Contains(haystack, needle))
}

func secretDataKeys(s *corev1.Secret) []string {
	keys := make([]string, 0, len(s.Data))
	for k := range s.Data {
		keys = append(keys, k)
	}
	return keys
}
