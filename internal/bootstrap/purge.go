package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lpasquali/bootstrap-capi/internal/capimanifest"
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/kindsync"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/opentofux"
	"github.com/lpasquali/bootstrap-capi/internal/proxmox"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// WaitForWorkloadClusterReady ports wait_for_workload_cluster_ready
// (L7362-L7391). Waits for CAPI Cluster Available, fetches its
// kubeconfig via clusterctl, then waits for Cilium + node readiness.
func WaitForWorkloadClusterReady(cfg *config.Config) {
	capimanifest.DiscoverWorkloadClusterIdentity(cfg, cfg.CAPIManifest)

	logx.Log("Waiting for workload cluster %s/%s Available...",
		cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
	if err := shell.Run("kubectl", "wait", "cluster", cfg.WorkloadClusterName,
		"--namespace", cfg.WorkloadClusterNamespace,
		"--for=condition=Available", "--timeout=60m"); err != nil {
		logx.Die("workload cluster did not become Available: %v", err)
	}

	kcfg := WorkloadKubeconfigFromClusterctl(cfg)
	if kcfg == "" {
		logx.Die("Failed to fetch kubeconfig for workload cluster %s after Available=True.",
			cfg.WorkloadClusterName)
	}
	defer os.Remove(kcfg)
	logx.Log("Workload cluster Available; kubeconfig fetched.")

	logx.Log("Waiting for Cilium rollout in workload cluster %s...", cfg.WorkloadClusterName)
	_ = shell.Run("kubectl", "--kubeconfig", kcfg, "rollout", "status",
		"daemonset/cilium", "-n", "kube-system", "--timeout=20m")
	_ = shell.Run("kubectl", "--kubeconfig", kcfg, "rollout", "status",
		"deployment/cilium-operator", "-n", "kube-system", "--timeout=20m")
	_ = shell.Run("kubectl", "--kubeconfig", kcfg, "wait", "nodes", "--all",
		"--for=condition=Ready", "--timeout=20m")
}

// PurgeStaleHostNetworking ports purge_stale_host_networking
// (L7393-L7441). Best-effort: every step ignores errors; removes
// leftover CNI config/state, stale kind/lxc bridge interfaces,
// Cilium-owned iptables chains, and cni-* network namespaces.
func PurgeStaleHostNetworking() {
	logx.Log("Purging stale host networking state from previous kind/CNI runs...")

	// /etc/cni/net.d — delete kindnet/cilium/flannel/cni config files.
	if fi, err := os.Stat("/etc/cni/net.d"); err == nil && fi.IsDir() {
		entries, _ := os.ReadDir("/etc/cni/net.d")
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.Contains(name, "kindnet") &&
				!strings.Contains(name, "cilium") &&
				!strings.Contains(name, "flannel") &&
				!strings.Contains(name, "cni") {
				continue
			}
			_ = shell.RunPrivileged("rm", "-f", filepath.Join("/etc/cni/net.d", name))
		}
	}

	// /var/lib/cni — delete networks/ and results/.
	if _, err := os.Stat("/var/lib/cni"); err == nil {
		_ = shell.RunPrivileged("rm", "-rf", "/var/lib/cni/networks", "/var/lib/cni/results")
	}

	// Stale kind/lxc bridge interfaces.
	out, _, _ := shell.Capture("ip", "link", "show")
	for _, line := range strings.Split(out, "\n") {
		// Bash parses: `/^[0-9]+: (lxc|kind)/ { print $2 }` then strips @...
		fields := strings.SplitN(line, ":", 3)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimSpace(fields[1])
		name = strings.SplitN(name, "@", 2)[0]
		if !strings.HasPrefix(name, "lxc") && !strings.HasPrefix(name, "kind") {
			continue
		}
		_ = shell.RunPrivileged("ip", "link", "delete", name)
	}

	// Cilium iptables chains.
	if shell.CommandExists("iptables") {
		for _, table := range []string{"filter", "nat", "mangle"} {
			out, _, _ := shell.Capture("sudo", "iptables", "-t", table, "-S")
			for _, line := range strings.Split(out, "\n") {
				line = strings.TrimSpace(line)
				if !strings.HasPrefix(line, "-N CILIUM") {
					continue
				}
				parts := strings.Fields(line)
				if len(parts) < 2 {
					continue
				}
				chain := parts[1]
				_ = shell.RunPrivileged("iptables", "-t", table, "-F", chain)
				_ = shell.RunPrivileged("iptables", "-t", table, "-X", chain)
			}
		}
	}

	// cni-* netns.
	if shell.CommandExists("ip") {
		out, _, _ := shell.Capture("ip", "netns", "list")
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			ns := fields[0]
			if !strings.HasPrefix(ns, "cni-") {
				continue
			}
			_ = shell.RunPrivileged("ip", "netns", "delete", ns)
		}
	}

	logx.Log("Host networking state purged.")
}

// DeleteWorkloadClusterBeforeKindDeletion ports
// delete_workload_cluster_before_kind_deletion (L7554-L7582).
func DeleteWorkloadClusterBeforeKindDeletion(cfg *config.Config) {
	ctx := "kind-" + cfg.KindClusterName
	name := cfg.WorkloadClusterName
	ns := cfg.WorkloadClusterNamespace
	if ns == "" {
		ns = "default"
	}
	if fi, err := os.Stat(cfg.CAPIManifest); err == nil && fi.Size() > 0 {
		capimanifest.DiscoverWorkloadClusterIdentity(cfg, cfg.CAPIManifest)
		if cfg.WorkloadClusterName != "" {
			name = cfg.WorkloadClusterName
		}
		if cfg.WorkloadClusterNamespace != "" {
			ns = cfg.WorkloadClusterNamespace
		}
	}
	if name == "" {
		logx.Warn("WORKLOAD_CLUSTER_NAME is empty; skipping workload cluster deletion before kind teardown.")
		return
	}
	if err := shell.Run("kubectl", "--context", ctx, "get", "cluster", name, "-n", ns); err != nil {
		logx.Log("No workload Cluster %s/%s found on %s; continuing with kind deletion.", ns, name, ctx)
		return
	}
	logx.Log("Deleting workload Cluster %s/%s before deleting kind cluster %s...",
		ns, name, cfg.KindClusterName)
	_ = shell.Run("kubectl", "--context", ctx, "delete", "cluster", name,
		"-n", ns, "--ignore-not-found")
	logx.Log("Waiting for workload Cluster %s/%s to be deleted...", ns, name)
	_ = shell.Run("kubectl", "--context", ctx, "wait",
		"--for=delete", "cluster/"+name, "-n", ns, "--timeout=30m")
}

// PurgeGeneratedArtifacts ports purge_generated_artifacts (L7584-L7618).
func PurgeGeneratedArtifacts(cfg *config.Config) {
	stateDir := opentofux.StateDir()
	logx.Log("Purging generated files and Terraform state...")

	if shell.CommandExists("kind") && shell.CommandExists("kubectl") {
		out, _, _ := shell.Capture("kind", "get", "clusters")
		found := false
		for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
			if strings.TrimSpace(ln) == cfg.KindClusterName {
				found = true
				break
			}
		}
		if found {
			_ = shell.Run("kubectl", "--context", "kind-"+cfg.KindClusterName,
				"delete", "namespace", cfg.ProxmoxBootstrapSecretNamespace,
				"--ignore-not-found", "--wait=false")
			DeleteWorkloadClusterBeforeKindDeletion(cfg)
			DeleteCAPIManifestSecret(cfg)
		} else {
			logx.Warn("Management kind cluster '%s' not present; skipping workload cluster deletion before Terraform destroy (any leftover Proxmox VMs must be cleaned up manually).",
				cfg.KindClusterName)
		}
	}

	if _, err := os.Stat(stateDir); err == nil {
		_ = opentofux.DestroyIdentity(cfg)
	}

	if cfg.CAPIManifest != "" {
		_ = os.Remove(cfg.CAPIManifest)
	}
	if cfg.ProxmoxCSIConfig != "" {
		_ = os.Remove(cfg.ProxmoxCSIConfig)
	}
	if cfg.ProxmoxAdminConfig != "" {
		if _, err := os.Stat(cfg.ProxmoxAdminConfig); err == nil {
			_ = os.Remove(cfg.ProxmoxAdminConfig)
		}
	}
	if cfg.ClusterctlCfg != "" {
		if _, err := os.Stat(cfg.ClusterctlCfg); err == nil {
			_ = os.Remove(cfg.ClusterctlCfg)
		}
	}
	if cfg.ProxmoxIdentityTF != "" {
		_ = os.Remove(cfg.ProxmoxIdentityTF)
	}
	_ = os.RemoveAll(stateDir)
	_ = os.RemoveAll(cfg.CAPMOXBuildDir)
	_ = os.RemoveAll("./cluster-api")
	_ = os.RemoveAll("./cluster-api-ipam-provider-in-cluster")

	logx.Log("Purge complete.")
}

// WorkloadRolloutCAPITouchRollout ports workload_rollout_capi_touch_rollout
// (L7763-L7858). Triggers CAPI to roll control-plane + worker Machines
// via `clusterctl alpha rollout restart` (preferred) or
// `spec.rolloutAfter` patch (fallback).
func WorkloadRolloutCAPITouchRollout(cfg *config.Config) {
	ctx := "kind-" + cfg.KindClusterName
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	selector := "cluster.x-k8s.io/cluster-name=" + cfg.WorkloadClusterName

	outK, _, _ := shell.Capture("kubectl", "--context", ctx,
		"-n", cfg.WorkloadClusterNamespace, "get", "kubeadmcontrolplane",
		"-l", selector, "-o", "name")
	kcps := nonEmptyLines(outK)
	outM, _, _ := shell.Capture("kubectl", "--context", ctx,
		"-n", cfg.WorkloadClusterNamespace, "get", "machinedeployment",
		"-l", selector, "-o", "name")
	mds := nonEmptyLines(outM)

	if len(kcps) == 0 {
		logx.Warn("workload-rollout: no KubeadmControlPlane with label %s in %s — nothing to roll for the control plane.",
			selector, cfg.WorkloadClusterNamespace)
	}
	if len(mds) == 0 {
		logx.Warn("workload-rollout: no MachineDeployment with label %s in %s — nothing to roll for workers.",
			selector, cfg.WorkloadClusterNamespace)
	}

	paused, _, _ := shell.Capture("kubectl", "--context", ctx,
		"-n", cfg.WorkloadClusterNamespace, "get", "cluster",
		cfg.WorkloadClusterName, "-o", "jsonpath={.spec.paused}")
	if strings.TrimSpace(paused) == "true" {
		logx.Warn("workload-rollout: Cluster %s has spec.paused=true — CAPI will not roll Machines until the Cluster is unpaused.",
			cfg.WorkloadClusterName)
	}
	for _, md := range mds {
		st, _, _ := shell.Capture("kubectl", "--context", ctx,
			"-n", cfg.WorkloadClusterNamespace, "get", md,
			"-o", "jsonpath={.spec.strategy.type}")
		if strings.TrimSpace(st) == "OnDelete" {
			logx.Warn("workload-rollout: %s uses spec.strategy.type=OnDelete — CAPI does not create replacement Machines until existing Machines are deleted. Use RollingUpdate, or delete Machine objects, or `kubectl delete machine <name>` for each node to replace.", md)
		}
	}

	var rkcfg string
	if shell.CommandExists("clusterctl") {
		f, err := os.CreateTemp("", "workload-rollout-kubeconfig-")
		if err != nil {
			logx.Warn("workload-rollout: mktemp failed for kubeconfig; using spec.rolloutAfter only")
		} else {
			out, _, capErr := shell.Capture("kubectl", "config", "view", "--raw")
			if capErr != nil || out == "" {
				logx.Warn("workload-rollout: could not write kubeconfig for clusterctl; using spec.rolloutAfter only")
				os.Remove(f.Name())
			} else {
				if _, err := f.WriteString(out); err == nil {
					rkcfg = f.Name()
				}
			}
			f.Close()
		}
	} else {
		logx.Warn("workload-rollout: clusterctl not on PATH — install it for `clusterctl alpha rollout restart` (most reliable). Falling back to spec.rolloutAfter patches only.")
	}
	if rkcfg != "" {
		defer os.Remove(rkcfg)
	}

	rollout := func(kind, resource string) {
		name := resource
		if i := strings.IndexByte(resource, '/'); i >= 0 {
			name = resource[i+1:]
		}
		ok := false
		if rkcfg != "" {
			if err := shell.Run("clusterctl", "alpha", "rollout", "restart",
				kind+"/"+name, "-n", cfg.WorkloadClusterNamespace,
				"--kubeconfig", rkcfg,
				"--kubeconfig-context", "kind-"+cfg.KindClusterName); err == nil {
				logx.Log("workload-rollout: clusterctl restarted %s/%s", kind, name)
				ok = true
			} else {
				logx.Warn("workload-rollout: clusterctl alpha rollout restart failed for %s/%s (see above) — trying spec.rolloutAfter", kind, name)
			}
		}
		if !ok {
			patch := `{"spec":{"rolloutAfter":"` + now + `"}}`
			if err := shell.Run("kubectl", "--context", ctx,
				"-n", cfg.WorkloadClusterNamespace, "patch", resource,
				"--type", "merge", "-p", patch); err == nil {
				logx.Log("workload-rollout: set spec.rolloutAfter on %s", resource)
			} else {
				logx.Warn("workload-rollout: failed to set spec.rolloutAfter on %s", resource)
			}
		}
	}
	for _, kcp := range kcps {
		rollout("kubeadmcontrolplane", kcp)
	}
	for _, md := range mds {
		rollout("machinedeployment", md)
	}

	_ = shell.Run("kubectl", "--context", ctx, "-n", cfg.WorkloadClusterNamespace,
		"annotate", "cluster", cfg.WorkloadClusterName,
		"reconcile.cluster.x-k8s.io/force-rollout="+now, "--overwrite")
	pms, _, _ := shell.Capture("kubectl", "--context", ctx,
		"-n", cfg.WorkloadClusterNamespace, "get", "proxmoxmachines",
		"-l", selector, "-o", "name")
	tsNow := time.Now().Unix()
	for _, pm := range nonEmptyLines(pms) {
		_ = shell.Run("kubectl", "--context", ctx,
			"-n", cfg.WorkloadClusterNamespace, "annotate", pm,
			"reconcile.cluster.x-k8s.io/request="+time.Unix(tsNow, 0).UTC().Format("2006-01-02T15:04:05Z"),
			"--overwrite")
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

// Silence unused import in case kindsync is pulled in later here.
var _ = kindsync.SyncBootstrapConfigToKind
var _ = proxmox.APIJSONURL
