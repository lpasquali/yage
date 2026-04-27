// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package config

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

// ApplyYAMLFile reads path and overlays its key-value pairs onto cfg.
//
// File format — flat YAML, keys are the same uppercase env-var names
// used throughout yage (the same keys that appear in the kind Secret
// snapshot, plus credentials and a few operational fields that the
// snapshot intentionally omits):
//
//	INFRA_PROVIDER: "proxmox"
//	PROXMOX_URL: "https://proxmox.example.com:8006"
//	PROXMOX_TOKEN: "root@pam!mytoken=abc123"
//	KIND_CLUSTER_NAME: "yage-mgmt"
//
// Keys not recognized by yage are silently ignored.
// A blank path is a no-op (config file is optional).
//
// Precedence: YAML values override what config.Load read from the
// environment; they are themselves overridden by CLI flags (which run
// after ApplyYAMLFile in main).
func ApplyYAMLFile(cfg *Config, path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	var kv map[string]string
	if err := yaml.Unmarshal(data, &kv); err != nil {
		return fmt.Errorf("config file %s: %w", path, err)
	}
	// Snapshot-covered (non-sensitive) fields.
	cfg.ApplySnapshotKV(kv)
	// Credential and non-snapshot fields.
	for k, v := range kv {
		if v == "" {
			continue
		}
		if fn, ok := cfg.yamlExtras()[k]; ok {
			fn(v)
		}
	}
	return nil
}

// ConfigFilePath returns the config file path to use. It scans argv for
// --config <path> or --config=<path>, then falls back to YAGE_CONFIG_FILE.
// argv should be os.Args[1:]. Returns "" when neither is set.
func ConfigFilePath(argv []string) string {
	for i, a := range argv {
		if a == "--config" && i+1 < len(argv) {
			return argv[i+1]
		}
		if strings.HasPrefix(a, "--config=") {
			return a[len("--config="):]
		}
	}
	return strings.TrimSpace(os.Getenv("YAGE_CONFIG_FILE"))
}

// yamlExtras maps uppercase env-var keys that are intentionally absent
// from the snapshot schema (credentials, bootstrap-bookkeeping, or
// critical operational fields) to their Config setter. Only used by
// ApplyYAMLFile — these keys are never round-tripped through the kind
// Secret.
func (c *Config) yamlExtras() map[string]func(string) {
	return map[string]func(string){
		// Operational
		"INFRA_PROVIDER":  func(v string) { c.InfraProvider = v },
		"BOOTSTRAP_MODE":  func(v string) { c.BootstrapMode = v },
		// Proxmox credentials
		"PROXMOX_TOKEN":            func(v string) { c.Providers.Proxmox.Token = v },
		"PROXMOX_SECRET":           func(v string) { c.Providers.Proxmox.Secret = v },
		"PROXMOX_ADMIN_TOKEN":      func(v string) { c.Providers.Proxmox.AdminToken = v },
		"PROXMOX_ADMIN_USERNAME":   func(v string) { c.Providers.Proxmox.AdminUsername = v },
		"PROXMOX_CSI_TOKEN_ID":     func(v string) { c.Providers.Proxmox.CSITokenID = v },
		"PROXMOX_CSI_TOKEN_SECRET": func(v string) { c.Providers.Proxmox.CSITokenSecret = v },
		"PROXMOX_CSI_USER_ID":      func(v string) { c.Providers.Proxmox.CSIUserID = v },
		"PROXMOX_CAPI_USER_ID":     func(v string) { c.Providers.Proxmox.CAPIUserID = v },
		// Cloud credentials
		"HCLOUD_TOKEN":     func(v string) { c.Providers.Hetzner.Token = v },
		"VSPHERE_USERNAME": func(v string) { c.Providers.Vsphere.Username = v },
		"VSPHERE_PASSWORD": func(v string) { c.Providers.Vsphere.Password = v },
		// DigitalOcean / Linode tokens reach the cost credentials side-table.
		"DIGITALOCEAN_TOKEN":  func(v string) { c.Cost.Credentials.DigitalOceanToken = v },
		"IBMCLOUD_API_KEY":    func(v string) { c.Cost.Credentials.IBMCloudAPIKey = v },
		"GOOGLE_BILLING_API_KEY": func(v string) { c.Cost.Credentials.GCPAPIKey = v },
	}
}
