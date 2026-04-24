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

func isRegularFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
