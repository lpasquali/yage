// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package helmvalues

import (
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
)

// VictoriaMetricsValues ports workload_argocd_victoria_helm_values
// (L6826-L6849). Static body — no config knobs.
func VictoriaMetricsValues() string {
	return `server:
  fullnameOverride: vmsingle
  scrape:
    enabled: true
    extraScrapeConfigs:
      - job_name: kubernetes-endpoints-metrics-ports
        kubernetes_sd_configs:
          - role: endpoints
        relabel_configs:
          - action: keep
            source_labels: [__meta_kubernetes_endpoint_port_name]
            regex: metrics|http-metrics
          - action: replace
            source_labels: [__meta_kubernetes_namespace]
            target_label: namespace
          - action: replace
            source_labels: [__meta_kubernetes_service_name]
            target_label: service
`
}

// OpenTelemetryValues ports workload_argocd_opentelemetry_helm_values
// (L6852-L6858).
func OpenTelemetryValues(cfg *config.Config) string {
	mode := cfg.OTELCollectorMode
	if mode == "" {
		mode = "deployment"
	}
	img := cfg.OTELImageRepository
	if img == "" {
		img = "otel/opentelemetry-collector-k8s"
	}
	return fmt.Sprintf("mode: %s\nimage:\n  repository: %s\n", mode, img)
}

// GrafanaValues ports workload_argocd_grafana_helm_values
// (L6860-L6909). Two branches: VictoriaMetrics disabled → service only;
// enabled → add a VictoriaMetrics datasource + three default dashboards.
func GrafanaValues(cfg *config.Config) string {
	if !cfg.VictoriaMetricsEnabled {
		return "service:\n  type: ClusterIP\n"
	}
	return fmt.Sprintf(`service:
  type: ClusterIP
datasources:
  datasources.yaml:
    apiVersion: 1
    datasources:
      - name: VictoriaMetrics
        type: prometheus
        url: http://vmsingle.%s.svc:8428
        access: proxy
        isDefault: true
        jsonData:
          httpMethod: POST
          manageAlerts: true
          prometheusType: Prometheus
dashboardProviders:
  dashboardproviders.yaml:
    apiVersion: 1
    providers:
      - name: default
        orgId: 1
        folder: ""
        type: file
        disableDeletion: false
        editable: true
        options:
          path: /var/lib/grafana/dashboards/default
dashboards:
  default:
    k8s-cluster:
      gnetId: 7249
      revision: 1
      datasource: VictoriaMetrics
    k8s-api-server:
      gnetId: 12006
      revision: 1
      datasource: VictoriaMetrics
    victoriametrics-single:
      gnetId: 10229
      revision: 1
      datasource: VictoriaMetrics
`,
		cfg.VictoriaMetricsNamespace,
	)
}

// SPIRESubchartTolerations ports _wl_argocd_spire_subchart_tolerations
// (L6914-L6950). Returns the tolerations block for spire-server,
// spire-controller-manager, spire-agent, spiffe-csi-driver — or "" when
// SPIRE_TOLERATE_CONTROL_PLANE is false.
func SPIRESubchartTolerations(cfg *config.Config) string {
	if !isTrue(cfg.SPIRETolerateControlPlane) {
		return ""
	}
	block := func(name string) string {
		return fmt.Sprintf(`%s:
  tolerations:
    - key: "node-role.kubernetes.io/control-plane"
      operator: Exists
      effect: NoSchedule
    - key: "node-role.kubernetes.io/master"
      operator: Exists
      effect: NoSchedule
`, name)
	}
	return block("spire-server") + block("spire-controller-manager") +
		block("spire-agent") + block("spiffe-csi-driver")
}

// SPIREValues ports workload_argocd_spire_helm_values (L6952-L6992).
func SPIREValues(cfg *config.Config) string {
	var sb strings.Builder
	sb.WriteString("global:\n")
	sb.WriteString("  spire:\n")
	fmt.Fprintf(&sb, "    clusterName: %s\n", cfg.WorkloadClusterName)
	fmt.Fprintf(&sb, "    trustDomain: k8s.%s.local\n", cfg.WorkloadClusterName)
	if !isTrue(cfg.SPIREHelmEnableGlobalHooks) {
		sb.WriteString("  installAndUpgradeHooks:\n    enabled: false\n")
		sb.WriteString("  deleteHooks:\n    enabled: false\n")
	}
	sb.WriteString(SPIRESubchartTolerations(cfg))
	sb.WriteString("spiffe-oidc-discovery-provider:\n")
	if isTrue(cfg.SPIRETolerateControlPlane) {
		sb.WriteString(`  tolerations:
    - key: "node-role.kubernetes.io/control-plane"
      operator: Exists
      effect: NoSchedule
    - key: "node-role.kubernetes.io/master"
      operator: Exists
      effect: NoSchedule
`)
	}
	if cfg.SPIREOIDCBundleSource == "ConfigMap" {
		sb.WriteString("  bundleSource: ConfigMap\n")
	} else {
		sb.WriteString("  bundleSource: CSI\n")
	}
	sb.WriteString("  config:\n")
	if cfg.KeycloakEnabled {
		sb.WriteString("    setKeyUse: true\n")
	} else {
		sb.WriteString("    setKeyUse: false\n")
	}
	if cfg.KeycloakEnabled && isTrue(cfg.SPIREOIDCInsecureHTTP) {
		sb.WriteString("  tls:\n    spire:\n      enabled: false\n")
	}
	return sb.String()
}

// KeycloakValues ports workload_argocd_keycloak_helm_values
// (L6994-L7036). Returns "" when KEYCLOAK_ENABLED is false.
func KeycloakValues(cfg *config.Config) string {
	if !cfg.KeycloakEnabled {
		return ""
	}
	kcHost := cfg.KeycloakKcHostname
	if kcHost == "" {
		kcHost = cfg.WorkloadClusterName + "-keycloak-keycloakx." +
			cfg.KeycloakNamespace + ".svc.cluster.local"
	}
	strict := cfg.KeycloakKcHostnameStrict
	if strict == "" {
		strict = "false"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, `args:
  - start
extraEnv: |
  - name: KC_HOSTNAME_STRICT
    value: "%s"
  - name: KC_HOSTNAME
    value: "%s"
`, strict, kcHost)
	if cfg.KeycloakKcDB != "" {
		fmt.Fprintf(&sb, "  - name: KC_DB\n    value: \"%s\"\n", cfg.KeycloakKcDB)
	}
	if cfg.SPIREEnabled {
		oidc := cfg.WorkloadClusterName + "-spire-spiffe-oidc-discovery-provider." +
			cfg.SPIRENamespace + ".svc.cluster.local"
		if isTrue(cfg.SPIREOIDCInsecureHTTP) {
			fmt.Fprintf(&sb, `  - name: SPIFFE_OIDC_WELL_KNOWN_URL
    value: "http://%s/.well-known/openid-configuration"
  - name: SPIFFE_OIDC_ISSUER_HOST
    value: "%s"
  - name: KEYCLOAK_SPIRE_IDP_HELP
    value: "Add an Identity Provider (OpenID v1) in Keycloak using SPIFFE_OIDC_WELL_KNOWN_URL; map SPIFFE SVIDs to users as needed."
`, oidc, oidc)
		} else {
			fmt.Fprintf(&sb, `  - name: SPIFFE_OIDC_WELL_KNOWN_URL
    value: "https://%s/.well-known/openid-configuration"
  - name: SPIFFE_OIDC_ISSUER_HOST
    value: "%s"
`, oidc, oidc)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}