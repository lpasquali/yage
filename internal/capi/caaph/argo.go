// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package caaph

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/airgap"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// ApplyWorkloadArgoHelmProxies installs Argo CD Operator + ArgoCD
// CR via CAAPH by applying a HelmChartProxy for the argoproj
// argocd-apps chart; the root Application name equals
// cfg.WorkloadClusterName.
//
// The Argo CD Operator install itself is delegated to the caller
// via `installOperator` so the caller stays in control of the
// kubeconfig plumbing.
func ApplyWorkloadArgoHelmProxies(cfg *config.Config, installOperator func()) {
	if !cfg.IsWorkloadGitopsCaaphMode() {
		return
	}
	if !cfg.ArgoCD.WorkloadEnabled {
		return
	}
	mctx := "kind-" + cfg.KindClusterName
	if cfg.ArgoCD.AppOfAppsGitURL == "" {
		logx.Die("WORKLOAD_APP_OF_APPS_GIT_URL is required in caaph mode (validated at start).")
	}
	logx.Log("Argo CD on the workload: Argo CD Operator + ArgoCD CR (in-bootstrap), then CAAPH argocd-apps (root Application name %s).",
		cfg.WorkloadClusterName)

	if installOperator != nil {
		installOperator()
	}

	u := strings.ReplaceAll(cfg.ArgoCD.AppOfAppsGitURL, `'`, `'"'"'`)
	p := strings.ReplaceAll(cfg.ArgoCD.AppOfAppsGitPath, `'`, `'"'"'`)
	r := strings.ReplaceAll(cfg.ArgoCD.AppOfAppsGitRef, `'`, `'"'"'`)
	argoNS := cfg.ArgoCD.WorkloadNamespace
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
	fmt.Fprintf(&sb, "  repoURL: %s\n", airgap.RewriteHelmRepo("https://argoproj.github.io/argo-helm"))
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

	cli, err := k8sclient.ForContext(mctx)
	if err != nil {
		logx.Die("Failed to load management context %s: %v", mctx, err)
	}
	if err := cli.ApplyYAML(context.Background(), []byte(sb.String())); err != nil {
		logx.Die("Failed to apply HelmChartProxy (argocd-apps / app-of-apps): %v", err)
	}
	logx.Log("Applied HelmChartProxy %s-caaph-argocd-apps (root app-of-apps Application name: %s; repo %s).",
		cfg.WorkloadClusterName, cfg.WorkloadClusterName, cfg.ArgoCD.AppOfAppsGitURL)
}

// WaitWorkloadArgoCDServer polls the workload cluster for the
// argocd-server Deployment to become Available.
func WaitWorkloadArgoCDServer(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	if !cfg.IsWorkloadGitopsCaaphMode() || !cfg.ArgoCD.WorkloadEnabled {
		return
	}
	wk, err := writeWorkloadKubeconfig()
	if err != nil || wk == "" {
		return
	}
	defer removePath(wk)
	ns := cfg.ArgoCD.WorkloadNamespace
	if ns == "" {
		ns = "argocd"
	}
	logx.Log("Waiting for Argo CD server (workload %s, ns %s)…", cfg.WorkloadClusterName, ns)

	cli, err := k8sclient.ForKubeconfigFile(wk)
	if err != nil {
		logx.Warn("Argo CD server: cannot load workload kubeconfig: %v", err)
		return
	}

	// Poll every 5s for up to 10 minutes.
	err = k8sclient.PollUntil(context.Background(), 5*time.Second, 10*time.Minute,
		func(c context.Context) (bool, error) {
			dep, err := cli.Typed.AppsV1().Deployments(ns).Get(c, "argocd-server", metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, nil
			}
			for _, cond := range dep.Status.Conditions {
				if string(cond.Type) == "Available" && cond.Status == corev1.ConditionTrue {
					return true, nil
				}
			}
			return false, nil
		})
	if err != nil {
		logx.Warn("Argo CD server did not become Available in time — check HelmReleaseProxy and pods on the workload.")
		return
	}
	logx.Log("Argo CD server is available on the workload cluster.")
}

// LogWorkloadArgoAppsStatus prints a status overview of the workload
// Argo CD Applications (sync + health per app).
func LogWorkloadArgoAppsStatus(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	if !cfg.IsWorkloadGitopsCaaphMode() || !cfg.ArgoCD.WorkloadEnabled {
		return
	}
	wk, err := writeWorkloadKubeconfig()
	if err != nil || wk == "" {
		return
	}
	defer removePath(wk)
	ns := cfg.ArgoCD.WorkloadNamespace
	if ns == "" {
		ns = "argocd"
	}
	logx.Log("App-of-apps: this script only waits for argocd-server — it does not wait for the argocd-apps Helm install, Git sync, or platform Deployments. Check sync below.")

	cli, err := k8sclient.ForKubeconfigFile(wk)
	if err != nil {
		logx.Warn("Could not load workload kubeconfig: %v", err)
		return
	}
	ctx := context.Background()
	appsGVR := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}

	list, err := cli.Dynamic.Resource(appsGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		logx.Warn("Could not list Application resources in %s (CRD or RBAC) — is Argo fully installed on the workload?", ns)
		return
	}

	// Mirror `kubectl get applications.argoproj.io` output (one name per
	// line, then a tabular listing).
	for _, item := range list.Items {
		fmt.Println("application.argoproj.io/" + item.GetName())
	}
	for _, item := range list.Items {
		sync, _, _ := unstructuredString(item.Object, "status", "sync", "status")
		health, _, _ := unstructuredString(item.Object, "status", "health", "status")
		fmt.Printf("%s\t%s\t%s\n", item.GetName(), sync, health)
	}

	root, err := cli.Dynamic.Resource(appsGVR).Namespace(ns).
		Get(ctx, cfg.WorkloadClusterName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logx.Warn("No root Application %s in %s yet. Often: CAAPH is still running the 'argocd-apps' install on the workload, the HelmChartProxy does not match the cluster (CAPI Cluster needs label caaph=enabled), or the chart failed; check: kubectl get helmchartproxy -A, controller logs, and Argo/Helm on the workload.", cfg.WorkloadClusterName, ns)
		} else {
			logx.Warn("Could not get root Application %s/%s: %v", ns, cfg.WorkloadClusterName, err)
		}
		return
	}
	sync, _, _ := unstructuredString(root.Object, "status", "sync", "status")
	health, _, _ := unstructuredString(root.Object, "status", "health", "status")
	fmt.Printf("NAME\tSYNC\tHEALTH\n%s\t%s\t%s\n", root.GetName(), sync, health)

	sync = strings.TrimSpace(sync)
	if sync != "" && sync != "Synced" {
		logx.Warn("Root app %s is not Synced yet (%s) — from a machine with workload kube: argocd app sync %s (or use the Argo CD UI), then watch child apps.",
			cfg.WorkloadClusterName, sync, cfg.WorkloadClusterName)
	}
}

// unstructuredString fetches a string value at a nested path from an
// unstructured object map; returns ("", false, nil) on missing/non-string.
func unstructuredString(obj map[string]any, path ...string) (string, bool, error) {
	cur := any(obj)
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false, nil
		}
		v, ok := m[k]
		if !ok {
			return "", false, nil
		}
		cur = v
	}
	s, ok := cur.(string)
	if !ok {
		return "", false, nil
	}
	return s, true, nil
}

func removePath(p string) {
	if p == "" {
		return
	}
	_ = os.Remove(p)
}