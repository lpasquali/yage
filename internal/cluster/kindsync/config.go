// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/platform/kubectl"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// configYAMLHeader is the non-secret notice prepended to config.yaml —
// tells anyone reading the Secret where the API secrets live. Mirrors
// the EONOTICE block in apply_bootstrap_config_to_management_cluster.
func configYAMLHeader(cfg *config.Config) string {
	return fmt.Sprintf(
		`# config.yaml — non-secret bootstrap state only. API token secrets are NEVER stored in this file.
# Kind Secrets in %s (use kubectl get secret -n %s):
#   - %s  — CAPI/clusterctl: PROXMOX_TOKEN, PROXMOX_SECRET, …
#   - %s     — all CSI: PROXMOX_CSI_URL, PROXMOX_CSI_USER_ID, tokens, chart/storage/smoke, …
#   - %s   — OpenTofu PVE admin (data key %s)
#   Legacy: if PROXMOX_BOOTSTRAP_SECRET_NAME is set, CAPI+CSI may share that Secret.
# The snapshot below does NOT list PROXMOX_CSI_* (except PROXMOX_CSI_ENABLED as a high-level flag) — use the CSI Secret for driver settings.
# VM_SSH_KEYS holds workload SSH *public* keys (comma-separated); they are not API secrets but are persisted for reproducible clusterctl runs.

`,
		cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapSecretNamespace,
		cfg.Providers.Proxmox.BootstrapCAPMOXSecretName,
		cfg.Providers.Proxmox.BootstrapCSISecretName,
		cfg.Providers.Proxmox.BootstrapAdminSecretName, cfg.Providers.Proxmox.BootstrapAdminSecretKey,
	)
}

// MergeBootstrapSecretsFromKind overlays kind Secret state onto cfg
// in two steps:
//
//  1. The config.yaml Secret — snapshot keys subject to *_EXPLICIT
//     guards; other keys fill-if-empty. Always generic: the snapshot
//     key set is provider-agnostic (see config.Snapshot()).
//  2. Provider credential Secrets — via Provider.BootstrapSecrets()
//     which returns an ordered list of (namespace, name, key-filter)
//     refs. For each ref the generic dispatcher fetches the Secret,
//     decodes all data entries, applies the optional KeyFilter, and
//     calls Provider.AbsorbConfigYAML. Any ref whose Secret is absent
//     on the kind cluster is silently skipped.
//
// The Proxmox provider's BootstrapSecrets() covers the CAPMOX / CSI /
// admin / legacy-combined Secrets plus the capmox-system live copy.
// Non-Proxmox providers return nil and step 2 is a no-op.
func MergeBootstrapSecretsFromKind(cfg *config.Config) {
	ctx, ok := kubectl.ResolveBootstrapContext(cfg)
	if !ok {
		return
	}

	// --- 1. config.yaml (generic snapshot) ---
	if applyConfigYAML(cfg, ctx) {
		logx.Log("Merged bootstrap state from kind bootstrap-config Secret (config.yaml: snapshot keys overlay; CLI --*-explicit take precedence) on %s.", ctx)
	}

	// --- 2. provider credential Secrets ---
	prov, err := provider.For(cfg)
	if err != nil {
		// No provider resolved (e.g. InfraProvider still empty after
		// config.yaml overlay). Continue — the tail cleanup still runs.
		cfg.ReapplyWorkloadGitDefaults()
		cfg.SyncCAPIControllerImagesToClusterctlVersion()
		return
	}
	refs := prov.BootstrapSecrets(cfg)
	for _, ref := range refs {
		if ref.Namespace == "" || ref.Name == "" {
			continue
		}
		secJSON := getSecretJSON(ctx, ref.Namespace, ref.Name)
		if secJSON == "" {
			continue
		}
		// Decode all Secret data entries.
		data := decodeAllSecretData(secJSON)
		if len(data) == 0 {
			continue
		}
		// Also merge embedded sub-YAML blobs (e.g. proxmox-admin.yaml
		// key inside the admin Secret) into the flat map.
		for _, yamlKey := range []string{"proxmox-admin.yaml"} {
			if blob := decodeSecretDataKey(secJSON, yamlKey); blob != "" {
				for k, v := range parseFlatYAMLOrJSON(blob) {
					if v != "" && data[k] == "" {
						data[k] = v
					}
				}
			}
		}
		// Apply optional key filter.
		if len(ref.KeyFilter) > 0 {
			allowed := make(map[string]struct{}, len(ref.KeyFilter))
			for _, k := range ref.KeyFilter {
				allowed[k] = struct{}{}
			}
			filtered := make(map[string]string, len(ref.KeyFilter))
			for k, v := range data {
				if _, ok := allowed[k]; ok {
					filtered[k] = v
				}
			}
			data = filtered
		}
		if prov.AbsorbConfigYAML(cfg, data) {
			logx.Log("Filled unset values from %s/%s on %s.", ref.Namespace, ref.Name, ctx)
			if ref.OnAbsorbed != nil {
				ref.OnAbsorbed(cfg)
			}
		}
	}

	// Tail: reapply workload-git defaults + sync CAPI controller
	// image refs to the (possibly merged) ClusterctlVersion.
	cfg.ReapplyWorkloadGitDefaults()
	cfg.SyncCAPIControllerImagesToClusterctlVersion()
}

// applyConfigYAML fetches the bootstrap-config Secret, parses the
// `config.yaml` (or `config.json`) body into a flat map, applies
// key migrations, then overlays via Config.ApplySnapshotKV.
// Returns true when something was applied.
func applyConfigYAML(cfg *config.Config, ctx string) bool {
	raw := getSecretJSON(ctx, cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapConfigSecretName)
	if raw == "" {
		return false
	}
	body := decodeSecretDataKey(raw, "config.yaml")
	if body == "" {
		body = decodeSecretDataKey(raw, "config.json")
	}
	if strings.TrimSpace(body) == "" {
		return false
	}
	kv := parseFlatYAMLOrJSON(body)
	migrateLegacyKeys(kv)
	cfg.ApplySnapshotKV(kv)
	return true
}

// migrateLegacyKeys folds older worker keys (BOOT_VOLUME_*, NUM_*,
// MEMORY_MIB) into their WORKER_* equivalents and applies the
// TEMPLATE_VMID → PROXMOX_TEMPLATE_ID carry-forward. Modifies kv
// in place.
func migrateLegacyKeys(kv map[string]string) {
	pairs := []struct{ old, new string }{
		{"BOOT_VOLUME_DEVICE", "WORKER_BOOT_VOLUME_DEVICE"},
		{"BOOT_VOLUME_SIZE", "WORKER_BOOT_VOLUME_SIZE"},
		{"NUM_SOCKETS", "WORKER_NUM_SOCKETS"},
		{"NUM_CORES", "WORKER_NUM_CORES"},
		{"MEMORY_MIB", "WORKER_MEMORY_MIB"},
	}
	for _, p := range pairs {
		if kv[p.new] == "" && kv[p.old] != "" {
			kv[p.new] = kv[p.old]
		}
		delete(kv, p.old)
	}
	if kv["PROXMOX_TEMPLATE_ID"] == "" && kv["TEMPLATE_VMID"] != "" {
		kv["PROXMOX_TEMPLATE_ID"] = kv["TEMPLATE_VMID"]
	}
	delete(kv, "TEMPLATE_VMID")
}

// fillEmptyFromMap dispatches to the active provider's
// AbsorbConfigYAML method (§11). Used by TryLoadBootstrapConfigFromKind
// to absorb the config.yaml snapshot into empty cfg fields after the
// generic ApplySnapshotKV pass. Cost-only providers inherit a no-op
// via MinStub.
//
// Returns true when at least one assignment happened.
func fillEmptyFromMap(cfg *config.Config, kv map[string]string) bool {
	prov, err := provider.For(cfg)
	if err != nil {
		// No provider resolved (unknown name, airgapped + cloud).
		// Nothing to absorb; caller continues with cfg unchanged.
		return false
	}
	return prov.AbsorbConfigYAML(cfg, kv)
}

// ApplyBootstrapConfigToManagementCluster ports
// apply_bootstrap_config_to_management_cluster.
func ApplyBootstrapConfigToManagementCluster(cfg *config.Config) error {
	kctx, ok := kubectl.ResolveBootstrapContext(cfg)
	if !ok {
		logx.Warn("Skipping bootstrap config Secret apply — no kind management context in kubeconfig (set KIND_CLUSTER_NAME / --kind-cluster-name or kind export kubeconfig).")
		return nil
	}
	key := cfg.Providers.Proxmox.BootstrapConfigSecretKey
	if key == "" {
		key = "config.yaml"
	}
	body := configYAMLHeader(cfg) + cfg.SnapshotYAML()

	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		logx.Die("Failed to load kubeconfig for %s: %v", kctx, err)
	}
	bg := context.Background()

	if err := cli.EnsureNamespace(bg, cfg.Providers.Proxmox.BootstrapSecretNamespace); err != nil {
		return err
	}

	// Server-side apply the Secret (idempotent equivalent of
	// `kubectl create secret generic ... --dry-run=client -o yaml | kubectl apply -f -`).
	if err := applySecret(bg, cli, cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapConfigSecretName,
		map[string][]byte{key: []byte(body)}, nil); err != nil {
		logx.Die("Failed to apply %s on management cluster: %v", cfg.Providers.Proxmox.BootstrapConfigSecretName, err)
	}
	logx.Log("Updated bootstrap config Secret %s/%s on %s (key %s: non-secret snapshot + file header; API tokens are in capmox/csi/admin Secrets in that namespace — never in this file).",
		cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapConfigSecretName, kctx, key)
	return nil
}

// TryLoadBootstrapConfigFromKind populates cfg from an existing kind
// Secret. Used very early (before CLI is parsed). Returns silently
// when not available.
func TryLoadBootstrapConfigFromKind(cfg *config.Config) {
	ctx, ok := kubectl.ResolveBootstrapContext(cfg)
	if !ok {
		return
	}
	raw := getSecretJSON(ctx, cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapConfigSecretName)
	if raw == "" {
		logx.Warn("Bootstrap config secret not found or empty on %s. Will use env/CLI and save config after kind is up.", ctx)
		return
	}
	body := decodeSecretDataKey(raw, "config.yaml")
	if body == "" {
		body = decodeSecretDataKey(raw, "config.json")
	}
	if strings.TrimSpace(body) == "" {
		logx.Warn("Bootstrap config secret not found or empty on %s. Will use env/CLI and save config after kind is up.", ctx)
		return
	}
	kv := parseFlatYAMLOrJSON(body)
	migrateLegacyKeys(kv)
	cfg.ApplySnapshotKV(kv)
	fillEmptyFromMap(cfg, kv)
	logx.Log("Loaded bootstrap configuration from Secret on %s.", ctx)
}

// --- small helpers ---

// getSecretJSON returns a JSON serialisation of the named Secret on the
// given context, or "" when the Secret is missing or the context cannot
// be loaded. corev1.Secret.Data is a map[string][]byte, and
// json.Marshal encodes []byte values as base64 strings — callers
// that base64-decode .data[key] see the original bytes.
func getSecretJSON(kctx, ns, name string) string {
	if ns == "" || name == "" {
		return ""
	}
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return ""
	}
	sec, err := cli.Typed.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	out, err := json.Marshal(sec)
	if err != nil {
		return ""
	}
	return string(out)
}

// applySecret server-side-applies a Secret on the given context. The Type
// is forced to corev1.SecretTypeOpaque, matching kubectl's
// `--from-file` / `--from-literal` defaults. Labels, when non-nil,
// are written through; pass nil to leave any pre-existing labels
// alone (SSA only sets fields the manifest specifies).
func applySecret(ctx context.Context, cli *k8sclient.Client, ns, name string, data map[string][]byte, labels map[string]string) error {
	sec := corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	body, err := json.Marshal(sec)
	if err != nil {
		return err
	}
	_, err = cli.Typed.CoreV1().Secrets(ns).Patch(ctx, name, types.ApplyPatchType, body, metav1.PatchOptions{
		FieldManager: k8sclient.FieldManager,
		Force:        boolPtr(true),
	})
	return err
}

func boolPtr(b bool) *bool { return &b }

// decodeSecretDataKey base64-decodes secretJSON's .data[key] value. Returns
// "" if the key is missing or the JSON can't be parsed.
func decodeSecretDataKey(secretJSON, key string) string {
	var s struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(secretJSON), &s); err != nil {
		return ""
	}
	v := s.Data[key]
	if v == "" {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

// decodeAllSecretData decodes every data entry of a Secret into a flat
// map[string]string. Base64 decode errors skip the key silently.
func decodeAllSecretData(secretJSON string) map[string]string {
	var s struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(secretJSON), &s); err != nil {
		return nil
	}
	out := make(map[string]string, len(s.Data))
	for k, v := range s.Data {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			continue
		}
		out[k] = string(raw)
	}
	return out
}

var flatKVLineRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$`)

// parseFlatYAMLOrJSON accepts either a JSON object literal
// (config.json) or a flat YAML key:value body (config.yaml).
// Strips `#` comments, honours a single pair of surrounding single-
// or double-quotes on values.
func parseFlatYAMLOrJSON(text string) map[string]string {
	trim := strings.TrimLeftFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := map[string]string{}
	if strings.HasPrefix(trim, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(trim), &obj); err == nil {
			for k, v := range obj {
				out[k] = fmt.Sprint(v)
			}
		}
		return out
	}
	for _, line := range strings.Split(text, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimRight(line, " \t\r\n")
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		m := flatKVLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		val := strings.TrimSpace(m[2])
		if len(val) >= 2 {
			first, last := val[0], val[len(val)-1]
			if first == '"' && last == '"' {
				var s string
				if err := json.Unmarshal([]byte(val), &s); err == nil {
					val = s
				} else {
					val = val[1 : len(val)-1]
				}
			} else if first == '\'' && last == '\'' {
				val = val[1 : len(val)-1]
			}
		}
		out[m[1]] = val
	}
	return out
}

