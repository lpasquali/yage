package kindsync

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/kubectlx"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/proxmox"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// SyncBootstrapConfigToKind ports sync_bootstrap_config_to_kind.
// Requires kubectl + kind on PATH and the resolved kind context to match
// an existing kind cluster. Delegates the real work to the (still
// unported) _get_all_bootstrap_variables_as_yaml /
// apply_bootstrap_config_to_management_cluster pair — emits a warn for
// now so callers see the gap.
func SyncBootstrapConfigToKind(cfg *config.Config) error {
	if !shell.CommandExists("kubectl") || !shell.CommandExists("kind") {
		return nil
	}
	ctx, ok := kubectlx.ResolveBootstrapContext(cfg)
	if !ok {
		return nil
	}
	name := strings.TrimPrefix(ctx, "kind-")
	if !kindClusterExists(name) {
		return nil
	}
	return applyBootstrapConfigToManagementCluster(cfg, ctx)
}

// SyncProxmoxBootstrapLiteralCredentialsToKind ports
// sync_proxmox_bootstrap_literal_credentials_to_kind.
//
// Two shapes:
//   - Legacy single Secret (PROXMOX_BOOTSTRAP_SECRET_NAME set): CAPI+CSI
//     keys live in one Secret; admin YAML lives alongside (same Secret if
//     admin-Secret-name matches, else a split Secret).
//   - Default split (PROXMOX_BOOTSTRAP_SECRET_NAME empty):
//     ${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME} gets the CAPI/clusterctl
//     keys; ${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} gets the CSI config
//     surface; ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME} gets the admin YAML.
//
// Also calls update_capmox_manager_secret_on_kind so capmox-system's live
// copy is restored on the next sync.
func SyncProxmoxBootstrapLiteralCredentialsToKind(cfg *config.Config) error {
	if !shell.CommandExists("kubectl") || !shell.CommandExists("kind") {
		return nil
	}
	ctx, ok := kubectlx.ResolveBootstrapContext(cfg)
	if !ok {
		return nil
	}
	name := strings.TrimPrefix(ctx, "kind-")
	if !kindClusterExists(name) {
		return nil
	}
	ns := cfg.ProxmoxBootstrapSecretNamespace
	if err := ensureNamespace(ctx, ns); err != nil {
		return err
	}

	env := proxmoxEnvMap(cfg)
	lookup := func(k string) string { return env[k] }

	// --- Legacy single-Secret branch ---
	if cfg.ProxmoxBootstrapSecretName != "" {
		legacyKeys := []string{
			"PROXMOX_URL", "PROXMOX_TOKEN", "PROXMOX_SECRET",
			"PROXMOX_CSI_URL", "PROXMOX_CSI_TOKEN_ID", "PROXMOX_CSI_TOKEN_SECRET",
			"PROXMOX_REGION", "PROXMOX_NODE",
		}
		_ = (kindSecret{
			Context:     ctx,
			Namespace:   ns,
			Name:        cfg.ProxmoxBootstrapSecretName,
			AllowedKeys: legacyKeys,
			LookupValue: lookup,
		}).apply()
		logx.Log("Updated %s/%s (legacy CAPI/CSI from current environment).", ns, cfg.ProxmoxBootstrapSecretName)

		adminTarget := cfg.ProxmoxBootstrapSecretName
		if cfg.ProxmoxBootstrapAdminSecretName != "" &&
			cfg.ProxmoxBootstrapAdminSecretName != cfg.ProxmoxBootstrapSecretName {
			adminTarget = cfg.ProxmoxBootstrapAdminSecretName
		}
		_ = applyAdminYAMLToKind(cfg, ctx, adminTarget)
		_ = UpdateCapmoxManagerSecretOnKind(cfg)
		return nil
	}

	// --- Default split: CAPMOX (clusterctl) Secret ---
	capmoxKeys := []string{
		"PROXMOX_URL", "PROXMOX_TOKEN", "PROXMOX_SECRET",
		"PROXMOX_REGION", "PROXMOX_NODE",
	}
	_ = (kindSecret{
		Context:     ctx,
		Namespace:   ns,
		Name:        cfg.ProxmoxBootstrapCAPMOXSecretName,
		AllowedKeys: capmoxKeys,
		LookupValue: lookup,
	}).apply()
	logx.Log("Updated %s/%s (CAPI / clusterctl keys from current environment).", ns, cfg.ProxmoxBootstrapCAPMOXSecretName)

	// Derive PROXMOX_CSI_URL from PROXMOX_URL when not set — bash L979-L982.
	if cfg.ProxmoxURL != "" && cfg.ProxmoxCSIURL == "" {
		cfg.ProxmoxCSIURL = proxmox.APIJSONURL(cfg)
		env["PROXMOX_CSI_URL"] = cfg.ProxmoxCSIURL
	}

	// --- Default split: CSI Secret ---
	csiKeys := []string{
		"PROXMOX_URL", "PROXMOX_REGION", "PROXMOX_NODE",
		"PROXMOX_CSI_URL", "PROXMOX_CSI_TOKEN_ID", "PROXMOX_CSI_TOKEN_SECRET",
		"PROXMOX_CSI_USER_ID", "PROXMOX_CSI_TOKEN_PREFIX", "PROXMOX_CSI_INSECURE",
		"PROXMOX_CSI_STORAGE_CLASS_NAME", "PROXMOX_CSI_STORAGE",
		"PROXMOX_CSI_RECLAIM_POLICY", "PROXMOX_CSI_FSTYPE", "PROXMOX_CSI_DEFAULT_CLASS",
		"PROXMOX_CSI_TOPOLOGY_LABELS", "PROXMOX_TOPOLOGY_REGION", "PROXMOX_TOPOLOGY_ZONE",
		"PROXMOX_CSI_CHART_REPO_URL", "PROXMOX_CSI_CHART_NAME", "PROXMOX_CSI_CHART_VERSION",
		"PROXMOX_CSI_NAMESPACE", "PROXMOX_CSI_CONFIG_PROVIDER",
		"PROXMOX_CSI_SMOKE_ENABLED",
		"ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL", "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH",
		"ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF", "ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE",
	}
	_ = (kindSecret{
		Context:     ctx,
		Namespace:   ns,
		Name:        cfg.ProxmoxBootstrapCSISecretName,
		AllowedKeys: csiKeys,
		LookupValue: lookup,
	}).apply()
	logx.Log("Updated %s/%s (CSI: API URL, token id/secret, user id, chart/storage toggles, … from current environment).",
		ns, cfg.ProxmoxBootstrapCSISecretName)

	if cfg.ProxmoxCSITokenID == "" || cfg.ProxmoxCSITokenSecret == "" {
		logx.Warn("PROXMOX_CSI_TOKEN_ID/PROXMOX_CSI_TOKEN_SECRET are unset; %s will not contain CSI tokens until Terraform identity outputs or the environment set them (URL/region are still written when known).",
			cfg.ProxmoxBootstrapCSISecretName)
	}

	_ = applyAdminYAMLToKind(cfg, ctx, cfg.ProxmoxBootstrapAdminSecretName)
	_ = UpdateCapmoxManagerSecretOnKind(cfg)
	return nil
}

// applyAdminYAMLToKind ports _apply_proxmox_bootstrap_admin_yaml_to_kind.
// Merges existing Secret data with a generated proxmox-admin.yaml blob
// (PROXMOX_URL + PROXMOX_ADMIN_* lines). No-op when all admin env vars
// are empty and there is nothing to write.
func applyAdminYAMLToKind(cfg *config.Config, ctx, targetSecret string) error {
	if targetSecret == "" {
		return nil
	}
	adminKeys := []string{
		"PROXMOX_URL", "PROXMOX_ADMIN_USERNAME", "PROXMOX_ADMIN_TOKEN", "PROXMOX_ADMIN_INSECURE",
	}
	env := proxmoxEnvMap(cfg)
	hasAny := false
	for _, k := range adminKeys {
		if env[k] != "" {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return nil
	}

	// Build the flat YAML body: lines of `KEY: "value"` using JSON quoting
	// for values, matching the bash pyjson.dumps().
	var sb strings.Builder
	for _, k := range adminKeys {
		v := env[k]
		if v == "" {
			continue
		}
		q, _ := json.Marshal(v)
		sb.WriteString(k)
		sb.WriteString(": ")
		sb.Write(q)
		sb.WriteByte('\n')
	}
	text := sb.String()
	if strings.TrimSpace(text) == "" {
		return nil
	}

	// Fetch existing Secret, preserve *all* existing data entries (no
	// key whitelist for admin), overlay the admin YAML under the
	// configured key.
	raw, _, _ := shell.Capture(
		"kubectl", "--context", ctx, "get", "secret", targetSecret,
		"-n", cfg.ProxmoxBootstrapSecretNamespace, "-o", "json",
	)
	var cur struct {
		Metadata struct {
			Labels map[string]string `json:"labels,omitempty"`
		} `json:"metadata,omitempty"`
		Data map[string]string `json:"data,omitempty"`
	}
	_ = json.Unmarshal([]byte(raw), &cur)

	data := map[string]string{}
	for k, v := range cur.Data {
		if v != "" {
			data[k] = v
		}
	}
	ak := cfg.ProxmoxBootstrapAdminSecretKey
	if ak == "" {
		ak = "proxmox-admin.yaml"
	}
	data[ak] = base64.StdEncoding.EncodeToString([]byte(text))

	meta := map[string]any{
		"name":      targetSecret,
		"namespace": cfg.ProxmoxBootstrapSecretNamespace,
	}
	if len(cur.Metadata.Labels) > 0 {
		meta["labels"] = cur.Metadata.Labels
	}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   meta,
		"type":       "Opaque",
		"data":       data,
	}
	doc, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	if err := shell.Pipe(string(doc), "kubectl", "--context", ctx, "apply", "-f", "-"); err != nil {
		logx.Warn("Failed to update %s/%s (proxmox-admin.yaml).", cfg.ProxmoxBootstrapSecretNamespace, targetSecret)
		return err
	}
	logx.Log("Updated %s/%s (merged %s from current environment).",
		cfg.ProxmoxBootstrapSecretNamespace, targetSecret, ak)
	return nil
}

// UpdateCapmoxManagerSecretOnKind ports update_capmox_manager_secret_on_kind.
// Writes url/token/secret into capmox-system/capmox-manager-credentials —
// required so a deleted in-cluster capmox credential is restored on the
// next sync without waiting for the full Argo / CAAPH loop.
func UpdateCapmoxManagerSecretOnKind(cfg *config.Config) error {
	if cfg.ProxmoxURL == "" || cfg.ProxmoxToken == "" || cfg.ProxmoxSecret == "" {
		return nil
	}
	ctx := "kind-" + cfg.KindClusterName
	if err := shell.Run("kubectl", "--context", ctx, "get", "namespace", "capmox-system"); err != nil {
		logx.Warn("namespace capmox-system not on %s — skip capmox-manager-credentials update.", ctx)
		return nil
	}
	doc := fmt.Sprintf(`{
      "apiVersion":"v1","kind":"Secret","type":"Opaque",
      "metadata":{"name":"capmox-manager-credentials","namespace":"capmox-system"},
      "data":{"url":%q,"token":%q,"secret":%q}
    }`,
		base64.StdEncoding.EncodeToString([]byte(cfg.ProxmoxURL)),
		base64.StdEncoding.EncodeToString([]byte(cfg.ProxmoxToken)),
		base64.StdEncoding.EncodeToString([]byte(cfg.ProxmoxSecret)),
	)
	if err := shell.Pipe(doc, "kubectl", "--context", ctx, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to update capmox-system/capmox-manager-credentials on %s.", ctx)
	}
	logx.Log("Updated capmox-system/capmox-manager-credentials on %s.", ctx)
	return nil
}

// RolloutRestartCapmoxController ports rollout_restart_capmox_controller.
// Best-effort: if the deployment is not ready, warn and continue — bash
// uses `|| warn` so never fails the script.
func RolloutRestartCapmoxController(cfg *config.Config) {
	ctx := "kind-" + cfg.KindClusterName
	if err := shell.Run("kubectl", "--context", ctx, "-n", "capmox-system",
		"rollout", "restart", "deployment/capmox-controller-manager"); err != nil {
		logx.Warn("capmox-controller-manager restart skipped or not ready (check capmox-system).")
		return
	}
	if err := shell.Run("kubectl", "--context", ctx, "-n", "capmox-system",
		"rollout", "status", "deployment/capmox-controller-manager", "--timeout=180s"); err != nil {
		logx.Warn("capmox-controller-manager restart skipped or not ready (check capmox-system).")
	}
}

// RolloutRestartProxmoxCSIOnWorkload ports rollout_restart_proxmox_csi_on_workload.
// Fetches the workload cluster's kubeconfig from the capi Secret on kind,
// writes it to a temp file, and restarts proxmox-csi-plugin-controller in
// the CSI namespace on the workload. No-op when the kubeconfig secret or
// the target deployment are missing.
func RolloutRestartProxmoxCSIOnWorkload(cfg *config.Config) {
	ctx := "kind-" + cfg.KindClusterName
	kcfg, err := writeWorkloadKubeconfig(cfg, ctx)
	if err != nil {
		logx.Warn("No workload kubeconfig — skip Proxmox CSI controller restart on workload.")
		return
	}
	defer removeFile(kcfg)

	ns := cfg.ProxmoxCSINamespace
	if err := shell.Run("kubectl", "--kubeconfig", kcfg, "-n", ns,
		"get", "deploy", "proxmox-csi-plugin-controller"); err != nil {
		logx.Warn("proxmox-csi controller deployment not found in %s — skip restart.", ns)
		return
	}
	_ = shell.Run("kubectl", "--kubeconfig", kcfg, "-n", ns,
		"rollout", "restart", "deploy/proxmox-csi-plugin-controller")
	_ = shell.Run("kubectl", "--kubeconfig", kcfg, "-n", ns,
		"rollout", "status", "deploy/proxmox-csi-plugin-controller", "--timeout=300s")
	logx.Log("Restarted Proxmox CSI controller on workload %s.", cfg.WorkloadClusterName)
}
