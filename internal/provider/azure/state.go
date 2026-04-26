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
// Credentials (AZURE_CLIENT_SECRET, etc.) deliberately stay in
// env — they're operator state. ClientID is a non-secret identity
// pointer and is round-tripped here.
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
	add("subscription_id", cfg.Providers.Azure.SubscriptionID)
	add("tenant_id", cfg.Providers.Azure.TenantID)
	add("resource_group", cfg.Providers.Azure.ResourceGroup)
	add("vnet_name", cfg.Providers.Azure.VNetName)
	add("subnet_name", cfg.Providers.Azure.SubnetName)
	add("client_id", cfg.Providers.Azure.ClientID)
	add("identity_model", cfg.Providers.Azure.IdentityModel)
	return out
}

// TemplateVars returns the Azure-specific clusterctl manifest
// substitution map. AZURE_CLIENT_SECRET flows from env (CAPZ
// reads it directly) and is NOT in this map; the rest of the
// Azure-side state landed in commit f6ca113 and is surfaced here
// so the workload manifest can substitute it.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"AZURE_LOCATION":                   orDefault(cfg.Providers.Azure.Location, "eastus"),
		"AZURE_CONTROL_PLANE_MACHINE_TYPE": orDefault(cfg.Providers.Azure.ControlPlaneMachineType, "Standard_D2s_v3"),
		"AZURE_NODE_MACHINE_TYPE":          orDefault(cfg.Providers.Azure.NodeMachineType, "Standard_D2s_v3"),
		"AZURE_SUBSCRIPTION_ID":            cfg.Providers.Azure.SubscriptionID,
		"AZURE_TENANT_ID":                  cfg.Providers.Azure.TenantID,
		"AZURE_RESOURCE_GROUP":             cfg.Providers.Azure.ResourceGroup,
		"AZURE_VNET_NAME":                  cfg.Providers.Azure.VNetName,
		"AZURE_SUBNET_NAME":                cfg.Providers.Azure.SubnetName,
		"AZURE_CLIENT_ID":                  cfg.Providers.Azure.ClientID,
	}
}

// AbsorbConfigYAML is the reverse of KindSyncFields: reads the
// lowercase bare-key map the new yage-system/bootstrap-config
// Secret schema writes (per-provider section; orchestrator strips
// the "azure." prefix before dispatching) and fills empty cfg
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
			assign(&cfg.Providers.Azure.Location, v)
		case "control_plane_machine_type":
			assign(&cfg.Providers.Azure.ControlPlaneMachineType, v)
		case "node_machine_type":
			assign(&cfg.Providers.Azure.NodeMachineType, v)
		case "mode":
			assign(&cfg.Providers.Azure.Mode, v)
		case "overhead_tier":
			assign(&cfg.Providers.Azure.OverheadTier, v)
		case "subscription_id":
			assign(&cfg.Providers.Azure.SubscriptionID, v)
		case "tenant_id":
			assign(&cfg.Providers.Azure.TenantID, v)
		case "resource_group":
			assign(&cfg.Providers.Azure.ResourceGroup, v)
		case "vnet_name":
			assign(&cfg.Providers.Azure.VNetName, v)
		case "subnet_name":
			assign(&cfg.Providers.Azure.SubnetName, v)
		case "client_id":
			assign(&cfg.Providers.Azure.ClientID, v)
		case "identity_model":
			assign(&cfg.Providers.Azure.IdentityModel, v)
		}
	}
	return assigned
}
