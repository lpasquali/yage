// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package proxmox

// State-handoff hooks for Proxmox: KindSyncFields (kind-side
// Secret), TemplateVars (clusterctl manifest substitution), Purge
// (cleanup of yage-managed Proxmox state), RolloutMachineAnnotations
// (reconcile nudge for ProxmoxMachine objects during rollout).
//
// See docs/abstraction-plan.md §11 + §14.D.

import (
	"context"
	"fmt"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
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
	add("capi_token", cfg.Providers.Proxmox.CAPIToken)
	add("capi_secret", cfg.Providers.Proxmox.CAPISecret)
	add("region", cfg.Providers.Proxmox.Region)
	add("node", cfg.Providers.Proxmox.Node)
	add("source_node", cfg.Providers.Proxmox.SourceNode)
	add("template_id", cfg.Providers.Proxmox.TemplateID)
	add("bridge", cfg.Providers.Proxmox.Bridge)
	add("pool", cfg.Providers.Proxmox.Pool)
	add("identity_suffix", cfg.Providers.Proxmox.IdentitySuffix)
	add("admin_username", cfg.Providers.Proxmox.AdminUsername)
	// admin_token is intentionally excluded — it is a sensitive credential that
	// should not be persisted to the orchestrator-state Secret (see issue #152/#153).
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
// cluster teardown, CAPI manifest Secrets); this method handles what
// yage created on the Proxmox side:
//
//  1. Proxmox bootstrap namespace on the kind cluster — deletes the
//     entire namespace that holds the CAPMOX/CSI/admin Secrets so the
//     kind cluster is left clean before it is destroyed downstream.
//  2. BPG OpenTofu state — `tofu destroy` reverses EnsureIdentity
//     (CAPI + CSI users + tokens on the PVE cluster).
//  3. Local Proxmox-flavored generated files (CSIConfig, AdminConfig,
//     IdentityTF).
//
// All steps are idempotent: re-running after a step has already been
// applied is a no-op (NotFound errors swallowed per §11).
func (p *Provider) Purge(cfg *config.Config) error {
	// 1. Delete the Proxmox bootstrap namespace on the kind cluster.
	//    This removes the CAPMOX/CSI/admin Secrets without needing
	//    to enumerate them individually. Best-effort: if the kind
	//    cluster is already gone this is silently a no-op.
	if cfg.KindClusterName != "" && cfg.Providers.Proxmox.BootstrapSecretNamespace != "" {
		kctx := "kind-" + cfg.KindClusterName
		if cli, err := k8sclient.ForContext(kctx); err == nil {
			_ = cli.Typed.CoreV1().Namespaces().
				Delete(context.Background(), cfg.Providers.Proxmox.BootstrapSecretNamespace, metav1.DeleteOptions{})
		}
	}

	// 2. Tear down the BPG OpenTofu state if it exists. tofu destroy
	//    reverses EnsureIdentity (CAPI + CSI users + tokens on PVE).
	//    os.Stat-then-act gives idempotency: re-running after the
	//    state is gone is a no-op.
	stateDir := opentofux.StateDir()
	if _, err := os.Stat(stateDir); err == nil {
		_ = opentofux.DestroyIdentity(cfg)
		_ = os.RemoveAll(stateDir)
	}

	// 3. Remove Proxmox-flavored generated files.
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

// RolloutMachineAnnotations patches every ProxmoxMachine in the
// workload cluster namespace that matches selector with the
// "reconcile.cluster.x-k8s.io/request" annotation, nudging the
// CAPMOX controller to re-evaluate each machine. Best-effort:
// individual patch failures are silently ignored (the controller
// will reconcile on its own watch loop regardless).
func (p *Provider) RolloutMachineAnnotations(cfg *config.Config, ctxName, ns, selector, now string) error {
	cli, err := k8sclient.ForContext(ctxName)
	if err != nil {
		return fmt.Errorf("proxmox RolloutMachineAnnotations: cannot build kube client for %s: %w", ctxName, err)
	}
	pmGVR := schema.GroupVersionResource{
		Group:    "infrastructure.cluster.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "proxmoxmachines",
	}
	bg := context.Background()
	ul, err := cli.Dynamic.Resource(pmGVR).Namespace(ns).
		List(bg, metav1.ListOptions{LabelSelector: selector})
	if err != nil || ul == nil {
		// CRD may not exist or no machines present — not an error.
		return nil
	}
	annPatch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{"reconcile.cluster.x-k8s.io/request":"%s"}}}`, now))
	for _, item := range ul.Items {
		name := item.GetName()
		if name == "" {
			continue
		}
		_, _ = cli.Dynamic.Resource(pmGVR).Namespace(ns).
			Patch(bg, name, types.MergePatchType, annPatch, metav1.PatchOptions{})
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
// reads the Proxmox-flavored keys (both uppercase PROXMOX_* from the
// config.yaml / creds.json / csi.json / admin.json envelopes and the
// lowercase bare-key aliases present in raw Secret data entries) and
// fills empty cfg fields with non-empty values. Lives in the provider
// package so kindsync can dispatch to the active provider generically.
// See §11.
//
// Lowercase aliases ("url", "capi_token", "token", "csi_token_id",
// etc.) are the native keys written into the CAPMOX / CSI credential
// Secrets; they are normalised to their PROXMOX_* canonical forms
// before the absorb switch so a single dispatch covers both paths.
func (p *Provider) AbsorbConfigYAML(cfg *config.Config, kv map[string]string) bool {
	// Normalise lowercase aliases to their canonical uppercase form so
	// the switch below stays simple. We merge into a copy so the caller's
	// map is not modified.
	norm := make(map[string]string, len(kv))
	for k, v := range kv {
		norm[k] = v
	}
	applyAlias := func(lower, upper string) {
		if v := norm[lower]; v != "" {
			if norm[upper] == "" {
				norm[upper] = v
			}
		}
	}
	applyAlias("url", "PROXMOX_URL")
	applyAlias("capi_token", "PROXMOX_CAPI_TOKEN")
	applyAlias("capi_secret", "PROXMOX_CAPI_SECRET")
	// backward compat: Secrets written before the CAPI_TOKEN rename
	applyAlias("token", "PROXMOX_CAPI_TOKEN")
	applyAlias("secret", "PROXMOX_CAPI_SECRET")
	applyAlias("capi_token_id", "PROXMOX_CAPI_TOKEN")
	applyAlias("capi_token_secret", "PROXMOX_CAPI_SECRET")
	applyAlias("csi_token_id", "PROXMOX_CSI_TOKEN_ID")
	applyAlias("csi_token_secret", "PROXMOX_CSI_TOKEN_SECRET")
	applyAlias("admin_username", "PROXMOX_ADMIN_USERNAME")
	applyAlias("admin_token", "PROXMOX_ADMIN_TOKEN")
	applyAlias("insecure", "PROXMOX_ADMIN_INSECURE")

	assigned := false
	assign := func(cur *string, v string) {
		if *cur == "" && v != "" {
			*cur = v
			assigned = true
		}
	}
	for k, v := range norm {
		switch k {
		case "PROXMOX_URL":
			assign(&cfg.Providers.Proxmox.URL, v)
		case "PROXMOX_CAPI_TOKEN", "PROXMOX_TOKEN":
			assign(&cfg.Providers.Proxmox.CAPIToken, v)
		case "PROXMOX_CAPI_SECRET", "PROXMOX_SECRET":
			assign(&cfg.Providers.Proxmox.CAPISecret, v)
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

// BootstrapSecrets returns the ordered list of credential Secrets the
// generic kindsync layer fetches and absorbs on each run. Order matters:
// later refs fill only fields still empty after earlier ones.
//
// Layout:
//  1. Single-Secret branch (PROXMOX_BOOTSTRAP_SECRET_NAME set): one
//     combined CAPI+CSI Secret; the admin Secret is conditional.
//  2. Default split (PROXMOX_BOOTSTRAP_SECRET_NAME empty): CAPMOX Secret,
//     CSI Secret, admin Secret.
//  3. Always: capmox-system/capmox-manager-credentials as a last-chance
//     fallback for URL/token/secret (only queried when those fields are
//     still empty at absorption time — controlled by the generic dispatcher).
func (p *Provider) BootstrapSecrets(cfg *config.Config) []provider.BootstrapSecretRef {
	ns := cfg.Providers.Proxmox.BootstrapSecretNamespace

	// admin-only key filter: restricts to the four admin fields so data
	// from an admin Secret doesn't inadvertently overwrite CAPI creds.
	adminKeys := []string{
		"PROXMOX_URL",
		"PROXMOX_ADMIN_USERNAME",
		"PROXMOX_ADMIN_TOKEN",
		"PROXMOX_ADMIN_INSECURE",
		// lowercase aliases that AbsorbConfigYAML now normalises
		"url",
		"admin_username",
		"admin_token",
		"insecure",
	}

	// markKindSecretUsed sets BootstrapKindSecretUsed so bootstrap.go:514
	// skips reading a local clusterctl config file when kind already provided creds.
	markKindSecretUsed := func(c *config.Config) {
		c.Providers.Proxmox.BootstrapKindSecretUsed = true
	}

	// capmox-system/capmox-manager-credentials: last-chance fallback.
	// OnAbsorbed sets KindCAPMOXActive (live controller was the source)
	// and BootstrapKindSecretUsed so bootstrap.go:514 triggers correctly.
	capmoxManagerRef := provider.BootstrapSecretRef{
		Namespace: "capmox-system",
		Name:      "capmox-manager-credentials",
		OnAbsorbed: func(c *config.Config) {
			c.Providers.Proxmox.KindCAPMOXActive = true
			c.Providers.Proxmox.BootstrapKindSecretUsed = true
		},
	}

	// Single-Secret branch
	if cfg.Providers.Proxmox.BootstrapSecretName != "" {
		refs := []provider.BootstrapSecretRef{
			{Namespace: ns, Name: cfg.Providers.Proxmox.BootstrapSecretName, OnAbsorbed: markKindSecretUsed},
		}
		if cfg.Providers.Proxmox.BootstrapAdminSecretName != "" &&
			cfg.Providers.Proxmox.BootstrapAdminSecretName != cfg.Providers.Proxmox.BootstrapSecretName {
			refs = append(refs, provider.BootstrapSecretRef{
				Namespace:  ns,
				Name:       cfg.Providers.Proxmox.BootstrapAdminSecretName,
				KeyFilter:  adminKeys,
				OnAbsorbed: markKindSecretUsed,
			})
		}
		refs = append(refs, capmoxManagerRef)
		return refs
	}

	// Default split branch
	refs := []provider.BootstrapSecretRef{
		{Namespace: ns, Name: cfg.Providers.Proxmox.BootstrapCAPMOXSecretName, OnAbsorbed: markKindSecretUsed},
		{Namespace: ns, Name: cfg.Providers.Proxmox.BootstrapCSISecretName, OnAbsorbed: markKindSecretUsed},
	}
	if cfg.Providers.Proxmox.BootstrapAdminSecretName != "" {
		refs = append(refs, provider.BootstrapSecretRef{
			Namespace:  ns,
			Name:       cfg.Providers.Proxmox.BootstrapAdminSecretName,
			KeyFilter:  adminKeys,
			OnAbsorbed: markKindSecretUsed,
		})
	}
	refs = append(refs, capmoxManagerRef)
	return refs
}

