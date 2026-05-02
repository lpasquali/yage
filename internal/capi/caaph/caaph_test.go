// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package caaph_test

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/capi/caaph"
	"github.com/lpasquali/yage/internal/capi/helmvalues"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
)

// fetcher returns a Fetcher pointed at the in-package testdata fixture,
// which mirrors the yage-manifests directory layout, with isTrue registered.
func fetcher() *manifests.Fetcher {
	f := &manifests.Fetcher{MountRoot: "testdata"}
	helmvalues.RegisterIsTrue(f)
	return f
}

// defaultCfg builds a Config with the fields used by caaph renderers.
// Empty strings rely on the functions' own defaults (matching the old
// inline renderers).
func defaultCfg() *config.Config {
	cfg := &config.Config{}
	cfg.ArgoCD.Version = "v3.3.8"
	cfg.ArgoCD.WorkloadNamespace = "argocd"
	cfg.ArgoCD.AppOfAppsGitURL = "https://github.com/lpasquali/workload-app-of-apps.git"
	cfg.ArgoCD.AppOfAppsGitPath = "examples/default"
	cfg.ArgoCD.AppOfAppsGitRef = "main"
	cfg.ArgoCD.PrometheusEnabled = "false"
	cfg.ArgoCD.MonitoringEnabled = "false"
	cfg.ArgoCD.DisableOperatorManagedIngress = "false"
	cfg.ArgoCD.ServerInsecure = "false"
	return cfg
}

// TestCiliumHelmChartProxyYAML_Default verifies byte-for-byte equivalence with
// the inline string renderer for the most common config: no kube-proxy
// replacement, no ingress, no Hubble, no gateway, default IPAM.
func TestCiliumHelmChartProxyYAML_Default(t *testing.T) {
	cfg := defaultCfg()
	// WorkloadClusterName="" → fallback "cluster"; WorkloadClusterNamespace="" → "default"
	// CiliumVersion="" → fallback "1.19.3"; kprOn=false

	got, err := caaph.CiliumHelmChartProxyYAML(cfg, false, fetcher())
	if err != nil {
		t.Fatalf("CiliumHelmChartProxyYAML error: %v", err)
	}

	// Build expected from the original Go renderer output.
	want := strings.Join([]string{
		"# yage-manifests/addons/cilium/helmchartproxy.yaml.tmpl",
		"apiVersion: addons.cluster.x-k8s.io/v1alpha1",
		"kind: HelmChartProxy",
		"metadata:",
		"  name: cluster-caaph-cilium",
		"  namespace: default",
		"spec:",
		"  clusterSelector:",
		"    matchLabels:",
		"      caaph: enabled",
		"  chartName: cilium",
		"  repoURL: https://helm.cilium.io/",
		`  version: "1.19.3"`,
		"  namespace: kube-system",
		"  options:",
		"    wait: true",
		"    waitForJobs: true",
		"    timeout: 15m0s",
		"    install:",
		"      createNamespace: true",
		"  valuesTemplate: |",
		"    cluster:",
		"      name: {{ .Cluster.metadata.name }}",
		`      id: {{ index .Cluster.metadata.labels "caaph.cilium.cluster-id" }}`,
		"    kubeProxyReplacement: false",
		`    ipam:`,
		"      operator:",
		`        clusterPoolIPv4PodCIDRList: ["10.244.0.0/16"]`,
		"        clusterPoolIPv4MaskSize: 24",
		"",
	}, "\n")

	if got != want {
		t.Errorf("CiliumHelmChartProxyYAML mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestCiliumHelmChartProxyYAML_KPR verifies the kube-proxy replacement case
// adds the required kubeProxyReplacement/k8sServiceHost/Port lines.
func TestCiliumHelmChartProxyYAML_KPR(t *testing.T) {
	cfg := defaultCfg()
	cfg.WorkloadClusterName = "wl-test"
	cfg.WorkloadClusterNamespace = "test-ns"
	cfg.CiliumVersion = "v1.16.4"

	got, err := caaph.CiliumHelmChartProxyYAML(cfg, true, fetcher())
	if err != nil {
		t.Fatalf("CiliumHelmChartProxyYAML error: %v", err)
	}

	checks := []string{
		"name: wl-test-caaph-cilium",
		"namespace: test-ns",
		`version: "1.16.4"`,
		"kubeProxyReplacement: true",
		`k8sServiceHost: {{ index .Cluster.metadata.labels "caaph.cilium.k8s-service-host" }}`,
		`k8sServicePort: {{ index .Cluster.metadata.labels "caaph.cilium.k8s-service-port" }}`,
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("CiliumHelmChartProxyYAML missing %q\nfull:\n%s", c, got)
		}
	}
}

// TestArgoCDAppsHelmChartProxyYAML_Default verifies byte-for-byte equivalence
// with the inline renderer in ApplyWorkloadArgoHelmProxies for the default config.
func TestArgoCDAppsHelmChartProxyYAML_Default(t *testing.T) {
	cfg := defaultCfg()
	cfg.WorkloadClusterName = "cluster"
	cfg.WorkloadClusterNamespace = "default"

	got, err := caaph.ArgoCDAppsHelmChartProxyYAML(cfg, fetcher())
	if err != nil {
		t.Fatalf("ArgoCDAppsHelmChartProxyYAML error: %v", err)
	}

	want := strings.Join([]string{
		"# yage-manifests/addons/argocd-apps/helmchartproxy.yaml.tmpl",
		"apiVersion: addons.cluster.x-k8s.io/v1alpha1",
		"kind: HelmChartProxy",
		"metadata:",
		"  name: cluster-caaph-argocd-apps",
		"  namespace: default",
		"spec:",
		"  clusterSelector:",
		"    matchLabels:",
		"      caaph: enabled",
		"  chartName: argocd-apps",
		"  repoURL: https://argoproj.github.io/argo-helm",
		"  namespace: argocd",
		"  options:",
		"    wait: true",
		"    waitForJobs: true",
		"    timeout: 20m0s",
		"    install:",
		"      createNamespace: true",
		"  valuesTemplate: |",
		"    applications:",
		`      "cluster":`,
		"        namespace: argocd",
		"        finalizers:",
		"          - resources-finalizer.argocd.argoproj.io",
		"        project: default",
		"        source:",
		"          repoURL: 'https://github.com/lpasquali/workload-app-of-apps.git'",
		"          path: 'examples/default'",
		"          targetRevision: 'main'",
		"        destination:",
		"          server: https://kubernetes.default.svc",
		"          namespace: argocd",
		"        syncPolicy:",
		"          automated:",
		"            prune: true",
		"            selfHeal: true",
		"          syncOptions:",
		"            - CreateNamespace=true",
		"",
	}, "\n")

	if got != want {
		t.Errorf("ArgoCDAppsHelmChartProxyYAML mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestArgoCDCRTemplate_Default verifies the ArgoCD CR template renders
// correctly for the default case (all booleans false; grpc ingress enabled).
func TestArgoCDCRTemplate_Default(t *testing.T) {
	cfg := defaultCfg()

	f := fetcher()
	got, err := caaph.BuildArgoCDCRForTest(cfg, f)
	if err != nil {
		t.Fatalf("buildArgoCDCR error: %v", err)
	}

	want := strings.Join([]string{
		"# yage-manifests/addons/argocd/argocd-cr.yaml.tmpl",
		"apiVersion: argoproj.io/v1beta1",
		"kind: ArgoCD",
		"metadata:",
		"  name: argocd",
		"  namespace: argocd",
		"spec:",
		"  version: v3.3.8",
		"  prometheus:",
		"    enabled: false",
		"  monitoring:",
		"    enabled: false",
		"  notifications:",
		"    enabled: true",
		"  server:",
		"    grpc:",
		"      ingress:",
		"        enabled: true",
		"",
	}, "\n")

	if got != want {
		t.Errorf("ArgoCD CR template (default) mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestArgoCDCRTemplate_DisableIngress verifies the ingress-disabled branch.
func TestArgoCDCRTemplate_DisableIngress(t *testing.T) {
	cfg := defaultCfg()
	cfg.ArgoCD.DisableOperatorManagedIngress = "true"

	f := fetcher()
	got, err := caaph.BuildArgoCDCRForTest(cfg, f)
	if err != nil {
		t.Fatalf("buildArgoCDCR error: %v", err)
	}

	checks := []string{
		"    ingress:\n      enabled: false",
		"    grpc:\n      ingress:\n        enabled: false",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("ArgoCD CR (disableIngress) missing %q\nfull:\n%s", c, got)
		}
	}
	if strings.Contains(got, "insecure: true") {
		t.Errorf("ArgoCD CR (disableIngress only) must not contain insecure: true")
	}
}

// TestArgoCDCRTemplate_ServerInsecure verifies the server-insecure branch.
func TestArgoCDCRTemplate_ServerInsecure(t *testing.T) {
	cfg := defaultCfg()
	cfg.ArgoCD.ServerInsecure = "true"

	f := fetcher()
	got, err := caaph.BuildArgoCDCRForTest(cfg, f)
	if err != nil {
		t.Fatalf("buildArgoCDCR error: %v", err)
	}

	checks := []string{
		"    insecure: true",
		"    grpc:\n      ingress:\n        enabled: true",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("ArgoCD CR (serverInsecure) missing %q\nfull:\n%s", c, got)
		}
	}
}

// TestArgoCDCRTemplate_DisableIngressAndInsecure verifies both flags together.
func TestArgoCDCRTemplate_DisableIngressAndInsecure(t *testing.T) {
	cfg := defaultCfg()
	cfg.ArgoCD.DisableOperatorManagedIngress = "true"
	cfg.ArgoCD.ServerInsecure = "true"

	f := fetcher()
	got, err := caaph.BuildArgoCDCRForTest(cfg, f)
	if err != nil {
		t.Fatalf("buildArgoCDCR error: %v", err)
	}

	checks := []string{
		"    insecure: true",
		"    ingress:\n      enabled: false",
		"    grpc:\n      ingress:\n        enabled: false",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("ArgoCD CR (disableIngress+serverInsecure) missing %q\nfull:\n%s", c, got)
		}
	}
}
