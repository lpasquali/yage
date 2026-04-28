// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package config

import "os"

// ClearCredentialEnvVars removes all credential-bearing environment variables
// from the process environment. Call once, immediately after Load().
// Wiping prevents child processes and /proc/self/environ from seeing secrets.
func ClearCredentialEnvVars() {
	for _, name := range credentialEnvVarNames {
		os.Unsetenv(name) //nolint:errcheck
	}
}

var credentialEnvVarNames = []string{
	// Proxmox
	"PROXMOX_CAPI_TOKEN", "PROXMOX_TOKEN",
	"PROXMOX_CAPI_SECRET", "PROXMOX_SECRET",
	"PROXMOX_ADMIN_TOKEN",
	"PROXMOX_CSI_TOKEN_SECRET",
	// vSphere
	"VSPHERE_PASSWORD",
	// Cost / cloud
	"YAGE_GCP_API_KEY", "GOOGLE_BILLING_API_KEY",
	"YAGE_HCLOUD_TOKEN", "HCLOUD_TOKEN",
	"YAGE_DO_TOKEN", "DIGITALOCEAN_TOKEN",
	"YAGE_LINODE_TOKEN", "LINODE_TOKEN",
	"YAGE_IBMCLOUD_API_KEY", "IBMCLOUD_API_KEY",
	// AWS (not read by yage directly but may be in env for SDKs)
	"AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
}
