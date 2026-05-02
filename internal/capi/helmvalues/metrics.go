// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package helmvalues hosts the Helm-values YAML generators for the
// workload add-ons the bootstrap ships. Each function returns the
// YAML body as a string; callers write it to disk, feed to `helm
// template`, or embed as valuesTemplate inside a HelmChartProxy.
package helmvalues

import (
	"github.com/lpasquali/yage/internal/capi/templates"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
)

// RegisterIsTrue registers the isTrue template function on f.
// Must be called before any Render invocation that uses a template
// with isTrue (ADR 0012 Errata E1).
func RegisterIsTrue(f *manifests.Fetcher) {
	f.RegisterFunc("isTrue", sysinfo.IsTrue)
}

// MetricsServerValues returns the metrics-server Helm values block
// rendered from the yage-manifests template.
func MetricsServerValues(f *manifests.Fetcher, cfg *config.Config) (string, error) {
	return f.Render("addons/metrics-server/values.yaml.tmpl", templates.HelmValuesData{Cfg: cfg})
}
