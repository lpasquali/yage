// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package gcp

// State-handoff hooks for GCP.
//
// See docs/abstraction-plan.md §11 + §14.D + §13's GCP validation.

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists the GCP-specific configuration. Service
// account JSON / ADC stays in env (operator state); Network and
// ImageFamily landed on cfg in commit f6ca113 and round-trip here.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("project", cfg.Providers.GCP.Project)
	add("region", cfg.Providers.GCP.Region)
	add("control_plane_machine_type", cfg.Providers.GCP.ControlPlaneMachineType)
	add("node_machine_type", cfg.Providers.GCP.NodeMachineType)
	add("mode", cfg.Providers.GCP.Mode)
	add("overhead_tier", cfg.Providers.GCP.OverheadTier)
	add("network", cfg.Providers.GCP.Network)
	add("image_family", cfg.Providers.GCP.ImageFamily)
	add("identity_model", cfg.Providers.GCP.IdentityModel)
	return out
}

// TemplateVars returns the GCP-specific clusterctl manifest
// substitution map. GCP_B64ENCODED_CREDENTIALS comes from env
// (CAPG reads it directly) and is NOT in this map.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"GCP_PROJECT":                    cfg.Providers.GCP.Project,
		"GCP_REGION":                     orDefault(cfg.Providers.GCP.Region, "us-central1"),
		"GCP_CONTROL_PLANE_MACHINE_TYPE": orDefault(cfg.Providers.GCP.ControlPlaneMachineType, "n2-standard-2"),
		"GCP_NODE_MACHINE_TYPE":          orDefault(cfg.Providers.GCP.NodeMachineType, "n2-standard-2"),
		"GCP_NETWORK_NAME":               cfg.Providers.GCP.Network,
		"GCP_IMAGE_FAMILY":               cfg.Providers.GCP.ImageFamily,
	}
}

// AbsorbConfigYAML is the reverse of KindSyncFields: reads the
// lowercase bare-key map the new yage-system/bootstrap-config
// Secret schema writes (per-provider section; orchestrator strips
// the "gcp." prefix before dispatching) and fills empty cfg fields
// with non-empty values. Universal keys (provider, cluster_name, …)
// are handled by the orchestrator.
func (p *Provider) AbsorbConfigYAML(cfg *config.Config, kv map[string]string) bool {
	assigned := false
	assign := func(cur *string, v string) {
		if *cur == "" && v != "" {
			*cur = v
			assigned = true
		}
	}
	for k, v := range kv {
		switch k {
		case "project":
			assign(&cfg.Providers.GCP.Project, v)
		case "region":
			assign(&cfg.Providers.GCP.Region, v)
		case "control_plane_machine_type":
			assign(&cfg.Providers.GCP.ControlPlaneMachineType, v)
		case "node_machine_type":
			assign(&cfg.Providers.GCP.NodeMachineType, v)
		case "mode":
			assign(&cfg.Providers.GCP.Mode, v)
		case "overhead_tier":
			assign(&cfg.Providers.GCP.OverheadTier, v)
		case "network":
			assign(&cfg.Providers.GCP.Network, v)
		case "image_family":
			assign(&cfg.Providers.GCP.ImageFamily, v)
		case "identity_model":
			assign(&cfg.Providers.GCP.IdentityModel, v)
		}
	}
	return assigned
}