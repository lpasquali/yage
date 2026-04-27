// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"fmt"
	"os"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/provider/proxmox/pveapi"
)

// GenerateConfigsFromOutputs reads the four token outputs from
// OpenTofu state, normalises + validates them, overwrites
// cfg.Proxmox{Token,Secret,CSITokenID,CSITokenSecret,CSIURL},
// refreshes derived identity token IDs, and syncs the state to
// kind + local files.
func GenerateConfigsFromOutputs(cfg *config.Config) {
	csiAPIURL := pveapi.APIJSONURL(cfg)
	capiTokenID := GetOutput("capi_token_id")
	capiTokenSec := GetOutput("capi_token_secret")
	csiTokenID := GetOutput("csi_token_id")
	csiTokenSec := GetOutput("csi_token_secret")

	capiTokenSec = pveapi.NormalizeTokenSecret(capiTokenSec, capiTokenID)
	csiTokenSec = pveapi.NormalizeTokenSecret(csiTokenSec, csiTokenID)
	pveapi.ValidateTokenSecret("OpenTofu capi_token_secret", capiTokenSec)
	pveapi.ValidateTokenSecret("OpenTofu csi_token_secret", csiTokenSec)

	cfg.Providers.Proxmox.CAPIToken = capiTokenID
	cfg.Providers.Proxmox.CAPISecret = capiTokenSec
	cfg.Providers.Proxmox.CSITokenID = csiTokenID
	cfg.Providers.Proxmox.CSITokenSecret = csiTokenSec
	if cfg.Providers.Proxmox.CSIURL == "" {
		cfg.Providers.Proxmox.CSIURL = csiAPIURL
	}
	pveapi.RefreshDerivedIdentityTokenIDs(cfg)

	_ = kindsync.SyncBootstrapConfigToKind(cfg)
	_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)
	if !cfg.PersistLocalSecrets() {
		logx.Log("Local CSI YAML persistence is off; bootstrap config was still pushed to kind when the cluster is reachable (use --persist-local-secrets to also write PROXMOX_CSI_CONFIG when set).")
	}

	WriteClusterctlConfigIfMissing(cfg)
	WriteCSIConfigIfMissing(cfg)
}

// WriteClusterctlConfigIfMissing does NOT write a local clusterctl
// YAML. It refreshes derived identity token IDs and syncs bootstrap
// state to kind, plus logs a summary of which Secrets hold what.
func WriteClusterctlConfigIfMissing(cfg *config.Config) {
	pveapi.RefreshDerivedIdentityTokenIDs(cfg)
	if cfg.ClusterctlCfgFilePresent() {
		return
	}
	if cfg.Providers.Proxmox.URL == "" || cfg.Providers.Proxmox.CAPIToken == "" || cfg.Providers.Proxmox.CAPISecret == "" {
		return
	}
	_ = kindsync.SyncBootstrapConfigToKind(cfg)
	_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)
	switch {
	case cfg.Providers.Proxmox.BootstrapSecretName != "" && cfg.Providers.Proxmox.BootstrapAdminSecretName != cfg.Providers.Proxmox.BootstrapSecretName:
		logx.Log("Bootstrap state synced to kind: %s (config.yaml), %s (CAPI+CSI), %s (proxmox-admin.yaml) when the management cluster is reachable; clusterctl uses a temp file for the CLI only.",
			cfg.Providers.Proxmox.BootstrapConfigSecretName, cfg.Providers.Proxmox.BootstrapSecretName, cfg.Providers.Proxmox.BootstrapAdminSecretName)
	case cfg.Providers.Proxmox.BootstrapSecretName != "":
		logx.Log("Bootstrap state synced to kind: %s (config.yaml) and %s (combined) when the management cluster is reachable; clusterctl uses a temp file for the CLI only.",
			cfg.Providers.Proxmox.BootstrapConfigSecretName, cfg.Providers.Proxmox.BootstrapSecretName)
	default:
		logx.Log("Bootstrap state synced to kind: %s (config.yaml), %s + %s + %s when the management cluster is reachable; clusterctl uses a temp file for the CLI only.",
			cfg.Providers.Proxmox.BootstrapConfigSecretName,
			cfg.Providers.Proxmox.BootstrapCAPMOXSecretName,
			cfg.Providers.Proxmox.BootstrapCSISecretName,
			cfg.Providers.Proxmox.BootstrapAdminSecretName)
	}
}

// WriteCSIConfigIfMissing writes the proxmox-csi Helm values YAML
// with the current cfg.ProxmoxCSI* fields. No-op unless
// cfg.Providers.Proxmox.CSIConfig is set AND does not yet exist
// AND PersistLocalSecrets is true.
func WriteCSIConfigIfMissing(cfg *config.Config) {
	pveapi.RefreshDerivedIdentityTokenIDs(cfg)
	if cfg.Providers.Proxmox.CSIConfig == "" {
		return
	}
	if _, err := os.Stat(cfg.Providers.Proxmox.CSIConfig); err == nil {
		return
	}
	if !cfg.PersistLocalSecrets() {
		return
	}
	if cfg.Providers.Proxmox.CSIURL == "" {
		cfg.Providers.Proxmox.CSIURL = pveapi.APIJSONURL(cfg)
	}
	if cfg.Providers.Proxmox.CSITokenID == "" || cfg.Providers.Proxmox.CSITokenSecret == "" || cfg.Providers.Proxmox.Region == "" {
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
		cfg.Providers.Proxmox.CSIURL,
		cfg.Providers.Proxmox.CSIInsecure,
		cfg.Providers.Proxmox.CSITokenID,
		cfg.Providers.Proxmox.CSITokenSecret,
		cfg.Providers.Proxmox.Region,
		cfg.Providers.Proxmox.CSIStorageClassName,
		cfg.Providers.Proxmox.CSIStorage,
		cfg.Providers.Proxmox.CSIReclaimPolicy,
		cfg.Providers.Proxmox.CSIFsType,
		cfg.Providers.Proxmox.CSIDefaultClass,
	)
	if err := os.WriteFile(cfg.Providers.Proxmox.CSIConfig, []byte(body), 0o600); err != nil {
		logx.Die("Failed to write %s: %v", cfg.Providers.Proxmox.CSIConfig, err)
	}
	logx.Log("Generated %s.", cfg.Providers.Proxmox.CSIConfig)
}