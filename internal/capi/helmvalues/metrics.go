// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package helmvalues hosts the Helm-values YAML generators for the
// workload add-ons the bootstrap ships. Each function returns the
// YAML body as a string; callers write it to disk, feed to `helm
// template`, or embed as valuesTemplate inside a HelmChartProxy.
package helmvalues

import (
	"github.com/lpasquali/yage/internal/config"
)

// MetricsServerValues returns the metrics-server Helm values block.
// Returns empty string when insecure-TLS is false (the Helm chart
// keeps its defaults).
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