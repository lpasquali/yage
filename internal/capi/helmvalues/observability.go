// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package helmvalues

import (
	"fmt"

	"github.com/lpasquali/yage/internal/capi/templates"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
)

// VictoriaMetricsValues returns the VictoriaMetrics Helm values block
// rendered from the yage-manifests template.
func VictoriaMetricsValues(f *manifests.Fetcher, cfg *config.Config) (string, error) {
	return f.Render("addons/observability/values-victoria-metrics.yaml.tmpl", templates.HelmValuesData{Cfg: cfg})
}

// OpenTelemetryValues returns the OpenTelemetry collector Helm values
// block rendered from the yage-manifests template.
func OpenTelemetryValues(f *manifests.Fetcher, cfg *config.Config) (string, error) {
	return f.Render("addons/opentelemetry/values.yaml.tmpl", templates.HelmValuesData{Cfg: cfg})
}

// GrafanaValues returns the Grafana Helm values block rendered from the
// yage-manifests template.
func GrafanaValues(f *manifests.Fetcher, cfg *config.Config) (string, error) {
	return f.Render("addons/observability/values-grafana.yaml.tmpl", templates.HelmValuesData{Cfg: cfg})
}

// SPIRESubchartTolerations returns the tolerations block for
// spire-server, spire-controller-manager, spire-agent, and
// spiffe-csi-driver — or "" when SPIRE_TOLERATE_CONTROL_PLANE is
// false. Kept as a pure-Go helper for backward compatibility until
// issue #142 retires this package.
func SPIRESubchartTolerations(cfg *config.Config) string {
	if !sysinfo.IsTrue(cfg.SPIRETolerateControlPlane) {
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

// SPIREValues returns the SPIRE Helm values block rendered from the
// yage-manifests template.
func SPIREValues(f *manifests.Fetcher, cfg *config.Config) (string, error) {
	return f.Render("addons/spire/values.yaml.tmpl", templates.HelmValuesData{Cfg: cfg})
}

// KeycloakValues returns the Keycloak Helm values block rendered from
// the yage-manifests template.
func KeycloakValues(f *manifests.Fetcher, cfg *config.Config) (string, error) {
	return f.Render("addons/keycloak/values.yaml.tmpl", templates.HelmValuesData{Cfg: cfg})
}
