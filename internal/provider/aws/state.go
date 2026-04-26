package aws

// Phase D state-handoff hooks for AWS: KindSyncFields + TemplateVars.
// Purge stays as MinStub default (nil) — yage doesn't currently
// create AWS-side state outside the workload cluster.
//
// See docs/abstraction-plan.md §11 + §14.D + §13's AWS validation.

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists the AWS-specific configuration the next
// yage run needs to reconstruct the active cluster. Operator-
// supplied credentials (AWS_ACCESS_KEY_ID/_SECRET) deliberately
// stay in env, not in the kind Secret — they're operator state,
// not yage state.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("region", cfg.Providers.AWS.Region)
	add("ami_id", cfg.Providers.AWS.AMIID)
	add("ssh_key_name", cfg.Providers.AWS.SSHKeyName)
	add("control_plane_machine_type", cfg.Providers.AWS.ControlPlaneMachineType)
	add("node_machine_type", cfg.Providers.AWS.NodeMachineType)
	add("mode", cfg.Providers.AWS.Mode)
	add("overhead_tier", cfg.Providers.AWS.OverheadTier)
	return out
}

// TemplateVars returns the AWS-specific clusterctl manifest
// substitution map.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"AWS_REGION":                     orDefault(cfg.Providers.AWS.Region, "us-east-1"),
		"AWS_CONTROL_PLANE_MACHINE_TYPE": orDefault(cfg.Providers.AWS.ControlPlaneMachineType, "t3.large"),
		"AWS_NODE_MACHINE_TYPE":          orDefault(cfg.Providers.AWS.NodeMachineType, "t3.medium"),
		"AWS_AMI_ID":                     cfg.Providers.AWS.AMIID,
		"AWS_SSH_KEY_NAME":               cfg.Providers.AWS.SSHKeyName,
	}
}

// AbsorbConfigYAML is the reverse of KindSyncFields: reads the
// lowercase bare-key map the new yage-system/bootstrap-config Secret
// schema writes (per provider section, no provider prefix — the
// orchestrator strips it before dispatching) and fills empty cfg
// fields with non-empty values. Universal keys (provider,
// cluster_name, …) are handled by the orchestrator and ignored here.
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
			assign(&cfg.Providers.AWS.Region, v)
		case "ami_id":
			assign(&cfg.Providers.AWS.AMIID, v)
		case "ssh_key_name":
			assign(&cfg.Providers.AWS.SSHKeyName, v)
		case "control_plane_machine_type":
			assign(&cfg.Providers.AWS.ControlPlaneMachineType, v)
		case "node_machine_type":
			assign(&cfg.Providers.AWS.NodeMachineType, v)
		case "mode":
			assign(&cfg.Providers.AWS.Mode, v)
		case "overhead_tier":
			assign(&cfg.Providers.AWS.OverheadTier, v)
		}
	}
	return assigned
}
