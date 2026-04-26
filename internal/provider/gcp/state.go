package gcp

// Phase D state-handoff hooks for GCP.
//
// See docs/abstraction-plan.md §11 + §14.D + §13's GCP validation.

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists the GCP-specific configuration. Service
// account JSON / ADC stays in env (operator state).
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
	}
}
