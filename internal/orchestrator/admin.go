package orchestrator

import (
	"fmt"
	"os"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/ui/promptx"
	"github.com/lpasquali/yage/internal/util/yamlx"
)

// EnsureProxmoxAdminConfig ports ensure_proxmox_admin_config
// (L7620-L7701). Populates cfg.Providers.Proxmox.URL / Username / Token from:
//
//  1. MergeProxmoxBootstrapSecretsFromKind (already run at top of the
//     bootstrap flow).
//  2. The PROXMOX_ADMIN_CONFIG legacy YAML file when set and present.
//  3. Interactive prompts (only if stdin is a TTY).
//
// Dies when it still can't satisfy all three admin fields.
func EnsureProxmoxAdminConfig(cfg *config.Config, merge, syncConfig, syncCreds func()) {
	if merge != nil {
		merge()
	}
	fillFromAdminYAML(cfg)
	if cfg.Providers.Proxmox.URL != "" && cfg.Providers.Proxmox.AdminUsername != "" && cfg.Providers.Proxmox.AdminToken != "" {
		return
	}
	var missing []string
	if cfg.Providers.Proxmox.URL == "" {
		missing = append(missing, "PROXMOX_URL")
	}
	if cfg.Providers.Proxmox.AdminUsername == "" {
		missing = append(missing, "PROXMOX_ADMIN_USERNAME")
	}
	if cfg.Providers.Proxmox.AdminToken == "" {
		missing = append(missing, "PROXMOX_ADMIN_TOKEN")
	}

	if !promptx.CanPrompt() {
		logx.Die("Missing admin Proxmox configuration: %v. Set them via environment variables, kind Secret %s/%s (admin API), or PROXMOX_ADMIN_CONFIG to a legacy local YAML (not written by this script by default).",
			missing, cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapAdminSecretName)
	}
	if cfg.ProxmoxAdminCfgFilePresent() {
		logx.Warn("Using %s to supply missing values only; prefer kind Secrets in %s.",
			cfg.Providers.Proxmox.AdminConfig, cfg.Providers.Proxmox.BootstrapSecretNamespace)
	}

	if promptx.Confirm("Enter Proxmox admin API credentials interactively for OpenTofu bootstrap?") {
		if cfg.Providers.Proxmox.URL == "" {
			fmt.Fprint(os.Stderr, "\033[1;36m[?]\033[0m Proxmox VE URL (e.g. https://pve.example:8006): ")
			cfg.Providers.Proxmox.URL = promptx.ReadLine()
		}
		if cfg.Providers.Proxmox.AdminUsername == "" {
			fmt.Fprint(os.Stderr, "\033[1;36m[?]\033[0m Proxmox admin username token ID (e.g. root@pam!capi-bootstrap): ")
			cfg.Providers.Proxmox.AdminUsername = promptx.ReadLine()
		}
		if cfg.Providers.Proxmox.AdminToken == "" {
			fmt.Fprint(os.Stderr, "\033[1;36m[?]\033[0m Proxmox admin token secret (UUID): ")
			cfg.Providers.Proxmox.AdminToken = promptx.ReadLine()
		}
		if syncConfig != nil {
			syncConfig()
		}
		if syncCreds != nil {
			syncCreds()
		}
		if !cfg.PersistLocalSecrets() {
			logx.Log("Local CSI / extra file persistence is off; admin API identity is still synced to kind when the cluster is reachable. No proxmox-admin file is written by default.")
		}
		return
	}

	logx.Warn("Skipping interactive creation. Add admin API identity to kind Secret %s/%s (data key %s, or flat keys for migration), export the variables, or set PROXMOX_ADMIN_CONFIG to a legacy file you maintain (not auto-written here).",
		cfg.Providers.Proxmox.BootstrapSecretNamespace, cfg.Providers.Proxmox.BootstrapAdminSecretName,
		fallback(cfg.Providers.Proxmox.BootstrapAdminSecretKey, "proxmox-admin.yaml"))
	logx.Warn("Expected format:")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, `  PROXMOX_URL: "https://pve.example:8006"`)
	fmt.Fprintln(os.Stderr, `  PROXMOX_ADMIN_USERNAME: "root@pam!capi-bootstrap"`)
	fmt.Fprintln(os.Stderr, `  PROXMOX_ADMIN_TOKEN: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"`)
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, "\033[1;33m[?]\033[0m Press ENTER once you have set kind Secrets, env, or a legacy admin YAML (PROXMOX_ADMIN_CONFIG)...")
	_ = promptx.ReadLine()

	if merge != nil {
		merge()
	}
	fillFromAdminYAML(cfg)
	if cfg.Providers.Proxmox.URL == "" || cfg.Providers.Proxmox.AdminUsername == "" || cfg.Providers.Proxmox.AdminToken == "" {
		logx.Die("Proxmox admin still unset: not in kind Secrets (see %s), not in the environment, and not in PROXMOX_ADMIN_CONFIG. Aborting.",
			cfg.Providers.Proxmox.BootstrapSecretNamespace)
	}
	logx.Log("Continuing with admin credentials from kind, environment, or legacy PROXMOX_ADMIN_CONFIG file.")
}

// fillFromAdminYAML reads the legacy admin YAML when set and fills any
// still-empty field. Non-destructive.
func fillFromAdminYAML(cfg *config.Config) {
	if !cfg.ProxmoxAdminCfgFilePresent() {
		return
	}
	if cfg.Providers.Proxmox.URL == "" {
		cfg.Providers.Proxmox.URL = yamlx.GetValue(cfg.Providers.Proxmox.AdminConfig, "PROXMOX_URL")
	}
	if cfg.Providers.Proxmox.AdminUsername == "" {
		cfg.Providers.Proxmox.AdminUsername = yamlx.GetValue(cfg.Providers.Proxmox.AdminConfig, "PROXMOX_ADMIN_USERNAME")
	}
	if cfg.Providers.Proxmox.AdminToken == "" {
		cfg.Providers.Proxmox.AdminToken = yamlx.GetValue(cfg.Providers.Proxmox.AdminConfig, "PROXMOX_ADMIN_TOKEN")
	}
}

func fallback(s, d string) string {
	if s != "" {
		return s
	}
	return d
}
