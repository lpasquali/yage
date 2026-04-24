// Methods and small predicates over *Config. These replace bash helpers
// that simply query the current global state (is_workload_gitops_caaph_mode,
// persist_local_secrets, _clusterctl_cfg_file_present, …).
package config

import "os"

// IsWorkloadGitopsCaaphMode ports is_workload_gitops_caaph_mode.
// Empty defaults to caaph (per the header and bash ${WORKLOAD_GITOPS_MODE:-caaph}).
func (c *Config) IsWorkloadGitopsCaaphMode() bool {
	return c.WorkloadGitopsMode == "" || c.WorkloadGitopsMode == "caaph"
}

// PersistLocalSecrets ports persist_local_secrets. When false (default),
// credentials are pushed only to kind Secrets — the well-known local YAML
// paths are never written.
func (c *Config) PersistLocalSecrets() bool {
	return c.BootstrapPersistLocalSecrets
}

// ClusterctlCfgFilePresent ports _clusterctl_cfg_file_present.
func (c *Config) ClusterctlCfgFilePresent() bool {
	return c.ClusterctlCfg != "" && isRegularFile(c.ClusterctlCfg)
}

// ProxmoxAdminCfgFilePresent ports _proxmox_admin_cfg_file_present.
func (c *Config) ProxmoxAdminCfgFilePresent() bool {
	return c.ProxmoxAdminConfig != "" && isRegularFile(c.ProxmoxAdminConfig)
}

// HaveClusterctlCredsInEnv ports have_clusterctl_creds_in_env: the trio
// the CAPI/CAPMOX provider needs before it will initialize.
func (c *Config) HaveClusterctlCredsInEnv() bool {
	return c.ProxmoxURL != "" && c.ProxmoxToken != "" && c.ProxmoxSecret != ""
}

// ReapplyWorkloadGitDefaults ports reapply_workload_git_defaults. When a
// merge from the in-cluster config Secret cleared the workload app-of-
// apps Git fields (empty values in config.yaml), re-derive defaults from
// any matching WORKLOAD_ARGO_GIT_BASE_URL_DEFAULT /
// WORKLOAD_GIT_EXAMPLES_* env vars. These are rarely set today but the
// bash honours them — preserved here for parity.
func (c *Config) ReapplyWorkloadGitDefaults() {
	if !c.IsWorkloadGitopsCaaphMode() {
		return
	}
	base := os.Getenv("WORKLOAD_ARGO_GIT_BASE_URL_DEFAULT")
	if c.WorkloadAppOfAppsGitURL == "" && base != "" {
		c.WorkloadAppOfAppsGitURL = base + "/workload-app-of-apps"
	}
	ex := os.Getenv("WORKLOAD_GIT_EXAMPLES_EXAMPLE")
	if c.WorkloadAppOfAppsGitPath == "" && ex != "" {
		c.WorkloadAppOfAppsGitPath = "examples/" + ex
	}
	defRef := os.Getenv("WORKLOAD_GIT_EXAMPLES_EXAMPLE_DEFAULT_REF")
	if c.WorkloadAppOfAppsGitRef == "" && defRef != "" {
		c.WorkloadAppOfAppsGitRef = defRef
	}
}

// SyncCAPIControllerImagesToClusterctlVersion ports
// bootstrap_sync_capi_controller_images_to_clusterctl_version. Re-derives
// the three registry.k8s.io CAPI controller image URIs from the current
// ClusterctlVersion so that a merged-in version (e.g. from the bootstrap
// config Secret) keeps the pre-load / kind-load images aligned.
func (c *Config) SyncCAPIControllerImagesToClusterctlVersion() {
	v := c.ClusterctlVersion
	c.CAPICoreImage = "registry.k8s.io/cluster-api/cluster-api-controller:" + v
	c.CAPIBootstrapImage = "registry.k8s.io/cluster-api/kubeadm-bootstrap-controller:" + v
	c.CAPIControlplaneImage = "registry.k8s.io/cluster-api/kubeadm-control-plane-controller:" + v
}

func isRegularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
