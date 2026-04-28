// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package digitalocean

// State-handoff hooks for DigitalOcean Cloud (CAPDO).
//
// See docs/abstraction-plan.md §11 + §14.D.
// DIGITALOCEAN_TOKEN deliberately stays in env — it's operator state,
// not yage state (same precedent as HCLOUD_TOKEN / AWS_ACCESS_KEY_ID).

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists the DigitalOcean-specific configuration the
// next yage run needs to reconstruct the active cluster. The DO API
// token stays in env and is NOT round-tripped here.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("region", cfg.Providers.DigitalOcean.Region)
	add("control_plane_size", cfg.Providers.DigitalOcean.ControlPlaneSize)
	add("node_size", cfg.Providers.DigitalOcean.NodeSize)
	add("vpc_uuid", cfg.Providers.DigitalOcean.VPCUUID)
	add("overhead_tier", cfg.Providers.DigitalOcean.OverheadTier)
	return out
}

// TemplateVars returns the DigitalOcean-specific clusterctl manifest
// substitution map. The DIGITALOCEAN_TOKEN env var is consumed directly
// by the CAPDO controller pod — not substituted into the manifest.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"DO_REGION":      orDefault(cfg.Providers.DigitalOcean.Region, "nyc3"),
		"DO_CP_SIZE":     orDefault(cfg.Providers.DigitalOcean.ControlPlaneSize, "s-2vcpu-4gb"),
		"DO_WORKER_SIZE": orDefault(cfg.Providers.DigitalOcean.NodeSize, "s-2vcpu-4gb"),
		"DO_VPC_UUID":    cfg.Providers.DigitalOcean.VPCUUID,
	}
}

// AbsorbConfigYAML is the reverse of KindSyncFields: reads the
// lowercase bare-key map the yage-system/bootstrap-config Secret
// schema writes (orchestrator strips the "digitalocean." prefix before
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
			assign(&cfg.Providers.DigitalOcean.Region, v)
		case "control_plane_size":
			assign(&cfg.Providers.DigitalOcean.ControlPlaneSize, v)
		case "node_size":
			assign(&cfg.Providers.DigitalOcean.NodeSize, v)
		case "vpc_uuid":
			assign(&cfg.Providers.DigitalOcean.VPCUUID, v)
		case "overhead_tier":
			assign(&cfg.Providers.DigitalOcean.OverheadTier, v)
		}
	}
	return assigned
}
