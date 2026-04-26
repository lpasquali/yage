// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package orchestrator

import (
	"reflect"
	"testing"
)

func TestApplyImageMirror(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		args   []string
		mirror string
		want   []string
	}{
		{
			name:   "empty mirror returns args unchanged",
			args:   []string{"clusterctl", "init", "--core", "registry.k8s.io/cluster-api/cluster-api-controller:v1.13.0"},
			mirror: "",
			want:   []string{"clusterctl", "init", "--core", "registry.k8s.io/cluster-api/cluster-api-controller:v1.13.0"},
		},
		{
			name:   "rewrites --core registry.k8s.io image",
			args:   []string{"--core", "registry.k8s.io/foo/bar:v1"},
			mirror: "harbor.internal",
			want:   []string{"--core", "harbor.internal/foo/bar:v1"},
		},
		{
			name:   "rewrites --bootstrap, --control-plane, --infrastructure image refs",
			args:   []string{
				"--core", "registry.k8s.io/cluster-api/cluster-api-controller:v1.13.0",
				"--bootstrap", "registry.k8s.io/cluster-api/kubeadm-bootstrap-controller:v1.13.0",
				"--control-plane", "registry.k8s.io/cluster-api/kubeadm-control-plane-controller:v1.13.0",
				"--infrastructure", "ghcr.io/k8s-sigs/cluster-api-provider-proxmox:v0.8.1",
			},
			mirror: "harbor.internal/yage-mirror",
			want: []string{
				"--core", "harbor.internal/yage-mirror/cluster-api/cluster-api-controller:v1.13.0",
				"--bootstrap", "harbor.internal/yage-mirror/cluster-api/kubeadm-bootstrap-controller:v1.13.0",
				"--control-plane", "harbor.internal/yage-mirror/cluster-api/kubeadm-control-plane-controller:v1.13.0",
				"--infrastructure", "harbor.internal/yage-mirror/k8s-sigs/cluster-api-provider-proxmox:v0.8.1",
			},
		},
		{
			name:   "rewrites quay.io prefix",
			args:   []string{"--bootstrap", "quay.io/jetstack/cert-manager:v1.0.0"},
			mirror: "harbor.internal/m",
			want:   []string{"--bootstrap", "harbor.internal/m/jetstack/cert-manager:v1.0.0"},
		},
		{
			name:   "trims trailing slash on mirror",
			args:   []string{"--core", "registry.k8s.io/foo:v1"},
			mirror: "harbor.internal/m/",
			want:   []string{"--core", "harbor.internal/m/foo:v1"},
		},
		{
			name:   "non-image args left unchanged",
			args:   []string{"clusterctl", "init", "--config", "/tmp/clusterctl.yaml", "--ipam", "in-cluster", "--addon", "helm"},
			mirror: "harbor.internal",
			want:   []string{"clusterctl", "init", "--config", "/tmp/clusterctl.yaml", "--ipam", "in-cluster", "--addon", "helm"},
		},
		{
			name:   "bare --infrastructure proxmox provider name left alone",
			args:   []string{"--infrastructure", "proxmox", "--ipam", "in-cluster"},
			mirror: "harbor.internal",
			want:   []string{"--infrastructure", "proxmox", "--ipam", "in-cluster"},
		},
		{
			name:   "bare --bootstrap k3s and --control-plane k3s left alone",
			args:   []string{"--control-plane", "k3s", "--bootstrap", "k3s"},
			mirror: "harbor.internal",
			want:   []string{"--control-plane", "k3s", "--bootstrap", "k3s"},
		},
		{
			name:   "image-bearing flag at end of args (no value) is a no-op",
			args:   []string{"clusterctl", "init", "--core"},
			mirror: "harbor.internal",
			want:   []string{"clusterctl", "init", "--core"},
		},
		{
			name:   "value not matching any known registry prefix is left as-is",
			args:   []string{"--infrastructure", "docker.io/library/foo:v1"},
			mirror: "harbor.internal",
			want:   []string{"--infrastructure", "docker.io/library/foo:v1"},
		},
		{
			name:   "nil/empty args slice round-trips",
			args:   nil,
			mirror: "harbor.internal",
			want:   []string{},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := applyImageMirror(tc.args, tc.mirror)
			// Special case: nil input should produce something that
			// reflects the empty-slice contract — we accept either nil
			// or len==0 to keep the helper non-allocating-friendly.
			if tc.args == nil {
				if len(got) != 0 {
					t.Fatalf("nil args: got %v, want empty slice", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("applyImageMirror(%v, %q):\n  got:  %v\n  want: %v", tc.args, tc.mirror, got, tc.want)
			}
		})
	}
}

// TestApplyImageMirror_DoesNotMutateInput guards the contract that the
// helper returns a fresh slice and leaves the caller's argv alone.
// Mutating bootstrap.go's initArgs in place would surprise callers that
// keep a reference for logging.
func TestApplyImageMirror_DoesNotMutateInput(t *testing.T) {
	t.Parallel()
	in := []string{"--core", "registry.k8s.io/foo:v1"}
	orig := []string{"--core", "registry.k8s.io/foo:v1"}
	_ = applyImageMirror(in, "harbor.internal")
	if !reflect.DeepEqual(in, orig) {
		t.Fatalf("input slice was mutated: got %v, want %v", in, orig)
	}
}