// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package helmvalues_test

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/capi/helmvalues"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
)

// fetcher returns a Fetcher with isTrue registered, pointed at the
// in-package testdata fixtures that mirror the yage-manifests layout.
func fetcher() *manifests.Fetcher {
	f := &manifests.Fetcher{MountRoot: "testdata"}
	helmvalues.RegisterIsTrue(f)
	return f
}

func defaultCfg() *config.Config {
	c := &config.Config{}
	c.WorkloadClusterName = "test-cluster"
	c.WorkloadMetricsServerInsecureTLS = "false"
	c.OTELCollectorMode = "deployment"
	c.OTELImageRepository = "otel/opentelemetry-collector-k8s"
	c.VictoriaMetricsEnabled = false
	c.VictoriaMetricsNamespace = "victoria-metrics"
	c.SPIRETolerateControlPlane = "false"
	c.SPIREHelmEnableGlobalHooks = "false"
	c.SPIREOIDCBundleSource = "CSI"
	c.SPIREOIDCInsecureHTTP = "false"
	c.SPIRENamespace = "spire"
	c.KeycloakEnabled = false
	c.KeycloakNamespace = "keycloak"
	c.KeycloakKcHostname = ""
	c.KeycloakKcHostnameStrict = "false"
	c.KeycloakKcDB = ""
	c.SPIREEnabled = false
	return c
}

// --- MetricsServerValues ---

func TestMetricsServerValues_InsecureTLSFalse(t *testing.T) {
	cfg := defaultCfg()
	cfg.WorkloadMetricsServerInsecureTLS = "false"

	got, err := helmvalues.MetricsServerValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("MetricsServerValues error: %v", err)
	}
	// When insecure TLS is false the template emits only the header comment
	// with no YAML body (the {{- if isTrue }} block is skipped).
	if strings.Contains(got, "kubelet-insecure-tls") {
		t.Errorf("MetricsServerValues (false): unexpected insecure-tls line in:\n%s", got)
	}
}

func TestMetricsServerValues_InsecureTLSTrue(t *testing.T) {
	cfg := defaultCfg()
	cfg.WorkloadMetricsServerInsecureTLS = "true"

	got, err := helmvalues.MetricsServerValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("MetricsServerValues error: %v", err)
	}
	want := `defaultArgs:
  - --cert-dir=/tmp
  - --kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname
  - --kubelet-use-node-status-port
  - --metric-resolution=15s
args:
  - --kubelet-insecure-tls`

	if !strings.Contains(got, want) {
		t.Errorf("MetricsServerValues (true) missing expected block:\ngot:\n%s", got)
	}
}

// --- VictoriaMetricsValues ---

func TestVictoriaMetricsValues_Static(t *testing.T) {
	cfg := defaultCfg()

	got, err := helmvalues.VictoriaMetricsValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("VictoriaMetricsValues error: %v", err)
	}
	checks := []string{
		"server:",
		"fullnameOverride: vmsingle",
		"kubernetes-endpoints-metrics-ports",
		"metrics|http-metrics",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("VictoriaMetricsValues missing %q\nfull:\n%s", c, got)
		}
	}
}

// --- OpenTelemetryValues ---

func TestOpenTelemetryValues_Defaults(t *testing.T) {
	cfg := defaultCfg()

	got, err := helmvalues.OpenTelemetryValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("OpenTelemetryValues error: %v", err)
	}
	if !strings.Contains(got, "mode: deployment") {
		t.Errorf("OpenTelemetryValues missing mode; got:\n%s", got)
	}
	if !strings.Contains(got, "otel/opentelemetry-collector-k8s") {
		t.Errorf("OpenTelemetryValues missing image repo; got:\n%s", got)
	}
}

func TestOpenTelemetryValues_CustomMode(t *testing.T) {
	cfg := defaultCfg()
	cfg.OTELCollectorMode = "daemonset"
	cfg.OTELImageRepository = "my.registry/otel-collector"

	got, err := helmvalues.OpenTelemetryValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("OpenTelemetryValues error: %v", err)
	}
	if !strings.Contains(got, "mode: daemonset") {
		t.Errorf("OpenTelemetryValues missing daemonset mode; got:\n%s", got)
	}
	if !strings.Contains(got, "my.registry/otel-collector") {
		t.Errorf("OpenTelemetryValues missing custom image repo; got:\n%s", got)
	}
}

// --- GrafanaValues ---

func TestGrafanaValues_VMDisabled(t *testing.T) {
	cfg := defaultCfg()
	cfg.VictoriaMetricsEnabled = false

	got, err := helmvalues.GrafanaValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("GrafanaValues error: %v", err)
	}
	if !strings.Contains(got, "service:") || !strings.Contains(got, "type: ClusterIP") {
		t.Errorf("GrafanaValues (vm=false) missing service block; got:\n%s", got)
	}
	if strings.Contains(got, "datasources:") {
		t.Errorf("GrafanaValues (vm=false) must not contain datasources; got:\n%s", got)
	}
}

func TestGrafanaValues_VMEnabled(t *testing.T) {
	cfg := defaultCfg()
	cfg.VictoriaMetricsEnabled = true
	cfg.VictoriaMetricsNamespace = "victoria-metrics"

	got, err := helmvalues.GrafanaValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("GrafanaValues error: %v", err)
	}
	checks := []string{
		"datasources:",
		"VictoriaMetrics",
		"http://vmsingle.victoria-metrics.svc:8428",
		"dashboardProviders:",
		"dashboards:",
		"k8s-cluster:",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("GrafanaValues (vm=true) missing %q\nfull:\n%s", c, got)
		}
	}
}

// --- SPIREValues ---

func TestSPIREValues_DefaultsNoTolerations(t *testing.T) {
	cfg := defaultCfg()
	cfg.SPIRETolerateControlPlane = "false"
	cfg.SPIREHelmEnableGlobalHooks = "false"
	cfg.SPIREOIDCBundleSource = "CSI"

	got, err := helmvalues.SPIREValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("SPIREValues error: %v", err)
	}
	if !strings.Contains(got, "clusterName: test-cluster") {
		t.Errorf("SPIREValues missing clusterName; got:\n%s", got)
	}
	if !strings.Contains(got, "trustDomain: k8s.test-cluster.local") {
		t.Errorf("SPIREValues missing trustDomain; got:\n%s", got)
	}
	if strings.Contains(got, "tolerations:") {
		t.Errorf("SPIREValues with tolerateCP=false must not contain tolerations; got:\n%s", got)
	}
	if !strings.Contains(got, "installAndUpgradeHooks:") {
		t.Errorf("SPIREValues missing installAndUpgradeHooks (hooks disabled by default); got:\n%s", got)
	}
	if !strings.Contains(got, "bundleSource: CSI") {
		t.Errorf("SPIREValues missing bundleSource CSI; got:\n%s", got)
	}
}

func TestSPIREValues_WithTolerations(t *testing.T) {
	cfg := defaultCfg()
	cfg.SPIRETolerateControlPlane = "true"

	got, err := helmvalues.SPIREValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("SPIREValues error: %v", err)
	}
	if !strings.Contains(got, "spire-server:") {
		t.Errorf("SPIREValues with tolerateCP=true missing spire-server tolerations; got:\n%s", got)
	}
	if !strings.Contains(got, "node-role.kubernetes.io/control-plane") {
		t.Errorf("SPIREValues with tolerateCP=true missing control-plane taint; got:\n%s", got)
	}
}

func TestSPIREValues_OIDCInsecureWithKeycloak(t *testing.T) {
	cfg := defaultCfg()
	cfg.SPIREOIDCInsecureHTTP = "true"
	cfg.KeycloakEnabled = true

	got, err := helmvalues.SPIREValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("SPIREValues error: %v", err)
	}
	if !strings.Contains(got, "tls:") || !strings.Contains(got, "enabled: false") {
		t.Errorf("SPIREValues (oidc insecure + keycloak) missing tls.spire.enabled: false; got:\n%s", got)
	}
}

// --- KeycloakValues ---

func TestKeycloakValues_Disabled(t *testing.T) {
	cfg := defaultCfg()
	cfg.KeycloakEnabled = false

	got, err := helmvalues.KeycloakValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("KeycloakValues error: %v", err)
	}
	// When disabled the template emits nothing (only the header comment).
	if strings.Contains(got, "KC_HOSTNAME") {
		t.Errorf("KeycloakValues (disabled) must not contain KC_HOSTNAME; got:\n%s", got)
	}
}

func TestKeycloakValues_EnabledNoSPIRE(t *testing.T) {
	cfg := defaultCfg()
	cfg.KeycloakEnabled = true
	cfg.KeycloakKcHostname = "keycloak.example.com"
	cfg.KeycloakKcHostnameStrict = "false"
	cfg.SPIREEnabled = false

	got, err := helmvalues.KeycloakValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("KeycloakValues error: %v", err)
	}
	checks := []string{
		"KC_HOSTNAME",
		"keycloak.example.com",
		"KC_HOSTNAME_STRICT",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("KeycloakValues (enabled, no spire) missing %q\nfull:\n%s", c, got)
		}
	}
	if strings.Contains(got, "SPIFFE_OIDC") {
		t.Errorf("KeycloakValues (no spire) must not contain SPIFFE_OIDC; got:\n%s", got)
	}
}

func TestKeycloakValues_EnabledWithSPIREInsecureHTTP(t *testing.T) {
	cfg := defaultCfg()
	cfg.KeycloakEnabled = true
	cfg.KeycloakKcHostname = "keycloak.example.com"
	cfg.KeycloakKcHostnameStrict = "false"
	cfg.SPIREEnabled = true
	cfg.SPIREOIDCInsecureHTTP = "true"
	cfg.SPIRENamespace = "spire"

	got, err := helmvalues.KeycloakValues(fetcher(), cfg)
	if err != nil {
		t.Fatalf("KeycloakValues error: %v", err)
	}
	checks := []string{
		"SPIFFE_OIDC_WELL_KNOWN_URL",
		"http://test-cluster-spire-spiffe-oidc-discovery-provider.spire.svc.cluster.local/.well-known/openid-configuration",
		"KEYCLOAK_SPIRE_IDP_HELP",
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Errorf("KeycloakValues (spire insecure) missing %q\nfull:\n%s", c, got)
		}
	}
}
