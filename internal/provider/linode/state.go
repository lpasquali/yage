// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package linode

// State-handoff hooks for Linode/Akamai Cloud (CAPL).
//
// See docs/abstraction-plan.md §11 + §14.D.
// LINODE_TOKEN deliberately stays in env — it's operator state, not yage
// state (same precedent as HCLOUD_TOKEN / DIGITALOCEAN_TOKEN).

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists the Linode-specific configuration the
// next yage run needs to reconstruct the active cluster. The Linode
// API token stays in env and is NOT round-tripped here.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("region", cfg.Providers.Linode.Region)
	add("control_plane_type", cfg.Providers.Linode.ControlPlaneType)
	add("node_type", cfg.Providers.Linode.NodeType)
	add("overhead_tier", cfg.Providers.Linode.OverheadTier)
	return out
}

// TemplateVars returns the Linode-specific clusterctl manifest
// substitution map. LINODE_TOKEN is consumed directly by the CAPL
// controller pod from its env/Secret — not substituted into the manifest.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"LINODE_REGION":      orDefault(cfg.Providers.Linode.Region, "us-east"),
		"LINODE_CP_TYPE":     orDefault(cfg.Providers.Linode.ControlPlaneType, "g6-standard-2"),
		"LINODE_WORKER_TYPE": orDefault(cfg.Providers.Linode.NodeType, "g6-standard-2"),
	}
}

// AbsorbConfigYAML is the reverse of KindSyncFields: reads the
// lowercase bare-key map the yage-system/bootstrap-config Secret
// schema writes (orchestrator strips the "linode." prefix before
// dispatching) and fills empty cfg fields with non-empty values.
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
		case "region":
			assign(&cfg.Providers.Linode.Region, v)
		case "control_plane_type":
			assign(&cfg.Providers.Linode.ControlPlaneType, v)
		case "node_type":
			assign(&cfg.Providers.Linode.NodeType, v)
		case "overhead_tier":
			assign(&cfg.Providers.Linode.OverheadTier, v)
		}
	}
	return assigned
}
