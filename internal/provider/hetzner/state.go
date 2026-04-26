// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

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

// AbsorbConfigYAML is the reverse of KindSyncFields: reads the
// lowercase bare-key map the new yage-system/bootstrap-config
// Secret schema writes (per-provider section; orchestrator strips
// the "hetzner." prefix before dispatching) and fills empty cfg
// fields with non-empty values. Universal keys (provider,
// cluster_name, …) are handled by the orchestrator.
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
		case "location":
			assign(&cfg.Providers.Hetzner.Location, v)
		case "control_plane_machine_type":
			assign(&cfg.Providers.Hetzner.ControlPlaneMachineType, v)
		case "node_machine_type":
			assign(&cfg.Providers.Hetzner.NodeMachineType, v)
		case "overhead_tier":
			assign(&cfg.Providers.Hetzner.OverheadTier, v)
		}
	}
	return assigned
}