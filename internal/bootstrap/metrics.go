package bootstrap

import (
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/kindsync"
	"github.com/lpasquali/bootstrap-capi/internal/kubectlx"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// InstallMetricsServerOnKindManagement ports
// install_metrics_server_on_kind_management_cluster (L4205-L4233).
// Applies the upstream components.yaml to the kind context when no
// metrics-server Deployment exists, then patches --kubelet-insecure-tls
// + --kubelet-preferred-address-types when the flag is missing.
func InstallMetricsServerOnKindManagement(cfg *config.Config) {
	if !cfg.EnableMetricsServer {
		return
	}
	ctx := "kind-" + cfg.KindClusterName
	if !contextExists(ctx) {
		return
	}
	url := cfg.MetricsServerManifestURL
	if url == "" {
		url = "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
	}
	present := exec.Command("kubectl", "--context", ctx,
		"get", "deployment", "metrics-server", "-n", "kube-system")
	present.Stdout, present.Stderr = nil, nil
	if err := present.Run(); err != nil {
		logx.Log("Installing metrics-server on %s (kubectl top / HPA)...", ctx)
		body, fetchErr := fetchAll(url)
		if fetchErr != nil {
			logx.Die("Failed to apply metrics-server manifest from %s", url)
		}
		if err := shell.Pipe(body, "kubectl", "--context", ctx, "apply", "-f", "-"); err != nil {
			logx.Die("Failed to apply metrics-server manifest from %s", url)
		}
	} else {
		logx.Log("metrics-server already installed on %s.", ctx)
	}

	args, _, _ := shell.Capture("kubectl", "--context", ctx, "get", "deploy", "metrics-server",
		"-n", "kube-system", "-o", "jsonpath={.spec.template.spec.containers[0].args[*]}")
	if !strings.Contains(args, "kubelet-insecure-tls") {
		logx.Log("Patching metrics-server for kind kubelet access (--kubelet-insecure-tls)...")
		patch := `[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}, {"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-preferred-address-types=InternalIP,Hostname"}]`
		if err := shell.Run("kubectl", "--context", ctx, "-n", "kube-system",
			"patch", "deployment", "metrics-server", "--type=json", "-p="+patch); err != nil {
			logx.Die("Failed to patch metrics-server for kind.")
		}
	}
	if err := shell.Run("kubectl", "--context", ctx, "rollout", "status",
		"deployment/metrics-server", "-n", "kube-system", "--timeout=180s"); err != nil {
		logx.Warn("metrics-server rollout not ready within 180s — kubectl top may fail until it stabilizes.")
	}
	logx.Log("metrics-server configured on %s (e.g. kubectl --context %s top nodes).", ctx, ctx)
}

// InstallMetricsServerOnWorkload ports install_metrics_server_on_workload_cluster
// (L4236-L4275). Materializes the workload kubeconfig from kind and runs
// the same install+patch flow against that kubeconfig. Skips when the
// workload kubeconfig Secret is missing (cluster not yet up).
func InstallMetricsServerOnWorkload(cfg *config.Config) {
	if !cfg.EnableWorkloadMetricsServer {
		return
	}
	mgmt := "kind-" + cfg.KindClusterName
	if !contextExists(mgmt) {
		return
	}
	if err := shell.Run("kubectl", "--context", mgmt, "-n", cfg.WorkloadClusterNamespace,
		"get", "secret", cfg.WorkloadClusterName+"-kubeconfig"); err != nil {
		logx.Warn("workload metrics-server: %s-kubeconfig not in %s — skipping (cluster not up?).",
			cfg.WorkloadClusterName, cfg.WorkloadClusterNamespace)
		return
	}
	kcfg, err := writeWorkloadKubeconfig(cfg, mgmt)
	if err != nil {
		return
	}
	defer os.Remove(kcfg)

	url := cfg.MetricsServerManifestURL
	if url == "" {
		url = "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml"
	}
	present := exec.Command("kubectl", "--kubeconfig", kcfg,
		"get", "deployment", "metrics-server", "-n", "kube-system")
	present.Stdout, present.Stderr = nil, nil
	if err := present.Run(); err != nil {
		logx.Log("Installing metrics-server on CAPI workload cluster %s (separate from kind)...", cfg.WorkloadClusterName)
		body, fetchErr := fetchAll(url)
		if fetchErr != nil {
			logx.Die("Failed to apply metrics-server on workload from %s", url)
		}
		if err := shell.Pipe(body, "kubectl", "--kubeconfig", kcfg, "apply", "-f", "-"); err != nil {
			logx.Die("Failed to apply metrics-server on workload from %s", url)
		}
	} else {
		logx.Log("metrics-server already on workload cluster %s.", cfg.WorkloadClusterName)
	}

	if isTrue(cfg.WorkloadMetricsServerInsecureTLS) {
		args, _, _ := shell.Capture("kubectl", "--kubeconfig", kcfg, "get", "deploy", "metrics-server",
			"-n", "kube-system", "-o", "jsonpath={.spec.template.spec.containers[0].args[*]}")
		if !strings.Contains(args, "kubelet-insecure-tls") {
			logx.Log("Patching workload metrics-server (--kubelet-insecure-tls) — set WORKLOAD_METRICS_SERVER_INSECURE_TLS=false if kubelet has proper certs.")
			patch := `[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}]`
			if err := shell.Run("kubectl", "--kubeconfig", kcfg, "-n", "kube-system",
				"patch", "deployment", "metrics-server", "--type=json", "-p="+patch); err != nil {
				logx.Die("Failed to patch metrics-server on workload.")
			}
		}
	}
	if err := shell.Run("kubectl", "--kubeconfig", kcfg, "rollout", "status",
		"deployment/metrics-server", "-n", "kube-system", "--timeout=180s"); err != nil {
		logx.Warn("Workload metrics-server not ready in 180s — kubectl top on workload may still fail until it stabilizes.")
	}
	logx.Log("Workloads on %s can use kubectl top / HPA (Resource Metrics) after metrics-server is healthy.", cfg.WorkloadClusterName)
}

// contextExists returns true when `kubectl config get-contexts` lists ctx.
func contextExists(ctx string) bool {
	out, _, _ := shell.Capture("kubectl", "config", "get-contexts", "-o", "name")
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if ln == ctx {
			return true
		}
	}
	return false
}

// fetchAll is a minimal `curl -fsSL URL` equivalent that returns the
// response body.
func fetchAll(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &http.ProtocolError{ErrorString: "HTTP " + resp.Status}
	}
	buf := make([]byte, 0, 1<<16)
	for {
		tmp := make([]byte, 4096)
		n, err := resp.Body.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return string(buf), nil
}

// writeWorkloadKubeconfig is the same shape as kindsync's helper, but
// this file belongs to the bootstrap orchestrator so we don't introduce
// a kindsync → bootstrap dependency cycle. Duplication is minimal.
func writeWorkloadKubeconfig(cfg *config.Config, ctx string) (string, error) {
	// Reuse the helper via kubectlx.ResolveBootstrapContext + shell.Capture —
	// here we delegate to the kindsync-internal shape by mirroring its logic:
	// fetch, b64-decode, write 0600 tmp file.
	out, _, _ := shell.Capture(
		"kubectl", "--context", ctx,
		"-n", cfg.WorkloadClusterNamespace,
		"get", "secret", cfg.WorkloadClusterName+"-kubeconfig",
		"-o", "jsonpath={.data.value}",
	)
	if strings.TrimSpace(out) == "" {
		return "", os.ErrNotExist
	}
	data, err := base64Decode(out)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "workload-kubeconfig-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// isTrue wraps sysinfo.IsTrue without adding another import line in
// multiple files.
func isTrue(s string) bool { return strings.EqualFold(s, "true") || s == "1" || strings.EqualFold(s, "yes") || strings.EqualFold(s, "on") }

// Sentinel use to stop goimports from removing kubectlx / kindsync when
// the file grows and we wire RecreateResyncCapmox or similar here.
var _ = kubectlx.ResolveBootstrapContext
var _ = kindsync.SyncBootstrapConfigToKind
