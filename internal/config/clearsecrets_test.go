// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package config

import (
	"os"
	"testing"
)

func TestClearCredentialEnvVars_RemovesFromEnv(t *testing.T) {
	t.Setenv("PROXMOX_CAPI_TOKEN", "test-token")
	t.Setenv("VSPHERE_PASSWORD", "hunter2")
	ClearCredentialEnvVars()
	if os.Getenv("PROXMOX_CAPI_TOKEN") != "" {
		t.Error("PROXMOX_CAPI_TOKEN not cleared")
	}
	if os.Getenv("VSPHERE_PASSWORD") != "" {
		t.Error("VSPHERE_PASSWORD not cleared")
	}
}
