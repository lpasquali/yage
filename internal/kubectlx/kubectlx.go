// Package kubectlx ports thin wrappers around kubectl that the rest of the
// bootstrap calls repeatedly.
//
// Bash source map (bootstrap-capi.sh):
//   - contains_line                                         ~L1130-1139
//   - _resolve_bootstrap_kubectl_context                    ~L821-835
//   - wait_for_service_endpoint                             ~L2059-2070
//   - apply_workload_cluster_manifest_to_management_cluster ~L2075-2154
//   - warn_regenerated_capi_manifest_immutable_risk         ~L2604-2613
package kubectlx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// ResolveBootstrapContext ports _resolve_bootstrap_kubectl_context.
// Returns "kind-<KIND_CLUSTER_NAME>" if that context exists, otherwise
// the current context if it is a kind context, otherwise "" + false.
func ResolveBootstrapContext(cfg *config.Config) (string, bool) {
	if !shell.CommandExists("kubectl") {
		return "", false
	}
	name := cfg.KindClusterName
	if name == "" {
		name = "capi-provisioner"
	}
	want := "kind-" + name
	out, _, _ := shell.Capture("kubectl", "config", "get-contexts", "-o", "name")
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if ln == want {
			return want, true
		}
	}
	cur, _, _ := shell.Capture("kubectl", "config", "current-context")
	cur = strings.TrimSpace(strings.ReplaceAll(cur, "\r", ""))
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
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, _, _ := shell.Capture("kubectl", "get", "endpoints", svc, "-n", ns,
			"-o", "jsonpath={.subsets[*].addresses[*].ip}")
		if strings.TrimSpace(out) != "" {
			logx.Log("Webhook endpoint ready: %s/%s", ns, svc)
			return
		}
		time.Sleep(5 * time.Second)
	}
	logx.Die("Timed out waiting for webhook endpoint: %s/%s", ns, svc)
}

// ApplyWorkloadManifestToManagementCluster ports
// apply_workload_cluster_manifest_to_management_cluster.
//
// Splits the multi-doc YAML on "\n---\n" boundaries; for each document:
//  1. runs `kubectl create --dry-run=client -o json -f -` to discover kind/
//     apiVersion/name/namespace;
//  2. if the doc is a ProxmoxCluster and a non-deleting object of that name
//     exists, skips the apply (avoids a no-op PATCH through the capmox
//     mutating webhook which has been known to flake with connection-refused
//     on reruns);
//  3. otherwise runs `kubectl apply -f -` with the doc on stdin.
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
	ctx := "kind-" + cfg.KindClusterName

	// Split on "\n---\n" boundaries, trim, drop empty shards. Matches the
	// exact split used in the bash inline Python.
	parts := strings.Split(string(raw), "\n---\n")
	for _, p := range parts {
		doc := strings.TrimSpace(p)
		if doc == "" {
			continue
		}
		doc += "\n"

		// 1. dry-run to get metadata.
		dry := exec.Command("kubectl", "--context", ctx, "create",
			"--dry-run=client", "-o", "json", "-f", "-")
		dry.Stdin = strings.NewReader(doc)
		var dryOut, dryErr bytes.Buffer
		dry.Stdout = &dryOut
		dry.Stderr = &dryErr
		if err := dry.Run(); err != nil {
			// Fall through to a real apply, letting kubectl report the
			// actual error. Matches bash behaviour (the dry-run stderr is
			// echoed to stderr, then apply runs anyway).
			if dryErr.Len() > 0 {
				_, _ = os.Stderr.Write(dryErr.Bytes())
			}
			if err := shell.Pipe(doc, "kubectl", "--context", ctx, "apply", "-f", "-"); err != nil {
				return err
			}
			continue
		}

		var obj struct {
			Kind       string `json:"kind"`
			APIVersion string `json:"apiVersion"`
			Metadata   struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(dryOut.Bytes(), &obj); err != nil {
			return fmt.Errorf("parse dry-run output: %w", err)
		}
		name := obj.Metadata.Name
		ns := obj.Metadata.Namespace
		if ns == "" {
			ns = "default"
		}

		// 2. skip ProxmoxCluster if already reconciled.
		if obj.Kind == "ProxmoxCluster" &&
			strings.Contains(obj.APIVersion, "infrastructure.cluster.x-k8s.io") &&
			name != "" {
			out, _, err := shell.Capture("kubectl", "--context", ctx, "get", "proxmoxcluster",
				name, "-n", ns, "-o", "jsonpath={.metadata.deletionTimestamp}")
			if err == nil && strings.TrimSpace(out) == "" {
				fmt.Fprintf(os.Stderr,
					"Skipping apply for existing ProxmoxCluster %s/%s "+
						"(already reconciled; avoids redundant webhook/patch).\n",
					ns, name)
				continue
			}
		}

		// 3. apply.
		if err := shell.Pipe(doc, "kubectl", "--context", ctx, "apply", "-f", "-"); err != nil {
			return err
		}
	}
	return nil
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
	if !shell.CommandExists("kubectl") {
		return
	}
	ctx := "kind-" + cfg.KindClusterName
	out, _, _ := shell.Capture("kubectl", "config", "get-contexts", "-o", "name")
	found := false
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if ln == ctx {
			found = true
			break
		}
	}
	if !found {
		return
	}
	// kubectl get cluster — we only care about exit status, not output.
	c := exec.Command("kubectl", "--context", ctx, "get", "cluster",
		cfg.WorkloadClusterName, "-n", cfg.WorkloadClusterNamespace)
	c.Stdout, c.Stderr = nil, nil
	if err := c.Run(); err != nil {
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

