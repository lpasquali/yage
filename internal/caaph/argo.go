package caaph

import (
	"fmt"
	"strings"
	"time"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// ApplyWorkloadArgoHelmProxies ports caaph_apply_workload_argo_helm_proxies
// (L5698-L5761). Installs Argo CD Operator + ArgoCD CR via CAAPH by
// applying a HelmChartProxy for the argoproj argocd-apps chart; the root
// Application name equals cfg.WorkloadClusterName.
//
// The Argo CD Operator install itself is delegated to the caller via
// `installOperator`, which ports apply_workload_argocd_operator_and_argocd_cr —
// that keeps the caller in control of the kubeconfig plumbing.
func ApplyWorkloadArgoHelmProxies(cfg *config.Config, installOperator func()) {
	if !cfg.IsWorkloadGitopsCaaphMode() {
		return
	}
	if !cfg.WorkloadArgoCDEnabled {
		return
	}
	mctx := "kind-" + cfg.KindClusterName
	if cfg.WorkloadAppOfAppsGitURL == "" {
		logx.Die("WORKLOAD_APP_OF_APPS_GIT_URL is required in caaph mode (validated at start).")
	}
	logx.Log("Argo CD on the workload: Argo CD Operator + ArgoCD CR (in-bootstrap), then CAAPH argocd-apps (root Application name %s).",
		cfg.WorkloadClusterName)

	if installOperator != nil {
		installOperator()
	}

	u := strings.ReplaceAll(cfg.WorkloadAppOfAppsGitURL, `'`, `'"'"'`)
	p := strings.ReplaceAll(cfg.WorkloadAppOfAppsGitPath, `'`, `'"'"'`)
	r := strings.ReplaceAll(cfg.WorkloadAppOfAppsGitRef, `'`, `'"'"'`)
	argoNS := cfg.WorkloadArgoCDNamespace
	if argoNS == "" {
		argoNS = "argocd"
	}
	wlNS := cfg.WorkloadClusterNamespace
	if wlNS == "" {
		wlNS = "default"
	}

	var sb strings.Builder
	fmt.Fprintln(&sb, "apiVersion: addons.cluster.x-k8s.io/v1alpha1")
	fmt.Fprintln(&sb, "kind: HelmChartProxy")
	fmt.Fprintln(&sb, "metadata:")
	fmt.Fprintf(&sb, "  name: %s-caaph-argocd-apps\n", cfg.WorkloadClusterName)
	fmt.Fprintf(&sb, "  namespace: %s\n", wlNS)
	fmt.Fprintln(&sb, "spec:")
	fmt.Fprintln(&sb, "  clusterSelector:")
	fmt.Fprintln(&sb, "    matchLabels:")
	fmt.Fprintln(&sb, "      caaph: enabled")
	fmt.Fprintln(&sb, "  chartName: argocd-apps")
	fmt.Fprintln(&sb, "  repoURL: https://argoproj.github.io/argo-helm")
	fmt.Fprintf(&sb, "  namespace: %s\n", argoNS)
	fmt.Fprintln(&sb, "  options:")
	fmt.Fprintln(&sb, "    wait: true")
	fmt.Fprintln(&sb, "    waitForJobs: true")
	fmt.Fprintln(&sb, "    timeout: 20m0s")
	fmt.Fprintln(&sb, "    install:")
	fmt.Fprintln(&sb, "      createNamespace: true")
	fmt.Fprintln(&sb, "  valuesTemplate: |")
	fmt.Fprintln(&sb, "    applications:")
	fmt.Fprintf(&sb, "      %q:\n", cfg.WorkloadClusterName)
	fmt.Fprintf(&sb, "        namespace: %s\n", argoNS)
	fmt.Fprintln(&sb, "        finalizers:")
	fmt.Fprintln(&sb, "          - resources-finalizer.argocd.argoproj.io")
	fmt.Fprintln(&sb, "        project: default")
	fmt.Fprintln(&sb, "        source:")
	fmt.Fprintf(&sb, "          repoURL: '%s'\n", u)
	fmt.Fprintf(&sb, "          path: '%s'\n", p)
	fmt.Fprintf(&sb, "          targetRevision: '%s'\n", r)
	fmt.Fprintln(&sb, "        destination:")
	fmt.Fprintln(&sb, "          server: https://kubernetes.default.svc")
	fmt.Fprintf(&sb, "          namespace: %s\n", argoNS)
	fmt.Fprintln(&sb, "        syncPolicy:")
	fmt.Fprintln(&sb, "          automated:")
	fmt.Fprintln(&sb, "            prune: true")
	fmt.Fprintln(&sb, "            selfHeal: true")
	fmt.Fprintln(&sb, "          syncOptions:")
	fmt.Fprintln(&sb, "            - CreateNamespace=true")

	if err := shell.Pipe(sb.String(), "kubectl", "--context", mctx, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to apply HelmChartProxy (argocd-apps / app-of-apps).")
	}
	logx.Log("Applied HelmChartProxy %s-caaph-argocd-apps (root app-of-apps Application name: %s; repo %s).",
		cfg.WorkloadClusterName, cfg.WorkloadClusterName, cfg.WorkloadAppOfAppsGitURL)
}

// WaitWorkloadArgoCDServer ports caaph_wait_workload_argocd_server.
func WaitWorkloadArgoCDServer(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	if !cfg.IsWorkloadGitopsCaaphMode() || !cfg.WorkloadArgoCDEnabled {
		return
	}
	wk, err := writeWorkloadKubeconfig()
	if err != nil || wk == "" {
		return
	}
	defer removePath(wk)
	ns := cfg.WorkloadArgoCDNamespace
	if ns == "" {
		ns = "argocd"
	}
	logx.Log("Waiting for Argo CD server (workload %s, ns %s)…", cfg.WorkloadClusterName, ns)
	for i := 0; i < 120; i++ {
		if err := shell.Run("kubectl", "--kubeconfig", wk, "-n", ns,
			"get", "deploy", "argocd-server"); err == nil {
			if err := shell.Run("kubectl", "--kubeconfig", wk,
				"wait", "-n", ns, "deploy/argocd-server",
				"--for=condition=Available", "--timeout=2m"); err == nil {
				logx.Log("Argo CD server is available on the workload cluster.")
				return
			}
		}
		time.Sleep(5 * time.Second)
	}
	logx.Warn("Argo CD server did not become Available in time — check HelmReleaseProxy and pods on the workload.")
}

// LogWorkloadArgoAppsStatus ports caaph_log_workload_argo_apps_status.
func LogWorkloadArgoAppsStatus(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	if !cfg.IsWorkloadGitopsCaaphMode() || !cfg.WorkloadArgoCDEnabled {
		return
	}
	wk, err := writeWorkloadKubeconfig()
	if err != nil || wk == "" {
		return
	}
	defer removePath(wk)
	ns := cfg.WorkloadArgoCDNamespace
	if ns == "" {
		ns = "argocd"
	}
	logx.Log("App-of-apps: this script only waits for argocd-server — it does not wait for the argocd-apps Helm install, Git sync, or platform Deployments. Check sync below.")
	if err := shell.Run("kubectl", "--kubeconfig", wk, "-n", ns,
		"get", "applications.argoproj.io", "-o", "name"); err != nil {
		logx.Warn("Could not list Application resources in %s (CRD or RBAC) — is Argo fully installed on the workload?", ns)
		return
	}
	_ = shell.Run("kubectl", "--kubeconfig", wk, "-n", ns, "get", "applications.argoproj.io")
	if err := shell.Run("kubectl", "--kubeconfig", wk, "-n", ns,
		"get", "application/"+cfg.WorkloadClusterName); err != nil {
		logx.Warn("No root Application %s in %s yet. Often: CAAPH is still running the 'argocd-apps' install on the workload, the HelmChartProxy does not match the cluster (CAPI Cluster needs label caaph=enabled), or the chart failed; check: kubectl get helmchartproxy -A, controller logs, and Argo/Helm on the workload.", cfg.WorkloadClusterName, ns)
		return
	}
	_ = shell.Run("kubectl", "--kubeconfig", wk, "-n", ns,
		"get", "application/"+cfg.WorkloadClusterName,
		"-o", "custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status")
	sync, _, _ := shell.Capture("kubectl", "--kubeconfig", wk, "-n", ns,
		"get", "application/"+cfg.WorkloadClusterName,
		"-o", "jsonpath={.status.sync.status}")
	sync = strings.TrimSpace(sync)
	if sync != "" && sync != "Synced" {
		logx.Warn("Root app %s is not Synced yet (%s) — from a machine with workload kube: argocd app sync %s (or use the Argo CD UI), then watch child apps.",
			cfg.WorkloadClusterName, sync, cfg.WorkloadClusterName)
	}
}

func removePath(p string) {
	if p == "" {
		return
	}
	_ = shell.Run("sh", "-c", "rm -f -- "+shellQuote(p))
}

func shellQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}
