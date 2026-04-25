package config

import (
	"strings"
	"testing"
)

// TestSnapshotRoundtrip proves that SnapshotYAML → parse → ApplySnapshotKV
// restores the same field values on a fresh Config.
func TestSnapshotRoundtrip(t *testing.T) {
	src := Load()
	// Mutate a mix of string + bool fields the snapshot covers.
	src.KindVersion = "v0.42.0"
	src.WorkloadClusterName = "edge-1"
	src.NodeIPRanges = "10.0.0.10-10.0.0.20"
	src.Gateway = "10.0.0.1"
	src.ArgoCDEnabled = false
	src.KyvernoEnabled = false
	src.VMSSHKeys = "ssh-ed25519 AAAA…"

	yaml := src.SnapshotYAML()
	if !strings.Contains(yaml, `KIND_VERSION: "v0.42.0"`) {
		t.Fatalf("missing KIND_VERSION line in YAML:\n%s", yaml)
	}
	if !strings.Contains(yaml, `ARGOCD_ENABLED: "false"`) {
		t.Fatalf("expected ARGOCD_ENABLED=false line:\n%s", yaml)
	}

	// Parse back into a map[string]string (strip quotes from the value).
	kv := map[string]string{}
	for _, line := range strings.Split(yaml, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, ": ")
		if i < 0 {
			continue
		}
		k := line[:i]
		v := strings.Trim(line[i+2:], `"`)
		kv[k] = v
	}

	dst := Load()
	dst.ApplySnapshotKV(kv)
	if dst.KindVersion != "v0.42.0" {
		t.Errorf("KindVersion not restored: got %q", dst.KindVersion)
	}
	if dst.WorkloadClusterName != "edge-1" {
		t.Errorf("WorkloadClusterName not restored: got %q", dst.WorkloadClusterName)
	}
	if dst.NodeIPRanges != "10.0.0.10-10.0.0.20" {
		t.Errorf("NodeIPRanges not restored: got %q", dst.NodeIPRanges)
	}
	if dst.ArgoCDEnabled {
		t.Errorf("ArgoCDEnabled should be false after roundtrip")
	}
	if dst.KyvernoEnabled {
		t.Errorf("KyvernoEnabled should be false after roundtrip")
	}
	if dst.VMSSHKeys != "ssh-ed25519 AAAA…" {
		t.Errorf("VMSSHKeys not restored: got %q", dst.VMSSHKeys)
	}
}

// TestExplicitGuardPreservesCurrent pins the CLI-wins semantics: if
// WORKLOAD_CLUSTER_NAME_EXPLICIT is true, an in-cluster snapshot value
// must NOT overwrite the current name.
func TestExplicitGuardPreservesCurrent(t *testing.T) {
	c := Load()
	c.WorkloadClusterName = "cli-name"
	c.WorkloadClusterNameExplicit = true

	c.ApplySnapshotKV(map[string]string{
		"WORKLOAD_CLUSTER_NAME": "secret-name", // would overwrite without guard
		"KIND_VERSION":          "v9.9.9",      // unguarded — should apply
	})
	if c.WorkloadClusterName != "cli-name" {
		t.Errorf("explicit guard failed: got %q", c.WorkloadClusterName)
	}
	if c.KindVersion != "v9.9.9" {
		t.Errorf("unguarded key should overlay: got %q", c.KindVersion)
	}
}

// TestEmptyValueSkipped — empty incoming values don't blank out current
// state (matches bash `if v is None or str(v) == "": continue`).
func TestEmptyValueSkipped(t *testing.T) {
	c := Load()
	c.KindVersion = "v1.2.3"
	c.ApplySnapshotKV(map[string]string{"KIND_VERSION": ""})
	if c.KindVersion != "v1.2.3" {
		t.Errorf("empty value should be ignored: got %q", c.KindVersion)
	}
}
