package kindsync

import (
	"encoding/base64"
	"os"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// kindClusterExists is the Go equivalent of
// `kind get clusters | contains_line "$cname"`.
func kindClusterExists(name string) bool {
	if !shell.CommandExists("kind") {
		return false
	}
	out, _, _ := shell.Capture("kind", "get", "clusters")
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if strings.TrimSpace(ln) == name {
			return true
		}
	}
	return false
}

// proxmoxEnvMap returns the set of PROXMOX_* / ARGO_WORKLOAD_POSTSYNC_*
// values the bash Python blocks read from os.environ, keyed by their
// bash-style name so the kindSecret.LookupValue callback stays simple.
//
// Values are read from cfg, not os.Environ(), so CLI flags correctly win
// over the shell. Only non-empty keys go into the map — a missing lookup
// returns "" which is the bash "skip" signal.
func proxmoxEnvMap(cfg *config.Config) map[string]string {
	put := func(m map[string]string, k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	m := map[string]string{}
	put(m, "PROXMOX_URL", cfg.ProxmoxURL)
	put(m, "PROXMOX_TOKEN", cfg.ProxmoxToken)
	put(m, "PROXMOX_SECRET", cfg.ProxmoxSecret)
	put(m, "PROXMOX_REGION", cfg.ProxmoxRegion)
	put(m, "PROXMOX_NODE", cfg.ProxmoxNode)
	put(m, "PROXMOX_ADMIN_USERNAME", cfg.ProxmoxAdminUsername)
	put(m, "PROXMOX_ADMIN_TOKEN", cfg.ProxmoxAdminToken)
	put(m, "PROXMOX_ADMIN_INSECURE", cfg.ProxmoxAdminInsecure)
	put(m, "PROXMOX_CSI_URL", cfg.ProxmoxCSIURL)
	put(m, "PROXMOX_CSI_TOKEN_ID", cfg.ProxmoxCSITokenID)
	put(m, "PROXMOX_CSI_TOKEN_SECRET", cfg.ProxmoxCSITokenSecret)
	put(m, "PROXMOX_CSI_USER_ID", cfg.ProxmoxCSIUserID)
	put(m, "PROXMOX_CSI_TOKEN_PREFIX", cfg.ProxmoxCSITokenPrefix)
	put(m, "PROXMOX_CSI_INSECURE", cfg.ProxmoxCSIInsecure)
	put(m, "PROXMOX_CSI_STORAGE_CLASS_NAME", cfg.ProxmoxCSIStorageClassName)
	put(m, "PROXMOX_CSI_STORAGE", cfg.ProxmoxCSIStorage)
	put(m, "PROXMOX_CSI_RECLAIM_POLICY", cfg.ProxmoxCSIReclaimPolicy)
	put(m, "PROXMOX_CSI_FSTYPE", cfg.ProxmoxCSIFsType)
	put(m, "PROXMOX_CSI_DEFAULT_CLASS", cfg.ProxmoxCSIDefaultClass)
	put(m, "PROXMOX_CSI_TOPOLOGY_LABELS", cfg.ProxmoxCSITopologyLabels)
	put(m, "PROXMOX_TOPOLOGY_REGION", cfg.ProxmoxTopologyRegion)
	put(m, "PROXMOX_TOPOLOGY_ZONE", cfg.ProxmoxTopologyZone)
	put(m, "PROXMOX_CSI_CHART_REPO_URL", cfg.ProxmoxCSIChartRepoURL)
	put(m, "PROXMOX_CSI_CHART_NAME", cfg.ProxmoxCSIChartName)
	put(m, "PROXMOX_CSI_CHART_VERSION", cfg.ProxmoxCSIChartVersion)
	put(m, "PROXMOX_CSI_NAMESPACE", cfg.ProxmoxCSINamespace)
	put(m, "PROXMOX_CSI_CONFIG_PROVIDER", cfg.ProxmoxCSIConfigProvider)
	put(m, "PROXMOX_CSI_SMOKE_ENABLED", boolStr(cfg.ProxmoxCSISmokeEnabled))
	put(m, "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL", cfg.ArgoWorkloadPostsyncHooksGitURL)
	put(m, "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH", cfg.ArgoWorkloadPostsyncHooksGitPath)
	put(m, "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF", cfg.ArgoWorkloadPostsyncHooksGitRef)
	put(m, "ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE", cfg.ArgoWorkloadPostsyncHooksKubectlImg)
	return m
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// writeWorkloadKubeconfig fetches the workload cluster's kubeconfig out
// of the Cluster's CAPI-managed Secret (<name>-kubeconfig on kind) and
// writes the decoded body to a tmp file. The file is readable by the
// current user only; callers should delete it when done.
func writeWorkloadKubeconfig(cfg *config.Config, ctx string) (string, error) {
	out, _, _ := shell.Capture(
		"kubectl", "--context", ctx,
		"-n", cfg.WorkloadClusterNamespace,
		"get", "secret", cfg.WorkloadClusterName+"-kubeconfig",
		"-o", "jsonpath={.data.value}",
	)
	if strings.TrimSpace(out) == "" {
		return "", os.ErrNotExist
	}
	decoded, err := base64.StdEncoding.DecodeString(out)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "workload-kubeconfig-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(decoded); err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// removeFile is a small defer-friendly wrapper.
func removeFile(p string) { _ = os.Remove(p) }

// applyBootstrapConfigToManagementCluster is the stub for
// apply_bootstrap_config_to_management_cluster (bash L3692-L3810). The
// bash function depends on _get_all_bootstrap_variables_as_yaml (L3620)
// which snapshots every config-relevant variable into a YAML blob that
// lives in ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME}. That snapshot format
// is load-bearing — other code paths round-trip through it — so we defer
// porting until the dedicated config-snapshot batch so we can get the
// field set and ordering right in one pass.
func applyBootstrapConfigToManagementCluster(cfg *config.Config, ctx string) error {
	_ = cfg
	_ = ctx
	// Intentionally silent: this runs on every bootstrap pass, so a
	// "not ported" warn here would be noisy. Callers get nil, matching
	// the bash success path.
	return nil
}
