// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package proxmox

// State-handoff hooks for Proxmox: KindSyncFields (kind-side
// Secret), TemplateVars (clusterctl manifest substitution), Purge
// (cleanup of yage-managed Proxmox state).
//
// See docs/abstraction-plan.md §11 + §14.D.

import (
	"fmt"
	"os"
	"time"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/opentofux"
	"github.com/lpasquali/yage/internal/provider"
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
// tree (which destroys the CAPI/CSI users + tokens via tofu
// destroy) and the local Proxmox-flavored config files. NotFound
// errors are swallowed for idempotency per §11.
func (p *Provider) Purge(cfg *config.Config) error {
	// 1. Tear down the BPG OpenTofu state if it exists. tofu
	//    destroy reverses EnsureIdentity (CAPI + CSI users +
	//    tokens). os.Stat-then-act gives idempotency: re-running
	//    after the state is gone is a no-op.
	stateDir := opentofux.StateDir()
	if _, err := os.Stat(stateDir); err == nil {
		_ = opentofux.DestroyIdentity(cfg)
		_ = os.RemoveAll(stateDir)
	}

	// 2. Remove Proxmox-flavored generated files. The orchestrator
	//    used to do this directly; ownership now lives with the
	//    provider that wrote them.
	for _, path := range []string{
		cfg.Providers.Proxmox.CSIConfig,
		cfg.Providers.Proxmox.AdminConfig,
		cfg.Providers.Proxmox.IdentityTF,
	} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("purge proxmox file %s: %w", path, err)
		}
	}
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

// AbsorbConfigYAML is the reverse direction of KindSyncFields:
// reads the Proxmox-flavored uppercase keys (PROXMOX_*) the kind-
// side bootstrap Secret + creds/csi/admin JSON envelopes use, and
// fills empty cfg fields with non-empty values. Lives in the
// provider package so kindsync can dispatch to the active provider
// generically. See §11.
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
		case "PROXMOX_URL":
			assign(&cfg.Providers.Proxmox.URL, v)
		case "PROXMOX_TOKEN":
			assign(&cfg.Providers.Proxmox.Token, v)
		case "PROXMOX_SECRET":
			assign(&cfg.Providers.Proxmox.Secret, v)
		case "PROXMOX_REGION":
			assign(&cfg.Providers.Proxmox.Region, v)
		case "PROXMOX_NODE":
			assign(&cfg.Providers.Proxmox.Node, v)
		case "PROXMOX_SOURCENODE":
			assign(&cfg.Providers.Proxmox.SourceNode, v)
		case "PROXMOX_TEMPLATE_ID":
			assign(&cfg.Providers.Proxmox.TemplateID, v)
		case "PROXMOX_BRIDGE":
			assign(&cfg.Providers.Proxmox.Bridge, v)
		case "PROXMOX_CSI_URL":
			assign(&cfg.Providers.Proxmox.CSIURL, v)
		case "PROXMOX_CSI_TOKEN_ID":
			assign(&cfg.Providers.Proxmox.CSITokenID, v)
		case "PROXMOX_CSI_TOKEN_SECRET":
			assign(&cfg.Providers.Proxmox.CSITokenSecret, v)
		case "PROXMOX_CSI_USER_ID":
			assign(&cfg.Providers.Proxmox.CSIUserID, v)
		case "PROXMOX_CSI_TOKEN_PREFIX":
			assign(&cfg.Providers.Proxmox.CSITokenPrefix, v)
		case "PROXMOX_CSI_INSECURE":
			assign(&cfg.Providers.Proxmox.CSIInsecure, v)
		case "PROXMOX_CSI_STORAGE_CLASS_NAME":
			assign(&cfg.Providers.Proxmox.CSIStorageClassName, v)
		case "PROXMOX_CSI_STORAGE":
			assign(&cfg.Providers.Proxmox.CSIStorage, v)
		case "PROXMOX_CSI_RECLAIM_POLICY":
			assign(&cfg.Providers.Proxmox.CSIReclaimPolicy, v)
		case "PROXMOX_CSI_FSTYPE":
			assign(&cfg.Providers.Proxmox.CSIFsType, v)
		case "PROXMOX_CSI_DEFAULT_CLASS":
			assign(&cfg.Providers.Proxmox.CSIDefaultClass, v)
		case "PROXMOX_CSI_TOPOLOGY_LABELS":
			assign(&cfg.Providers.Proxmox.CSITopologyLabels, v)
		case "PROXMOX_TOPOLOGY_REGION":
			assign(&cfg.Providers.Proxmox.TopologyRegion, v)
		case "PROXMOX_TOPOLOGY_ZONE":
			assign(&cfg.Providers.Proxmox.TopologyZone, v)
		case "PROXMOX_CAPI_USER_ID":
			assign(&cfg.Providers.Proxmox.CAPIUserID, v)
		case "PROXMOX_CAPI_TOKEN_PREFIX":
			assign(&cfg.Providers.Proxmox.CAPITokenPrefix, v)
		case "PROXMOX_ADMIN_USERNAME":
			assign(&cfg.Providers.Proxmox.AdminUsername, v)
		case "PROXMOX_ADMIN_TOKEN":
			assign(&cfg.Providers.Proxmox.AdminToken, v)
		case "PROXMOX_ADMIN_INSECURE":
			assign(&cfg.Providers.Proxmox.AdminInsecure, v)
		}
	}
	return assigned
}

// PivotTarget returns the destination kubeconfig + namespaces for
// clusterctl move. Proxmox is the only provider that ships a real
// pivot target — the BPG-managed mgmt cluster running on Proxmox
// VMs.
//
// The kubeconfig path is read from cfg.MgmtKubeconfigPath, which
// the orchestrator sets after EnsureManagementCluster() returns
// (per §13.4 #5). When pivot is disabled or the kubeconfig path
// is unset, return ErrNotApplicable so the orchestrator falls
// through to keeping kind as the mgmt cluster.
func (p *Provider) PivotTarget(cfg *config.Config) (provider.PivotTarget, error) {
	if !cfg.Pivot.Enabled {
		return provider.PivotTarget{}, provider.ErrNotApplicable
	}
	if cfg.MgmtKubeconfigPath == "" {
		// EnsureManagementCluster hasn't run yet (or pivot is being
		// queried out of order). The orchestrator's pivot phase
		// fills this field in; before then there's nothing to
		// point at.
		return provider.PivotTarget{}, fmt.Errorf("pivot target not yet ready: cfg.MgmtKubeconfigPath empty")
	}
	return provider.PivotTarget{
		KubeconfigPath: cfg.MgmtKubeconfigPath,
		Namespaces:     nil, // nil = all CAPI namespaces (idiomatic sentinel)
		ReadyTimeout:   10 * time.Minute,
	}, nil
}