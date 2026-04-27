// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/capi/csi"
	"github.com/lpasquali/yage/internal/capi/helmvalues"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/capi/postsync"
	"github.com/lpasquali/yage/internal/provider/proxmox/pveapi"
	"github.com/lpasquali/yage/internal/capi/wlargocd"
)

// argoAppGVR is reused by waiters and renderers when reaching for the
// argoproj.io Application CRD on the workload cluster.
var argoAppGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// cnpgSuppressedByManagedPG reports whether the in-cluster cnpg Helm
// install should be skipped because the operator opted into the active
// vendor's managed Postgres SKU. avoid double-stacking with managed PG.
func cnpgSuppressedByManagedPG(cfg *config.Config) bool {
	return cfg.UseManagedPostgres && cost.VendorOffersManaged(cfg.InfraProvider, cost.MSPostgres)
}

// ApplyWorkloadArgoCDApplications renders every enabled
// in-cluster Application (metrics-server, proxmox-csi, kyverno,
// cert-manager, crossplane, cnpg, external-secrets, infisical,
// spire-crds+spire, victoriametrics, otel, grafana, backstage,
// keycloak, keycloak-realm-operator) into a single multi-doc YAML
// and applies it to the workload via its kubeconfig.
func ApplyWorkloadArgoCDApplications(cfg *config.Config) {
	if !cfg.ArgoCD.Enabled || !cfg.ArgoCD.WorkloadEnabled {
		return
	}
	wk, err := writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
	if err != nil {
		logx.Die("Could not read workload kubeconfig to apply in-cluster Argo CD Applications.")
	}
	defer os.Remove(wk)
	logx.Log("Rendering in-cluster Argo CD Applications on workload %s (platform apps + PostSync hooks from argo-postsync-hooks/* when git URL is set)...", cfg.WorkloadClusterName)

	var sb strings.Builder

	if cfg.EnableWorkloadMetricsServer {
		sb.WriteString(wlargocd.HelmGit(cfg,
			cfg.WorkloadClusterName+"-metrics-server", "kube-system",
			"https://github.com/kubernetes-sigs/metrics-server",
			"charts/metrics-server", cfg.MetricsServerGitChartTag,
			"-3", helmvalues.MetricsServerValues(cfg),
			"metrics-server", "metrics-server",
		))
	}

	if cfg.Providers.Proxmox.CSIEnabled {
		csi.LoadVarsFromConfig(cfg)
		if cfg.Providers.Proxmox.CSIURL == "" {
			cfg.Providers.Proxmox.CSIURL = pveapi.APIJSONURL(cfg)
		}
		if cfg.Providers.Proxmox.CSIURL == "" || cfg.Providers.Proxmox.CSITokenID == "" ||
			cfg.Providers.Proxmox.CSITokenSecret == "" || cfg.Providers.Proxmox.Region == "" {
			logx.Die("Proxmox CSI credentials incomplete — cannot render in-cluster Argo Application.")
		}
		csi.ApplyConfigSecretToWorkload(cfg, func() (string, error) {
			return writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
		})
		csiValues := fmt.Sprintf(`existingConfigSecret: "%s-proxmox-csi-config"
existingConfigSecretKey: config.yaml
config:
  features:
    provider: %s
  clusters: []
storageClass:
  - name: "%s"
    storage: "%s"
    reclaimPolicy: "%s"
    fstype: "%s"
    annotations:
      storageclass.kubernetes.io/is-default-class: "%s"
`, cfg.WorkloadClusterName, cfg.Providers.Proxmox.CSIConfigProvider,
			cfg.Providers.Proxmox.CSIStorageClassName, cfg.Providers.Proxmox.CSIStorage,
			cfg.Providers.Proxmox.CSIReclaimPolicy, cfg.Providers.Proxmox.CSIFsType, cfg.Providers.Proxmox.CSIDefaultClass)
		oci := strings.TrimSuffix(cfg.Providers.Proxmox.CSIChartRepoURL, "/")
		if !strings.HasSuffix(oci, "/"+cfg.Providers.Proxmox.CSIChartName) {
			oci += "/" + cfg.Providers.Proxmox.CSIChartName
		}
		var h1P, h1K, h2P, h2K string
		if cfg.Providers.Proxmox.CSISmokeEnabled && cfg.ArgoCD.PostsyncHooksEnabled {
			h1P = postsync.FullRelpath(cfg, "proxmox-csi-pvc")
			h2P = postsync.FullRelpath(cfg, "proxmox-csi-rollout")
			h1K = postsync.SmokeRenderKustomizeBlock(cfg)
			h2K = postsync.KustomizeBlockForJob(cfg, "proxmox-csi-rollout-smoketest")
		}
		sb.WriteString(wlargocd.HelmOCI(cfg,
			cfg.WorkloadClusterName+"-proxmox-csi", cfg.Providers.Proxmox.CSINamespace,
			oci, cfg.Providers.Proxmox.CSIChartVersion, "-2", csiValues,
			h1P, h1K, h2P, h2K))
	}

	if cfg.KyvernoEnabled {
		sb.WriteString(wlargocd.Kyverno(cfg,
			cfg.WorkloadClusterName+"-kyverno", cfg.KyvernoNamespace,
			cfg.KyvernoChartRepoURL, "kyverno", cfg.KyvernoChartVersion, "0", "kyverno"))
	}
	if cfg.CertManagerEnabled {
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-cert-manager", cfg.CertManagerNamespace,
			cfg.CertManagerChartRepoURL, "cert-manager", cfg.CertManagerChartVersion,
			"1", "crds:\n  enabled: true\n", "cert-manager"))
	}
	if cfg.CrossplaneEnabled {
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-crossplane", cfg.CrossplaneNamespace,
			cfg.CrossplaneChartRepoURL, "crossplane", cfg.CrossplaneChartVersion,
			"2", "", "crossplane"))
	}
	if cfg.CNPGEnabled {
		if cnpgSuppressedByManagedPG(cfg) {
			logx.Log("cnpg skipped: managed Postgres on %s selected (--no-managed-postgres to keep in-cluster cnpg).", cfg.InfraProvider)
		} else {
			sb.WriteString(wlargocd.Helm(cfg,
				cfg.WorkloadClusterName+"-cnpg", cfg.CNPGNamespace,
				cfg.CNPGChartRepoURL, cfg.CNPGChartName, cfg.CNPGChartVersion,
				"2", "", "cnpg"))
		}
	}
	if cfg.ExternalSecretsEnabled {
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-external-secrets", cfg.ExternalSecretsNamespace,
			cfg.ExternalSecretsChartRepoURL, "external-secrets", cfg.ExternalSecretsChartVersion,
			"3", "", "external-secrets"))
	}
	if cfg.InfisicalOperatorEnabled {
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-infisical-secrets-operator", cfg.InfisicalNamespace,
			cfg.InfisicalChartRepoURL, cfg.InfisicalChartName, cfg.InfisicalChartVersion,
			"4", "", "infisical"))
	}
	if cfg.SPIREEnabled {
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-spire-crds", cfg.SPIRENamespace,
			cfg.SPIREChartRepoURL, cfg.SPIRECRDsChartName, cfg.SPIRECRDsChartVersion,
			"-3", "", ""))
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-spire", cfg.SPIRENamespace,
			cfg.SPIREChartRepoURL, cfg.SPIREChartName, cfg.SPIREChartVersion,
			"5", helmvalues.SPIREValues(cfg), "spire"))
	}
	if cfg.VictoriaMetricsEnabled {
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-victoria-metrics-single", cfg.VictoriaMetricsNamespace,
			cfg.VictoriaMetricsChartRepoURL, cfg.VictoriaMetricsChartName, cfg.VictoriaMetricsChartVersion,
			"6", helmvalues.VictoriaMetricsValues(), "victoria-metrics"))
	}
	if cfg.OTELEnabled {
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-opentelemetry-collector", cfg.OTELNamespace,
			cfg.OTELChartRepoURL, cfg.OTELChartName, cfg.OTELChartVersion,
			"6", helmvalues.OpenTelemetryValues(cfg), "opentelemetry"))
	}
	if cfg.GrafanaEnabled {
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-grafana", cfg.GrafanaNamespace,
			cfg.GrafanaChartRepoURL, "grafana", cfg.GrafanaChartVersion,
			"6", helmvalues.GrafanaValues(cfg), "grafana"))
	}
	if cfg.BackstageEnabled {
		if cfg.BackstageChartRepoURL == "" {
			logx.Warn("BACKSTAGE_ENABLED but BACKSTAGE_CHART_REPO_URL is empty — set a Helm repo and chart name, or set BACKSTAGE_ENABLED=false. Skipping Backstage.")
		} else {
			sb.WriteString(wlargocd.Helm(cfg,
				cfg.WorkloadClusterName+"-backstage", cfg.BackstageNamespace,
				cfg.BackstageChartRepoURL, cfg.BackstageChartName, cfg.BackstageChartVersion,
				"7", "", "backstage"))
		}
	}
	if cfg.KeycloakEnabled {
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-keycloak", cfg.KeycloakNamespace,
			cfg.KeycloakChartRepoURL, cfg.KeycloakChartName, cfg.KeycloakChartVersion,
			"8", helmvalues.KeycloakValues(cfg), "keycloak"))
	}
	if cfg.KeycloakOperatorEnabled && cfg.KeycloakEnabled {
		if cfg.KeycloakOperatorGitURL == "" {
			logx.Warn("KEYCLOAK_OPERATOR_ENABLED but KEYCLOAK_OPERATOR_GIT_URL is empty — skipping Keycloak operator Application.")
		} else {
			sb.WriteString(wlargocd.KustomizeGit(cfg,
				cfg.WorkloadClusterName+"-keycloak-realm-operator", cfg.KeycloakOperatorNS,
				cfg.KeycloakOperatorGitURL, cfg.KeycloakOperatorGitPath, cfg.KeycloakOperatorGitRef,
				"9", "    kustomize: {}\n", ""))
		}
	}

	body := sb.String()
	if strings.TrimSpace(body) == "" {
		logx.Log("No in-cluster Argo CD Applications to apply (all add-ons disabled).")
		return
	}
	cli, err := k8sclient.ForKubeconfigFile(wk)
	if err != nil {
		logx.Die("Failed to build kube client for workload kubeconfig: %v", err)
	}
	if err := cli.ApplyMultiDocYAML(context.Background(), []byte(body)); err != nil {
		logx.Die("Failed to apply in-cluster Argo CD Applications on the workload cluster: %v", err)
	}
	logx.Log("In-cluster Argo CD Applications submitted on the workload.")
}

// WaitForWorkloadArgoCDApplicationsHealthy waits for every enabled
// in-cluster Argo CD Application on the workload to reach
// Healthy + Synced state.
func WaitForWorkloadArgoCDApplicationsHealthy(cfg *config.Config) {
	if !cfg.ArgoCD.Enabled || !cfg.ArgoCD.WorkloadEnabled {
		return
	}
	add := func(apps *[]string, enabled bool, name string) {
		if enabled {
			*apps = append(*apps, cfg.WorkloadClusterName+"-"+name)
		}
	}
	var apps []string
	add(&apps, cfg.EnableWorkloadMetricsServer, "metrics-server")
	add(&apps, cfg.Providers.Proxmox.CSIEnabled, "proxmox-csi")
	add(&apps, cfg.KyvernoEnabled, "kyverno")
	add(&apps, cfg.CertManagerEnabled, "cert-manager")
	add(&apps, cfg.CrossplaneEnabled, "crossplane")
	add(&apps, cfg.CNPGEnabled && !cnpgSuppressedByManagedPG(cfg), "cnpg")
	add(&apps, cfg.ExternalSecretsEnabled, "external-secrets")
	add(&apps, cfg.InfisicalOperatorEnabled, "infisical-secrets-operator")
	if cfg.SPIREEnabled {
		apps = append(apps, cfg.WorkloadClusterName+"-spire-crds", cfg.WorkloadClusterName+"-spire")
	}
	add(&apps, cfg.VictoriaMetricsEnabled, "victoria-metrics-single")
	add(&apps, cfg.OTELEnabled, "opentelemetry-collector")
	add(&apps, cfg.GrafanaEnabled, "grafana")
	if cfg.BackstageEnabled && cfg.BackstageChartRepoURL != "" {
		apps = append(apps, cfg.WorkloadClusterName+"-backstage")
	}
	add(&apps, cfg.KeycloakEnabled, "keycloak")
	if cfg.KeycloakOperatorEnabled && cfg.KeycloakEnabled && cfg.KeycloakOperatorGitURL != "" {
		apps = append(apps, cfg.WorkloadClusterName+"-keycloak-realm-operator")
	}

	if len(apps) == 0 {
		logx.Log("No in-cluster Argo Applications to wait for.")
		return
	}
	wk, err := writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
	if err != nil {
		logx.Die("Could not read workload kubeconfig to wait for in-cluster Argo CD Applications.")
	}
	defer os.Remove(wk)
	cli, err := k8sclient.ForKubeconfigFile(wk)
	if err != nil {
		logx.Die("Could not build kube client for workload kubeconfig: %v", err)
	}
	bg := context.Background()
	for _, app := range apps {
		logx.Log("Waiting for Argo Application %s (workload) to become Synced+Healthy...", app)
		if err := waitArgoApplicationCondition(cli, bg, cfg.ArgoCD.WorkloadNamespace, app, "Synced", "Healthy", 30*time.Minute); err != nil {
			logx.Die("Argo Application %s (workload) did not reach Synced+Healthy: %v", app, err)
		}
	}
	logx.Log("All in-cluster Argo CD Applications on the workload are Synced+Healthy.")
}

// waitArgoApplicationCondition polls the workload Argo Application until
// status.sync.status == wantSync AND status.health.status == wantHealth, or
// the timeout fires. Replaces the two `kubectl wait --for=jsonpath` calls.
func waitArgoApplicationCondition(cli *k8sclient.Client, bg context.Context, ns, name, wantSync, wantHealth string, timeout time.Duration) error {
	return k8sclient.PollUntil(bg, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		u, err := cli.Dynamic.Resource(argoAppGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if k8sclient.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		sync, _, _ := unstructuredStr(u.Object, "status", "sync", "status")
		health, _, _ := unstructuredStr(u.Object, "status", "health", "status")
		return sync == wantSync && health == wantHealth, nil
	})
}

// unstructuredStr fetches a string at a dotted path inside an Unstructured
// object's nested map. Returns "", false when any segment is missing or
// not a string.
func unstructuredStr(obj map[string]interface{}, path ...string) (string, bool, error) {
	cur := obj
	for i, p := range path {
		v, ok := cur[p]
		if !ok || v == nil {
			return "", false, nil
		}
		if i == len(path)-1 {
			s, ok := v.(string)
			return s, ok, nil
		}
		next, ok := v.(map[string]interface{})
		if !ok {
			return "", false, nil
		}
		cur = next
	}
	return "", false, nil
}