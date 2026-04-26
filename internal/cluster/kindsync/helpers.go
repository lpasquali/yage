package kindsync

import (
	"context"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/platform/shell"
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
	put(m, "PROXMOX_URL", cfg.Providers.Proxmox.URL)
	put(m, "PROXMOX_TOKEN", cfg.Providers.Proxmox.Token)
	put(m, "PROXMOX_SECRET", cfg.Providers.Proxmox.Secret)
	put(m, "PROXMOX_REGION", cfg.Providers.Proxmox.Region)
	put(m, "PROXMOX_NODE", cfg.Providers.Proxmox.Node)
	put(m, "PROXMOX_ADMIN_USERNAME", cfg.Providers.Proxmox.AdminUsername)
	put(m, "PROXMOX_ADMIN_TOKEN", cfg.Providers.Proxmox.AdminToken)
	put(m, "PROXMOX_ADMIN_INSECURE", cfg.Providers.Proxmox.AdminInsecure)
	put(m, "PROXMOX_CSI_URL", cfg.Providers.Proxmox.CSIURL)
	put(m, "PROXMOX_CSI_TOKEN_ID", cfg.Providers.Proxmox.CSITokenID)
	put(m, "PROXMOX_CSI_TOKEN_SECRET", cfg.Providers.Proxmox.CSITokenSecret)
	put(m, "PROXMOX_CSI_USER_ID", cfg.Providers.Proxmox.CSIUserID)
	put(m, "PROXMOX_CSI_TOKEN_PREFIX", cfg.Providers.Proxmox.CSITokenPrefix)
	put(m, "PROXMOX_CSI_INSECURE", cfg.Providers.Proxmox.CSIInsecure)
	put(m, "PROXMOX_CSI_STORAGE_CLASS_NAME", cfg.Providers.Proxmox.CSIStorageClassName)
	put(m, "PROXMOX_CSI_STORAGE", cfg.Providers.Proxmox.CSIStorage)
	put(m, "PROXMOX_CSI_RECLAIM_POLICY", cfg.Providers.Proxmox.CSIReclaimPolicy)
	put(m, "PROXMOX_CSI_FSTYPE", cfg.Providers.Proxmox.CSIFsType)
	put(m, "PROXMOX_CSI_DEFAULT_CLASS", cfg.Providers.Proxmox.CSIDefaultClass)
	put(m, "PROXMOX_CSI_TOPOLOGY_LABELS", cfg.Providers.Proxmox.CSITopologyLabels)
	put(m, "PROXMOX_TOPOLOGY_REGION", cfg.Providers.Proxmox.TopologyRegion)
	put(m, "PROXMOX_TOPOLOGY_ZONE", cfg.Providers.Proxmox.TopologyZone)
	put(m, "PROXMOX_CSI_CHART_REPO_URL", cfg.Providers.Proxmox.CSIChartRepoURL)
	put(m, "PROXMOX_CSI_CHART_NAME", cfg.Providers.Proxmox.CSIChartName)
	put(m, "PROXMOX_CSI_CHART_VERSION", cfg.Providers.Proxmox.CSIChartVersion)
	put(m, "PROXMOX_CSI_NAMESPACE", cfg.Providers.Proxmox.CSINamespace)
	put(m, "PROXMOX_CSI_CONFIG_PROVIDER", cfg.Providers.Proxmox.CSIConfigProvider)
	put(m, "PROXMOX_CSI_SMOKE_ENABLED", boolStr(cfg.Providers.Proxmox.CSISmokeEnabled))
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
func writeWorkloadKubeconfig(cfg *config.Config, kctx string) (string, error) {
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return "", err
	}
	sec, err := cli.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
		Get(context.Background(), cfg.WorkloadClusterName+"-kubeconfig", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	body := sec.Data["value"]
	if len(body) == 0 {
		return "", os.ErrNotExist
	}
	f, err := os.CreateTemp("", "workload-kubeconfig-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// removeFile is a small defer-friendly wrapper.
func removeFile(p string) { _ = os.Remove(p) }

// applyBootstrapConfigToManagementCluster keeps the internal (package-
// private) call-site used by SyncBootstrapConfigToKind. It just forwards
// to the ported ApplyBootstrapConfigToManagementCluster.
func applyBootstrapConfigToManagementCluster(cfg *config.Config, _ string) error {
	return ApplyBootstrapConfigToManagementCluster(cfg)
}
