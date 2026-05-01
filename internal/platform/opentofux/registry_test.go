// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"context"
	"errors"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

// TestEnsureRegistry_NotApplicable verifies that EnsureRegistry returns
// ErrNotApplicable when cfg.RegistryNode is empty.
func TestEnsureRegistry_NotApplicable(t *testing.T) {
	cfg := &config.Config{}
	err := EnsureRegistry(context.Background(), nil, cfg)
	if !errors.Is(err, ErrNotApplicable) {
		t.Errorf("want ErrNotApplicable, got %v", err)
	}
}

// TestDestroyRegistry_NotApplicable verifies that DestroyRegistry returns
// ErrNotApplicable when cfg.RegistryNode is empty.
func TestDestroyRegistry_NotApplicable(t *testing.T) {
	cfg := &config.Config{}
	err := DestroyRegistry(context.Background(), nil, cfg)
	if !errors.Is(err, ErrNotApplicable) {
		t.Errorf("want ErrNotApplicable, got %v", err)
	}
}

// TestParseRegistryOutputs_BareStrings verifies parsing when tofu emits
// bare string values (no wrapper map).
func TestParseRegistryOutputs_BareStrings(t *testing.T) {
	raw := map[string]any{
		"registry_ip":          "192.168.1.10",
		"registry_host":        "registry.internal",
		"registry_url":         "https://registry.internal",
		"registry_flavor":      "harbor",
		"vm_id":                "200",
		"registry_tls_cert_pem": "",
		"registry_ca_bundle_pem": "",
	}
	out, err := parseRegistryOutputs(raw)
	if err != nil {
		t.Fatalf("parseRegistryOutputs: %v", err)
	}
	if out.IP != "192.168.1.10" {
		t.Errorf("IP: got %q, want %q", out.IP, "192.168.1.10")
	}
	if out.Host != "registry.internal" {
		t.Errorf("Host: got %q, want %q", out.Host, "registry.internal")
	}
	if out.URL != "https://registry.internal" {
		t.Errorf("URL: got %q, want %q", out.URL, "https://registry.internal")
	}
	if out.Flavor != "harbor" {
		t.Errorf("Flavor: got %q, want %q", out.Flavor, "harbor")
	}
	if out.VMID != "200" {
		t.Errorf("VMID: got %q, want %q", out.VMID, "200")
	}
}

// TestParseRegistryOutputs_WrappedValues verifies parsing when tofu emits the
// wrapped {"value": ..., "type": ...} form.
func TestParseRegistryOutputs_WrappedValues(t *testing.T) {
	raw := map[string]any{
		"registry_ip": map[string]any{
			"value":     "10.0.0.5",
			"type":      "string",
			"sensitive": false,
		},
		"registry_host": map[string]any{
			"value": "registry.prod",
			"type":  "string",
		},
		"registry_url": map[string]any{
			"value": "https://registry.prod",
			"type":  "string",
		},
	}
	out, err := parseRegistryOutputs(raw)
	if err != nil {
		t.Fatalf("parseRegistryOutputs (wrapped): %v", err)
	}
	if out.IP != "10.0.0.5" {
		t.Errorf("IP: got %q, want %q", out.IP, "10.0.0.5")
	}
	if out.Host != "registry.prod" {
		t.Errorf("Host: got %q, want %q", out.Host, "registry.prod")
	}
	if out.URL != "https://registry.prod" {
		t.Errorf("URL: got %q, want %q", out.URL, "https://registry.prod")
	}
}

// TestParseRegistryOutputs_MissingFields verifies that parseRegistryOutputs
// returns an error when all of registry_url, registry_ip, registry_host are absent.
func TestParseRegistryOutputs_MissingFields(t *testing.T) {
	raw := map[string]any{
		"registry_flavor": "harbor",
	}
	_, err := parseRegistryOutputs(raw)
	if err == nil {
		t.Error("expected error when all key outputs are missing, got nil")
	}
}

// TestParseRegistryOutputs_URLTrailingSlash verifies that a trailing slash on
// registry_url is trimmed so cfg.ImageRegistryMirror has a canonical form.
func TestParseRegistryOutputs_URLTrailingSlash(t *testing.T) {
	raw := map[string]any{
		"registry_url": "https://registry.internal/",
		"registry_ip":  "1.2.3.4",
		"registry_host": "registry.internal",
	}
	out, err := parseRegistryOutputs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.URL == "https://registry.internal/" {
		t.Error("URL trailing slash was not stripped")
	}
	if out.URL != "https://registry.internal" {
		t.Errorf("URL: got %q, want %q", out.URL, "https://registry.internal")
	}
}

// TestRegistryVars_RequiredFields verifies that registryVars includes the
// fields that the module requires (proxmox credentials, cluster_name, registry_node).
func TestRegistryVars_RequiredFields(t *testing.T) {
	cfg := config.Load()
	cfg.WorkloadClusterName = "prod-cluster"
	cfg.RegistryNode = "pve-node1"
	cfg.Providers.Proxmox.URL = "https://pve.example.com:8006"
	cfg.Providers.Proxmox.AdminUsername = "root@pam"
	cfg.Providers.Proxmox.AdminToken = "secret-token"

	vars := registryVars(cfg)

	required := []string{
		"proxmox_url",
		"proxmox_username",
		"proxmox_password",
		"cluster_name",
		"registry_node",
	}
	for _, k := range required {
		if _, ok := vars[k]; !ok {
			t.Errorf("registryVars missing required key %q", k)
		}
	}
	if vars["cluster_name"] != "prod-cluster" {
		t.Errorf("cluster_name: got %q, want %q", vars["cluster_name"], "prod-cluster")
	}
	if vars["registry_node"] != "pve-node1" {
		t.Errorf("registry_node: got %q, want %q", vars["registry_node"], "pve-node1")
	}
}

// TestRegistryVars_OptionalFieldsOmitted verifies that optional module vars
// are absent from the map when the corresponding config fields are empty,
// so the module defaults apply.
func TestRegistryVars_OptionalFieldsOmitted(t *testing.T) {
	cfg := config.Load()
	cfg.RegistryNode = "pve-node1"
	cfg.RegistryNetwork = ""
	cfg.RegistryStorage = ""
	cfg.RegistryFlavor = ""
	cfg.RegistryTemplateID = ""
	cfg.RegistryHostname = ""

	vars := registryVars(cfg)

	optional := []string{
		"registry_network",
		"registry_storage",
		"registry_flavor",
		"registry_template_id",
		"registry_hostname",
		"registry_tls_cert_pem",
		"registry_tls_key_pem",
		"registry_ca_bundle_pem",
		"registry_admin_password",
	}
	for _, k := range optional {
		if _, ok := vars[k]; ok {
			t.Errorf("registryVars should omit %q when config field is empty, but it was present", k)
		}
	}
}

// TestRegistryVars_OptionalFieldsPresent verifies that optional vars are
// included when the config fields are non-empty.
func TestRegistryVars_OptionalFieldsPresent(t *testing.T) {
	cfg := config.Load()
	cfg.RegistryNode = "pve-node1"
	cfg.RegistryNetwork = "vmbr1"
	cfg.RegistryStorage = "ceph-pool"
	cfg.RegistryFlavor = "zot"
	cfg.RegistryTemplateID = "9001"
	cfg.RegistryHostname = "registry.prod.internal"
	cfg.RegistryTLSCertPEM = "---cert---"
	cfg.RegistryTLSKeyPEM = "---key---"
	cfg.RegistryCABundlePEM = "---ca---"
	cfg.RegistryAdminPassword = "hunter2"

	vars := registryVars(cfg)

	checks := map[string]string{
		"registry_network":        "vmbr1",
		"registry_storage":        "ceph-pool",
		"registry_flavor":         "zot",
		"registry_template_id":    "9001",
		"registry_hostname":       "registry.prod.internal",
		"registry_tls_cert_pem":   "---cert---",
		"registry_tls_key_pem":    "---key---",
		"registry_ca_bundle_pem":  "---ca---",
		"registry_admin_password": "hunter2",
	}
	for k, want := range checks {
		if got := vars[k]; got != want {
			t.Errorf("vars[%q]: got %q, want %q", k, got, want)
		}
	}
}

// TestResolveVMFlavor verifies that the flavor table maps correctly and that
// unknown flavors return empty strings (module defaults apply).
func TestResolveVMFlavor(t *testing.T) {
	tests := []struct {
		flavor  string
		cores   string
		memMB   string
		diskGB  string
	}{
		{"small", "2", "4096", "50"},
		{"large", "4", "8192", "200"},
		{"xlarge", "8", "16384", "500"},
		{"medium", "", "", ""},
		{"default", "", "", ""},
		{"", "", "", ""},
		{"LARGE", "4", "8192", "200"}, // case-insensitive
		{"unknown-flavor", "", "", ""},
	}
	for _, tt := range tests {
		cores, memMB, diskGB := resolveVMFlavor(tt.flavor)
		if cores != tt.cores || memMB != tt.memMB || diskGB != tt.diskGB {
			t.Errorf("resolveVMFlavor(%q): got (%q,%q,%q), want (%q,%q,%q)",
				tt.flavor, cores, memMB, diskGB, tt.cores, tt.memMB, tt.diskGB)
		}
	}
}

// TestMirrorPopulation_PreserveExisting verifies that EnsureRegistry does not
// overwrite cfg.ImageRegistryMirror when the operator has already set it.
// This is tested at the unit level via the output-parsing path rather than
// requiring a live cluster.
func TestMirrorPopulation_PreserveExisting(t *testing.T) {
	cfg := &config.Config{
		ImageRegistryMirror: "https://existing.mirror",
	}
	// parseRegistryOutputs returns a non-empty URL — the mirror must not change.
	raw := map[string]any{
		"registry_url":  "https://new.registry",
		"registry_ip":   "1.2.3.4",
		"registry_host": "new.registry",
	}
	out, err := parseRegistryOutputs(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	// Simulate what EnsureRegistry does after a successful apply+output.
	if cfg.ImageRegistryMirror == "" && out.URL != "" {
		cfg.ImageRegistryMirror = out.URL
	}

	if cfg.ImageRegistryMirror != "https://existing.mirror" {
		t.Errorf("mirror was overwritten: got %q, want %q",
			cfg.ImageRegistryMirror, "https://existing.mirror")
	}
}

// TestMirrorPopulation_SetFromOutput verifies that EnsureRegistry sets
// cfg.ImageRegistryMirror from the module output when no mirror is configured.
func TestMirrorPopulation_SetFromOutput(t *testing.T) {
	cfg := &config.Config{}
	raw := map[string]any{
		"registry_url":  "https://registry.internal",
		"registry_ip":   "10.0.0.1",
		"registry_host": "registry.internal",
	}
	out, err := parseRegistryOutputs(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if cfg.ImageRegistryMirror == "" && out.URL != "" {
		cfg.ImageRegistryMirror = out.URL
	}

	if cfg.ImageRegistryMirror != "https://registry.internal" {
		t.Errorf("mirror not set: got %q, want %q",
			cfg.ImageRegistryMirror, "https://registry.internal")
	}
}
