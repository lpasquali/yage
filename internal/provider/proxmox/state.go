package proxmox

// Phase D state-handoff hooks for Proxmox: KindSyncFields (kind-
// side Secret), TemplateVars (clusterctl manifest substitution),
// Purge (cleanup of yage-managed Proxmox state).
//
// See docs/abstraction-plan.md §11 + §14.D.

import (
	"fmt"

	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields returns the Proxmox-specific fields the
// orchestrator persists in Secret/yage-system/bootstrap-config so
// subsequent runs can read them back. Per §11 the orchestrator
// wraps these with "proxmox.<key>" prefixes when writing the
// Secret data; this method returns bare keys.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("url", cfg.Providers.Proxmox.URL)
	add("token", cfg.Providers.Proxmox.Token)
	add("secret", cfg.Providers.Proxmox.Secret)
	add("region", cfg.Providers.Proxmox.Region)
	add("node", cfg.Providers.Proxmox.Node)
	add("source_node", cfg.Providers.Proxmox.SourceNode)
	add("template_id", cfg.Providers.Proxmox.TemplateID)
	add("bridge", cfg.Providers.Proxmox.Bridge)
	add("pool", cfg.Providers.Proxmox.Pool)
	add("identity_suffix", cfg.Providers.Proxmox.IdentitySuffix)
	add("admin_username", cfg.Providers.Proxmox.AdminUsername)
	add("admin_token", cfg.Providers.Proxmox.AdminToken)
	if cfg.Providers.Proxmox.AdminInsecure != "" {
		add("admin_insecure", cfg.Providers.Proxmox.AdminInsecure)
	}
	add("capi_user_id", cfg.Providers.Proxmox.CAPIUserID)
	add("capi_token_prefix", cfg.Providers.Proxmox.CAPITokenPrefix)
	add("csi_url", cfg.Providers.Proxmox.CSIURL)
	add("csi_token_id", cfg.Providers.Proxmox.CSITokenID)
	add("csi_token_secret", cfg.Providers.Proxmox.CSITokenSecret)
	add("csi_user_id", cfg.Providers.Proxmox.CSIUserID)
	add("csi_token_prefix", cfg.Providers.Proxmox.CSITokenPrefix)
	add("csi_insecure", cfg.Providers.Proxmox.CSIInsecure)
	add("csi_storage", cfg.Providers.Proxmox.CSIStorage)
	add("csi_storage_class_name", cfg.Providers.Proxmox.CSIStorageClassName)
	add("cloudinit_storage", cfg.Providers.Proxmox.CloudinitStorage)
	return out
}

// TemplateVars returns the env-style placeholders the clusterctl
// manifest template substitutes for Proxmox. Universal vars
// (CLUSTER_NAME, NAMESPACE, KUBERNETES_VERSION, etc.) come from
// the orchestrator's value map and are NOT included here.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"PROXMOX_URL":               cfg.Providers.Proxmox.URL,
		"PROXMOX_REGION":            cfg.Providers.Proxmox.Region,
		"PROXMOX_NODE":              cfg.Providers.Proxmox.Node,
		"PROXMOX_TEMPLATE_ID":       cfg.Providers.Proxmox.TemplateID,
		"PROXMOX_SOURCENODE":        firstNonEmpty(cfg.Providers.Proxmox.SourceNode, cfg.Providers.Proxmox.Node),
		"BRIDGE":                    cfg.Providers.Proxmox.Bridge,
		"PROXMOX_CLOUDINIT_STORAGE": cfg.Providers.Proxmox.CloudinitStorage,
		"PROXMOX_MEMORY_ADJUSTMENT": cfg.Providers.Proxmox.MemoryAdjustment,
		"PROXMOX_POOL":              cfg.Providers.Proxmox.Pool,
	}
}

// Purge is the Proxmox-specific cleanup half of --purge. The
// orchestrator's purge flow handles cross-cutting cleanup (kind
// cluster, generated dirs, manifest Secrets); this method handles
// what yage created on the Proxmox side: the BPG OpenTofu state
// tree, the CAPI/CSI users + tokens (deleted via tofu destroy), and
// the auto-created pool.
//
// Phase D scaffold: today the orchestrator's purge.go still owns
// the actual Terraform invocation. As the orchestrator's --purge
// flow migrates to call Provider.Purge per §14.D, this body grows
// to absorb that logic. For now we return nil so the orchestrator's
// --purge flow can call this on every provider unconditionally.
func (p *Provider) Purge(cfg *config.Config) error {
	// TODO(D.4): port internal/orchestrator/purge.go's Proxmox-
	// specific cleanup (opentofux destroy + pool delete +
	// generated-files removal) here, behind NotFound-swallowing
	// idempotency per §11. Today's --purge flow still drives
	// these directly; this method is a no-op until that landing.
	_ = fmt.Sprintf // keep imports stable as the body grows
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
