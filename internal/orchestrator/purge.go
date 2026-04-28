// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/kind/pkg/cluster"

	"github.com/lpasquali/yage/internal/capi/manifest"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/logx"
)

var capiClusterGVR = schema.GroupVersionResource{
	Group:    "cluster.x-k8s.io",
	Version:  "v1beta2",
	Resource: "clusters",
}

// WaitForWorkloadClusterReady waits for the workload Cluster to
// report Available, then waits for Cilium and node readiness.
func WaitForWorkloadClusterReady(cfg *config.Config) {
	capimanifest.DiscoverWorkloadClusterIdentity(cfg, cfg.CAPIManifest)

	logx.Log("Waiting for workload cluster %s/%s Available...",
		cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
	mgmt, err := k8sclient.ForCurrent()
	if err != nil {
		logx.Die("waiting for workload cluster: cannot build management client: %v", err)
	}
	bg := context.Background()
	if err := waitClusterAvailable(mgmt, bg, cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, 60*time.Minute); err != nil {
		logx.Die("workload cluster did not become Available: %v", err)
	}

	kcfg := WorkloadKubeconfigFromClusterctl(cfg)
	if kcfg == "" {
		logx.Die("Failed to fetch kubeconfig for workload cluster %s after Available=True.",
			cfg.WorkloadClusterName)
	}
	defer os.Remove(kcfg)
	logx.Log("Workload cluster Available; kubeconfig fetched.")

	cli, err := k8sclient.ForKubeconfigFile(kcfg)
	if err != nil {
		logx.Warn("workload kubeconfig client: %v", err)
		return
	}
	logx.Log("Waiting for Cilium rollout in workload cluster %s...", cfg.WorkloadClusterName)
	_ = waitDaemonSetReady(cli, "kube-system", "cilium", 20*time.Minute)
	_ = waitDeploymentReady(cli, "kube-system", "cilium-operator", 20*time.Minute)
	_ = waitAllNodesReady(cli, 20*time.Minute)
}

// waitClusterAvailable polls a CAPI Cluster's Available condition.
func waitClusterAvailable(cli *k8sclient.Client, bg context.Context, ns, name string, timeout time.Duration) error {
	return k8sclient.PollUntil(bg, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		u, err := cli.Dynamic.Resource(capiClusterGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		conds, _, _ := unstructuredSlice(u.Object, "status", "conditions")
		for _, c := range conds {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			t, _ := cm["type"].(string)
			s, _ := cm["status"].(string)
			if t == "Available" && s == "True" {
				return true, nil
			}
		}
		return false, nil
	})
}

// waitDaemonSetReady waits for a DaemonSet's NumberReady == DesiredNumberScheduled.
func waitDaemonSetReady(cli *k8sclient.Client, ns, name string, timeout time.Duration) error {
	bg := context.Background()
	return k8sclient.PollUntil(bg, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		ds, err := cli.Typed.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return ds.Status.DesiredNumberScheduled > 0 &&
			ds.Status.NumberReady == ds.Status.DesiredNumberScheduled, nil
	})
}

// waitAllNodesReady polls every Node for Ready=True.
func waitAllNodesReady(cli *k8sclient.Client, timeout time.Duration) error {
	bg := context.Background()
	return k8sclient.PollUntil(bg, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		nodes, err := cli.Typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		if len(nodes.Items) == 0 {
			return false, nil
		}
		for _, n := range nodes.Items {
			ready := false
			for _, c := range n.Status.Conditions {
				if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
					ready = true
					break
				}
			}
			if !ready {
				return false, nil
			}
		}
		return true, nil
	})
}

// PurgeStaleHostNetworking is unchanged from the previous implementation
// (uses ip / iptables / sudo, not kubectl).
func PurgeStaleHostNetworking() {
	logx.Log("Purging stale host networking state from previous kind/CNI runs...")

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

	if _, err := os.Stat("/var/lib/cni"); err == nil {
		_ = shell.RunPrivileged("rm", "-rf", "/var/lib/cni/networks", "/var/lib/cni/results")
	}

	out, _, _ := shell.Capture("ip", "link", "show")
	for _, line := range strings.Split(out, "\n") {
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

// DeleteWorkloadClusterBeforeKindDeletion deletes the workload
// Cluster CR on the kind management cluster and waits for it to
// disappear, so that downstream provider state is reaped before
// kind itself is destroyed.
func DeleteWorkloadClusterBeforeKindDeletion(cfg *config.Config) {
	ctxName := "kind-" + cfg.KindClusterName
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
	cli, err := k8sclient.ForContext(ctxName)
	if err != nil {
		logx.Warn("Cannot build kube client for %s: %v; skipping workload cluster deletion.", ctxName, err)
		return
	}
	bg := context.Background()
	if _, err := cli.Dynamic.Resource(capiClusterGVR).Namespace(ns).Get(bg, name, metav1.GetOptions{}); err != nil {
		logx.Log("No workload Cluster %s/%s found on %s; continuing with kind deletion.", ns, name, ctxName)
		return
	}
	logx.Log("Deleting workload Cluster %s/%s before deleting kind cluster %s...",
		ns, name, cfg.KindClusterName)
	if err := cli.Dynamic.Resource(capiClusterGVR).Namespace(ns).
		Delete(bg, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		logx.Warn("delete workload Cluster: %v", err)
	}
	logx.Log("Waiting for workload Cluster %s/%s to be deleted...", ns, name)
	_ = waitClusterDeleted(cli, bg, ns, name, 30*time.Minute)
}

func waitClusterDeleted(cli *k8sclient.Client, bg context.Context, ns, name string, timeout time.Duration) error {
	return k8sclient.PollUntil(bg, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		_, err := cli.Dynamic.Resource(capiClusterGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	})
}

// PurgeGeneratedArtifacts owns the cross-cutting cleanup (kind
// cluster, CAPI manifest path, CAPMOX build cache, vendored CAPI
// clones). Provider-specific cleanup (Proxmox tofu destroy, pool
// delete, generated files) lives in Provider.Purge per §14.D.
func PurgeGeneratedArtifacts(cfg *config.Config) {
	logx.Log("Purging generated files and Terraform state...")

	kindProv := cluster.NewProvider()
	if names, err := kindProv.List(); err == nil {
		found := false
		for _, n := range names {
			if n == cfg.KindClusterName {
				found = true
				break
			}
		}
		if found {
			DeleteWorkloadClusterBeforeKindDeletion(cfg)
			DeleteCAPIManifestSecret(cfg)
		} else {
			logx.Warn("Management kind cluster '%s' not present; skipping workload cluster deletion before Terraform destroy (any leftover Proxmox VMs must be cleaned up manually).",
				cfg.KindClusterName)
		}
	}

	// Provider-specific cleanup (§11). For Proxmox this runs
	// `tofu destroy` on the BPG identity tree and removes the
	// Proxmox-flavored generated files (CSIConfig, AdminConfig,
	// IdentityTF). For other providers it is a no-op (MinStub
	// default returns nil) — they do not create state outside the
	// workload cluster.
	if prov, perr := provider.For(cfg); perr == nil {
		if err := prov.Purge(cfg); err != nil {
			logx.Warn("provider %s Purge: %v (continuing)", prov.Name(), err)
		}
	}

	// Cross-cutting cleanup that stays in the orchestrator: the
	// CAPI manifest path, the operator-supplied clusterctl config,
	// the CAPMOX build cache, and clones of upstream CAPI repos.
	if cfg.CAPIManifest != "" {
		_ = os.Remove(cfg.CAPIManifest)
	}
	if cfg.ClusterctlCfg != "" {
		if _, err := os.Stat(cfg.ClusterctlCfg); err == nil {
			_ = os.Remove(cfg.ClusterctlCfg)
		}
	}
	_ = os.RemoveAll(cfg.CAPMOXBuildDir)
	_ = os.RemoveAll("./cluster-api")
	_ = os.RemoveAll("./cluster-api-ipam-provider-in-cluster")

	logx.Log("Purge complete.")
}

// WorkloadRolloutCAPITouchRollout triggers CAPI to roll
// control-plane + worker Machines. `clusterctl alpha rollout
// restart` is intentionally retained as a shell-out (driving that
// subcommand without pulling cluster-api/client in-process is far
// heavier).
func WorkloadRolloutCAPITouchRollout(cfg *config.Config) {
	ctxName := "kind-" + cfg.KindClusterName
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	selector := "cluster.x-k8s.io/cluster-name=" + cfg.WorkloadClusterName

	cli, err := k8sclient.ForContext(ctxName)
	if err != nil {
		logx.Die("workload-rollout: cannot build kube client for %s: %v", ctxName, err)
	}
	bg := context.Background()

	kcpGVR := schema.GroupVersionResource{Group: "controlplane.cluster.x-k8s.io", Version: "v1beta2", Resource: "kubeadmcontrolplanes"}
	mdGVR := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta2", Resource: "machinedeployments"}
	pmGVR := schema.GroupVersionResource{Group: "infrastructure.cluster.x-k8s.io", Version: "v1alpha1", Resource: "proxmoxmachines"}

	listNames := func(gvr schema.GroupVersionResource) []string {
		ul, err := cli.Dynamic.Resource(gvr).Namespace(cfg.WorkloadClusterNamespace).
			List(bg, metav1.ListOptions{LabelSelector: selector})
		if err != nil || ul == nil {
			return nil
		}
		out := make([]string, 0, len(ul.Items))
		for _, it := range ul.Items {
			if nm := it.GetName(); nm != "" {
				out = append(out, nm)
			}
		}
		return out
	}
	kcps := listNames(kcpGVR)
	mds := listNames(mdGVR)

	if len(kcps) == 0 {
		logx.Warn("workload-rollout: no KubeadmControlPlane with label %s in %s — nothing to roll for the control plane.",
			selector, cfg.WorkloadClusterNamespace)
	}
	if len(mds) == 0 {
		logx.Warn("workload-rollout: no MachineDeployment with label %s in %s — nothing to roll for workers.",
			selector, cfg.WorkloadClusterNamespace)
	}

	if u, err := cli.Dynamic.Resource(capiClusterGVR).Namespace(cfg.WorkloadClusterNamespace).
		Get(bg, cfg.WorkloadClusterName, metav1.GetOptions{}); err == nil {
		paused, _ := u.Object["spec"].(map[string]interface{})["paused"].(bool)
		if paused {
			logx.Warn("workload-rollout: Cluster %s has spec.paused=true — CAPI will not roll Machines until the Cluster is unpaused.",
				cfg.WorkloadClusterName)
		}
	}
	for _, md := range mds {
		u, err := cli.Dynamic.Resource(mdGVR).Namespace(cfg.WorkloadClusterNamespace).
			Get(bg, md, metav1.GetOptions{})
		if err != nil {
			continue
		}
		st, _, _ := unstructuredStr(u.Object, "spec", "strategy", "type")
		if st == "OnDelete" {
			logx.Warn("workload-rollout: %s uses spec.strategy.type=OnDelete — CAPI does not create replacement Machines until existing Machines are deleted.", md)
		}
	}

	var rkcfg string
	if shell.CommandExists("clusterctl") {
		f, err := os.CreateTemp("", "workload-rollout-kubeconfig-")
		if err != nil {
			logx.Warn("workload-rollout: mktemp failed for kubeconfig; using spec.rolloutAfter only")
		} else {
			rules := clientcmd.NewDefaultClientConfigLoadingRules()
			cc, err := rules.Load()
			if err != nil || cc == nil {
				logx.Warn("workload-rollout: could not load kubeconfig for clusterctl; using spec.rolloutAfter only")
				os.Remove(f.Name())
			} else {
				body, err := clientcmd.Write(*cc)
				if err != nil {
					logx.Warn("workload-rollout: could not serialize kubeconfig for clusterctl; using spec.rolloutAfter only")
					os.Remove(f.Name())
				} else if _, err := f.Write(body); err == nil {
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

	patchRolloutAfter := func(gvr schema.GroupVersionResource, name string) error {
		body := []byte(fmt.Sprintf(`{"spec":{"rolloutAfter":"%s"}}`, now))
		_, err := cli.Dynamic.Resource(gvr).Namespace(cfg.WorkloadClusterNamespace).
			Patch(bg, name, types.MergePatchType, body, metav1.PatchOptions{})
		return err
	}

	rollout := func(gvr schema.GroupVersionResource, kind, name string) {
		ok := false
		if rkcfg != "" {
			if err := shell.Run("clusterctl", "alpha", "rollout", "restart",
				kind+"/"+name, "-n", cfg.WorkloadClusterNamespace,
				"--kubeconfig", rkcfg,
				"--kubeconfig-context", "kind-"+cfg.KindClusterName); err == nil {
				logx.Log("workload-rollout: clusterctl restarted %s/%s", kind, name)
				ok = true
			} else {
				logx.Warn("workload-rollout: clusterctl alpha rollout restart failed for %s/%s — trying spec.rolloutAfter", kind, name)
			}
		}
		if !ok {
			if err := patchRolloutAfter(gvr, name); err == nil {
				logx.Log("workload-rollout: set spec.rolloutAfter on %s/%s", kind, name)
			} else {
				logx.Warn("workload-rollout: failed to set spec.rolloutAfter on %s/%s: %v", kind, name, err)
			}
		}
	}
	for _, kcp := range kcps {
		rollout(kcpGVR, "kubeadmcontrolplane", kcp)
	}
	for _, md := range mds {
		rollout(mdGVR, "machinedeployment", md)
	}

	annPatch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{"reconcile.cluster.x-k8s.io/force-rollout":"%s"}}}`, now))
	_, _ = cli.Dynamic.Resource(capiClusterGVR).Namespace(cfg.WorkloadClusterNamespace).
		Patch(bg, cfg.WorkloadClusterName, types.MergePatchType, annPatch, metav1.PatchOptions{})

	pms := listNames(pmGVR)
	for _, pm := range pms {
		pmAnn := []byte(fmt.Sprintf(`{"metadata":{"annotations":{"reconcile.cluster.x-k8s.io/request":"%s"}}}`,
			time.Now().UTC().Format("2006-01-02T15:04:05Z")))
		_, _ = cli.Dynamic.Resource(pmGVR).Namespace(cfg.WorkloadClusterNamespace).
			Patch(bg, pm, types.MergePatchType, pmAnn, metav1.PatchOptions{})
	}
}

// unstructuredSlice fetches a []interface{} at path; returns nil on miss.
func unstructuredSlice(obj map[string]interface{}, path ...string) ([]interface{}, bool, error) {
	cur := obj
	for i, p := range path {
		v, ok := cur[p]
		if !ok || v == nil {
			return nil, false, nil
		}
		if i == len(path)-1 {
			s, ok := v.([]interface{})
			return s, ok, nil
		}
		next, ok := v.(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	return nil, false, nil
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

