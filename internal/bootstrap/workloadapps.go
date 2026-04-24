package bootstrap

import (
	"fmt"
	"os"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/csix"
	"github.com/lpasquali/bootstrap-capi/internal/helmvalues"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/postsync"
	"github.com/lpasquali/bootstrap-capi/internal/proxmox"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
	"github.com/lpasquali/bootstrap-capi/internal/wlargocd"
)

// ApplyWorkloadArgoCDApplications ports apply_workload_argocd_applications
// (L7039-L7311). Renders every enabled in-cluster Application (metrics-
// server, proxmox-csi, kyverno, cert-manager, crossplane, cnpg,
// external-secrets, infisical, spire-crds+spire, victoriametrics, otel,
// grafana, backstage, keycloak, keycloak-realm-operator) into a single
// multi-doc YAML and applies it to the workload via its kubeconfig.
func ApplyWorkloadArgoCDApplications(cfg *config.Config) {
	if !cfg.ArgoCDEnabled || !cfg.WorkloadArgoCDEnabled {
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

	if cfg.ProxmoxCSIEnabled {
		csix.LoadVarsFromConfig(cfg)
		if cfg.ProxmoxCSIURL == "" {
			cfg.ProxmoxCSIURL = proxmox.APIJSONURL(cfg)
		}
		if cfg.ProxmoxCSIURL == "" || cfg.ProxmoxCSITokenID == "" ||
			cfg.ProxmoxCSITokenSecret == "" || cfg.ProxmoxRegion == "" {
			logx.Die("Proxmox CSI credentials incomplete — cannot render in-cluster Argo Application.")
		}
		csix.ApplyConfigSecretToWorkload(cfg, func() (string, error) {
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
`, cfg.WorkloadClusterName, cfg.ProxmoxCSIConfigProvider,
			cfg.ProxmoxCSIStorageClassName, cfg.ProxmoxCSIStorage,
			cfg.ProxmoxCSIReclaimPolicy, cfg.ProxmoxCSIFsType, cfg.ProxmoxCSIDefaultClass)
		oci := strings.TrimSuffix(cfg.ProxmoxCSIChartRepoURL, "/")
		if !strings.HasSuffix(oci, "/"+cfg.ProxmoxCSIChartName) {
			oci += "/" + cfg.ProxmoxCSIChartName
		}
		var h1P, h1K, h2P, h2K string
		if cfg.ProxmoxCSISmokeEnabled && cfg.ArgoWorkloadPostsyncHooksEnabled {
			h1P = postsync.FullRelpath(cfg, "proxmox-csi-pvc")
			h2P = postsync.FullRelpath(cfg, "proxmox-csi-rollout")
			h1K = postsync.SmokeRenderKustomizeBlock(cfg)
			h2K = postsync.KustomizeBlockForJob(cfg, "proxmox-csi-rollout-smoketest")
		}
		sb.WriteString(wlargocd.HelmOCI(cfg,
			cfg.WorkloadClusterName+"-proxmox-csi", cfg.ProxmoxCSINamespace,
			oci, cfg.ProxmoxCSIChartVersion, "-2", csiValues,
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
		sb.WriteString(wlargocd.Helm(cfg,
			cfg.WorkloadClusterName+"-cnpg", cfg.CNPGNamespace,
			cfg.CNPGChartRepoURL, cfg.CNPGChartName, cfg.CNPGChartVersion,
			"2", "", "cnpg"))
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
	if err := shell.Pipe(body, "kubectl", "--kubeconfig", wk, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to apply in-cluster Argo CD Applications on the workload cluster.")
	}
	logx.Log("In-cluster Argo CD Applications submitted on the workload.")
}

// WaitForWorkloadArgoCDApplicationsHealthy ports
// wait_for_workload_argocd_applications_healthy (L7313-L7360).
func WaitForWorkloadArgoCDApplicationsHealthy(cfg *config.Config) {
	if !cfg.ArgoCDEnabled || !cfg.WorkloadArgoCDEnabled {
		return
	}
	add := func(apps *[]string, enabled bool, name string) {
		if enabled {
			*apps = append(*apps, cfg.WorkloadClusterName+"-"+name)
		}
	}
	var apps []string
	add(&apps, cfg.EnableWorkloadMetricsServer, "metrics-server")
	add(&apps, cfg.ProxmoxCSIEnabled, "proxmox-csi")
	add(&apps, cfg.KyvernoEnabled, "kyverno")
	add(&apps, cfg.CertManagerEnabled, "cert-manager")
	add(&apps, cfg.CrossplaneEnabled, "crossplane")
	add(&apps, cfg.CNPGEnabled, "cnpg")
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
	for _, app := range apps {
		logx.Log("Waiting for Argo Application %s (workload) to become Synced+Healthy...", app)
		if err := shell.Run("kubectl", "--kubeconfig", wk,
			"-n", cfg.WorkloadArgoCDNamespace, "wait",
			"--for=jsonpath={.status.sync.status}=Synced",
			"application/"+app, "--timeout=30m"); err != nil {
			logx.Die("Argo Application %s (workload) did not reach Synced.", app)
		}
		if err := shell.Run("kubectl", "--kubeconfig", wk,
			"-n", cfg.WorkloadArgoCDNamespace, "wait",
			"--for=jsonpath={.status.health.status}=Healthy",
			"application/"+app, "--timeout=30m"); err != nil {
			logx.Die("Argo Application %s (workload) did not reach Healthy.", app)
		}
	}
	logx.Log("All in-cluster Argo CD Applications on the workload are Synced+Healthy.")
}
