// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package config

import (
	"testing"
)

// TestCostCredentialsLoad confirms the §16 contract: each pricing
// vendor's credential is reachable through cfg.Cost.Credentials
// after Load() runs, and the YAGE_X / VENDOR_X dual-spelling
// fallback chain is preserved.
func TestCostCredentialsLoad(t *testing.T) {
	cases := []struct {
		name      string
		envs      map[string]string
		wantField func(c *Config) string
		want      string
	}{
		{
			name:      "gcp prefers YAGE_GCP_API_KEY",
			envs:      map[string]string{"YAGE_GCP_API_KEY": "yk", "GOOGLE_BILLING_API_KEY": "vk"},
			wantField: func(c *Config) string { return c.Cost.Credentials.GCPAPIKey },
			want:      "yk",
		},
		{
			name:      "gcp falls back to GOOGLE_BILLING_API_KEY",
			envs:      map[string]string{"GOOGLE_BILLING_API_KEY": "vk"},
			wantField: func(c *Config) string { return c.Cost.Credentials.GCPAPIKey },
			want:      "vk",
		},
		{
			name:      "hetzner prefers YAGE_HCLOUD_TOKEN",
			envs:      map[string]string{"YAGE_HCLOUD_TOKEN": "yt", "HCLOUD_TOKEN": "vt"},
			wantField: func(c *Config) string { return c.Cost.Credentials.HetznerToken },
			want:      "yt",
		},
		{
			name:      "hetzner falls back to HCLOUD_TOKEN",
			envs:      map[string]string{"HCLOUD_TOKEN": "vt"},
			wantField: func(c *Config) string { return c.Cost.Credentials.HetznerToken },
			want:      "vt",
		},
		{
			name:      "digitalocean prefers YAGE_DO_TOKEN",
			envs:      map[string]string{"YAGE_DO_TOKEN": "ydo", "DIGITALOCEAN_TOKEN": "vdo"},
			wantField: func(c *Config) string { return c.Cost.Credentials.DigitalOceanToken },
			want:      "ydo",
		},
		{
			name:      "ibmcloud prefers YAGE_IBMCLOUD_API_KEY",
			envs:      map[string]string{"YAGE_IBMCLOUD_API_KEY": "yi", "IBMCLOUD_API_KEY": "vi"},
			wantField: func(c *Config) string { return c.Cost.Credentials.IBMCloudAPIKey },
			want:      "yi",
		},
		{
			name:      "currency prefers YAGE_TALLER_CURRENCY",
			envs:      map[string]string{"YAGE_TALLER_CURRENCY": "EUR", "YAGE_CURRENCY": "USD"},
			wantField: func(c *Config) string { return c.Cost.Currency.DisplayCurrency },
			want:      "EUR",
		},
		{
			name:      "EUR_USD override travels through Cost.Currency",
			envs:      map[string]string{"YAGE_EUR_USD": "1.07"},
			wantField: func(c *Config) string { return c.Cost.Currency.EURUSDOverride },
			want:      "1.07",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Clear all relevant env vars first so each case is hermetic.
			for _, k := range []string{
				"YAGE_GCP_API_KEY", "GOOGLE_BILLING_API_KEY",
				"YAGE_HCLOUD_TOKEN", "HCLOUD_TOKEN",
				"YAGE_DO_TOKEN", "DIGITALOCEAN_TOKEN",
				"YAGE_IBMCLOUD_API_KEY", "IBMCLOUD_API_KEY",
				"YAGE_TALLER_CURRENCY", "YAGE_CURRENCY", "YAGE_EUR_USD",
			} {
				t.Setenv(k, "")
			}
			for k, v := range tc.envs {
				t.Setenv(k, v)
			}
			c := Load()
			if got := tc.wantField(c); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestHetznerTokenCrossFill confirms cfg.Cost.Credentials.HetznerToken
// and cfg.Providers.Hetzner.Token cross-fill: same secret, two
// consumers (provider + pricing).
func TestHetznerTokenCrossFill(t *testing.T) {
	t.Run("HCLOUD_TOKEN populates both", func(t *testing.T) {
		t.Setenv("HCLOUD_TOKEN", "shared-token")
		t.Setenv("YAGE_HCLOUD_TOKEN", "")
		c := Load()
		if c.Cost.Credentials.HetznerToken != "shared-token" {
			t.Fatalf("Cost.Credentials.HetznerToken: got %q, want shared-token", c.Cost.Credentials.HetznerToken)
		}
		if c.Providers.Hetzner.Token != "shared-token" {
			t.Fatalf("Providers.Hetzner.Token: got %q, want shared-token (cross-fill)", c.Providers.Hetzner.Token)
		}
	})
}

// TestProviderConfigEnvBackcompat covers the env-var → cfg wiring for
// the per-provider fields landed in commit f6ca113 (Azure / GCP /
// OpenStack / vSphere). One case per field on the spec sheet, plus
// the OPENSTACK_ vs. OS_ legacy-fallback contract for the two fields
// that already have a clouds.yaml convention (OS_PROJECT_NAME,
// OS_REGION_NAME).
func TestProviderConfigEnvBackcompat(t *testing.T) {
	cases := []struct {
		name      string
		envs      map[string]string
		wantField func(c *Config) string
		want      string
	}{
		// Azure
		{
			name:      "AZURE_SUBSCRIPTION_ID populates Providers.Azure.SubscriptionID",
			envs:      map[string]string{"AZURE_SUBSCRIPTION_ID": "sub-guid"},
			wantField: func(c *Config) string { return c.Providers.Azure.SubscriptionID },
			want:      "sub-guid",
		},
		{
			name:      "AZURE_TENANT_ID populates Providers.Azure.TenantID",
			envs:      map[string]string{"AZURE_TENANT_ID": "tenant-guid"},
			wantField: func(c *Config) string { return c.Providers.Azure.TenantID },
			want:      "tenant-guid",
		},
		{
			name:      "AZURE_RESOURCE_GROUP populates Providers.Azure.ResourceGroup",
			envs:      map[string]string{"AZURE_RESOURCE_GROUP": "rg-yage"},
			wantField: func(c *Config) string { return c.Providers.Azure.ResourceGroup },
			want:      "rg-yage",
		},
		{
			name:      "AZURE_VNET_NAME populates Providers.Azure.VNetName",
			envs:      map[string]string{"AZURE_VNET_NAME": "vnet-yage"},
			wantField: func(c *Config) string { return c.Providers.Azure.VNetName },
			want:      "vnet-yage",
		},
		{
			name:      "AZURE_SUBNET_NAME populates Providers.Azure.SubnetName",
			envs:      map[string]string{"AZURE_SUBNET_NAME": "subnet-yage"},
			wantField: func(c *Config) string { return c.Providers.Azure.SubnetName },
			want:      "subnet-yage",
		},
		{
			name:      "AZURE_CLIENT_ID populates Providers.Azure.ClientID",
			envs:      map[string]string{"AZURE_CLIENT_ID": "client-guid"},
			wantField: func(c *Config) string { return c.Providers.Azure.ClientID },
			want:      "client-guid",
		},
		{
			name:      "AZURE_IDENTITY_MODEL populates Providers.Azure.IdentityModel",
			envs:      map[string]string{"AZURE_IDENTITY_MODEL": "workload-identity"},
			wantField: func(c *Config) string { return c.Providers.Azure.IdentityModel },
			want:      "workload-identity",
		},
		{
			name:      "Providers.Azure.IdentityModel defaults to service-principal",
			envs:      map[string]string{},
			wantField: func(c *Config) string { return c.Providers.Azure.IdentityModel },
			want:      "service-principal",
		},

		// GCP
		{
			name:      "GCP_NETWORK_NAME populates Providers.GCP.Network",
			envs:      map[string]string{"GCP_NETWORK_NAME": "vpc-yage"},
			wantField: func(c *Config) string { return c.Providers.GCP.Network },
			want:      "vpc-yage",
		},
		{
			name:      "GCP_IMAGE_FAMILY populates Providers.GCP.ImageFamily",
			envs:      map[string]string{"GCP_IMAGE_FAMILY": "ubuntu-2204-lts"},
			wantField: func(c *Config) string { return c.Providers.GCP.ImageFamily },
			want:      "ubuntu-2204-lts",
		},
		{
			name:      "GCP_IDENTITY_MODEL populates Providers.GCP.IdentityModel",
			envs:      map[string]string{"GCP_IDENTITY_MODEL": "adc"},
			wantField: func(c *Config) string { return c.Providers.GCP.IdentityModel },
			want:      "adc",
		},
		{
			name:      "Providers.GCP.IdentityModel defaults to service-account",
			envs:      map[string]string{},
			wantField: func(c *Config) string { return c.Providers.GCP.IdentityModel },
			want:      "service-account",
		},

		// OpenStack — primary spelling
		{
			name:      "OPENSTACK_CLOUD populates Providers.OpenStack.Cloud",
			envs:      map[string]string{"OPENSTACK_CLOUD": "devstack"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.Cloud },
			want:      "devstack",
		},
		{
			name:      "OPENSTACK_PROJECT_NAME wins over OS_PROJECT_NAME",
			envs:      map[string]string{"OPENSTACK_PROJECT_NAME": "primary", "OS_PROJECT_NAME": "legacy"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.ProjectName },
			want:      "primary",
		},
		{
			name:      "OS_PROJECT_NAME falls back when OPENSTACK_PROJECT_NAME is unset",
			envs:      map[string]string{"OS_PROJECT_NAME": "legacy"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.ProjectName },
			want:      "legacy",
		},
		{
			name:      "OPENSTACK_REGION wins over OS_REGION_NAME",
			envs:      map[string]string{"OPENSTACK_REGION": "RegionOne", "OS_REGION_NAME": "RegionTwo"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.Region },
			want:      "RegionOne",
		},
		{
			name:      "OS_REGION_NAME falls back when OPENSTACK_REGION is unset",
			envs:      map[string]string{"OS_REGION_NAME": "RegionTwo"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.Region },
			want:      "RegionTwo",
		},
		{
			name:      "OPENSTACK_FAILURE_DOMAIN populates Providers.OpenStack.FailureDomain",
			envs:      map[string]string{"OPENSTACK_FAILURE_DOMAIN": "az1"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.FailureDomain },
			want:      "az1",
		},
		{
			name:      "OPENSTACK_IMAGE_NAME populates Providers.OpenStack.ImageName",
			envs:      map[string]string{"OPENSTACK_IMAGE_NAME": "ubuntu-22.04"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.ImageName },
			want:      "ubuntu-22.04",
		},
		{
			name:      "OPENSTACK_CONTROL_PLANE_FLAVOR populates Providers.OpenStack.ControlPlaneFlavor",
			envs:      map[string]string{"OPENSTACK_CONTROL_PLANE_FLAVOR": "m1.large"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.ControlPlaneFlavor },
			want:      "m1.large",
		},
		{
			name:      "OPENSTACK_WORKER_FLAVOR populates Providers.OpenStack.WorkerFlavor",
			envs:      map[string]string{"OPENSTACK_WORKER_FLAVOR": "m1.medium"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.WorkerFlavor },
			want:      "m1.medium",
		},
		{
			name:      "OPENSTACK_DNS_NAMESERVERS populates Providers.OpenStack.DNSNameservers",
			envs:      map[string]string{"OPENSTACK_DNS_NAMESERVERS": "1.1.1.1,1.0.0.1"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.DNSNameservers },
			want:      "1.1.1.1,1.0.0.1",
		},
		{
			name:      "OPENSTACK_SSH_KEY_NAME populates Providers.OpenStack.SSHKeyName",
			envs:      map[string]string{"OPENSTACK_SSH_KEY_NAME": "yage-key"},
			wantField: func(c *Config) string { return c.Providers.OpenStack.SSHKeyName },
			want:      "yage-key",
		},

		// vSphere
		{
			name:      "VSPHERE_SERVER populates Providers.Vsphere.Server",
			envs:      map[string]string{"VSPHERE_SERVER": "vcenter.example"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.Server },
			want:      "vcenter.example",
		},
		{
			name:      "VSPHERE_DATACENTER populates Providers.Vsphere.Datacenter",
			envs:      map[string]string{"VSPHERE_DATACENTER": "DC1"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.Datacenter },
			want:      "DC1",
		},
		{
			name:      "VSPHERE_FOLDER populates Providers.Vsphere.Folder",
			envs:      map[string]string{"VSPHERE_FOLDER": "yage/clusters"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.Folder },
			want:      "yage/clusters",
		},
		{
			name:      "VSPHERE_RESOURCE_POOL populates Providers.Vsphere.ResourcePool",
			envs:      map[string]string{"VSPHERE_RESOURCE_POOL": "compute/yage"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.ResourcePool },
			want:      "compute/yage",
		},
		{
			name:      "VSPHERE_DATASTORE populates Providers.Vsphere.Datastore",
			envs:      map[string]string{"VSPHERE_DATASTORE": "ds01"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.Datastore },
			want:      "ds01",
		},
		{
			name:      "VSPHERE_NETWORK populates Providers.Vsphere.Network",
			envs:      map[string]string{"VSPHERE_NETWORK": "VM Network"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.Network },
			want:      "VM Network",
		},
		{
			name:      "VSPHERE_TEMPLATE populates Providers.Vsphere.Template",
			envs:      map[string]string{"VSPHERE_TEMPLATE": "ubuntu-2204"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.Template },
			want:      "ubuntu-2204",
		},
		{
			name:      "VSPHERE_TLS_THUMBPRINT populates Providers.Vsphere.TLSThumbprint",
			envs:      map[string]string{"VSPHERE_TLS_THUMBPRINT": "AA:BB:CC"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.TLSThumbprint },
			want:      "AA:BB:CC",
		},
		{
			name:      "VSPHERE_USERNAME populates Providers.Vsphere.Username",
			envs:      map[string]string{"VSPHERE_USERNAME": "admin@vsphere.local"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.Username },
			want:      "admin@vsphere.local",
		},
		{
			name:      "VSPHERE_PASSWORD populates Providers.Vsphere.Password",
			envs:      map[string]string{"VSPHERE_PASSWORD": "s3cret"},
			wantField: func(c *Config) string { return c.Providers.Vsphere.Password },
			want:      "s3cret",
		},
	}

	// Every env-var any case touches; cleared at the start of each
	// subtest so the table is hermetic.
	allKeys := []string{
		"AZURE_SUBSCRIPTION_ID", "AZURE_TENANT_ID", "AZURE_RESOURCE_GROUP",
		"AZURE_VNET_NAME", "AZURE_SUBNET_NAME", "AZURE_CLIENT_ID",
		"AZURE_IDENTITY_MODEL",
		"GCP_NETWORK_NAME", "GCP_IMAGE_FAMILY", "GCP_IDENTITY_MODEL",
		"OPENSTACK_CLOUD", "OPENSTACK_PROJECT_NAME", "OS_PROJECT_NAME",
		"OPENSTACK_REGION", "OS_REGION_NAME", "OPENSTACK_FAILURE_DOMAIN",
		"OPENSTACK_IMAGE_NAME", "OPENSTACK_CONTROL_PLANE_FLAVOR",
		"OPENSTACK_WORKER_FLAVOR", "OPENSTACK_DNS_NAMESERVERS",
		"OPENSTACK_SSH_KEY_NAME",
		"VSPHERE_SERVER", "VSPHERE_DATACENTER", "VSPHERE_FOLDER",
		"VSPHERE_RESOURCE_POOL", "VSPHERE_DATASTORE", "VSPHERE_NETWORK",
		"VSPHERE_TEMPLATE", "VSPHERE_TLS_THUMBPRINT",
		"VSPHERE_USERNAME", "VSPHERE_PASSWORD",
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range allKeys {
				t.Setenv(k, "")
			}
			for k, v := range tc.envs {
				t.Setenv(k, v)
			}
			c := Load()
			if got := tc.wantField(c); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}