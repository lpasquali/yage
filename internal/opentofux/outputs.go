package opentofux

import (
	"fmt"
	"os"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/kindsync"
	"github.com/lpasquali/yage/internal/logx"
	"github.com/lpasquali/yage/internal/proxmox"
)

// GenerateConfigsFromOutputs ports generate_configs_from_terraform_outputs
// (L3523-L3555). Reads the four token outputs from OpenTofu state,
// normalises + validates them, overwrites cfg.Proxmox{Token,Secret,
// CSITokenID,CSITokenSecret,CSIURL}, refreshes derived identity token
// IDs, and syncs the state to kind + local files.
func GenerateConfigsFromOutputs(cfg *config.Config) {
	csiAPIURL := proxmox.APIJSONURL(cfg)
	capiTokenID := GetOutput("capi_token_id")
	capiTokenSec := GetOutput("capi_token_secret")
	csiTokenID := GetOutput("csi_token_id")
	csiTokenSec := GetOutput("csi_token_secret")

	capiTokenSec = proxmox.NormalizeTokenSecret(capiTokenSec, capiTokenID)
	csiTokenSec = proxmox.NormalizeTokenSecret(csiTokenSec, csiTokenID)
	proxmox.ValidateTokenSecret("OpenTofu capi_token_secret", capiTokenSec)
	proxmox.ValidateTokenSecret("OpenTofu csi_token_secret", csiTokenSec)

	cfg.ProxmoxToken = capiTokenID
	cfg.ProxmoxSecret = capiTokenSec
	cfg.ProxmoxCSITokenID = csiTokenID
	cfg.ProxmoxCSITokenSecret = csiTokenSec
	if cfg.ProxmoxCSIURL == "" {
		cfg.ProxmoxCSIURL = csiAPIURL
	}
	proxmox.RefreshDerivedIdentityTokenIDs(cfg)

	_ = kindsync.SyncBootstrapConfigToKind(cfg)
	_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)
	if !cfg.PersistLocalSecrets() {
		logx.Log("Local CSI YAML persistence is off; bootstrap config was still pushed to kind when the cluster is reachable (use --persist-local-secrets to also write PROXMOX_CSI_CONFIG when set).")
	}

	WriteClusterctlConfigIfMissing(cfg)
	WriteCSIConfigIfMissing(cfg)
}

// WriteClusterctlConfigIfMissing ports write_clusterctl_config_if_missing
// (L3557-L3577). This function does NOT write a local clusterctl YAML —
// that path was removed. It only refreshes derived identity token IDs
// and syncs bootstrap state to kind, plus logs a summary of which
// Secrets hold what.
func WriteClusterctlConfigIfMissing(cfg *config.Config) {
	proxmox.RefreshDerivedIdentityTokenIDs(cfg)
	if cfg.ClusterctlCfgFilePresent() {
		return
	}
	if cfg.ProxmoxURL == "" || cfg.ProxmoxToken == "" || cfg.ProxmoxSecret == "" {
		return
	}
	_ = kindsync.SyncBootstrapConfigToKind(cfg)
	_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)
	switch {
	case cfg.ProxmoxBootstrapSecretName != "" && cfg.ProxmoxBootstrapAdminSecretName != cfg.ProxmoxBootstrapSecretName:
		logx.Log("Bootstrap state synced to kind: %s (config.yaml), %s (CAPI+CSI), %s (proxmox-admin.yaml) when the management cluster is reachable; clusterctl uses a temp file for the CLI only.",
			cfg.ProxmoxBootstrapConfigSecretName, cfg.ProxmoxBootstrapSecretName, cfg.ProxmoxBootstrapAdminSecretName)
	case cfg.ProxmoxBootstrapSecretName != "":
		logx.Log("Bootstrap state synced to kind: %s (config.yaml) and %s (legacy combined) when the management cluster is reachable; clusterctl uses a temp file for the CLI only.",
			cfg.ProxmoxBootstrapConfigSecretName, cfg.ProxmoxBootstrapSecretName)
	default:
		logx.Log("Bootstrap state synced to kind: %s (config.yaml), %s + %s + %s when the management cluster is reachable; clusterctl uses a temp file for the CLI only.",
			cfg.ProxmoxBootstrapConfigSecretName,
			cfg.ProxmoxBootstrapCAPMOXSecretName,
			cfg.ProxmoxBootstrapCSISecretName,
			cfg.ProxmoxBootstrapAdminSecretName)
	}
}

// WriteCSIConfigIfMissing ports write_csi_config_if_missing
// (L3579-L3614). No-op unless cfg.ProxmoxCSIConfig is set AND does not
// yet exist AND PersistLocalSecrets is true. Writes the proxmox-csi
// Helm values YAML with the current cfg.ProxmoxCSI* fields.
func WriteCSIConfigIfMissing(cfg *config.Config) {
	proxmox.RefreshDerivedIdentityTokenIDs(cfg)
	if cfg.ProxmoxCSIConfig == "" {
		return
	}
	if _, err := os.Stat(cfg.ProxmoxCSIConfig); err == nil {
		return
	}
	if !cfg.PersistLocalSecrets() {
		return
	}
	if cfg.ProxmoxCSIURL == "" {
		cfg.ProxmoxCSIURL = proxmox.APIJSONURL(cfg)
	}
	if cfg.ProxmoxCSITokenID == "" || cfg.ProxmoxCSITokenSecret == "" || cfg.ProxmoxRegion == "" {
		return
	}
	body := fmt.Sprintf(`config:
  clusters:
    - url: %s
      insecure: %s
      token_id: "%s"
      token_secret: "%s"
      region: "%s"

storageClass:
  - name: %s
    storage: %s
    reclaimPolicy: %s
    fstype: %s
    annotations:
      storageclass.kubernetes.io/is-default-class: "%s"
`,
		cfg.ProxmoxCSIURL,
		cfg.ProxmoxCSIInsecure,
		cfg.ProxmoxCSITokenID,
		cfg.ProxmoxCSITokenSecret,
		cfg.ProxmoxRegion,
		cfg.ProxmoxCSIStorageClassName,
		cfg.ProxmoxCSIStorage,
		cfg.ProxmoxCSIReclaimPolicy,
		cfg.ProxmoxCSIFsType,
		cfg.ProxmoxCSIDefaultClass,
	)
	if err := os.WriteFile(cfg.ProxmoxCSIConfig, []byte(body), 0o600); err != nil {
		logx.Die("Failed to write %s: %v", cfg.ProxmoxCSIConfig, err)
	}
	logx.Log("Generated %s.", cfg.ProxmoxCSIConfig)
}
