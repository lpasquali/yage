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

// MergeProxmoxBootstrapSecretsFromKind ports
// merge_proxmox_bootstrap_secrets_from_kind. Overlays from (in order):
//
//  1. the config.yaml Secret — snapshot keys subject to *_EXPLICIT
//     guards, other keys fill-if-empty.
//  2. the CAPI/CSI credentials Secret(s): legacy single Secret when
//     PROXMOX_BOOTSTRAP_SECRET_NAME is set, otherwise split
//     CAPMOX + CSI Secrets, with a fallback to the old
//     "proxmox-bootstrap-credentials" name.
//  3. the admin Secret (proxmox-admin.yaml data key) when distinct
//     from the legacy credentials Secret.
//  4. capmox-system/capmox-manager-credentials — last-chance fallback
//     for PROXMOX_URL/TOKEN/SECRET.
//
// Sets Providers.Proxmox.BootstrapKindSecretUsed=true when any Secret contributed
// data, and Providers.Proxmox.KindCAPMOXActive=true when the capmox-system
// Secret was the one that filled URL/token/secret.
func MergeProxmoxBootstrapSecretsFromKind(cfg *config.Config) {
	ctx, ok := kubectl.ResolveBootstrapContext(cfg)
	if !ok {
		return
	}

	// --- 1. config.yaml ---
	if applyConfigYAML(cfg, ctx) {
		logx.Log("Merged bootstrap state from %s/%s (config.yaml: snapshot keys overlay in-cluster; CLI --*-explicit take precedence) on %s.",
			cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapConfigSecretName, ctx)
		cfg.Providers.Proxmox.BootstrapKindSecretUsed = true
	}

	// --- 2. credentials ---
	var capmoxJSON, csiJSON, legacyJSON string
	if cfg.Providers.Proxmox.BootstrapSecretName != "" {
		legacyJSON = getSecretJSON(ctx, cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapSecretName)
	} else {
		capmoxJSON = getSecretJSON(ctx, cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapCAPMOXSecretName)
		csiJSON = getSecretJSON(ctx, cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapCSISecretName)
		if capmoxJSON == "" && csiJSON == "" {
			// Fallback to the pre-split Secret name.
			legacyJSON = getSecretJSON(ctx, cfg.Providers.Proxmox.BootstrapSecretNamespace, "proxmox-bootstrap-credentials")
		}
	}
	if legacyJSON != "" {
		if fillFromCredsJSON(cfg, legacyJSON, true) {
			logx.Log("Filled unset values from cluster API secrets on %s (legacy or migration combined Secret).", ctx)
			cfg.Providers.Proxmox.BootstrapKindSecretUsed = true
		}
	}
	if cfg.Providers.Proxmox.BootstrapSecretName == "" && capmoxJSON != "" {
		if fillFromCredsJSON(cfg, capmoxJSON, true) {
			logx.Log("Filled unset values from %s/%s on %s.",
				cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapCAPMOXSecretName, ctx)
			cfg.Providers.Proxmox.BootstrapKindSecretUsed = true
		}
	}
	if cfg.Providers.Proxmox.BootstrapSecretName == "" && csiJSON != "" {
		if fillFromCSIJSON(cfg, csiJSON) {
			logx.Log("Filled unset values from %s/%s on %s.",
				cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapCSISecretName, ctx)
			cfg.Providers.Proxmox.BootstrapKindSecretUsed = true
		}
	}

	// --- 3. admin Secret (skip when same as legacy or same name already merged) ---
	if cfg.Providers.Proxmox.BootstrapAdminSecretName != "" &&
		(cfg.Providers.Proxmox.BootstrapSecretName == "" ||
			cfg.Providers.Proxmox.BootstrapAdminSecretName != cfg.Providers.Proxmox.BootstrapSecretName) {
		j := getSecretJSON(ctx, cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapAdminSecretName)
		if j != "" && fillFromAdminJSON(cfg, j) {
			logx.Log("Filled unset values from %s/%s (OpenTofu / admin) on %s.",
				cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapAdminSecretName, ctx)
			cfg.Providers.Proxmox.BootstrapKindSecretUsed = true
		}
	}

	// --- 4. capmox-system/capmox-manager-credentials fallback ---
	if cfg.Providers.Proxmox.URL == "" || cfg.Providers.Proxmox.Token == "" || cfg.Providers.Proxmox.Secret == "" {
		j := getSecretJSON(ctx, "capmox-system", "capmox-manager-credentials")
		if j != "" && fillFromCapmoxManagerJSON(cfg, j) {
			logx.Log("Filled unset CAPI Proxmox API values from capmox-system/capmox-manager-credentials on %s.", ctx)
			cfg.Providers.Proxmox.KindCAPMOXActive = true
		}
	}

	// Bash tail: reapply_workload_git_defaults + sync CAPI controller
	// image refs to the (possibly merged) ClusterctlVersion.
	cfg.ReapplyWorkloadGitDefaults()
	cfg.SyncCAPIControllerImagesToClusterctlVersion()
}

// applyConfigYAML fetches the bootstrap-config Secret, parses the
// `config.yaml` (or legacy `config.json`) body into a flat map, applies
// legacy-key migrations, then overlays via Config.ApplySnapshotKV.
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

// migrateLegacyKeys mirrors the bash `_legacy_to_worker` fold and the
// TEMPLATE_VMID → PROXMOX_TEMPLATE_ID carry-forward. Modifies kv in place.
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

// fillFromCredsJSON decodes a combined CAPI-ish Secret (url/token/secret
// + any embedded proxmox-admin.yaml) and fills matching cfg fields that
// are currently empty. The `capiAliases` switch controls which alias set
// applies; we reuse the same list for both the legacy single-Secret and
// the default-split CAPMOX Secret paths. Returns true when any field was
// actually set.
func fillFromCredsJSON(cfg *config.Config, secJSON string, _ bool) bool {
	data := decodeAllSecretData(secJSON)
	if len(data) == 0 {
		return false
	}
	// Merge embedded proxmox-admin.yaml lines into the top-level key map
	// (match bash: ytxt.splitlines → cfg["k"] = v).
	ak := stringOrDefault(decodeSecretDataKey(secJSON, "proxmox-admin.yaml"), data["proxmox-admin.yaml"])
	if ak != "" {
		for k, v := range parseFlatYAMLOrJSON(ak) {
			if v != "" {
				data[k] = v
			}
		}
	}
	aliases := map[string]string{
		"url":               "PROXMOX_URL",
		"token":             "PROXMOX_TOKEN",
		"secret":            "PROXMOX_SECRET",
		"capi_token_id":     "PROXMOX_TOKEN",
		"capi_token_secret": "PROXMOX_SECRET",
		"csi_token_id":      "PROXMOX_CSI_TOKEN_ID",
		"csi_token_secret":  "PROXMOX_CSI_TOKEN_SECRET",
		"admin_username":    "PROXMOX_ADMIN_USERNAME",
		"admin_token":       "PROXMOX_ADMIN_TOKEN",
	}
	for old, newKey := range aliases {
		if v := data[old]; v != "" {
			if _, ok := data[newKey]; !ok {
				data[newKey] = v
			}
		}
	}
	return fillEmptyFromMap(cfg, data)
}

// fillFromCSIJSON handles the default-split CSI Secret.
func fillFromCSIJSON(cfg *config.Config, secJSON string) bool {
	data := decodeAllSecretData(secJSON)
	if len(data) == 0 {
		return false
	}
	aliases := map[string]string{
		"url":              "PROXMOX_URL",
		"csi_token_id":     "PROXMOX_CSI_TOKEN_ID",
		"csi_token_secret": "PROXMOX_CSI_TOKEN_SECRET",
	}
	for old, newKey := range aliases {
		if v := data[old]; v != "" {
			if _, ok := data[newKey]; !ok {
				data[newKey] = v
			}
		}
	}
	return fillEmptyFromMap(cfg, data)
}

// fillFromAdminJSON handles the distinct admin Secret: restricts to the
// four admin keys.
func fillFromAdminJSON(cfg *config.Config, secJSON string) bool {
	data := decodeAllSecretData(secJSON)
	if len(data) == 0 {
		return false
	}
	// Merge embedded proxmox-admin.yaml (the key from cfg; default).
	ak := decodeSecretDataKey(secJSON, "proxmox-admin.yaml")
	if ak != "" {
		for k, v := range parseFlatYAMLOrJSON(ak) {
			if v != "" {
				data[k] = v
			}
		}
	}
	aliases := map[string]string{
		"url":            "PROXMOX_URL",
		"admin_username": "PROXMOX_ADMIN_USERNAME",
		"admin_token":    "PROXMOX_ADMIN_TOKEN",
		"insecure":       "PROXMOX_ADMIN_INSECURE",
	}
	for old, newKey := range aliases {
		if v := data[old]; v != "" {
			if _, ok := data[newKey]; !ok {
				data[newKey] = v
			}
		}
	}
	adminKeys := map[string]struct{}{
		"PROXMOX_URL":            {},
		"PROXMOX_ADMIN_USERNAME": {},
		"PROXMOX_ADMIN_TOKEN":    {},
		"PROXMOX_ADMIN_INSECURE": {},
	}
	out := map[string]string{}
	for k, v := range data {
		if _, ok := adminKeys[k]; ok {
			out[k] = v
		}
	}
	return fillEmptyFromMap(cfg, out)
}

// fillFromCapmoxManagerJSON handles capmox-system/capmox-manager-credentials
// (url/token/secret only).
func fillFromCapmoxManagerJSON(cfg *config.Config, secJSON string) bool {
	data := decodeAllSecretData(secJSON)
	out := map[string]string{}
	for k, v := range map[string]string{
		"PROXMOX_URL":    data["url"],
		"PROXMOX_TOKEN":  data["token"],
		"PROXMOX_SECRET": data["secret"],
	} {
		if v != "" {
			out[k] = v
		}
	}
	return fillEmptyFromMap(cfg, out)
}

// fillEmptyFromMap assigns non-empty kv values to cfg fields *only* when
// the current cfg value is empty. Unknown keys and keys matching a
// currently-non-empty field are ignored. Returns true when at least one
// assignment happened.
//
// The schema here is narrower than Config.Snapshot(): we also accept
// credentials fields (PROXMOX_TOKEN, PROXMOX_SECRET, PROXMOX_CSI_TOKEN_*,
// PROXMOX_ADMIN_*) which are never in the config.yaml snapshot.
func fillEmptyFromMap(cfg *config.Config, kv map[string]string) bool {
	assigned := false
	assign := func(cur *string, v string) {
		if *cur == "" && v != "" {
			*cur = v
			assigned = true
		}
	}
	assignBool := func(cur *bool, v string, explicit bool) {
		if explicit {
			return
		}
		// Only overwrite when bool-like; bash's `os.environ.get(k, "") ==
		// ""` test is not a 1:1 match for bools, but since these flags
		// default to true in config.Load(), we treat "false" as meaningful
		// only when it actually appears.
		if v == "" {
			return
		}
		switch strings.ToLower(v) {
		case "true", "1", "yes", "on":
			*cur = true
			assigned = true
		case "false", "0", "no", "off":
			*cur = false
			assigned = true
		}
	}
	_ = assignBool // retained for future bool credentials keys

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

// TryLoadBootstrapConfigFromKind ports try_load_bootstrap_config_from_kind.
// Used very early (before CLI is parsed) to populate cfg from an existing
// kind Secret. Returns silently when not available.
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
// be loaded. The serialisation matches what `kubectl get secret -o json`
// produced previously: corev1.Secret.Data is a map[string][]byte, and
// json.Marshal encodes []byte values as base64 strings — so existing
// callers that base64-decode the .data[key] still see the same bytes.
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
// is forced to corev1.SecretTypeOpaque, matching the bash `--from-file` /
// `--from-literal` defaults. Labels, when non-nil, are written through;
// pass nil to leave any pre-existing labels alone (SSA only sets fields
// the manifest specifies).
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

// parseFlatYAMLOrJSON accepts either a JSON object literal (legacy
// config.json) or a flat YAML key:value body (config.yaml). Strips
// `#` comments, honours a single pair of surrounding single- or
// double-quotes on values. Matches bash `parse_bootstrap_map`.
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
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		out[m[1]] = val
	}
	return out
}

func stringOrDefault(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
