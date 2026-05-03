// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package wlargocd_test

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/capi/helmvalues"
	"github.com/lpasquali/yage/internal/capi/wlargocd"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
)

// fetcher returns a Fetcher pointed at the in-package testdata fixture
// that mirrors the yage-manifests directory layout.
func fetcher() *manifests.Fetcher {
	return &manifests.Fetcher{MountRoot: "testdata"}
}

// defaultCfg builds a Config with the fields used by wlargocd renderers.
func defaultCfg() *config.Config {
	cfg := &config.Config{}
	cfg.WorkloadClusterName = "wl-test"
	cfg.ArgoCD.WorkloadNamespace = "argocd"
	return cfg
}

// TestHelm_SingleSource_NoPostSync verifies the most common shape:
// HTTP Helm-repo source, no PostSync hook, valuesYAML present.
func TestHelm_SingleSource_NoPostSync(t *testing.T) {
	cfg := defaultCfg()
	got, err := wlargocd.Helm(fetcher(), cfg, "cert-manager",
		"wl-test-cert-manager", "cert-manager",
		"https://charts.jetstack.io", "cert-manager", "v1.16.1",
		"1", "crds:\n  enabled: true\n", "")
	if err != nil {
		t.Fatalf("Helm: %v", err)
	}
	wants := []string{
		"---\n",
		"apiVersion: argoproj.io/v1alpha1",
		"kind: Application",
		"  name: wl-test-cert-manager",
		"  namespace: argocd",
		`    argocd.argoproj.io/sync-wave: "1"`,
		"    namespace: cert-manager",
		"  source:",
		"    repoURL: https://charts.jetstack.io",
		"    chart: cert-manager",
		`    targetRevision: "v1.16.1"`,
		"    helm:",
		"      valuesObject:",
		"        crds:",
		"          enabled: true",
		"  syncPolicy:",
		"    automated:",
		"      prune: true",
		"      selfHeal: true",
		"    syncOptions:",
		"      - CreateNamespace=true",
		"      - ServerSideApply=true",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("Helm output missing %q\nfull:\n%s", w, got)
		}
	}
	// Must not have multi-source structure when no postsync
	if strings.Contains(got, "sources:") {
		t.Errorf("Helm should use 'source:' not 'sources:' when no PostSync\nfull:\n%s", got)
	}
}

// TestHelm_NoValues_EmptyDict verifies that empty valuesYAML produces
// the literal `{}` for valuesObject.
func TestHelm_NoValues_EmptyDict(t *testing.T) {
	cfg := defaultCfg()
	got, err := wlargocd.Helm(fetcher(), cfg, "cert-manager",
		"wl-test-crossplane", "crossplane",
		"https://charts.crossplane.io/stable", "crossplane", "1.18.0",
		"2", "", "")
	if err != nil {
		t.Fatalf("Helm: %v", err)
	}
	if !strings.Contains(got, "        {}") {
		t.Errorf("expected literal '        {}' for empty valuesYAML\nfull:\n%s", got)
	}
}

// TestHelm_TrimsTrailingSlashOnRepoURL verifies that the trailing slash
// on the repo URL is trimmed (matches original wlargocd.Helm).
func TestHelm_TrimsTrailingSlashOnRepoURL(t *testing.T) {
	cfg := defaultCfg()
	got, err := wlargocd.Helm(fetcher(), cfg, "cert-manager",
		"x", "cert-manager",
		"https://charts.example.org/", "cert-manager", "1.0.0",
		"1", "", "")
	if err != nil {
		t.Fatalf("Helm: %v", err)
	}
	if strings.Contains(got, "https://charts.example.org/\n") {
		t.Errorf("repoURL trailing slash should have been trimmed\nfull:\n%s", got)
	}
	if !strings.Contains(got, "repoURL: https://charts.example.org\n") {
		t.Errorf("repoURL line missing\nfull:\n%s", got)
	}
}

// TestHelm_DefaultsVersionToStar verifies that empty version → "*".
func TestHelm_DefaultsVersionToStar(t *testing.T) {
	cfg := defaultCfg()
	got, err := wlargocd.Helm(fetcher(), cfg, "cert-manager",
		"x", "ns", "https://charts.example.org", "cert-manager", "",
		"0", "", "")
	if err != nil {
		t.Fatalf("Helm: %v", err)
	}
	if !strings.Contains(got, `targetRevision: "*"`) {
		t.Errorf("expected `targetRevision: \"*\"` on empty version\nfull:\n%s", got)
	}
}

// TestHelm_MultiSource_WithPostSync exercises the postsync-hooks-on
// branch: the rendered Application uses `sources:` (plural) with the
// chart and the kustomize hook side-by-side.
func TestHelm_MultiSource_WithPostSync(t *testing.T) {
	cfg := defaultCfg()
	cfg.ArgoCD.PostsyncHooksEnabled = true
	cfg.ArgoCD.PostsyncHooksGitURL = "https://github.com/lpasquali/workload-smoketests"
	cfg.ArgoCD.PostsyncHooksGitRef = "main"
	cfg.ArgoCD.PostsyncHooksGitPath = ""
	cfg.WorkloadPostsyncNamespace = "workload-smoke"

	got, err := wlargocd.Helm(fetcher(), cfg, "cert-manager",
		"wl-test-cert-manager", "cert-manager",
		"https://charts.jetstack.io", "cert-manager", "v1.16.1",
		"1", "crds:\n  enabled: true\n", "cert-manager")
	if err != nil {
		t.Fatalf("Helm: %v", err)
	}
	wants := []string{
		"  sources:",
		"    - repoURL: https://charts.jetstack.io",
		"      chart: cert-manager",
		`      targetRevision: "v1.16.1"`,
		"      helm:",
		"        valuesObject:",
		// values are 10-space indented in the multi-source case
		"          crds:",
		"            enabled: true",
		"    - repoURL: https://github.com/lpasquali/workload-smoketests",
		"      path: argo-postsync-hooks/cert-manager",
		"      targetRevision: 'main'",
		// kustomize partial is pre-indented by 2 inside `sources:`
		"      kustomize:",
		"        namespace: workload-smoke",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("Helm multi-source output missing %q\nfull:\n%s", w, got)
		}
	}
	if strings.Contains(got, "  source:\n    repoURL") {
		t.Errorf("Helm multi-source should not contain singular `source:` block\nfull:\n%s", got)
	}
}

// fetcherKyverno returns a Fetcher with isTrue registered (Kyverno
// template uses isTrue on KyvernoTolerateControlPlane).
func fetcherKyverno() *manifests.Fetcher {
	f := fetcher()
	helmvalues.RegisterIsTrue(f)
	return f
}

// TestHelmGit_SingleSource_NoPostSync exercises metrics-server-style
// HelmGit shape: chart pulled from a Git source.
func TestHelmGit_SingleSource_NoPostSync(t *testing.T) {
	cfg := defaultCfg()
	got, err := wlargocd.HelmGit(fetcher(), cfg, "metrics-server",
		"wl-test-metrics-server", "kube-system",
		"https://github.com/kubernetes-sigs/metrics-server",
		"charts/metrics-server", "metrics-server-helm-chart-3.12.2",
		"-3", "args:\n  - --kubelet-insecure-tls\n",
		"metrics-server", "")
	if err != nil {
		t.Fatalf("HelmGit: %v", err)
	}
	wants := []string{
		"---\n",
		"  name: wl-test-metrics-server",
		"    namespace: kube-system",
		"  source:",
		"    repoURL: https://github.com/kubernetes-sigs/metrics-server",
		"    path: charts/metrics-server",
		"    targetRevision: 'metrics-server-helm-chart-3.12.2'",
		"    helm:",
		"      releaseName: metrics-server",
		"      valuesObject:",
		"        args:",
		"          - --kubelet-insecure-tls",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("HelmGit output missing %q\nfull:\n%s", w, got)
		}
	}
}

// TestHelmOCI_SingleSource_NoPostSync exercises proxmox-csi-style OCI
// chart pull, no PostSync.
func TestHelmOCI_SingleSource_NoPostSync(t *testing.T) {
	cfg := defaultCfg()
	got, err := wlargocd.HelmOCI(fetcher(), cfg, "proxmox-csi",
		"wl-test-proxmox-csi", "csi-proxmox",
		"oci://ghcr.io/sergelogvinov/charts/proxmox-csi-plugin", "0.10.5",
		"-2", "config:\n  features:\n    provider: capmox\n",
		"", "", "", "")
	if err != nil {
		t.Fatalf("HelmOCI: %v", err)
	}
	wants := []string{
		"  source:",
		"    repoURL: oci://ghcr.io/sergelogvinov/charts/proxmox-csi-plugin",
		`    path: "."`,
		`    targetRevision: "0.10.5"`,
		"    helm:",
		"      valuesObject:",
		"        config:",
		"          features:",
		"            provider: capmox",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("HelmOCI output missing %q\nfull:\n%s", w, got)
		}
	}
}

// TestHelmOCI_TwoHooks exercises the proxmox-csi smoke-test variant:
// two PostSync hooks attached together when both kustomize blocks +
// both paths are non-empty.
func TestHelmOCI_TwoHooks(t *testing.T) {
	cfg := defaultCfg()
	cfg.ArgoCD.PostsyncHooksEnabled = true
	cfg.ArgoCD.PostsyncHooksGitURL = "https://github.com/lpasquali/workload-smoketests"
	cfg.ArgoCD.PostsyncHooksGitRef = "main"
	cfg.Providers.Proxmox.CSISmokeEnabled = true

	got, err := wlargocd.HelmOCI(fetcher(), cfg, "proxmox-csi",
		"wl-test-proxmox-csi", "csi-proxmox",
		"oci://ghcr.io/example/proxmox-csi", "1.0.0",
		"-2", "",
		"argo-postsync-hooks/proxmox-csi-pvc", "    kustomize:\n      hook1: true\n",
		"argo-postsync-hooks/proxmox-csi-rollout", "    kustomize:\n      hook2: true\n")
	if err != nil {
		t.Fatalf("HelmOCI: %v", err)
	}
	wants := []string{
		"  sources:",
		"      path: argo-postsync-hooks/proxmox-csi-pvc",
		"      path: argo-postsync-hooks/proxmox-csi-rollout",
		"      hook1: true",
		"      hook2: true",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("HelmOCI two-hooks output missing %q\nfull:\n%s", w, got)
		}
	}
}

// TestKustomizeGit_SingleSource exercises keycloak-realm-operator-style
// KustomizeGit shape: kustomize tree from a Git source.
func TestKustomizeGit_SingleSource(t *testing.T) {
	cfg := defaultCfg()
	got, err := wlargocd.KustomizeGit(fetcher(), cfg, "keycloak-realm-operator",
		"wl-test-krealm", "keycloak",
		"https://github.com/example/keycloak-realm-operator",
		"deploy/", "v0.5.0",
		"9", "    kustomize: {}\n", "")
	if err != nil {
		t.Fatalf("KustomizeGit: %v", err)
	}
	wants := []string{
		"  source:",
		"    repoURL: https://github.com/example/keycloak-realm-operator",
		"    path: deploy/",
		"    targetRevision: 'v0.5.0'",
		"    kustomize: {}",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("KustomizeGit output missing %q\nfull:\n%s", w, got)
		}
	}
}

// TestKyverno_SingleSource_NoToleration exercises the Kyverno template:
// extra annotation, structured valuesObject, no toleration block when
// KyvernoTolerateControlPlane is empty.
func TestKyverno_SingleSource_NoToleration(t *testing.T) {
	cfg := defaultCfg()
	cfg.KyvernoTolerateControlPlane = ""
	got, err := wlargocd.Kyverno(fetcherKyverno(), cfg, "kyverno",
		"wl-test-kyverno", "kyverno",
		"https://kyverno.github.io/kyverno", "kyverno", "3.2.6",
		"0", "")
	if err != nil {
		t.Fatalf("Kyverno: %v", err)
	}
	wants := []string{
		"argocd.argoproj.io/compare-options: ServerSideDiff=true,IncludeMutationWebhook=true",
		"  source:",
		"    chart: kyverno",
		"    targetRevision:",
		"        config:",
		"          preserve: false",
		"          webhookLabels:",
		"        admissionController:",
		"          replicas: 1",
		"  ignoreDifferences:",
		"    - group: admissionregistration.k8s.io",
		"      kind: MutatingWebhookConfiguration",
		"      jqPathExpressions:",
		"        - .webhooks[]?.clientConfig.caBundle",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("Kyverno output missing %q\nfull:\n%s", w, got)
		}
	}
	if strings.Contains(got, `"node-role.kubernetes.io/control-plane"`) {
		t.Errorf("Kyverno without toleration must not emit control-plane key\nfull:\n%s", got)
	}
}

// TestKyverno_WithToleration verifies isTrue switches on the global
// toleration block when KyvernoTolerateControlPlane is "true".
func TestKyverno_WithToleration(t *testing.T) {
	cfg := defaultCfg()
	cfg.KyvernoTolerateControlPlane = "true"
	got, err := wlargocd.Kyverno(fetcherKyverno(), cfg, "kyverno",
		"wl-test-kyverno", "kyverno",
		"https://kyverno.github.io/kyverno", "kyverno", "3.2.6",
		"0", "")
	if err != nil {
		t.Fatalf("Kyverno: %v", err)
	}
	wants := []string{
		"        global:",
		"          tolerations:",
		`            - key: "node-role.kubernetes.io/control-plane"`,
		`            - key: "node-role.kubernetes.io/master"`,
		"              effect: NoSchedule",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("Kyverno toleration output missing %q\nfull:\n%s", w, got)
		}
	}
}
