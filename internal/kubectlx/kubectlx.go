// Package kubectlx ports thin wrappers around kubectl that the rest of the
// bootstrap calls repeatedly.
//
// Bash source map (the original bash port):
//   - contains_line                                         ~L1130-1139
//   - _resolve_bootstrap_kubectl_context                    ~L821-835
//   - wait_for_service_endpoint                             ~L2059-2070
//   - apply_workload_cluster_manifest_to_management_cluster ~L2075-2154
//   - warn_regenerated_capi_manifest_immutable_risk         ~L2604-2613
//
// All kubectl shell-outs in this file have been migrated to the in-process
// k8sclient (client-go) layer.
package kubectlx

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/k8sclient"
	"github.com/lpasquali/yage/internal/logx"
)

// ResolveBootstrapContext ports _resolve_bootstrap_kubectl_context.
// Returns "kind-<KIND_CLUSTER_NAME>" if that context exists, otherwise
// the current context if it is a kind context, otherwise "" + false.
func ResolveBootstrapContext(cfg *config.Config) (string, bool) {
	name := cfg.KindClusterName
	if name == "" {
		name = "capi-provisioner"
	}
	want := "kind-" + name
	for _, ln := range k8sclient.ListContexts() {
		if ln == want {
			return want, true
		}
	}
	cur := strings.TrimSpace(k8sclient.CurrentContext())
	if strings.HasPrefix(cur, "kind-") {
		return cur, true
	}
	return "", false
}

// WaitForServiceEndpoint ports wait_for_service_endpoint. Polls every 5s up
// to timeout (default 300s) for at least one endpoint address on the
// named Service. Dies on timeout, matching bash.
func WaitForServiceEndpoint(ns, svc string, timeout time.Duration) {
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	cli, err := k8sclient.ForCurrent()
	if err != nil {
		logx.Die("WaitForServiceEndpoint: load kubeconfig: %v", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ep, err := cli.Typed.CoreV1().Endpoints(ns).Get(context.Background(), svc, metav1.GetOptions{})
		if err == nil {
			for _, sub := range ep.Subsets {
				if len(sub.Addresses) > 0 {
					logx.Log("Webhook endpoint ready: %s/%s", ns, svc)
					return
				}
			}
		}
		time.Sleep(5 * time.Second)
	}
	logx.Die("Timed out waiting for webhook endpoint: %s/%s", ns, svc)
}

// ApplyWorkloadManifestToManagementCluster ports
// apply_workload_cluster_manifest_to_management_cluster.
//
// Splits the multi-doc YAML on "\n---\n" boundaries; for each document,
// when it is a ProxmoxCluster and a non-deleting object of that name
// already exists, skips the apply (avoids a no-op PATCH through the
// capmox mutating webhook which has been known to flake with
// connection-refused on reruns); otherwise server-side-applies the doc
// through the dynamic client.
//
// Returns an error on the first failure, matching the bash `|| return $rc`
// semantics.
func ApplyWorkloadManifestToManagementCluster(cfg *config.Config, manifestPath string) error {
	if _, err := os.Stat(manifestPath); err != nil {
		logx.Die("Manifest not found: %s", manifestPath)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return fmt.Errorf("load context %s: %w", kctx, err)
	}

	ctx := context.Background()

	// Split on "\n---\n" boundaries, trim, drop empty shards. Matches the
	// exact split used in the bash inline Python.
	for _, p := range strings.Split(string(raw), "\n---\n") {
		doc := strings.TrimSpace(p)
		if doc == "" {
			continue
		}

		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(doc), &u.Object); err != nil {
			return fmt.Errorf("parse manifest doc: %w", err)
		}
		if u.Object == nil || u.GetKind() == "" {
			continue
		}

		gvk := u.GroupVersionKind()
		name := u.GetName()
		ns := u.GetNamespace()
		if ns == "" {
			ns = "default"
		}

		// Skip ProxmoxCluster if already reconciled.
		if gvk.Kind == "ProxmoxCluster" &&
			strings.Contains(gvk.Group, "infrastructure.cluster.x-k8s.io") &&
			name != "" {
			if existing, err := getResource(ctx, cli, gvk, ns, name); err == nil && existing != nil {
				if existing.GetDeletionTimestamp() == nil {
					fmt.Fprintf(os.Stderr,
						"Skipping apply for existing ProxmoxCluster %s/%s "+
							"(already reconciled; avoids redundant webhook/patch).\n",
						ns, name)
					continue
				}
			}
		}

		if err := cli.ApplyUnstructured(ctx, u); err != nil {
			return err
		}
	}
	return nil
}

// getResource returns the named object via the dynamic client, mapping
// the GVK to its REST resource. Returns (nil, nil) when the object is
// absent.
func getResource(ctx context.Context, cli *k8sclient.Client, gvk schema.GroupVersionKind, ns, name string) (*unstructured.Unstructured, error) {
	mapping, err := cli.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}
	obj, err := cli.Dynamic.Resource(mapping.Resource).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8sclient.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return obj, nil
}

// WarnRegeneratedManifestImmutableRisk ports
// warn_regenerated_capi_manifest_immutable_risk. Only warns when
// BOOTSTRAP_CLUSTERCTL_REGENERATED_MANIFEST is true, the suppression env
// is false, and the Cluster resource exists in the management cluster.
func WarnRegeneratedManifestImmutableRisk(cfg *config.Config) {
	if cfg.BootstrapSkipImmutableManifestWarning {
		return
	}
	if !cfg.BootstrapClusterctlRegeneratedManifest {
		return
	}
	kctx := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(kctx) {
		return
	}
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return
	}
	gvk := schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta2",
		Kind:    "Cluster",
	}
	// Try v1beta1 first, then v1beta2 — RESTMapping resolves whichever
	// exists.
	mapping, err := cli.Mapper.RESTMapping(gvk.GroupKind())
	if err != nil {
		return
	}
	if _, err := cli.Dynamic.Resource(mapping.Resource).Namespace(cfg.WorkloadClusterNamespace).
		Get(context.Background(), cfg.WorkloadClusterName, metav1.GetOptions{}); err != nil {
		return
	}
	logx.Warn(
		"Regenerated workload manifest (clusterctl) while Cluster %s/%s already exists. "+
			"If you changed immutable fields (pod/service CIDRs, cluster name, infra API, "+
			"control plane wiring, …), delete that Cluster and wait for cleanup before "+
			"re-applying; otherwise kubectl apply may error or ignore changes. "+
			"Set BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING=true to hide this.",
		cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName,
	)
}
