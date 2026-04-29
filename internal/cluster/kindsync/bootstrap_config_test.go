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
	"strings"
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

// TestBootstrapConfigSecretName verifies that the Secret name is always
// "bootstrap-config" — the namespace is the discriminator.
func TestBootstrapConfigSecretName(t *testing.T) {
	cfg := config.Load()
	for _, name := range []string{"dev", "prod-eu-low-cost", "scenario-aws-vs-hetzner", ""} {
		cfg.ConfigName = name
		if got := BootstrapConfigSecretName(cfg); got != "bootstrap-config" {
			t.Errorf("ConfigName=%q: got %q, want %q", name, got, "bootstrap-config")
		}
	}
}

// TestBootstrapConfigNamespace verifies the per-config namespace naming.
func TestBootstrapConfigNamespace(t *testing.T) {
	cfg := config.Load()
	cases := []struct{ configName, want string }{
		{"dev", "yage-dev"},
		{"prod-eu-low-cost", "yage-prod-eu-low-cost"},
		{"scenario-aws-vs-hetzner", "yage-scenario-aws-vs-hetzner"},
		{"", "yage-default"},
	}
	for _, c := range cases {
		cfg.ConfigName = c.configName
		if got := BootstrapConfigNamespace(cfg); got != c.want {
			t.Errorf("ConfigName=%q: got %q, want %q", c.configName, got, c.want)
		}
	}
}

// TestSanitizeLabelValue verifies the K8s label charset sanitization.
func TestSanitizeLabelValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"aws", "aws"},
		{"us-east-1", "us-east-1"},
		{"prod-eu-low-cost", "prod-eu-low-cost"},
		{"PROD", "prod"},
		{"my cluster", "my-cluster"},
		{"--leading--", "leading"},
		{"trailing--", "trailing"},
		{"a_b", "a-b"},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeLabelValue(c.in); got != c.want {
			t.Errorf("sanitizeLabelValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Values longer than 63 chars are truncated without trailing hyphen.
	long := "a" + strings.Repeat("-", 62) + "b"
	if got := sanitizeLabelValue(long); len(got) > 63 {
		t.Errorf("long label: len=%d > 63", len(got))
	}
}

// TestBootstrapLabelsCase1 verifies that two configs with distinct WorkloadClusterNames
// live in distinct namespaces (yage-<name>) with the correct Secret labels.
func TestBootstrapLabelsCase1(t *testing.T) {
	for _, wl := range []string{"dev", "staging", "prod"} {
		cfg := config.Load()
		cfg.WorkloadClusterName = wl
		cfg.ConfigName = wl // default rule applied in Load()
		cfg.InfraProvider = "aws"

		// Secret is always "bootstrap-config"; namespace is the discriminator.
		if got := BootstrapConfigSecretName(cfg); got != "bootstrap-config" {
			t.Errorf("case1 %s: Secret name %q, want %q", wl, got, "bootstrap-config")
		}
		if got := BootstrapConfigNamespace(cfg); got != "yage-"+wl {
			t.Errorf("case1 %s: namespace %q, want %q", wl, got, "yage-"+wl)
		}

		lbl := bootstrapLabels(cfg, "draft")
		if lbl["yage.io/config-name"] != wl {
			t.Errorf("case1 %s: config-name label %q", wl, lbl["yage.io/config-name"])
		}
		if lbl["yage.io/workload-cluster"] != wl {
			t.Errorf("case1 %s: workload-cluster label %q", wl, lbl["yage.io/workload-cluster"])
		}
		if lbl["yage.io/config-status"] != "draft" {
			t.Errorf("case1 %s: config-status label %q", wl, lbl["yage.io/config-status"])
		}
		if lbl["yage.io/provider"] != "aws" {
			t.Errorf("case1 %s: provider label %q", wl, lbl["yage.io/provider"])
		}

		// Namespace labels carry the discovery marker + provider.
		nsLbl := configNamespaceLabels(cfg)
		if nsLbl["infra.yage-deployment.bucaniere.us"] != "true" {
			t.Errorf("case1 %s: missing discovery label", wl)
		}
		if nsLbl["infra.capi-provider.bucaniere.us"] != "aws" {
			t.Errorf("case1 %s: provider ns-label %q", wl, nsLbl["infra.capi-provider.bucaniere.us"])
		}
	}
}

// TestBootstrapLabelsCase2 verifies two configs for the same workload cluster
// (different ConfigName) live in distinct namespaces, both carrying workload-cluster=prod.
func TestBootstrapLabelsCase2(t *testing.T) {
	for _, configName := range []string{"prod", "prod-eu-low-cost"} {
		cfg := config.Load()
		cfg.WorkloadClusterName = "prod"
		cfg.ConfigName = configName
		cfg.InfraProvider = "azure"

		// Secret name is always "bootstrap-config"; namespace differs.
		if got := BootstrapConfigSecretName(cfg); got != "bootstrap-config" {
			t.Errorf("case2 %s: Secret name %q, want bootstrap-config", configName, got)
		}
		if got := BootstrapConfigNamespace(cfg); got != "yage-"+configName {
			t.Errorf("case2 %s: namespace %q, want %q", configName, got, "yage-"+configName)
		}
		lbl := bootstrapLabels(cfg, "draft")
		if lbl["yage.io/workload-cluster"] != "prod" {
			t.Errorf("case2 %s: workload-cluster %q", configName, lbl["yage.io/workload-cluster"])
		}
		if lbl["yage.io/config-name"] != configName {
			t.Errorf("case2 %s: config-name %q", configName, lbl["yage.io/config-name"])
		}
	}
}

// TestBootstrapLabelsCase3 verifies a draft scenario config has status="draft"
// in its labels, and the namespace discovery label is present but provider label absent.
func TestBootstrapLabelsCase3(t *testing.T) {
	cfg := config.Load()
	cfg.ConfigName = "scenario-aws-vs-hetzner"
	cfg.WorkloadClusterName = ""
	cfg.InfraProvider = ""

	lbl := bootstrapLabels(cfg, "draft")
	if lbl["yage.io/config-status"] != "draft" {
		t.Errorf("case3: config-status %q", lbl["yage.io/config-status"])
	}
	if _, ok := lbl["yage.io/workload-cluster"]; ok {
		t.Errorf("case3: workload-cluster label should be absent when WorkloadClusterName is empty")
	}
	if _, ok := lbl["yage.io/provider"]; ok {
		t.Errorf("case3: provider label should be absent when InfraProvider is empty")
	}

	// Namespace labels: discovery marker always present, provider absent when empty.
	nsLbl := configNamespaceLabels(cfg)
	if nsLbl["infra.yage-deployment.bucaniere.us"] != "true" {
		t.Errorf("case3: missing discovery label on namespace")
	}
	if _, ok := nsLbl["infra.capi-provider.bucaniere.us"]; ok {
		t.Errorf("case3: provider ns-label should be absent when InfraProvider is empty")
	}
}

// TestBootstrapLabelsPromoted verifies the realized status string.
func TestBootstrapLabelsPromoted(t *testing.T) {
	cfg := config.Load()
	cfg.ConfigName = "dev"
	lbl := bootstrapLabels(cfg, "realized")
	if lbl["yage.io/config-status"] != "realized" {
		t.Errorf("realized label: got %q", lbl["yage.io/config-status"])
	}
}

// TestConfigNameRoundTripSnapshot verifies YAGE_CONFIG_NAME round-trips
// through SnapshotYAML → ApplySnapshotKV correctly, and that the explicit
// guard prevents overwrite.
func TestConfigNameRoundTripSnapshot(t *testing.T) {
	src := config.Load()
	src.ConfigName = "prod-eu-low-cost"
	src.ConfigNameExplicit = false // not explicit, so snapshot can restore it

	yaml := src.SnapshotYAML()
	kv := parseFlatYAMLOrJSON(yaml)

	dst := config.Load()
	dst.ApplySnapshotKV(kv)

	if dst.ConfigName != "prod-eu-low-cost" {
		t.Errorf("ConfigName round-trip: got %q, want %q", dst.ConfigName, "prod-eu-low-cost")
	}

	// With explicit guard set, the CLI-provided value wins.
	dst2 := config.Load()
	dst2.ConfigName = "my-override"
	dst2.ConfigNameExplicit = true
	dst2.ApplySnapshotKV(kv)
	if dst2.ConfigName != "my-override" {
		t.Errorf("ConfigName explicit guard: got %q, want %q", dst2.ConfigName, "my-override")
	}
}

// TestPickBootstrapConfigNonTTY verifies that pickBootstrapConfig auto-picks
// the first candidate when stdin is not a TTY (the expected state in CI/tests).
func TestPickBootstrapConfigNonTTY(t *testing.T) {
	candidates := []BootstrapCandidate{
		{KindCluster: "k1", ConfigName: "prod", Workload: "prod", Status: "realized", Provider: "aws"},
		{KindCluster: "k1", ConfigName: "prod-eu-low-cost", Workload: "prod", Status: "draft", Provider: "azure"},
		{KindCluster: "k2", ConfigName: "dev", Workload: "dev", Status: "draft", Provider: "gcp"},
	}
	// In test environments stdin is not a TTY, so pickBootstrapConfig should
	// return the first candidate without prompting.
	if isTTY() {
		t.Skip("skipping non-TTY test when stdin is a TTY (interactive terminal)")
	}
	c := pickBootstrapConfig(candidates, "test picker")
	if c == nil {
		t.Fatal("pickBootstrapConfig returned nil")
	}
	if c.ConfigName != "prod" {
		t.Errorf("non-TTY auto-pick: got %q, want %q", c.ConfigName, "prod")
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
