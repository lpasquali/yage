// Package helmvalues hosts the Helm-values YAML generators for the
// workload add-ons the bootstrap ships. Each function returns the YAML
// body as a string; callers write it to disk, feed to `helm template`,
// or embed as valuesTemplate inside a HelmChartProxy.
//
// Bash source map (yage.sh):
//   - workload_argocd_metrics_server_helm_values             ~L6010-6024
//   - workload_argocd_victoria_helm_values                   ~L6826-6850
//   - workload_argocd_opentelemetry_helm_values              ~L6852-6858
//   - workload_argocd_grafana_helm_values                    ~L6860-6912
//   - _wl_argocd_spire_subchart_tolerations                  ~L6914-6950
//   - workload_argocd_spire_helm_values                      ~L6952-6992
//   - workload_argocd_keycloak_helm_values                   ~L6994-7037
package helmvalues

import (
	"github.com/lpasquali/yage/internal/config"
)

// MetricsServerValues ports workload_argocd_metrics_server_helm_values.
// Returns empty string when insecure-TLS is false (bash returns no
// output and the Helm chart keeps its defaults).
func MetricsServerValues(cfg *config.Config) string {
	if cfg.WorkloadMetricsServerInsecureTLS == "" ||
		!isTrue(cfg.WorkloadMetricsServerInsecureTLS) {
		return ""
	}
	return `defaultArgs:
  - --cert-dir=/tmp
  - --kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname
  - --kubelet-use-node-status-port
  - --metric-resolution=15s
args:
  - --kubelet-insecure-tls
`
}

func isTrue(s string) bool {
	switch s {
	case "true", "1", "yes", "y", "on", "TRUE", "Yes", "Y", "On":
		return true
	}
	return false
}
