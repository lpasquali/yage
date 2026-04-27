// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// EnsureCAPIManifestPath has two modes:
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

// RefreshDefaultCAPIManifestPath is called after the interactive
// cluster picker chose a different cluster — clears the stale
// ephemeral file and logs which Secret the next load/gen will
// touch.
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
	logx.Die("yage: internal error — CAPI manifest path refresh with neither user file nor Secret mode.")
}

// TryLoadCAPIManifestFromSecret loads the CAPI manifest from the
// kind-side Secret when BootstrapCAPIUseSecret=true and both the
// kind-context and the Secret exist. No-op otherwise.
func TryLoadCAPIManifestFromSecret(cfg *config.Config) {
	if !cfg.BootstrapCAPIUseSecret || cfg.CAPIManifest == "" {
		return
	}
	ctxName := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(ctxName) {
		return
	}
	cli, err := k8sclient.ForContext(ctxName)
	if err != nil {
		return
	}
	bg := context.Background()
	if _, err := cli.Typed.CoreV1().Namespaces().Get(bg, cfg.CAPIManifestSecretNamespace, metav1.GetOptions{}); err != nil {
		return
	}
	sec, err := cli.Typed.CoreV1().Secrets(cfg.CAPIManifestSecretNamespace).
		Get(bg, cfg.CAPIManifestSecretName, metav1.GetOptions{})
	if err != nil {
		return
	}
	body, ok := sec.Data[cfg.CAPIManifestSecretKey]
	if !ok || len(body) == 0 {
		return
	}
	if err := os.WriteFile(cfg.CAPIManifest, body, 0o600); err != nil {
		return
	}
	logx.Log("Loaded workload manifest from Secret %s/%s (key %s, context %s).",
		cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, cfg.CAPIManifestSecretKey, ctxName)
}

// PushCAPIManifestToSecret pushes the local CAPI manifest into the
// kind-side Secret. No-op when BootstrapCAPIUseSecret is false or
// the local CAPI manifest is empty. Dies when the manifest
// exceeds the ~1 MiB Secret data limit.
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
	ctxName := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(ctxName)
	if err != nil {
		logx.Die("Failed to store workload manifest in Secret %s/%s: cannot build kube client for %s: %v",
			cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, ctxName, err)
	}
	bg := context.Background()
	if err := cli.EnsureNamespace(bg, cfg.CAPIManifestSecretNamespace); err != nil {
		logx.Die("Failed to ensure namespace %s: %v", cfg.CAPIManifestSecretNamespace, err)
	}
	body, err := os.ReadFile(cfg.CAPIManifest)
	if err != nil {
		logx.Die("Failed to read CAPI manifest %s: %v", cfg.CAPIManifest, err)
	}
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cfg.CAPIManifestSecretName,
			Namespace: cfg.CAPIManifestSecretNamespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "yage"},
		},
		Data: map[string][]byte{cfg.CAPIManifestSecretKey: body},
	}
	jsonBody, err := yaml.Marshal(sec)
	if err == nil {
		jsonBody, err = yaml.YAMLToJSON(jsonBody)
	}
	if err != nil {
		logx.Die("Failed to encode workload manifest Secret: %v", err)
	}
	if _, err := cli.Typed.CoreV1().Secrets(cfg.CAPIManifestSecretNamespace).
		Patch(bg, cfg.CAPIManifestSecretName, types.ApplyPatchType, jsonBody, metav1.PatchOptions{
			FieldManager: k8sclient.FieldManager,
			Force:        boolPtrLocal(true),
		}); err != nil {
		logx.Die("Failed to store workload manifest in Secret %s/%s (key %s): %v",
			cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, cfg.CAPIManifestSecretKey, err)
	}
	logx.Log("Wrote workload manifest to Secret %s/%s (key %s). No persistent file under ~/.yage — debug via k9s or kubectl get secret -n %s %s -o yaml.",
		cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName, cfg.CAPIManifestSecretKey,
		cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName)
	TouchWorkloadGencodeStamp(cfg)
}

// boolPtrLocal returns a pointer to b — package-local helper to avoid an
// import cycle with the foundation when both are in the same module.
func boolPtrLocal(b bool) *bool { return &b }

// DeleteCAPIManifestSecret deletes the kind-side CAPI manifest
// Secret. No-op when BootstrapCAPIUseSecret is false or the kind
// context is not reachable.
func DeleteCAPIManifestSecret(cfg *config.Config) {
	if !cfg.BootstrapCAPIUseSecret {
		return
	}
	ctxName := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(ctxName) {
		return
	}
	cli, err := k8sclient.ForContext(ctxName)
	if err != nil {
		return
	}
	bg := context.Background()
	if err := cli.Typed.CoreV1().Secrets(cfg.CAPIManifestSecretNamespace).
		Delete(bg, cfg.CAPIManifestSecretName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		logx.Warn("delete CAPI manifest Secret: %v", err)
	}
}

// ResolvedLocalConfigYAMLPath returns the explicit override when
// it exists on disk, ./config.yaml when it exists, or "".
func ResolvedLocalConfigYAMLPath(cfg *config.Config) string {
	if cfg.Providers.Proxmox.BootstrapConfigFile != "" {
		if _, err := os.Stat(cfg.Providers.Proxmox.BootstrapConfigFile); err == nil {
			return cfg.Providers.Proxmox.BootstrapConfigFile
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

// WorkloadGencodeStampPath returns the on-disk stamp path. Uses
// XDG_STATE_HOME when set, else $HOME/.local/state.
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
	return filepath.Join(base, "yage", "gencode", name, "workload.last-clusterctl")
}

// TouchWorkloadGencodeStamp updates (or creates) the stamp file's
// mtime. Best-effort: bail quietly on mkdir / open failures.
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

// WorkloadClusterctlIsStale returns true when the caller should
// re-run clusterctl.
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

// SyncClusterctlConfigFile uses an explicit CLUSTERCTL_CFG file
// when present. For Proxmox, creates a minimal ephemeral YAML with
// the three Proxmox env keys when no file is set. For other
// infrastructure providers (AWS, Azure, …) CAP* credentials come
// from the process environment; clusterctl's --config is optional —
// returns "" so init/generate omit it.
func SyncClusterctlConfigFile(cfg *config.Config) string {
	if cfg.ClusterctlCfgFilePresent() {
		return cfg.ClusterctlCfg
	}
	if cfg.InfraProvider != "proxmox" {
		return ""
	}
	var missing []string
	if cfg.Providers.Proxmox.URL == "" {
		missing = append(missing, "PROXMOX_URL")
	}
	if cfg.Providers.Proxmox.CAPIToken == "" {
		missing = append(missing, "PROXMOX_CAPI_TOKEN")
	}
	if cfg.Providers.Proxmox.CAPISecret == "" {
		missing = append(missing, "PROXMOX_CAPI_SECRET")
	}
	if len(missing) > 0 {
		logx.Die("SyncClusterctlConfigFile: Proxmox credentials are not set. Missing: %s", strings.Join(missing, " "))
	}
	RegisterExitTrap()
	f, err := os.CreateTemp("", "yage-clusterctl.*.yaml")
	if err != nil {
		logx.Die("Cannot create ephemeral clusterctl config: %v", err)
	}
	defer f.Close()
	body := fmt.Sprintf("PROXMOX_URL: %q\nPROXMOX_TOKEN: %q\nPROXMOX_SECRET: %q\n",
		cfg.Providers.Proxmox.URL, cfg.Providers.Proxmox.CAPIToken, cfg.Providers.Proxmox.CAPISecret)
	if _, err := f.WriteString(body); err != nil {
		logx.Die("Cannot write ephemeral clusterctl config: %v", err)
	}
	SetEphemeralClusterctlConfig(f.Name())
	logx.Log("Using ephemeral clusterctl config under %s (bootstrap state lives in kind Secret %s, not a local clusterctl path).",
		os.TempDir(), cfg.Providers.Proxmox.BootstrapConfigSecretName)
	return f.Name()
}