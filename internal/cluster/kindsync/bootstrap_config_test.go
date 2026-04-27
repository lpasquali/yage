// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

// TestKindSyncConfigRoundTrip exercises the config.yaml round-trip that
// WriteBootstrapConfigSecret and MergeBootstrapConfigFromKind implement:
//
//  1. WriteBootstrapConfigSecret stores cfg.SnapshotYAML() as the
//     "config.yaml" data key of the yage-system/bootstrap-config Secret.
//  2. MergeBootstrapConfigFromKind reads that key back, calls
//     parseFlatYAMLOrJSON + migrateLegacyKeys, then cfg.ApplySnapshotKV.
//
// This test exercises the pure data-transformation layer (no live k8s
// cluster required) by calling the same internal functions directly.

import (
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestKindSyncConfigRoundTrip(t *testing.T) {
	// Build a source config with distinct, non-empty values that span
	// the snapshot schema: string fields, bool fields, EXPLICIT-guarded
	// fields, and nested provider fields.
	src := config.Load()
	src.KindClusterName = "test-kind"
	src.WorkloadClusterName = "my-cluster"
	src.NodeIPRanges = "10.10.10.0/24"
	src.Gateway = "10.10.10.1"
	src.KindVersion = "v0.27.0"
	src.KubectlVersion = "v1.32.0"
	src.Providers.Proxmox.URL = "https://proxmox.example.com"
	src.Providers.Proxmox.Region = "us-west"
	src.Providers.Proxmox.Node = "pve-node-1"
	src.Providers.Proxmox.TemplateID = "101"
	src.Providers.Proxmox.WorkerMemoryMiB = "8192"
	src.Providers.Proxmox.WorkerNumCores = "4"
	src.ArgoCD.Enabled = true
	src.ArgoCD.Version = "v2.14.0"
	src.CertManagerEnabled = true
	src.KyvernoEnabled = false
	src.ClusterSetID = "cluster-abc123"
	src.WorkloadKubernetesVersion = "v1.32.0"

	// Step 1: emit the snapshot YAML — exactly what WriteBootstrapConfigSecret
	// stores as data["config.yaml"] in the kind Secret.
	yaml := src.SnapshotYAML()
	if yaml == "" {
		t.Fatal("SnapshotYAML returned empty string")
	}

	// Spot-check a few expected lines so a regression in SnapshotYAML is
	// caught close to the source, not buried in the diff below.
	for _, want := range []string{
		`KIND_CLUSTER_NAME:`,
		`WORKLOAD_CLUSTER_NAME:`,
		`PROXMOX_URL:`,
		`NODE_IP_RANGES:`,
	} {
		found := false
		for _, line := range splitLines(yaml) {
			if len(line) >= len(want) && line[:len(want)] == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("SnapshotYAML: expected line starting with %q not found in:\n%s", want, yaml)
		}
	}

	// Step 2: parse the YAML — exactly what MergeBootstrapConfigFromKind
	// does with the raw Secret data before calling ApplySnapshotKV.
	kv := parseFlatYAMLOrJSON(yaml)
	migrateLegacyKeys(kv)

	// Step 3: apply onto a fresh config — the merge half of the round-trip.
	dst := config.Load()
	dst.ApplySnapshotKV(kv)

	// Step 4: assert fields are faithfully restored.
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"KindClusterName", dst.KindClusterName, src.KindClusterName},
		{"WorkloadClusterName", dst.WorkloadClusterName, src.WorkloadClusterName},
		{"NodeIPRanges", dst.NodeIPRanges, src.NodeIPRanges},
		{"Gateway", dst.Gateway, src.Gateway},
		{"KindVersion", dst.KindVersion, src.KindVersion},
		{"KubectlVersion", dst.KubectlVersion, src.KubectlVersion},
		{"Providers.Proxmox.URL", dst.Providers.Proxmox.URL, src.Providers.Proxmox.URL},
		{"Providers.Proxmox.Region", dst.Providers.Proxmox.Region, src.Providers.Proxmox.Region},
		{"Providers.Proxmox.Node", dst.Providers.Proxmox.Node, src.Providers.Proxmox.Node},
		{"Providers.Proxmox.TemplateID", dst.Providers.Proxmox.TemplateID, src.Providers.Proxmox.TemplateID},
		{"Providers.Proxmox.WorkerMemoryMiB", dst.Providers.Proxmox.WorkerMemoryMiB, src.Providers.Proxmox.WorkerMemoryMiB},
		{"Providers.Proxmox.WorkerNumCores", dst.Providers.Proxmox.WorkerNumCores, src.Providers.Proxmox.WorkerNumCores},
		{"ArgoCD.Version", dst.ArgoCD.Version, src.ArgoCD.Version},
		{"ClusterSetID", dst.ClusterSetID, src.ClusterSetID},
		{"WorkloadKubernetesVersion", dst.WorkloadKubernetesVersion, src.WorkloadKubernetesVersion},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}

	// Bool fields serialised as "true"/"false".
	if !dst.ArgoCD.Enabled {
		t.Errorf("ArgoCD.Enabled: got false, want true")
	}
	if !dst.CertManagerEnabled {
		t.Errorf("CertManagerEnabled: got false, want true")
	}
	if dst.KyvernoEnabled {
		t.Errorf("KyvernoEnabled: got true, want false")
	}
}

// TestKindSyncExplicitGuardPreservedOnMerge verifies that fields locked by
// *_EXPLICIT flags at the dst side are not overwritten by the Secret value —
// the "CLI wins" contract required by MergeBootstrapConfigFromKind.
func TestKindSyncExplicitGuardPreservedOnMerge(t *testing.T) {
	src := config.Load()
	src.WorkloadClusterName = "secret-name"
	src.KindVersion = "v0.25.0"

	yaml := src.SnapshotYAML()
	kv := parseFlatYAMLOrJSON(yaml)

	dst := config.Load()
	dst.WorkloadClusterName = "cli-name"
	dst.WorkloadClusterNameExplicit = true // simulate --workload-cluster-name on CLI

	dst.ApplySnapshotKV(kv)

	if dst.WorkloadClusterName != "cli-name" {
		t.Errorf("EXPLICIT guard failed: got %q, want %q", dst.WorkloadClusterName, "cli-name")
	}
	if dst.KindVersion != "v0.25.0" {
		t.Errorf("unguarded KindVersion should be applied: got %q", dst.KindVersion)
	}
}

// splitLines is a tiny helper to avoid importing strings in the test body.
func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
