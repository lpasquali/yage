// Package opentofux ports the OpenTofu-backed Proxmox identity
// bootstrap flow.
//
// Bash source map (bootstrap-capi.sh):
//   - install_bpg_proxmox_provider                          ~L2860-2879
//   - write_embedded_terraform_files                        ~L2881-3038
//   - apply_proxmox_identity_terraform                      ~L3040-3067
//   - resolve_recreate_proxmox_identity_context             ~L3098-3127
//   - proxmox_identity_terraform_state_rm_all               ~L3141-3151
//   - recreate_proxmox_identities_terraform                 ~L3201-3281
//   - recreate_identities_resync_and_rollout_capmox         ~L3284-3290
//   - recreate_identities_workload_csi_secrets              ~L3293-3309
//   - extract_identity_tf_inputs_from_state                 ~L7443-7494
//   - destroy_proxmox_identity_terraform_state              ~L7497-7552
//
// State format is unchanged from Terraform — the on-disk state file is
// named terraform.tfstate (the OpenTofu default) and is read/written by
// either binary.
package opentofux

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/kindsync"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/proxmox"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
	"github.com/lpasquali/bootstrap-capi/internal/sysinfo"
)

// StateDir returns ~/.bootstrap-capi/proxmox-identity-terraform.
func StateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".bootstrap-capi", "proxmox-identity-terraform")
}

// stateFile returns ${StateDir}/terraform.tfstate (OpenTofu's default).
func stateFile() string { return filepath.Join(StateDir(), "terraform.tfstate") }

// WriteEmbeddedFiles ports write_embedded_terraform_files. Creates
// StateDir and writes the identity HCL to the configured filename
// (cfg.ProxmoxIdentityTF, default: proxmox-identity.tf).
func WriteEmbeddedFiles(cfg *config.Config) error {
	dir := StateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := cfg.ProxmoxIdentityTF
	if name == "" {
		name = "proxmox-identity.tf"
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(IdentityHCL), 0o644)
}

// InstallBPGProvider ports install_bpg_proxmox_provider. Writes a
// throwaway main.tf into a scratch dir and runs `tofu init -upgrade` to
// warm ~/.terraform.d/plugin-cache. OpenTofu honours that cache dir.
func InstallBPGProvider(cfg *config.Config) error {
	home, _ := os.UserHomeDir()
	cache := filepath.Join(home, ".terraform.d", "plugin-cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "bpg-provider-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := os.WriteFile(filepath.Join(tmp, "main.tf"), []byte(BPGProviderHCL), 0o644); err != nil {
		return err
	}
	logx.Log("Installing OpenTofu provider bpg/proxmox...")
	c := exec.Command("tofu", "-chdir="+tmp, "init", "-backend=false", "-upgrade")
	c.Env = append(os.Environ(), "TF_PLUGIN_CACHE_DIR="+cache)
	// Discard chatty stdout like bash `>/dev/null`.
	c.Stdout = nil
	c.Stderr = os.Stderr
	return c.Run()
}

// tofuEnv returns the env var trio bpg/proxmox needs to authenticate
// during `tofu init/apply/destroy` with admin credentials from cfg.
func tofuEnv(cfg *config.Config) []string {
	return append(
		os.Environ(),
		"PROXMOX_VE_ENDPOINT="+cfg.ProxmoxURL,
		"PROXMOX_VE_API_TOKEN="+cfg.ProxmoxAdminUsername+"="+cfg.ProxmoxAdminToken,
		"PROXMOX_VE_INSECURE="+cfg.ProxmoxAdminInsecure,
	)
}

// runTofu runs `tofu -chdir=<dir> args...` inheriting stderr, streaming
// stdout, with tofuEnv set.
func runTofu(cfg *config.Config, args ...string) error {
	argv := append([]string{"tofu", "-chdir=" + StateDir()}, args...)
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = tofuEnv(cfg)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// applyVars returns the standard -var flag set.
func applyVars(cfg *config.Config) []string {
	return []string{
		"-var", "cluster_set_id=" + cfg.ProxmoxIdentitySuffix,
		"-var", "csi_user_id=" + cfg.ProxmoxCSIUserID,
		"-var", "csi_token_prefix=" + cfg.ProxmoxCSITokenPrefix,
		"-var", "capi_user_id=" + cfg.ProxmoxCAPIUserID,
		"-var", "capi_token_prefix=" + cfg.ProxmoxCAPITokenPrefix,
	}
}

// ApplyIdentity ports apply_proxmox_identity_terraform (L3040-L3067).
func ApplyIdentity(cfg *config.Config) error {
	if err := WriteEmbeddedFiles(cfg); err != nil {
		return err
	}
	logx.Log("Applying OpenTofu identity bootstrap for CAPI/CSI users...")
	if err := runTofu(cfg, "init", "-upgrade"); err != nil {
		return err
	}
	args := append([]string{"apply", "-auto-approve"}, applyVars(cfg)...)
	return runTofu(cfg, args...)
}

// StateRmAll ports proxmox_identity_terraform_state_rm_all (L3141-L3151).
// Walks `tofu state list` and runs `tofu state rm` on each entry, with
// warn-don't-fail semantics on individual failures.
func StateRmAll(cfg *config.Config) error {
	if _, err := os.Stat(stateFile()); err != nil {
		logx.Warn("No OpenTofu state to clear at %s.", StateDir())
		return nil
	}
	logx.Log("Removing all resources from Proxmox identity OpenTofu state (PVE may be empty; next apply is create-only)...")
	list, _, err := shell.Capture("tofu", "-chdir="+StateDir(), "state", "list")
	if err != nil {
		return nil
	}
	for _, addr := range strings.Split(list, "\n") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		c := exec.Command("tofu", "-chdir="+StateDir(), "state", "rm", addr)
		c.Stdin = nil
		if err := c.Run(); err != nil {
			logx.Warn("state rm failed for %s", addr)
		}
	}
	return nil
}

// GetOutput returns `tofu output -raw <name>` as a trimmed string.
func GetOutput(name string) string {
	out, _, _ := shell.Capture("tofu", "-chdir="+StateDir(), "output", "-raw", name)
	return strings.TrimSpace(out)
}

// ExtractIdentityInputsFromState ports extract_identity_tf_inputs_from_state
// (L7443-L7494). Returns (clusterSetID, csiUserID, csiTokenPrefix,
// capiUserID, capiTokenPrefix) by parsing the tfstate JSON.
func ExtractIdentityInputsFromState(statePath string) (string, string, string, string, string, error) {
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return "", "", "", "", "", err
	}
	type instance struct {
		IndexKey   any            `json:"index_key"`
		Attributes map[string]any `json:"attributes"`
	}
	type resource struct {
		Type      string     `json:"type"`
		Instances []instance `json:"instances"`
	}
	var s struct {
		Resources []resource `json:"resources"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", "", "", "", "", err
	}
	vals := map[string]string{
		"cluster_set_id":    "",
		"csi_user_id":       "",
		"csi_token_prefix":  "",
		"capi_user_id":      "",
		"capi_token_prefix": "",
	}
	tokenSuffixRE := regexp.MustCompile(`^(.+)-([^-]+)$`)
	roleSuffixRE := regexp.MustCompile(`^Kubernetes-(?:CSI|CAPI)-(.+)$`)
	for _, r := range s.Resources {
		for _, inst := range r.Instances {
			idx := fmt.Sprint(inst.IndexKey)
			attrs := inst.Attributes
			asStr := func(k string) string {
				if v, ok := attrs[k].(string); ok {
					return v
				}
				return ""
			}
			if r.Type == "proxmox_virtual_environment_user" && (idx == "csi" || idx == "capi") {
				vals[idx+"_user_id"] = asStr("user_id")
			}
			if r.Type == "proxmox_virtual_environment_user_token" && (idx == "csi" || idx == "capi") {
				tn := asStr("token_name")
				if m := tokenSuffixRE.FindStringSubmatch(tn); m != nil {
					vals[idx+"_token_prefix"] = m[1]
					if vals["cluster_set_id"] == "" {
						vals["cluster_set_id"] = m[2]
					}
				}
			}
			if r.Type == "proxmox_virtual_environment_role" && vals["cluster_set_id"] == "" {
				if m := roleSuffixRE.FindStringSubmatch(asStr("role_id")); m != nil {
					vals["cluster_set_id"] = m[1]
				}
			}
		}
	}
	return vals["cluster_set_id"],
		vals["csi_user_id"],
		vals["csi_token_prefix"],
		vals["capi_user_id"],
		vals["capi_token_prefix"],
		nil
}

// ResolveRecreateContext ports resolve_recreate_proxmox_identity_context
// (L3098-L3127). Prefers the extracted state-file inputs, falls back to
// token-id inference if no state exists.
func ResolveRecreateContext(cfg *config.Config) {
	if _, err := os.Stat(stateFile()); err == nil {
		csID, csiUser, csiPfx, capiUser, capiPfx, err := ExtractIdentityInputsFromState(stateFile())
		if err != nil || csID == "" || csiUser == "" || csiPfx == "" || capiUser == "" || capiPfx == "" {
			logx.Die("Could not read identity inputs from %s (state incomplete).", stateFile())
		}
		if cfg.ClusterSetID == "" {
			cfg.ClusterSetID = csID
		}
		cfg.ProxmoxCSIUserID = csiUser
		cfg.ProxmoxCSITokenPrefix = csiPfx
		cfg.ProxmoxCAPIUserID = capiUser
		cfg.ProxmoxCAPITokenPrefix = capiPfx
		if cfg.ProxmoxIdentitySuffix == "" {
			cfg.ProxmoxIdentitySuffix = proxmox.DeriveIdentitySuffix(cfg.ClusterSetID)
		}
		logx.Log("Re-creation: identity from OpenTofu state (%s): cluster_set_id var=%s.", StateDir(), csID)
		return
	}
	logx.Warn("No OpenTofu state at %s — inferring from PROXMOX_CSI_TOKEN_ID and PROXMOX_TOKEN (CAPI) in env/kind.", stateFile())
	if !proxmox.InferIdentityFromTokenIDs(cfg) {
		logx.Die("Cannot resolve identity: restore %s or set PROXMOX_CSI_TOKEN_ID + PROXMOX_TOKEN to existing token *names* (user@pve!prefix-suffix) from Kubernetes Secrets.", stateFile())
	}
	if cfg.ProxmoxIdentitySuffix == "" {
		logx.Die("Recreate: PROXMOX_IDENTITY_SUFFIX is empty after inference.")
	}
	logx.Log("Re-creation: inferred Proxmox identity suffix %s from token id format.", cfg.ProxmoxIdentitySuffix)
}

// RecreateIdentities ports recreate_proxmox_identities_terraform
// (L3201-L3281). Full rotation flow with two branches:
//
//   - PROXMOX_IDENTITY_RECREATE_STATE_RM=true: empty state then apply from
//     scratch (PVE was wiped).
//   - Default: `tofu apply -replace=...` scoped by
//     PROXMOX_IDENTITY_RECREATE_SCOPE (capi / csi / both).
func RecreateIdentities(cfg *config.Config) error {
	if !shell.CommandExists("tofu") {
		logx.Die("OpenTofu (tofu) is required for --recreate-proxmox-identities.")
	}
	// ensure_proxmox_admin_config lives in the orchestrator, not here —
	// callers should have run it before.
	if cfg.ProxmoxURL == "" || cfg.ProxmoxAdminUsername == "" || cfg.ProxmoxAdminToken == "" {
		logx.Die("Recreate: need PROXMOX_URL, PROXMOX_ADMIN_USERNAME, PROXMOX_ADMIN_TOKEN (set env, kind Secret %s/%s, or PROXMOX_ADMIN_CONFIG to a legacy file).",
			cfg.ProxmoxBootstrapSecretNamespace, cfg.ProxmoxBootstrapAdminSecretName)
	}
	ResolveRecreateContext(cfg)
	proxmox.ValidateClusterSetIDFormat(cfg)
	if cfg.ProxmoxIdentitySuffix == "" {
		cfg.ProxmoxIdentitySuffix = proxmox.DeriveIdentitySuffix(cfg.ClusterSetID)
	}
	proxmox.RefreshDerivedIdentityUserIDs(cfg)
	proxmox.CheckAdminAPIConnectivity(cfg)
	if err := WriteEmbeddedFiles(cfg); err != nil {
		return err
	}
	if err := runTofu(cfg, "init", "-upgrade"); err != nil {
		return err
	}
	if sysinfo.IsTrue(boolStr(cfg.ProxmoxIdentityRecreateStateRm)) {
		if err := StateRmAll(cfg); err != nil {
			return err
		}
		args := append([]string{"apply", "-auto-approve"}, applyVars(cfg)...)
		if err := runTofu(cfg, args...); err != nil {
			return err
		}
	} else {
		var targets []string
		scope := cfg.ProxmoxIdentityRecreateScope
		if scope == "" {
			scope = "both"
		}
		switch scope {
		case "both":
			targets = append(targets,
				`-replace=proxmox_virtual_environment_role.identity["capi"]`,
				`-replace=proxmox_virtual_environment_role.identity["csi"]`,
				`-replace=proxmox_virtual_environment_user.identity["capi"]`,
				`-replace=proxmox_virtual_environment_user.identity["csi"]`,
				`-replace=proxmox_virtual_environment_user_token.identity["capi"]`,
				`-replace=proxmox_virtual_environment_user_token.identity["csi"]`,
				`-replace=proxmox_virtual_environment_acl.identity["capi"]`,
				`-replace=proxmox_virtual_environment_acl.identity["csi"]`,
			)
		case "csi":
			targets = append(targets,
				`-replace=proxmox_virtual_environment_role.identity["csi"]`,
				`-replace=proxmox_virtual_environment_user.identity["csi"]`,
				`-replace=proxmox_virtual_environment_user_token.identity["csi"]`,
				`-replace=proxmox_virtual_environment_acl.identity["csi"]`,
			)
		case "capi":
			targets = append(targets,
				`-replace=proxmox_virtual_environment_role.identity["capi"]`,
				`-replace=proxmox_virtual_environment_user.identity["capi"]`,
				`-replace=proxmox_virtual_environment_user_token.identity["capi"]`,
				`-replace=proxmox_virtual_environment_acl.identity["capi"]`,
			)
		default:
			logx.Die("Invalid --recreate-proxmox-identities-scope: %s (use capi, csi, or both).", scope)
		}
		args := append([]string{"apply", "-auto-approve"}, applyVars(cfg)...)
		args = append(args, targets...)
		if err := runTofu(cfg, args...); err != nil {
			return err
		}
	}
	GenerateConfigsFromOutputs(cfg)
	logx.Log("Proxmox identity OpenTofu re-apply complete (outputs merged into the environment, kind, and local stubs where enabled).")
	return nil
}

// RecreateResyncCapmox ports recreate_identities_resync_and_rollout_capmox
// (L3284-L3290). After capmox-system and its webhook exist, re-push
// in-cluster creds and restart the CAPMOX controller.
func RecreateResyncCapmox(cfg *config.Config) {
	if !cfg.RecreateProxmoxIdentities {
		return
	}
	logx.Log("Re-syncing in-cluster CAPI/capmox credentials after Proxmox provider is installed (recreate mode)...")
	_ = kindsync.SyncBootstrapConfigToKind(cfg)
	_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)
	kindsync.RolloutRestartCapmoxController(cfg)
}

// DestroyIdentity ports destroy_proxmox_identity_terraform_state
// (L7497-L7552). No-op when the state file is missing. Dies when any of
// the five state-extracted inputs is empty.
func DestroyIdentity(cfg *config.Config) error {
	sf := stateFile()
	if _, err := os.Stat(sf); err != nil {
		logx.Log("No OpenTofu state file found at %s; skipping OpenTofu destroy.", sf)
		return nil
	}
	if !shell.CommandExists("tofu") {
		logx.Die("OpenTofu (tofu) is required to destroy existing Proxmox identity resources during purge.")
	}
	if cfg.ProxmoxURL == "" {
		logx.Die("Cannot purge OpenTofu identities: PROXMOX_URL is required.")
	}
	if cfg.ProxmoxAdminUsername == "" {
		logx.Die("Cannot purge OpenTofu identities: PROXMOX_ADMIN_USERNAME is required.")
	}
	if cfg.ProxmoxAdminToken == "" {
		logx.Die("Cannot purge OpenTofu identities: PROXMOX_ADMIN_TOKEN is required.")
	}

	csID, csiUser, csiPfx, capiUser, capiPfx, err := ExtractIdentityInputsFromState(sf)
	if err != nil || csID == "" {
		logx.Die("Cannot determine cluster_set_id from OpenTofu state %s.", sf)
	}
	if csiUser == "" {
		logx.Die("Cannot determine csi_user_id from OpenTofu state %s.", sf)
	}
	if csiPfx == "" {
		logx.Die("Cannot determine csi_token_prefix from OpenTofu state %s.", sf)
	}
	if capiUser == "" {
		logx.Die("Cannot determine capi_user_id from OpenTofu state %s.", sf)
	}
	if capiPfx == "" {
		logx.Die("Cannot determine capi_token_prefix from OpenTofu state %s.", sf)
	}

	logx.Log("Destroying OpenTofu-managed Proxmox identity resources before purge...")
	// Quiet init, streaming destroy.
	initCmd := exec.Command("tofu", "-chdir="+StateDir(), "init", "-upgrade")
	initCmd.Env = tofuEnv(cfg)
	initCmd.Stderr = os.Stderr
	_ = initCmd.Run()

	args := []string{
		"destroy", "-auto-approve", "-input=false",
		"-var", "cluster_set_id=" + csID,
		"-var", "csi_user_id=" + csiUser,
		"-var", "csi_token_prefix=" + csiPfx,
		"-var", "capi_user_id=" + capiUser,
		"-var", "capi_token_prefix=" + capiPfx,
	}
	if err := runTofu(cfg, args...); err != nil {
		return err
	}
	logx.Log("OpenTofu-managed Proxmox identity resources destroyed.")
	return nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
