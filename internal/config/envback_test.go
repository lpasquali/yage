// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package config

import (
	"testing"
)

// TestEnvVarBackcompat pins the Phase C contract: env-var spellings
// (PROXMOX_*, AWS_*, AZURE_*, HCLOUD_*, ...) are unchanged after the
// internal struct path moved under cfg.Providers.<Name>.*. A user who
// scripts around `PROXMOX_TOKEN=foo yage --pivot` keeps working without
// edits.
func TestEnvVarBackcompat(t *testing.T) {
	t.Setenv("PROXMOX_URL", "https://pve.example:8006/api2/json")
	t.Setenv("PROXMOX_TOKEN", "tok-123")
	t.Setenv("PROXMOX_SECRET", "sec-456")
	t.Setenv("PROXMOX_NODE", "pve-1")
	t.Setenv("PROXMOX_TEMPLATE_ID", "9001")
	t.Setenv("PROXMOX_BRIDGE", "vmbr1")
	t.Setenv("PROXMOX_POOL", "yage-test")
	t.Setenv("PROXMOX_CSI_STORAGE", "ceph-rbd")

	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_MODE", "eks")
	t.Setenv("AWS_OVERHEAD_TIER", "enterprise")
	t.Setenv("AWS_NODE_MACHINE_TYPE", "m5.xlarge")

	t.Setenv("AZURE_LOCATION", "northeurope")
	t.Setenv("AZURE_MODE", "aks")
	t.Setenv("GCP_REGION", "europe-west1")
	t.Setenv("GCP_PROJECT", "my-proj")
	t.Setenv("HCLOUD_REGION", "nbg1")
	t.Setenv("HETZNER_OVERHEAD_TIER", "dev")
	t.Setenv("DIGITALOCEAN_REGION", "fra1")
	t.Setenv("LINODE_REGION", "us-west")
	t.Setenv("OCI_REGION", "eu-frankfurt-1")
	t.Setenv("IBMCLOUD_REGION", "eu-de")

	t.Setenv("MGMT_CLUSTER_NAME", "mgmt-eu")
	t.Setenv("MGMT_CONTROL_PLANE_NUM_CORES", "4")
	t.Setenv("MGMT_PROXMOX_POOL", "mgmt-pool")
	t.Setenv("MGMT_PROXMOX_CSI_ENABLED", "true")

	c := Load()

	// Proxmox provider sub-config
	if got, want := c.Providers.Proxmox.URL, "https://pve.example:8006/api2/json"; got != want {
		t.Errorf("Providers.Proxmox.URL = %q, want %q", got, want)
	}
	if got, want := c.Providers.Proxmox.Token, "tok-123"; got != want {
		t.Errorf("Providers.Proxmox.Token = %q, want %q", got, want)
	}
	if got, want := c.Providers.Proxmox.Secret, "sec-456"; got != want {
		t.Errorf("Providers.Proxmox.Secret = %q, want %q", got, want)
	}
	if got, want := c.Providers.Proxmox.Node, "pve-1"; got != want {
		t.Errorf("Providers.Proxmox.Node = %q, want %q", got, want)
	}
	if got, want := c.Providers.Proxmox.TemplateID, "9001"; got != want {
		t.Errorf("Providers.Proxmox.TemplateID = %q, want %q", got, want)
	}
	if got, want := c.Providers.Proxmox.Bridge, "vmbr1"; got != want {
		t.Errorf("Providers.Proxmox.Bridge = %q, want %q", got, want)
	}
	if got, want := c.Providers.Proxmox.Pool, "yage-test"; got != want {
		t.Errorf("Providers.Proxmox.Pool = %q, want %q", got, want)
	}
	if got, want := c.Providers.Proxmox.CSIStorage, "ceph-rbd"; got != want {
		t.Errorf("Providers.Proxmox.CSIStorage = %q, want %q", got, want)
	}

	// AWS provider sub-config
	if got, want := c.Providers.AWS.Region, "eu-west-1"; got != want {
		t.Errorf("Providers.AWS.Region = %q, want %q", got, want)
	}
	if got, want := c.Providers.AWS.Mode, "eks"; got != want {
		t.Errorf("Providers.AWS.Mode = %q, want %q", got, want)
	}
	if got, want := c.Providers.AWS.OverheadTier, "enterprise"; got != want {
		t.Errorf("Providers.AWS.OverheadTier = %q, want %q", got, want)
	}
	if got, want := c.Providers.AWS.NodeMachineType, "m5.xlarge"; got != want {
		t.Errorf("Providers.AWS.NodeMachineType = %q, want %q", got, want)
	}

	// Other clouds
	if got, want := c.Providers.Azure.Location, "northeurope"; got != want {
		t.Errorf("Providers.Azure.Location = %q, want %q", got, want)
	}
	if got, want := c.Providers.Azure.Mode, "aks"; got != want {
		t.Errorf("Providers.Azure.Mode = %q, want %q", got, want)
	}
	if got, want := c.Providers.GCP.Region, "europe-west1"; got != want {
		t.Errorf("Providers.GCP.Region = %q, want %q", got, want)
	}
	if got, want := c.Providers.GCP.Project, "my-proj"; got != want {
		t.Errorf("Providers.GCP.Project = %q, want %q", got, want)
	}
	if got, want := c.Providers.Hetzner.Location, "nbg1"; got != want {
		t.Errorf("Providers.Hetzner.Location = %q, want %q", got, want)
	}
	if got, want := c.Providers.Hetzner.OverheadTier, "dev"; got != want {
		t.Errorf("Providers.Hetzner.OverheadTier = %q, want %q", got, want)
	}
	if got, want := c.Providers.DigitalOcean.Region, "fra1"; got != want {
		t.Errorf("Providers.DigitalOcean.Region = %q, want %q", got, want)
	}
	if got, want := c.Providers.Linode.Region, "us-west"; got != want {
		t.Errorf("Providers.Linode.Region = %q, want %q", got, want)
	}
	if got, want := c.Providers.OCI.Region, "eu-frankfurt-1"; got != want {
		t.Errorf("Providers.OCI.Region = %q, want %q", got, want)
	}
	if got, want := c.Providers.IBMCloud.Region, "eu-de"; got != want {
		t.Errorf("Providers.IBMCloud.Region = %q, want %q", got, want)
	}

	// Mgmt — universal vs Proxmox-only split.
	if got, want := c.Mgmt.ClusterName, "mgmt-eu"; got != want {
		t.Errorf("Mgmt.ClusterName = %q, want %q", got, want)
	}
	if got, want := c.Providers.Proxmox.Mgmt.ControlPlaneNumCores, "4"; got != want {
		t.Errorf("Providers.Proxmox.Mgmt.ControlPlaneNumCores = %q, want %q", got, want)
	}
	if got, want := c.Providers.Proxmox.Mgmt.Pool, "mgmt-pool"; got != want {
		t.Errorf("Providers.Proxmox.Mgmt.Pool = %q, want %q", got, want)
	}
	if !c.Providers.Proxmox.Mgmt.CSIEnabled {
		t.Errorf("Providers.Proxmox.Mgmt.CSIEnabled = false, want true (MGMT_PROXMOX_CSI_ENABLED=true)")
	}
}