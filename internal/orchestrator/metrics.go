package orchestrator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/platform/kubectl"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// InstallMetricsServerOnKindManagement ports
// install_metrics_server_on_kind_management_cluster (L4205-L4233).
func InstallMetricsServerOnKindManagement(cfg *config.Config) {
	if !cfg.EnableMetricsServer {
		return
	}
	ctxName := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(ctxName) {
		return
	}
	cli, err := k8sclient.ForContext(ctxName)
	if err != nil {
		logx.Warn("metrics-server: cannot build client for %s: %v", ctxName, err)
		return
	}
	url := cfg.MetricsServerManifestURL
	if url == "" {
		url = "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
	}
	bg := context.Background()
	if !deploymentExists(cli, bg, "kube-system", "metrics-server") {
		logx.Log("Installing metrics-server on %s (kubectl top / HPA)...", ctxName)
		body, fetchErr := fetchAll(url)
		if fetchErr != nil {
			logx.Die("Failed to apply metrics-server manifest from %s", url)
		}
		if err := cli.ApplyMultiDocYAML(bg, []byte(body)); err != nil {
			logx.Die("Failed to apply metrics-server manifest from %s: %v", url, err)
		}
	} else {
		logx.Log("metrics-server already installed on %s.", ctxName)
	}

	if !deploymentHasArg(cli, bg, "kube-system", "metrics-server", "kubelet-insecure-tls") {
		logx.Log("Patching metrics-server for kind kubelet access (--kubelet-insecure-tls)...")
		patch := `[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}, {"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-preferred-address-types=InternalIP,Hostname"}]`
		if _, err := cli.Typed.AppsV1().Deployments("kube-system").
			Patch(bg, "metrics-server", types.JSONPatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			logx.Die("Failed to patch metrics-server for kind: %v", err)
		}
	}
	if err := waitDeploymentReady(cli, "kube-system", "metrics-server", 180*time.Second); err != nil {
		logx.Warn("metrics-server rollout not ready within 180s — kubectl top may fail until it stabilizes.")
	}
	logx.Log("metrics-server configured on %s (e.g. kubectl --context %s top nodes).", ctxName, ctxName)
}

// InstallMetricsServerOnWorkload ports install_metrics_server_on_workload_cluster
// (L4236-L4275).
func InstallMetricsServerOnWorkload(cfg *config.Config) {
	if !cfg.EnableWorkloadMetricsServer {
		return
	}
	mgmt := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(mgmt) {
		return
	}
	mgmtCli, err := k8sclient.ForContext(mgmt)
	if err != nil {
		return
	}
	bg := context.Background()
	if _, err := mgmtCli.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
		Get(bg, cfg.WorkloadClusterName+"-kubeconfig", metav1.GetOptions{}); err != nil {
		logx.Warn("workload metrics-server: %s-kubeconfig not in %s — skipping (cluster not up?).",
			cfg.WorkloadClusterName, cfg.WorkloadClusterNamespace)
		return
	}
	kcfg, err := writeWorkloadKubeconfig(cfg, mgmt)
	if err != nil {
		return
	}
	defer os.Remove(kcfg)

	cli, err := k8sclient.ForKubeconfigFile(kcfg)
	if err != nil {
		logx.Warn("workload metrics-server: cannot build client: %v", err)
		return
	}
	url := cfg.MetricsServerManifestURL
	if url == "" {
		url = "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
	}
	if !deploymentExists(cli, bg, "kube-system", "metrics-server") {
		logx.Log("Installing metrics-server on CAPI workload cluster %s (separate from kind)...", cfg.WorkloadClusterName)
		body, fetchErr := fetchAll(url)
		if fetchErr != nil {
			logx.Die("Failed to apply metrics-server on workload from %s", url)
		}
		if err := cli.ApplyMultiDocYAML(bg, []byte(body)); err != nil {
			logx.Die("Failed to apply metrics-server on workload from %s: %v", url, err)
		}
	} else {
		logx.Log("metrics-server already on workload cluster %s.", cfg.WorkloadClusterName)
	}

	if isTrue(cfg.WorkloadMetricsServerInsecureTLS) &&
		!deploymentHasArg(cli, bg, "kube-system", "metrics-server", "kubelet-insecure-tls") {
		logx.Log("Patching workload metrics-server (--kubelet-insecure-tls) — set WORKLOAD_METRICS_SERVER_INSECURE_TLS=false if kubelet has proper certs.")
		patch := `[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}]`
		if _, err := cli.Typed.AppsV1().Deployments("kube-system").
			Patch(bg, "metrics-server", types.JSONPatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			logx.Die("Failed to patch metrics-server on workload: %v", err)
		}
	}
	if err := waitDeploymentReady(cli, "kube-system", "metrics-server", 180*time.Second); err != nil {
		logx.Warn("Workload metrics-server not ready in 180s — kubectl top on workload may still fail until it stabilizes.")
	}
	logx.Log("Workloads on %s can use kubectl top / HPA (Resource Metrics) after metrics-server is healthy.", cfg.WorkloadClusterName)
}

// deploymentExists returns true when the deployment is present (errors other
// than NotFound are treated as "missing" so the install path runs).
func deploymentExists(cli *k8sclient.Client, ctx context.Context, ns, name string) bool {
	_, err := cli.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	return err == nil
}

// deploymentHasArg checks containers[0].args for a substring.
func deploymentHasArg(cli *k8sclient.Client, ctx context.Context, ns, name, needle string) bool {
	d, err := cli.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil || d == nil || len(d.Spec.Template.Spec.Containers) == 0 {
		return false
	}
	for _, a := range d.Spec.Template.Spec.Containers[0].Args {
		if strings.Contains(a, needle) {
			return true
		}
	}
	return false
}

// waitDeploymentReady polls Available=True with a timeout.
func waitDeploymentReady(cli *k8sclient.Client, ns, name string, timeout time.Duration) error {
	bg := context.Background()
	return k8sclient.PollUntil(bg, 3*time.Second, timeout, func(ctx context.Context) (bool, error) {
		d, err := cli.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		for _, c := range d.Status.Conditions {
			if string(c.Type) == "Available" && c.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

// fetchAll is a minimal `curl -fsSL URL` equivalent that returns the response body.
func fetchAll(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// writeWorkloadKubeconfig fetches the workload kubeconfig from the management
// kind cluster Secret and materializes it to a 0600 temp file. Caller
// removes the file via os.Remove(path).
func writeWorkloadKubeconfig(cfg *config.Config, ctxName string) (string, error) {
	cli, err := k8sclient.ForContext(ctxName)
	if err != nil {
		return "", err
	}
	bg := context.Background()
	sec, err := cli.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
		Get(bg, cfg.WorkloadClusterName+"-kubeconfig", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	body, ok := sec.Data["value"]
	if !ok || len(body) == 0 {
		return "", os.ErrNotExist
	}
	path, _, err := k8sclient.WriteTempKubeconfig("workload-kubeconfig", body)
	if err != nil {
		return "", err
	}
	return path, nil
}

// contextExists is a thin alias kept so siblings in this package can stay
// terse; delegates to k8sclient.ContextExists.
func contextExists(ctx string) bool { return k8sclient.ContextExists(ctx) }

// isTrue wraps a small subset of sysinfo.IsTrue for the orchestrator.
func isTrue(s string) bool {
	return strings.EqualFold(s, "true") || s == "1" || strings.EqualFold(s, "yes") || strings.EqualFold(s, "on")
}

// Keep these symbols used so goimports doesn't drop kubectlx/kindsync if
// future code in this file adds back the helpers.
var _ = kubectl.ResolveBootstrapContext
var _ = kindsync.SyncBootstrapConfigToKind
