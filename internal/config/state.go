// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Methods and small predicates over *Config that query current state
// (workload GitOps mode, secret persistence, clusterctl config presence,
// …).
package config

import "os"

// IsWorkloadGitopsCaaphMode reports whether the workload GitOps mode
// is "caaph". Empty defaults to "caaph".
func (c *Config) IsWorkloadGitopsCaaphMode() bool {
	return c.WorkloadGitopsMode == "" || c.WorkloadGitopsMode == "caaph"
}

// PersistLocalSecrets reports whether credentials should also be
// persisted to local YAML files. When false (default), credentials are
// pushed only to kind Secrets — the well-known local YAML paths are
// never written.
func (c *Config) PersistLocalSecrets() bool {
	return c.BootstrapPersistLocalSecrets
}

// ClusterctlCfgFilePresent reports whether the clusterctl config file
// path is set and points to a regular file.
func (c *Config) ClusterctlCfgFilePresent() bool {
	return c.ClusterctlCfg != "" && isRegularFile(c.ClusterctlCfg)
}

// ProxmoxAdminCfgFilePresent reports whether the Proxmox admin config
// file path is set and points to a regular file.
func (c *Config) ProxmoxAdminCfgFilePresent() bool {
	return c.Providers.Proxmox.AdminConfig != "" && isRegularFile(c.Providers.Proxmox.AdminConfig)
}

// HaveClusterctlCredsInEnv reports whether the Proxmox URL/token/secret
// trio that the CAPI/CAPMOX provider needs before it will initialize is
// present.
func (c *Config) HaveClusterctlCredsInEnv() bool {
	return c.Providers.Proxmox.URL != "" && c.Providers.Proxmox.CAPIToken != "" && c.Providers.Proxmox.CAPISecret != ""
}

// HaveAWSCloudCreds reports whether static IAM user keys are present in
// the environment (CAPA / clusterctl generate typical path).
func (c *Config) HaveAWSCloudCreds() bool {
	return os.Getenv("AWS_ACCESS_KEY_ID") != "" && os.Getenv("AWS_SECRET_ACCESS_KEY") != ""
}

// ReapplyWorkloadGitDefaults re-derives the workload app-of-apps Git
// fields from the WORKLOAD_ARGO_GIT_BASE_URL_DEFAULT /
// WORKLOAD_GIT_EXAMPLES_* env vars when a merge from the in-cluster
// config Secret cleared them.
func (c *Config) ReapplyWorkloadGitDefaults() {
	if !c.IsWorkloadGitopsCaaphMode() {
		return
	}
	base := os.Getenv("WORKLOAD_ARGO_GIT_BASE_URL_DEFAULT")
	if c.ArgoCD.AppOfAppsGitURL == "" && base != "" {
		c.ArgoCD.AppOfAppsGitURL = base + "/workload-app-of-apps"
	}
	ex := os.Getenv("WORKLOAD_GIT_EXAMPLES_EXAMPLE")
	if c.ArgoCD.AppOfAppsGitPath == "" && ex != "" {
		c.ArgoCD.AppOfAppsGitPath = "examples/" + ex
	}
	defRef := os.Getenv("WORKLOAD_GIT_EXAMPLES_EXAMPLE_DEFAULT_REF")
	if c.ArgoCD.AppOfAppsGitRef == "" && defRef != "" {
		c.ArgoCD.AppOfAppsGitRef = defRef
	}
}

// SyncCAPIControllerImagesToClusterctlVersion re-derives the three
// registry.k8s.io CAPI controller image URIs from the current
// ClusterctlVersion so that a merged-in version (e.g. from the bootstrap
// config Secret) keeps the pre-load / kind-load images aligned.
func (c *Config) SyncCAPIControllerImagesToClusterctlVersion() {
	v := c.ClusterctlVersion
	c.CAPICoreImage = "registry.k8s.io/cluster-api/cluster-api-controller:" + v
	c.CAPIBootstrapImage = "registry.k8s.io/cluster-api/kubeadm-bootstrap-controller:" + v
	c.CAPIControlplaneImage = "registry.k8s.io/cluster-api/kubeadm-control-plane-controller:" + v
}

// WorkloadGroupName returns the provider-specific grouping name for the
// workload cluster's VMs (Proxmox pool, vSphere folder, …). Returns ""
// when the active provider has no grouping concept or none is configured.
// Callers should skip EnsureGroup when the returned name is empty.
func (c *Config) WorkloadGroupName() string {
	switch c.InfraProvider {
	case "proxmox":
		return c.Providers.Proxmox.Pool
	case "vsphere":
		return c.Providers.Vsphere.Folder
	default:
		return ""
	}
}

// MgmtGroupName returns the provider-specific grouping name for the
// management cluster's VMs (Proxmox pool, vSphere folder, …). Returns ""
// when the active provider has no grouping concept or none is configured.
// Callers should skip EnsureGroup when the returned name is empty.
func (c *Config) MgmtGroupName() string {
	switch c.InfraProvider {
	case "proxmox":
		return c.Providers.Proxmox.Mgmt.Pool
	case "vsphere":
		return c.Providers.Vsphere.Folder
	default:
		return ""
	}
}

func isRegularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}