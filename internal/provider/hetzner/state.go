package hetzner

// Phase D state-handoff hooks for Hetzner Cloud.
//
// See docs/abstraction-plan.md §11 + §14.D + §13's Hetzner validation.

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields for Hetzner: minimal. Per-cluster state lives in
// the operator's HCLOUD_TOKEN scope; yage carries the few region/
// type fields the next run needs.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("location", cfg.Providers.Hetzner.Location)
	add("control_plane_machine_type", cfg.Providers.Hetzner.ControlPlaneMachineType)
	add("node_machine_type", cfg.Providers.Hetzner.NodeMachineType)
	add("overhead_tier", cfg.Providers.Hetzner.OverheadTier)
	return out
}

// TemplateVars returns Hetzner clusterctl manifest substitutions.
// HCLOUD_TOKEN is operator-supplied via env and not in this map —
// CAPHV reads it directly.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"HCLOUD_REGION":                     orDefault(cfg.Providers.Hetzner.Location, "fsn1"),
		"HCLOUD_CONTROL_PLANE_MACHINE_TYPE": orDefault(cfg.Providers.Hetzner.ControlPlaneMachineType, "cx22"),
		"HCLOUD_WORKER_MACHINE_TYPE":        orDefault(cfg.Providers.Hetzner.NodeMachineType, "cx22"),
	}
}
