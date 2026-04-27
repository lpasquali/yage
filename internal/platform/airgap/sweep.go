// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package airgap

import "github.com/lpasquali/yage/internal/config"

// RewriteConfigChartURLs walks every Helm chart-repo URL on cfg
// through RewriteHelmRepo. No-op when HelmMirror() is unset.
//
// Called once from cmd/yage/main.go after cli.Parse + airgap.Apply
// — by then the mirror global is set, so this single sweep covers
// every yage-bundled add-on without each call site having to wrap
// individual chart URLs.
//
// Inline chart URLs in the manifest renderers (caaph/argo/pivot
// HelmChartProxy literals, etc.) wrap their literals at render
// time; the §17 design has both "mirror at config-load" and
// "mirror at render time" paths because some chart references live
// in cfg, others are hard-coded in template strings.
func RewriteConfigChartURLs(cfg *config.Config) {
	if HelmMirror() == "" || cfg == nil {
		return
	}
	cfg.KyvernoChartRepoURL = RewriteHelmRepo(cfg.KyvernoChartRepoURL)
	cfg.CertManagerChartRepoURL = RewriteHelmRepo(cfg.CertManagerChartRepoURL)
	cfg.CrossplaneChartRepoURL = RewriteHelmRepo(cfg.CrossplaneChartRepoURL)
	cfg.CNPGChartRepoURL = RewriteHelmRepo(cfg.CNPGChartRepoURL)
	cfg.ExternalSecretsChartRepoURL = RewriteHelmRepo(cfg.ExternalSecretsChartRepoURL)
	cfg.InfisicalChartRepoURL = RewriteHelmRepo(cfg.InfisicalChartRepoURL)
	cfg.SPIREChartRepoURL = RewriteHelmRepo(cfg.SPIREChartRepoURL)
	cfg.OTELChartRepoURL = RewriteHelmRepo(cfg.OTELChartRepoURL)
	cfg.GrafanaChartRepoURL = RewriteHelmRepo(cfg.GrafanaChartRepoURL)
	cfg.VictoriaMetricsChartRepoURL = RewriteHelmRepo(cfg.VictoriaMetricsChartRepoURL)
	cfg.BackstageChartRepoURL = RewriteHelmRepo(cfg.BackstageChartRepoURL)
	cfg.KeycloakChartRepoURL = RewriteHelmRepo(cfg.KeycloakChartRepoURL)
	cfg.Providers.Proxmox.CSIChartRepoURL = RewriteHelmRepo(cfg.Providers.Proxmox.CSIChartRepoURL)
}
