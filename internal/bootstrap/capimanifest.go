package bootstrap

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// EnsureCAPIManifestPath ports bootstrap_ensure_capi_manifest_path
// (L4450-L4464). Two modes:
//
//   - cfg.CAPIManifest already set: caller-managed file on disk; no
//     Secret round-trip, no ephemeral file.
//   - unset: create a temp file and flip BootstrapCAPIUseSecret=true so
//     that the real source of truth becomes a kind Secret.
func EnsureCAPIManifestPath(cfg *config.Config) {
	if cfg.CAPIManifest != "" {
		cfg.BootstrapCAPIUseSecret = false
		cfg.BootstrapCAPIManifestEphemeral = false
		cfg.BootstrapCAPIManifestUserSet = true
		return
	}
	RegisterExitTrap()
	cfg.BootstrapCAPIUseSecret = true
	f, err := os.CreateTemp("", "capi-wl-*.yaml")
	if err != nil {
		logx.Die("Cannot create ephemeral CAPI manifest: %v", err)
	}
	f.Close()
	cfg.CAPIManifest = f.Name()
	cfg.BootstrapCAPIManifestEphemeral = true
	cfg.BootstrapCAPIManifestUserSet = false
	SetEphemeralCAPIManifest(f.Name())
	logx.Log("Workload CAPI manifest is stored in the management cluster as a Secret (namespace %s, secret %s, data key %s) — this process only uses a temp file. Use --capi-manifest for a file on disk; inspect live YAML with k9s or kubectl.",
		cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, cfg.CAPIManifestSecretKey)
}

// RefreshDefaultCAPIManifestPath ports
// bootstrap_refresh_default_capi_manifest_path (L4467-L4475). Called after
// the interactive cluster picker chose a different cluster — clears the
// stale ephemeral file and logs which Secret the next load/gen will touch.
func RefreshDefaultCAPIManifestPath(cfg *config.Config) {
	if cfg.BootstrapCAPIManifestUserSet {
		return
	}
	if cfg.BootstrapCAPIUseSecret {
		if cfg.CAPIManifest != "" {
			if _, err := os.Stat(cfg.CAPIManifest); err == nil {
				_ = os.WriteFile(cfg.CAPIManifest, nil, 0o600)
			}
		}
		ns := cfg.WorkloadClusterNamespace
		if ns == "" {
			ns = "default"
		}
		logx.Log("Workload selection updated; will load or generate for %s %s/%s (Secret %s).",
			cfg.KindClusterName, ns, cfg.WorkloadClusterName, cfg.CAPIManifestSecretName)
		return
	}
	logx.Die("bootstrap-capi: internal error — CAPI manifest path refresh with neither user file nor Secret mode.")
}

// TryLoadCAPIManifestFromSecret ports capi_manifest_try_load_from_secret
// (L4325-L4347). No-op unless BootstrapCAPIUseSecret=true and the
// kind-context exists and the Secret exists.
func TryLoadCAPIManifestFromSecret(cfg *config.Config) {
	if !cfg.BootstrapCAPIUseSecret || cfg.CAPIManifest == "" {
		return
	}
	if !shell.CommandExists("kubectl") {
		return
	}
	ctx := "kind-" + cfg.KindClusterName
	if !contextExists(ctx) {
		return
	}
	// namespace + secret existence checks are cheap; bail early on either.
	if err := shell.Run("kubectl", "--context", ctx, "get", "ns", cfg.CAPIManifestSecretNamespace); err != nil {
		return
	}
	if err := shell.Run("kubectl", "--context", ctx, "get", "secret",
		"-n", cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName); err != nil {
		return
	}
	raw, _, _ := shell.Capture(
		"kubectl", "--context", ctx, "get", "secret",
		"-n", cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName,
		"-o", "json",
	)
	if raw == "" {
		return
	}
	var sec struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &sec); err != nil {
		return
	}
	body, err := base64.StdEncoding.DecodeString(sec.Data[cfg.CAPIManifestSecretKey])
	if err != nil || len(body) == 0 {
		return
	}
	if err := os.WriteFile(cfg.CAPIManifest, body, 0o600); err != nil {
		return
	}
	logx.Log("Loaded workload manifest from Secret %s/%s (key %s, context %s).",
		cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, cfg.CAPIManifestSecretKey, ctx)
}

// PushCAPIManifestToSecret ports capi_manifest_push_to_secret
// (L4412-L4437). No-op when BootstrapCAPIUseSecret is false or the local
// CAPI manifest is empty. Dies when the manifest exceeds the ~1 MiB
// Secret data limit.
func PushCAPIManifestToSecret(cfg *config.Config) {
	if !cfg.BootstrapCAPIUseSecret {
		return
	}
	if cfg.CAPIManifest == "" {
		return
	}
	fi, err := os.Stat(cfg.CAPIManifest)
	if err != nil || fi.Size() == 0 {
		return
	}
	if fi.Size() >= 1000000 {
		logx.Die("Workload manifest is %d bytes (Secret data limit is ~1 MiB). Set CAPI_MANIFEST or use --capi-manifest with a file path, or reduce the manifest.", fi.Size())
	}
	ctx := "kind-" + cfg.KindClusterName
	nsDoc := fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q}}`,
		cfg.CAPIManifestSecretNamespace)
	_ = shell.Pipe(nsDoc, "kubectl", "--context", ctx, "apply", "-f", "-")

	secretYAML, _, _ := shell.Capture(
		"kubectl", "--context", ctx,
		"-n", cfg.CAPIManifestSecretNamespace,
		"create", "secret", "generic", cfg.CAPIManifestSecretName,
		"--from-file="+cfg.CAPIManifestSecretKey+"="+cfg.CAPIManifest,
		"--dry-run=client", "-o", "yaml",
	)
	if secretYAML == "" {
		logx.Die("Failed to store workload manifest in Secret %s/%s (key %s).",
			cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, cfg.CAPIManifestSecretKey)
	}
	if err := shell.Pipe(secretYAML, "kubectl", "--context", ctx, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to store workload manifest in Secret %s/%s (key %s).",
			cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, cfg.CAPIManifestSecretKey)
	}
	_ = shell.Run("kubectl", "--context", ctx, "-n", cfg.CAPIManifestSecretNamespace,
		"label", "secret", cfg.CAPIManifestSecretName,
		"app.kubernetes.io/managed-by=bootstrap-capi", "--overwrite")
	logx.Log("Wrote workload manifest to Secret %s/%s (key %s). No persistent file under ~/.bootstrap-capi — debug via k9s or kubectl get secret -n %s %s -o yaml.",
		cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, cfg.CAPIManifestSecretKey,
		cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName)
	TouchWorkloadGencodeStamp(cfg)
}

// DeleteCAPIManifestSecret ports capi_manifest_delete_secret (L4439-L4448).
func DeleteCAPIManifestSecret(cfg *config.Config) {
	if !cfg.BootstrapCAPIUseSecret {
		return
	}
	if !shell.CommandExists("kubectl") {
		return
	}
	ctx := "kind-" + cfg.KindClusterName
	if !contextExists(ctx) {
		return
	}
	_ = shell.Run("kubectl", "--context", ctx, "delete", "secret",
		"-n", cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, "--ignore-not-found")
}

// ResolvedLocalConfigYAMLPath ports bootstrap_resolved_local_config_yaml_path
// (L4350-L4360). Returns the explicit override when it exists on disk,
// ./config.yaml when it exists, or "".
func ResolvedLocalConfigYAMLPath(cfg *config.Config) string {
	if cfg.ProxmoxBootstrapConfigFile != "" {
		if _, err := os.Stat(cfg.ProxmoxBootstrapConfigFile); err == nil {
			return cfg.ProxmoxBootstrapConfigFile
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	p := filepath.Join(cwd, "config.yaml")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// WorkloadGencodeStampPath ports capi_bootstrap_workload_gencode_stamp_path
// (L4363-L4367). Uses XDG_STATE_HOME when set, else $HOME/.local/state.
func WorkloadGencodeStampPath(cfg *config.Config) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	name := cfg.KindClusterName
	if name == "" {
		name = "capi-provisioner"
	}
	return filepath.Join(base, "bootstrap-capi", "gencode", name, "workload.last-clusterctl")
}

// TouchWorkloadGencodeStamp ports capi_bootstrap_touch_workload_gencode_stamp
// (L4369-L4383). Best-effort: bail quietly on mkdir / open failures.
func TouchWorkloadGencodeStamp(cfg *config.Config) {
	s := WorkloadGencodeStampPath(cfg)
	if s == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s), 0o755); err != nil {
		return
	}
	// Create if missing, update mtime if present. os.Chtimes with now,
	// falling back to truncate-create if the stamp doesn't exist.
	if _, err := os.Stat(s); err != nil {
		if f, err := os.Create(s); err == nil {
			f.Close()
		}
		return
	}
	_ = os.Chtimes(s, timeNow(), timeNow())
}

// WorkloadClusterctlIsStale ports capi_bootstrap_workload_clusterctl_is_stale
// (L4386-L4410). Returns true when the caller should re-run clusterctl.
func WorkloadClusterctlIsStale(cfg *config.Config) bool {
	if cfg.BootstrapRegenerateCAPIManifest {
		return true
	}
	cfgPath := ResolvedLocalConfigYAMLPath(cfg)
	if cfgPath == "" {
		return false
	}
	if cfg.BootstrapCAPIUseSecret {
		st := WorkloadGencodeStampPath(cfg)
		stInfo, err := os.Stat(st)
		if err != nil {
			// New host: no stamp yet. If a local config.yaml exists,
			// assume we want a fresh generate.
			if _, err := os.Stat(cfgPath); err == nil {
				return true
			}
			return false
		}
		cfgInfo, err := os.Stat(cfgPath)
		if err != nil {
			return false
		}
		return cfgInfo.ModTime().After(stInfo.ModTime())
	}
	if cfg.CAPIManifest == "" {
		return false
	}
	mi, err := os.Stat(cfg.CAPIManifest)
	if err != nil {
		return false
	}
	ci, err := os.Stat(cfgPath)
	if err != nil {
		return false
	}
	return ci.ModTime().After(mi.ModTime())
}

// SyncClusterctlConfigFile ports bootstrap_sync_clusterctl_config_file
// (L4650-L4676). Uses an explicit CLUSTERCTL_CFG file when present, else
// creates a minimal ephemeral YAML with just the three Proxmox env keys.
// Returns the path the caller should hand to clusterctl.
func SyncClusterctlConfigFile(cfg *config.Config) string {
	var missing []string
	if cfg.ProxmoxURL == "" {
		missing = append(missing, "PROXMOX_URL")
	}
	if cfg.ProxmoxToken == "" {
		missing = append(missing, "PROXMOX_TOKEN")
	}
	if cfg.ProxmoxSecret == "" {
		missing = append(missing, "PROXMOX_SECRET")
	}
	if len(missing) > 0 {
		logx.Die("bootstrap_sync_clusterctl_config_file: Proxmox credentials are not set. Missing: %s", strings.Join(missing, " "))
	}
	if cfg.ClusterctlCfgFilePresent() {
		return cfg.ClusterctlCfg
	}
	RegisterExitTrap()
	f, err := os.CreateTemp("", "bootstrap-capi-clusterctl.*.yaml")
	if err != nil {
		logx.Die("Cannot create ephemeral clusterctl config: %v", err)
	}
	defer f.Close()
	body := fmt.Sprintf("PROXMOX_URL: %q\nPROXMOX_TOKEN: %q\nPROXMOX_SECRET: %q\n",
		cfg.ProxmoxURL, cfg.ProxmoxToken, cfg.ProxmoxSecret)
	if _, err := f.WriteString(body); err != nil {
		logx.Die("Cannot write ephemeral clusterctl config: %v", err)
	}
	SetEphemeralClusterctlConfig(f.Name())
	logx.Log("Using ephemeral clusterctl config under %s (bootstrap state lives in kind Secret %s, not a local clusterctl path).",
		os.TempDir(), cfg.ProxmoxBootstrapConfigSecretName)
	return f.Name()
}
