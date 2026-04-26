package azure

// Phase D state-handoff hooks for Azure.
//
// See docs/abstraction-plan.md §11 + §14.D + §13's Azure validation.
// Identity-model discriminator (SP / Managed Identity / Workload
// Identity) deferred per §13.4 #4 — today Azure consumes operator-
// supplied creds via env, so KindSyncFields holds only the
// non-secret runtime config.

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists the Azure-specific configuration the
// next yage run needs to reconstruct the active cluster.
// Credentials (AZURE_CLIENT_ID/SECRET, etc.) deliberately stay in
// env — they're operator state.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("location", cfg.Providers.Azure.Location)
	add("control_plane_machine_type", cfg.Providers.Azure.ControlPlaneMachineType)
	add("node_machine_type", cfg.Providers.Azure.NodeMachineType)
	add("mode", cfg.Providers.Azure.Mode)
	add("overhead_tier", cfg.Providers.Azure.OverheadTier)
	return out
}

// TemplateVars returns the Azure-specific clusterctl manifest
// substitution map. Subscription/Tenant/Client IDs flow from env
// (CAPZ reads them directly) and are NOT in this map.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"AZURE_LOCATION":                    orDefault(cfg.Providers.Azure.Location, "eastus"),
		"AZURE_CONTROL_PLANE_MACHINE_TYPE":  orDefault(cfg.Providers.Azure.ControlPlaneMachineType, "Standard_D2s_v3"),
		"AZURE_NODE_MACHINE_TYPE":           orDefault(cfg.Providers.Azure.NodeMachineType, "Standard_D2s_v3"),
	}
}
