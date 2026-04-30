// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package orchestrator drives the top-level bootstrap phases and the
// standalone modes (rollout, backup, argocd, …).
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/lpasquali/yage/internal/capi/argocd"
	"github.com/lpasquali/yage/internal/capi/caaph"
	"github.com/lpasquali/yage/internal/cluster/capacity"
	"github.com/lpasquali/yage/internal/capi/manifest"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/csi/proxmoxcsi"
	"github.com/lpasquali/yage/internal/platform/installer"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/cluster/kind"
	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/platform/kubectl"
	"github.com/lpasquali/yage/internal/obs"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/platform/opentofux"
	"github.com/lpasquali/yage/internal/capi/pivot"
	"github.com/lpasquali/yage/internal/ui/promptx"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/provider/proxmox/api"
	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/util/idgen"
	"github.com/lpasquali/yage/internal/util/yamlx"
)

// Run executes the top-level bootstrap flow. Returns an exit code.
func Run(ctx context.Context, cfg *config.Config) int {
	// Apply the KIND_CLUSTER_NAME default.
	if cfg.KindClusterName == "" {
		if cfg.ClusterName != "" {
			cfg.KindClusterName = cfg.ClusterName
		} else {
			cfg.KindClusterName = "capi-provisioner"
		}
	}
	// ALLOWED_NODES falls back to PROXMOX_NODE once parsing is done.
	if cfg.AllowedNodes == "" {
		cfg.AllowedNodes = cfg.Providers.Proxmox.Node
	}

	// CAPI VMs need at least 2 vCPUs (sockets × cores) per role to
	// schedule kubeadm or k3s reliably. Validate before any phase runs;
	// fail fast so we don't burn cycles on kind + clusterctl init only
	// to have CAPI reject an undersized PMT later.
	if err := validateMinVCPU(cfg); err != nil {
		logx.Die("%v", err)
	}

	// Top-level dry-run: print the plan and exit before any phase runs.
	// Distinct from PivotDryRun (that flag actually provisions mgmt and
	// stops at clusterctl move).
	if cfg.DryRun {
		PrintPlan(cfg)
		return 0
	}

	// -------------------------------------------------------------------------
	// Standalone: kind backup / restore
	// -------------------------------------------------------------------------
	switch cfg.BootstrapKindStateOp {
	case "backup":
		if err := installer.Kubectl(cfg); err != nil {
			logx.Die("ensure_kubectl failed: %v", err)
		}
		if err := kind.Backup(cfg, cfg.BootstrapKindBackupOut); err != nil {
			logx.Die("%v", err)
		}
		return 0
	case "restore":
		if err := installer.Kubectl(cfg); err != nil {
			logx.Die("ensure_kubectl failed: %v", err)
		}
		if err := kind.Restore(cfg, cfg.BootstrapKindStatePath); err != nil {
			logx.Die("%v", err)
		}
		return 0
	}

	// -------------------------------------------------------------------------
	// Standalone: --workload-rollout
	// -------------------------------------------------------------------------
	if cfg.WorkloadRolloutStandalone {
		shell.RequireCmd("kubectl")
		kindsync.MergeBootstrapSecretsFromKind(cfg)
		_ = kindsync.SyncBootstrapConfigToKind(cfg)
		if ctx, ok := kubectl.ResolveBootstrapContext(cfg); ok {
			cfg.KindClusterName = strings.TrimPrefix(ctx, "kind-")
		}
		shell.RequireCmd("python3")
		if cfg.WorkloadClusterName == "" {
			cfg.WorkloadClusterName = "capi-quickstart"
		}
		if cfg.WorkloadClusterNamespace == "" {
			cfg.WorkloadClusterNamespace = "default"
		}
		if cfg.CAPIManifest != "" {
			if fi, err := os.Stat(cfg.CAPIManifest); err == nil && fi.Size() > 0 {
				capimanifest.DiscoverWorkloadClusterIdentity(cfg, cfg.CAPIManifest)
			}
		}
		// If workload kubeconfig Secret not yet present, try argocd standalone discovery.
		if !workloadKubeconfigSecretExists(cfg) {
			_ = argocd.StandaloneDiscoverWorkloadKubeconfigRef(cfg)
		}
		if cfg.WorkloadRolloutMode == "argocd" || cfg.WorkloadRolloutMode == "all" {
			if !workloadKubeconfigSecretExists(cfg) {
				logx.Die("Workload kubeconfig not found: namespace %s secret %s-kubeconfig. "+
					"Set --workload-cluster-name and --workload-cluster-namespace, or CAPI_MANIFEST, "+
					"or ensure CAPI has created the cluster.",
					cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
			}
		}
		switch cfg.WorkloadRolloutMode {
		case "argocd", "capi", "all":
		default:
			logx.Die("Invalid --workload-rollout mode: %s (use argocd, capi, or all)", cfg.WorkloadRolloutMode)
		}
		logx.Log("workload-rollout: mode=%s (management context kind-%s, workload %s/%s)",
			cfg.WorkloadRolloutMode, cfg.KindClusterName, cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
		if !contextExists(cfg.KindClusterName) {
			logx.Die("Management cluster context 'kind-%s' not found. The kind cluster must be running for this command.", cfg.KindClusterName)
		}
		if cfg.WorkloadRolloutMode == "capi" || cfg.WorkloadRolloutMode == "all" {
			EnsureCAPIManifestPath(cfg)
			capimanifest.TryFillWorkloadInputsFromManagement(cfg)
			kindsync.MergeBootstrapSecretsFromKind(cfg)
			if cfg.Providers.Proxmox.TemplateID == "" {
				cfg.Providers.Proxmox.TemplateID = "104"
			}
			TryLoadCAPIManifestFromSecret(cfg)
			if fi, err := os.Stat(cfg.CAPIManifest); err != nil || fi.Size() == 0 {
				SyncClusterctlConfigFile(cfg)
			}
			capimanifest.GenerateWorkloadManifestIfMissing(cfg,
				func() bool { return WorkloadClusterctlIsStale(cfg) },
				func() string { return SyncClusterctlConfigFile(cfg) },
				func() { _ = kindsync.SyncBootstrapConfigToKind(cfg) },
			)
			_ = capimanifest.PatchProxmoxCSITopologyLabels(cfg)
			_ = capimanifest.PatchKubeadmSkipKubeProxyForCilium(cfg)
			_, _ = capimanifest.PatchProxmoxMachineTemplateSpecRevisions(cfg)
			capimanifest.DiscoverWorkloadClusterIdentity(cfg, cfg.CAPIManifest)
			_ = capimanifest.EnsureWorkloadClusterLabel(cfg, cfg.CAPIManifest, cfg.WorkloadClusterName)
			PushCAPIManifestToSecret(cfg)
			kubectl.WarnRegeneratedManifestImmutableRisk(cfg)
			for attempt := 1; attempt <= 3; attempt++ {
				if err := kubectl.ApplyWorkloadManifestToManagementCluster(cfg, cfg.CAPIManifest); err == nil {
					break
				}
				if attempt == 3 {
					logx.Die("CAPI manifest apply failed after %d attempts.", attempt)
				}
				logx.Warn("Apply failed (attempt %d/3). Retrying in 10s while webhooks settle...", attempt)
				time.Sleep(10 * time.Second)
			}
			logx.Log("workload-rollout: CAPI manifest re-applied to the management cluster.")
			logx.Log("workload-rollout: Forcing machine rollout (clusterctl alpha rollout restart when available, else spec.rolloutAfter)…")
			WorkloadRolloutCAPITouchRollout(cfg)
		}
		if cfg.WorkloadRolloutMode == "argocd" || cfg.WorkloadRolloutMode == "all" {
			if !cfg.ArgoCD.Enabled {
				logx.Die("ARGOCD_ENABLED is false — cannot use argocd rollout. Use --workload-rollout capi, or set ARGOCD_ENABLED=true.")
			}
			if !cfg.ArgoCD.WorkloadEnabled {
				logx.Die("WORKLOAD_ARGOCD_ENABLED is false — no workload Argo.")
			}
			logx.Log("workload-rollout: CAAPH + app-of-apps Git — re-sync from the workload Argo CD (e.g. `argocd app sync %s` with workload kubeconfig, or refresh the root/child Applications in the UI).", cfg.WorkloadClusterName)
		}
		logx.Log("workload-rollout: done.")
		return 0
	}

	// -------------------------------------------------------------------------
	// Standalone: --argocd-print-access / --argocd-port-forward
	// -------------------------------------------------------------------------
	if cfg.ArgoCD.PrintAccessStandalone || cfg.ArgoCD.PortForwardStandalone {
		shell.RequireCmd("kubectl")
		kindsync.MergeBootstrapSecretsFromKind(cfg)
		_ = kindsync.SyncBootstrapConfigToKind(cfg)
		if ctx, ok := kubectl.ResolveBootstrapContext(cfg); ok {
			cfg.KindClusterName = strings.TrimPrefix(ctx, "kind-")
		}
		if cfg.WorkloadClusterName == "" {
			cfg.WorkloadClusterName = "capi-quickstart"
		}
		if cfg.WorkloadClusterNamespace == "" {
			cfg.WorkloadClusterNamespace = "default"
		}
		if cfg.CAPIManifest != "" {
			if fi, err := os.Stat(cfg.CAPIManifest); err == nil && fi.Size() > 0 {
				capimanifest.DiscoverWorkloadClusterIdentity(cfg, cfg.CAPIManifest)
			}
		}
		if !workloadKubeconfigSecretExists(cfg) {
			_ = argocd.StandaloneDiscoverWorkloadKubeconfigRef(cfg)
		}
		if cfg.ArgoCD.PrintAccessStandalone {
			argocd.PrintAccessInfo(cfg)
		}
		if cfg.ArgoCD.PortForwardStandalone {
			argocd.RunPortForwards(cfg)
		}
		return 0
	}

	// -------------------------------------------------------------------------
	// Pre-phase: ensure CAPI manifest path
	// -------------------------------------------------------------------------
	EnsureCAPIManifestPath(cfg)

	// --- Purge ---
	if cfg.Purge {
		if !cfg.Force {
			if !promptx.Confirm("Purge generated files and Terraform state before continuing?") {
				logx.Die("Purge cancelled by user.")
			}
		}
		PurgeGeneratedArtifacts(cfg)
	}

	// --- CLUSTER_SET_ID + identity suffix ---
	if cfg.Providers.Proxmox.RecreateIdentities {
		logx.Log("Re-creation mode: identity parameters are resolved later (Terraform state or CAPI/CSI token IDs in kind / env).")
		if cfg.ClusterSetID != "" && cfg.Providers.Proxmox.IdentitySuffix == "" {
			cfg.Providers.Proxmox.IdentitySuffix = api.DeriveIdentitySuffix(cfg.ClusterSetID)
		}
		if cfg.ClusterSetID != "" {
			api.ValidateClusterSetIDFormat(cfg)
		}
		if cfg.Providers.Proxmox.IdentitySuffix != "" {
			logx.Log("Using Proxmox identity suffix: %s", cfg.Providers.Proxmox.IdentitySuffix)
		}
	} else {
		if cfg.ClusterSetID == "" {
			cfg.ClusterSetID = idgen.GenerateUUIDv4()
			logx.Log("Generated CLUSTER_SET_ID: %s", cfg.ClusterSetID)
		}
		if cfg.Providers.Proxmox.IdentitySuffix == "" {
			cfg.Providers.Proxmox.IdentitySuffix = api.DeriveIdentitySuffix(cfg.ClusterSetID)
		}
		api.ValidateClusterSetIDFormat(cfg)
		logx.Log("Using Proxmox identity suffix: %s", cfg.Providers.Proxmox.IdentitySuffix)
	}

	// =========================================================================
	// Dependency install phase
	// =========================================================================
	ctx, phDeps := obs.StartPhase(ctx, "dependency-install")

	if err := installer.SystemDependencies(); err != nil {
		logx.Die("ensure_system_dependencies failed: %v", err)
	}
	shell.RequireCmd("git")
	shell.RequireCmd("curl")
	shell.RequireCmd("python3")

	// Docker
	installer.Docker()

	// kubectl first pass, then merge secrets from kind (may update ClusterctlVersion
	// and image pins), then re-run kubectl in case the pinned version changed.
	if err := installer.Kubectl(cfg); err != nil {
		logx.Die("ensure_kubectl failed: %v", err)
	}
	kindsync.MergeBootstrapSecretsFromKind(cfg)
	if err := installer.Kubectl(cfg); err != nil {
		logx.Die("ensure_kubectl (2nd pass) failed: %v", err)
	}
	if err := installer.Kind(cfg); err != nil {
		logx.Die("ensure_kind failed: %v", err)
	}
	if err := installer.Clusterctl(cfg); err != nil {
		logx.Die("ensure_clusterctl failed: %v", err)
	}
	if err := installer.CiliumCLI(cfg); err != nil {
		logx.Die("ensure_cilium_cli failed: %v", err)
	}
	if err := installer.Helm(); err != nil {
		logx.Die("ensure_helm_present failed: %v", err)
	}
	if cfg.ArgoCD.Enabled {
		if err := installer.ArgoCDCLI(cfg); err != nil {
			logx.Die("ensure_argocd_cli failed: %v", err)
		}
		if cfg.KyvernoEnabled {
			if err := installer.KyvernoCLI(cfg); err != nil {
				logx.Die("ensure_kyverno_cli failed: %v", err)
			}
		}
		if cfg.CertManagerEnabled {
			if err := installer.Cmctl(cfg); err != nil {
				logx.Die("ensure_cmctl failed: %v", err)
			}
		}
	}
	shell.RequireCmd("kind")
	shell.RequireCmd("kubectl")
	shell.RequireCmd("clusterctl")
	shell.RequireCmd("cilium")
	shell.RequireCmd("helm")
	if cfg.ArgoCD.Enabled {
		shell.RequireCmd("argocd")
		if cfg.KyvernoEnabled {
			shell.RequireCmd("kyverno")
		}
		if cfg.CertManagerEnabled {
			shell.RequireCmd("cmctl")
		}
	}

	MaybeInteractiveSelectKindCluster(cfg)
	if !cfg.BootstrapCAPIManifestUserSet {
		RefreshDefaultCAPIManifestPath(cfg)
	}

	kindsync.MergeBootstrapSecretsFromKind(cfg)
	_ = kindsync.SyncBootstrapConfigToKind(cfg)

	// Determine whether to skip heavy maintenance (upgrade Docker, BPG provider).
	skipHeavy := false
	if cfg.NoDeleteKind {
		skipHeavy = true
	} else {
		out, _, _ := shell.Capture("kind", "get", "clusters")
		for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
			if strings.TrimSpace(ln) == cfg.KindClusterName {
				skipHeavy = !cfg.Force
				break
			}
		}
	}

	if skipHeavy {
		logx.Log("Existing kind cluster '%s' detected (or NO_DELETE_KIND) — skipping Docker package upgrade and bpg/proxmox provider install.", cfg.KindClusterName)
	} else {
		logx.Log("Updating Docker via package manager...")
		installer.UpgradeDocker()
		v, _, _ := shell.Capture("docker", "--version")
		logx.Log("Docker version: %s", strings.TrimSpace(v))
	}

	if err := installer.OpenTofu(cfg); err != nil {
		logx.Die("ensure_opentofu failed: %v", err)
	}
	shell.RequireCmd("tofu")
	// TODO(#71): move to Provider.InstallDependencies once that interface method exists.
	if !skipHeavy && cfg.InfraProvider == "proxmox" {
		if err := opentofux.InstallBPGProvider(cfg); err != nil {
			logx.Die("install_bpg_proxmox_provider failed: %v", err)
		}
	}

	EnsureKindConfig(cfg)
	phDeps.End()

	// =========================================================================
	// Bootstrap phase
	// =========================================================================

	// --- Proxmox identity bootstrap ---
	ctx, phIdentity := obs.StartPhase(ctx, "identity-bootstrap",
		obs.Str("provider", cfg.InfraProvider),
	)
	recreateOpenTofuDone := false
	phase0IdentityBootstrap := false
	// TODO(#71): replace with prov.EnsureIdentity(cfg) once the identity
	// bootstrap logic is fully delegated to the provider interface.
	if cfg.InfraProvider == "proxmox" {
		if !cfg.ClusterctlCfgFilePresent() && !cfg.HaveClusterctlCredsInEnv() {
			phase0IdentityBootstrap = true
		}
		if cfg.Providers.Proxmox.CSIConfig != "" {
			if _, err := os.Stat(cfg.Providers.Proxmox.CSIConfig); err != nil {
				phase0IdentityBootstrap = true
			}
		} else if cfg.Providers.Proxmox.CSITokenID == "" || cfg.Providers.Proxmox.CSITokenSecret == "" {
			phase0IdentityBootstrap = true
		}
	}

	if phase0IdentityBootstrap {
		logx.Warn("Clusterctl API identity and/or CSI credentials are not satisfied from env or an explicit local clusterctl file — checking further.")
		api.RefreshDerivedIdentityTokenIDs(cfg)
		opentofux.WriteClusterctlConfigIfMissing(cfg)
		opentofux.WriteCSIConfigIfMissing(cfg)

		needTerraform := false
		if !cfg.ClusterctlCfgFilePresent() && !cfg.HaveClusterctlCredsInEnv() {
			needTerraform = true
		}
		if cfg.Providers.Proxmox.CSIConfig != "" {
			if _, err := os.Stat(cfg.Providers.Proxmox.CSIConfig); err != nil {
				if cfg.Providers.Proxmox.CSITokenID == "" || cfg.Providers.Proxmox.CSITokenSecret == "" {
					needTerraform = true
				}
			}
		} else if cfg.Providers.Proxmox.CSITokenID == "" || cfg.Providers.Proxmox.CSITokenSecret == "" {
			needTerraform = true
		}

		if needTerraform {
			logx.Warn("CLI/env values are insufficient — running Terraform bootstrap for CAPI/CSI identities.")
			if cfg.Providers.Proxmox.URL == "" || cfg.Providers.Proxmox.AdminUsername == "" || cfg.Providers.Proxmox.AdminToken == "" {
				EnsureProxmoxAdminConfig(cfg,
					func() { kindsync.MergeBootstrapSecretsFromKind(cfg) },
					func() { _ = kindsync.SyncBootstrapConfigToKind(cfg) })
			}
			var missingAdmin []string
			if cfg.Providers.Proxmox.URL == "" {
				missingAdmin = append(missingAdmin, "PROXMOX_URL")
			}
			if cfg.Providers.Proxmox.AdminUsername == "" {
				missingAdmin = append(missingAdmin, "PROXMOX_ADMIN_USERNAME")
			}
			if cfg.Providers.Proxmox.AdminToken == "" {
				missingAdmin = append(missingAdmin, "PROXMOX_ADMIN_TOKEN")
			}
			if len(missingAdmin) > 0 {
				logx.Die("Missing admin Proxmox configuration: %v. Cannot run Terraform bootstrap without admin credentials.", missingAdmin)
			}
			if err := api.ResolveRegionAndNodeFromAdminAPI(cfg); err != nil {
				logx.Warn("resolve_proxmox_region_and_node_from_admin_api: %v", err)
			}
			_ = api.ResolveAvailableClusterSetIDForRoles(cfg)
			api.CheckAdminAPIConnectivity(cfg)
			if cfg.Providers.Proxmox.RecreateIdentities {
				if err := opentofux.RecreateIdentities(cfg); err != nil {
					logx.Die("recreate_proxmox_identities_terraform failed: %v", err)
				}
				recreateOpenTofuDone = true
			} else {
				if err := opentofux.ApplyIdentity(cfg); err != nil {
					logx.Die("apply_proxmox_identity_terraform failed: %v", err)
				}
				opentofux.GenerateConfigsFromOutputs(cfg)
			}
		}
	}

	// TODO(#71): RecreateIdentities flag should live behind Provider.EnsureIdentity
	// once the full identity lifecycle is delegated to the provider interface.
	if cfg.InfraProvider == "proxmox" && cfg.Providers.Proxmox.RecreateIdentities && !recreateOpenTofuDone {
		if cfg.Providers.Proxmox.URL == "" || cfg.Providers.Proxmox.AdminUsername == "" || cfg.Providers.Proxmox.AdminToken == "" {
			EnsureProxmoxAdminConfig(cfg,
				func() { kindsync.MergeBootstrapSecretsFromKind(cfg) },
				func() { _ = kindsync.SyncBootstrapConfigToKind(cfg) })
		}
		if err := api.ResolveRegionAndNodeFromAdminAPI(cfg); err != nil {
			logx.Warn("%v", err)
		}
		_ = api.ResolveAvailableClusterSetIDForRoles(cfg)
		api.CheckAdminAPIConnectivity(cfg)
		if err := opentofux.RecreateIdentities(cfg); err != nil {
			logx.Die("%v", err)
		}
	}
	phIdentity.End()

	var clusterctlCfgPath string
	// TODO(#71): the interactive clusterctl credential collection below is
	// Proxmox-specific. Move to Provider.EnsureIdentity once other providers
	// have a parallel interactive credential-prompt path.
	if cfg.InfraProvider == "proxmox" {
		// --- Ensure clusterctl credentials ---
		if !cfg.ClusterctlCfgFilePresent() && !cfg.HaveClusterctlCredsInEnv() {
			logx.Warn("Proxmox clusterctl API identity is not in the environment and no explicit local CLUSTERCTL_CFG is set.")
			if promptx.Confirm("Enter Proxmox API values interactively now?") {
				fmt.Fprint(os.Stderr, "\033[1;36m[?]\033[0m Proxmox VE URL (e.g. https://pve.example:8006): ")
				cfg.Providers.Proxmox.URL = promptx.ReadLine()
				fmt.Fprint(os.Stderr, "\033[1;36m[?]\033[0m Proxmox API TokenID (e.g. capmox@pve!capi): ")
				cfg.Providers.Proxmox.CAPIToken = promptx.ReadLine()
				fmt.Fprint(os.Stderr, "\033[1;36m[?]\033[0m Proxmox API Token secret (UUID): ")
				cfg.Providers.Proxmox.CAPISecret = promptx.ReadLine()
				_ = kindsync.SyncBootstrapConfigToKind(cfg)
				logx.Log("Proxmox API identity updated in kind when the management cluster is reachable. clusterctl on disk is not used by default (temp file for CLI only).")
			} else {
				logx.Warn("Skipping interactive creation. Set PROXMOX_URL, PROXMOX_CAPI_TOKEN, and PROXMOX_CAPI_SECRET, or add them to %s on kind, or set CLUSTERCTL_CFG to a local YAML you maintain.",
					cfg.Providers.Proxmox.BootstrapSecretNamespace)
				logx.Warn("Expected format:")
				fmt.Fprintln(os.Stderr)
				fmt.Fprintln(os.Stderr, `  PROXMOX_URL: "https://pve.example:8006"`)
				fmt.Fprintln(os.Stderr, `  PROXMOX_TOKEN: "capmox@pve!capi"`)
				fmt.Fprintln(os.Stderr, `  PROXMOX_SECRET: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"`)
				fmt.Fprintln(os.Stderr)
				fmt.Fprint(os.Stderr, "\033[1;33m[?]\033[0m Press ENTER once you have set env vars or kind Secrets, or a CLUSTERCTL_CFG file...")
				_ = promptx.ReadLine()
				kindsync.MergeBootstrapSecretsFromKind(cfg)
				if !cfg.ClusterctlCfgFilePresent() && !cfg.HaveClusterctlCredsInEnv() {
					logx.Die("Proxmox API identity still unset: not in kind Secrets, not in the environment, and no usable CLUSTERCTL_CFG. Aborting.")
				}
				logx.Log("Continuing with Proxmox credentials from env, kind, or explicit CLUSTERCTL_CFG file.")
			}
		}

		// Fill URL/Token/Secret from an explicit local clusterctl file if not
		// already populated from kind.
		if !cfg.Providers.Proxmox.BootstrapKindSecretUsed && !cfg.Providers.Proxmox.KindCAPMOXActive && cfg.ClusterctlCfgFilePresent() {
			if cfg.Providers.Proxmox.URL == "" {
				cfg.Providers.Proxmox.URL = yamlx.GetValue(cfg.ClusterctlCfg, "PROXMOX_URL")
			}
			if cfg.Providers.Proxmox.CAPIToken == "" {
				cfg.Providers.Proxmox.CAPIToken = yamlx.GetValue(cfg.ClusterctlCfg, "PROXMOX_TOKEN")
			}
			if cfg.Providers.Proxmox.CAPISecret == "" {
				cfg.Providers.Proxmox.CAPISecret = yamlx.GetValue(cfg.ClusterctlCfg, "PROXMOX_SECRET")
			}
		}
		cfg.Providers.Proxmox.CAPISecret = api.NormalizeTokenSecret(cfg.Providers.Proxmox.CAPISecret, cfg.Providers.Proxmox.CAPIToken)
		api.ValidateTokenSecret("PROXMOX_CAPI_SECRET", cfg.Providers.Proxmox.CAPISecret)
		api.RefreshDerivedIdentityTokenIDs(cfg)
		if cfg.Providers.Proxmox.TemplateID == "" {
			cfg.Providers.Proxmox.TemplateID = "104"
		}
		if cfg.AllowedNodes == "" {
			cfg.AllowedNodes = cfg.Providers.Proxmox.Node
		}

		var missingCreds []string
		if cfg.Providers.Proxmox.URL == "" {
			missingCreds = append(missingCreds, "PROXMOX_URL")
		}
		if cfg.Providers.Proxmox.CAPIToken == "" {
			missingCreds = append(missingCreds, "PROXMOX_CAPI_TOKEN")
		}
		if cfg.Providers.Proxmox.CAPISecret == "" {
			missingCreds = append(missingCreds, "PROXMOX_CAPI_SECRET")
		}
		if len(missingCreds) > 0 {
			logx.Warn("Missing Proxmox configuration: %v", missingCreds)
			logx.Die("Configure Proxmox credentials before running this script.")
		}

		// Test Proxmox API connectivity with the clusterctl token.
		logx.Log("Testing Proxmox API connectivity at %s (clusterctl token)...", api.HostBaseURL(cfg))
		if err := api.ResolveRegionAndNodeFromClusterctlAPI(cfg); err != nil {
			logx.Die("Proxmox API connectivity check failed: %v. Verify PROXMOX_URL, PROXMOX_CAPI_TOKEN, and PROXMOX_CAPI_SECRET.", err)
		}
		logx.Log("Proxmox API reachable.")

		clusterctlCfgPath = SyncClusterctlConfigFile(cfg)
	} else {
		ensureNonProxmoxClusterctlCredentials(cfg)
		clusterctlCfgPath = SyncClusterctlConfigFile(cfg)
	}
	// Publish the resolved path so pivot.EnsureManagementCluster (and
	// any other later callers) can read it from cfg rather than having
	// it only in the local variable.
	if clusterctlCfgPath != "" {
		cfg.ClusterctlCfg = clusterctlCfgPath
	}

	// --- Check for existing kind clusters ---
	ctx, phKind := obs.StartPhase(ctx, "kind-cluster",
		obs.Str("cluster", cfg.KindClusterName),
	)
	kindClusterReused := false
	{
		out, _, _ := shell.Capture("kind", "get", "clusters")
		kindExists := false
		for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
			if strings.TrimSpace(ln) == cfg.KindClusterName {
				kindExists = true
				break
			}
		}
		if kindExists {
			if cfg.Force && !cfg.NoDeleteKind {
				logx.Log("Force mode: replacing kind cluster '%s'...", cfg.KindClusterName)
				DeleteWorkloadClusterBeforeKindDeletion(cfg)
				if err := shell.Run("kind", "delete", "cluster", "--name", cfg.KindClusterName); err != nil {
					logx.Die("Failed to delete kind cluster '%s'.", cfg.KindClusterName)
				}
				logx.Log("Cluster deleted.")
				PurgeStaleHostNetworking()
			} else {
				if cfg.Force && cfg.NoDeleteKind {
					logx.Warn("NO_DELETE_KIND is set — keeping existing kind cluster despite --force.")
				}
				logx.Log("Reusing existing kind cluster '%s' (use --force to destroy and recreate; --no-delete-kind prevents deletion).", cfg.KindClusterName)
				kindClusterReused = true
			}
		} else {
			logx.Log("No existing cluster found; purging any leftover networking state before fresh orchestrator.")
			PurgeStaleHostNetworking()
		}
	}

	// --- Resolve CAPMOX image tag (Proxmox only) ---
	// TODO(#71): move to Provider.ResolveImages once that interface method exists.
	var capmoxImage, capmoxTag string
	if cfg.InfraProvider == "proxmox" {
		logx.Log("Resolving CAPMOX image tag...")
		capmoxTag = cfg.CAPMOXVersion
		if capmoxTag != "" {
			logx.Log("Using pinned CAPMOX version: %s", capmoxTag)
		} else {
			logx.Log("Cloning %s to determine latest stable tag...", cfg.CAPMOXRepo)
			_ = os.RemoveAll(cfg.CAPMOXBuildDir)
			if err := shell.Run("git", "clone", "--filter=blob:none", cfg.CAPMOXRepo, cfg.CAPMOXBuildDir); err != nil {
				logx.Die("Failed to clone CAPMOX repo: %v", err)
			}
			out, _, _ := shell.CaptureIn(cfg.CAPMOXBuildDir, "git", "tag", "--list", "v*")
			stableRE := regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
			for _, ln := range strings.Split(out, "\n") {
				ln = strings.TrimSpace(ln)
				if stableRE.MatchString(ln) {
					capmoxTag = ln // last stable tag after sort -V ordering
				}
			}
			if capmoxTag == "" {
				logx.Die("Could not determine a stable release tag from %s.", cfg.CAPMOXRepo)
			}
			logx.Log("Latest stable tag detected: %s", capmoxTag)
		}
		capmoxImage = cfg.CAPMOXImageRepo + ":" + capmoxTag
	}

	// --- Create kind cluster ---
	if kindClusterReused {
		logx.Log("Skipping kind cluster creation; reusing existing cluster '%s'.", cfg.KindClusterName)
	} else {
		logx.Log("Creating kind cluster using %s...", cfg.KindConfig)
		if err := shell.Run("kind", "create", "cluster", "--name", cfg.KindClusterName, "--config", cfg.KindConfig); err != nil {
			logx.Die("Failed to create kind cluster '%s'.", cfg.KindClusterName)
		}
	}
	// Merge kubeconfig so kubectl --context works even after a reuse.
	{
		out, _, _ := shell.Capture("kind", "get", "clusters")
		kindExists := false
		for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
			if strings.TrimSpace(ln) == cfg.KindClusterName {
				kindExists = true
				break
			}
		}
		if kindExists && (kindClusterReused || !contextExists(cfg.KindClusterName)) {
			logx.Log("Merging kubeconfig for kind cluster '%s' (context kind-%s)...", cfg.KindClusterName, cfg.KindClusterName)
			if err := shell.Run("kind", "export", "kubeconfig", "--name", cfg.KindClusterName); err != nil {
				logx.Die("The kind cluster '%s' exists, but 'kind export kubeconfig' failed. Fix container runtime / kind, set KUBECONFIG, or run: kind export kubeconfig --name %s",
					cfg.KindClusterName, cfg.KindClusterName)
			}
		}
	}

	phKind.End()

	// Load arm64 images or build them when the registry doesn't have arm64.
	if kindClusterReused {
		logx.Log("Reusing existing kind cluster — skipping arm64 image checks and kind-load (images already present from previous bootstrap).")
	} else {
		logx.Log("Checking arm64 availability for all provider/CAPI images and loading into kind...")
		ipamTag := ""
		if i := strings.LastIndex(cfg.IPAMImage, ":"); i >= 0 {
			ipamTag = cfg.IPAMImage[i+1:]
		}
		// TODO(#71): move to Provider.LoadImages once that interface method exists.
		if cfg.InfraProvider == "proxmox" && capmoxImage != "" {
			_ = installer.BuildIfNoArm64(cfg, capmoxImage, cfg.CAPMOXRepo, capmoxTag, cfg.CAPMOXBuildDir, cfg.KindClusterName)
		}
		_ = installer.BuildIfNoArm64(cfg, cfg.CAPICoreImage, cfg.CAPICoreRepo, cfg.ClusterctlVersion, "./cluster-api", cfg.KindClusterName)
		_ = installer.BuildIfNoArm64(cfg, cfg.CAPIBootstrapImage, cfg.CAPICoreRepo, cfg.ClusterctlVersion, "./cluster-api", cfg.KindClusterName)
		_ = installer.BuildIfNoArm64(cfg, cfg.CAPIControlplaneImage, cfg.CAPICoreRepo, cfg.ClusterctlVersion, "./cluster-api", cfg.KindClusterName)
		_ = installer.BuildIfNoArm64(cfg, cfg.IPAMImage, cfg.IPAMRepo, ipamTag, "./cluster-api-ipam-provider-in-cluster", cfg.KindClusterName)
	}
	_ = kindsync.SyncBootstrapConfigToKind(cfg)

	// --- Management cluster CNI ---
	logx.Log("Using kind's default CNI (kindnet) on the management cluster; skipping Cilium install.")

	// --- Initialize Cluster API ---
	InstallMetricsServerOnKindManagement(cfg)

	ctx, phClusterctl := obs.StartPhase(ctx, "clusterctl-init",
		obs.Str("provider", cfg.InfraProvider),
		obs.Str("ipam", cfg.IPAMProvider),
	)
	clusterctlCfgPath = SyncClusterctlConfigFile(cfg)
	initArgs := []string{"clusterctl", "init"}
	if clusterctlCfgPath != "" {
		initArgs = append(initArgs, "--config", clusterctlCfgPath)
	}
	initArgs = append(initArgs,
		"--infrastructure", cfg.InfraProvider,
		"--ipam", cfg.IPAMProvider,
		"--addon", "helm",
	)
	// --bootstrap-mode k3s swaps the kubeadm control-plane + bootstrap
	// providers for the K3s ones. kubeadm is the default and clusterctl
	// installs it implicitly when --control-plane / --bootstrap are
	// omitted.
	if cfg.BootstrapMode == "k3s" {
		logx.Log("BOOTSTRAP_MODE=k3s — initializing CAPI with K3s control-plane + bootstrap providers (instead of kubeadm)")
		initArgs = append(initArgs, "--control-plane", "k3s", "--bootstrap", "k3s")
	}
	// Wire cfg.ImageRegistryMirror (§17): when non-empty, rewrite any
	// public-registry image references that follow --core / --bootstrap
	// / --control-plane / --infrastructure so the airgapped mgmt cluster
	// pulls CAPI provider images from the operator's internal mirror.
	// No-op when the mirror is unset (the common, non-airgapped path).
	if cfg.ImageRegistryMirror != "" {
		logx.Log("Image registry mirror set (%s) — rewriting clusterctl init image references.", cfg.ImageRegistryMirror)
		initArgs = applyImageMirror(initArgs, cfg.ImageRegistryMirror)
	}
	if err := shell.RunWithEnv(
		[]string{
			"EXP_CLUSTER_RESOURCE_SET=false",
			"CLUSTER_TOPOLOGY=" + cfg.ClusterTopology,
			"EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION=" + cfg.ExpKubeadmBootstrapFormatIgnition,
		},
		initArgs...,
	); err != nil {
		logx.Die("clusterctl init failed: %v", err)
	}

	// Wait for CAAPH add-on provider.
	logx.Log("Waiting for CAAPH (add-on provider Helm) to become ready, if present...")
	mgmtCli, mgmtErr := k8sclient.ForCurrent()
	if mgmtErr != nil {
		logx.Die("kube client for current context: %v", mgmtErr)
	}
	if dl, err := mgmtCli.Typed.AppsV1().Deployments("caaph-system").List(ctx, metav1.ListOptions{}); err == nil && dl != nil {
		for _, d := range dl.Items {
			_ = waitDeploymentReady(mgmtCli, "caaph-system", d.Name, 300*time.Second)
		}
	} else {
		logx.Warn("caaph-system not found after clusterctl init --addon helm — verify CAAPH; HelmChartProxy may fail without it.")
	}

	// Wait for core CAPI controllers.
	logx.Log("Waiting for core CAPI controllers to become ready...")
	for _, d := range []struct{ ns, name string }{
		{"capi-system", "capi-controller-manager"},
		{"capi-kubeadm-bootstrap-system", "capi-kubeadm-bootstrap-controller-manager"},
		{"capi-kubeadm-control-plane-system", "capi-kubeadm-control-plane-controller-manager"},
	} {
		if err := waitDeploymentReady(mgmtCli, d.ns, d.name, 300*time.Second); err != nil {
			logx.Die("%s did not become Available: %v", d.name, err)
		}
	}

	// Wait for webhook service endpoints.
	logx.Log("Waiting for CAPI webhook service endpoints...")
	kubectl.WaitForServiceEndpoint("capi-kubeadm-bootstrap-system", "capi-kubeadm-bootstrap-webhook-service", 300*time.Second)
	kubectl.WaitForServiceEndpoint("capi-kubeadm-control-plane-system", "capi-kubeadm-control-plane-webhook-service", 300*time.Second)

	waitInfraProviderAfterClusterctlInit(cfg, mgmtCli)
	phClusterctl.End()

	// --- Capacity check: confirm the planned clusters fit inside
	// cfg.Capacity.ResourceBudgetFraction (default 0.75) of the available
	// Proxmox host CPU/memory/storage. Aborts before any VM is created.
	ctx, phCapacity := obs.StartPhase(ctx, "capacity-preflight")
	if err := preflightCapacity(cfg); err != nil {
		logx.Die("%v", err)
	}
	phCapacity.End()

	// --- Pre-create provider groups (Proxmox pools / vSphere folders /
	// etc.) before any workload VMs are created. EnsureGroup is
	// idempotent; ErrNotApplicable is silently ignored (providers that
	// have no grouping concept skip cleanly via MinStub).
	ctx, phGroup := obs.StartPhase(ctx, "group-ensure",
		obs.Str("provider", cfg.InfraProvider),
	)
	if groupProv, gpErr := provider.For(cfg); gpErr == nil {
		if workloadGroup := cfg.WorkloadGroupName(); workloadGroup != "" {
			if err := groupProv.EnsureGroup(cfg, workloadGroup); err != nil && !errors.Is(err, provider.ErrNotApplicable) {
				logx.Warn("EnsureGroup(%s workload): %v — VMs may fail to register; create it manually if needed.", workloadGroup, err)
			} else if err == nil {
				logx.Log("Provider group '%s' ensured (workload).", workloadGroup)
			}
		}
		if cfg.Pivot.Enabled {
			if mgmtGroup := cfg.MgmtGroupName(); mgmtGroup != "" {
				if err := groupProv.EnsureGroup(cfg, mgmtGroup); err != nil && !errors.Is(err, provider.ErrNotApplicable) {
					logx.Warn("EnsureGroup(%s mgmt): %v — mgmt VMs may fail to register; create it manually if needed.", mgmtGroup, err)
				} else if err == nil {
					logx.Log("Provider group '%s' ensured (management).", mgmtGroup)
				}
			}
		}
	}
	phGroup.End()

	// --- Pivot ---
	// CAPI bootstrap-and-pivot pattern. With PivotEnabled (the
	// default): kind provisions a single-node management cluster on
	// the active provider, clusterctl init runs against it,
	// clusterctl move migrates CAPI inventory from kind to mgmt, the
	// yage-system Secrets are mirrored, the kind context is rebound
	// to point at the mgmt cluster, and downstream phases run
	// against mgmt. The kind cluster is torn down at the end (unless
	// --pivot-keep-kind / --no-delete-kind).
	//
	// When the active provider does not implement PivotTarget,
	// pivot is silently downgraded to "kind stays as the management
	// plane" so PivotEnabled-by-default does not break runs against
	// providers without a pivot path.
	if cfg.Pivot.Enabled {
		if pt, perr := provider.For(cfg); perr == nil {
			if _, terr := pt.PivotTarget(cfg); errors.Is(terr, provider.ErrNotApplicable) {
				logx.Log("pivot: %s does not yet implement PivotTarget — keeping kind as the management plane.", cfg.InfraProvider)
				cfg.Pivot.Enabled = false
			}
		}
	}
	ctx, phPivot := obs.StartPhase(ctx, "pivot",
		obs.Str("provider", cfg.InfraProvider),
		obs.Bool("enabled", cfg.Pivot.Enabled),
	)
	if cfg.Pivot.Enabled {
		mgmtKubeconfig, err := pivot.EnsureManagementCluster(cfg)
		if err != nil {
			logx.Die("pivot: EnsureManagementCluster: %v", err)
		}
		// Thread the kubeconfig path through cfg so Provider.PivotTarget
		// can return it. Provider is stateless; orchestrator publishes
		// the path here.
		cfg.MgmtKubeconfigPath = mgmtKubeconfig
		if err := pivot.InstallCAPIOnManagement(cfg, mgmtKubeconfig); err != nil {
			logx.Die("pivot: InstallCAPIOnManagement: %v", err)
		}
		if err := pivot.MoveCAPIState(cfg, mgmtKubeconfig); err != nil {
			logx.Die("pivot: MoveCAPIState: %v", err)
		}
		if cfg.Pivot.DryRun {
			logx.Log("pivot: dry-run complete. Inspect the management cluster:")
			logx.Log("  KUBECONFIG=%s kubectl get nodes", mgmtKubeconfig)
			logx.Log("  KUBECONFIG=%s kubectl -n capi-system get pods", mgmtKubeconfig)
			logx.Log("Re-run without --pivot-dry-run to perform the real move + handoff + kind teardown.")
			logx.Log("(kind cluster '%s' has been left intact; workload is still managed by it.)", cfg.KindClusterName)
			phPivot.End()
			return 0
		}
		copied, err := kindsync.HandOffBootstrapSecretsToManagement(cfg, "kind-"+cfg.KindClusterName, mgmtKubeconfig)
		if err != nil {
			logx.Warn("pivot: handoff Secrets returned error after %d copies: %v", copied, err)
		} else {
			logx.Log("pivot: handoff complete (%d Secrets copied to mgmt cluster).", copied)
		}
		if err := pivot.VerifyParity(cfg, mgmtKubeconfig); err != nil {
			logx.Die("pivot: VerifyParity: %v", err)
		}
		if err := rebindKindContextToMgmt(cfg, mgmtKubeconfig); err != nil {
			logx.Die("pivot: rebind kind-%s context to mgmt kubeconfig: %v", cfg.KindClusterName, err)
		}
		logx.Log("pivot: complete; subsequent phases will target the management cluster.")
	}
	phPivot.End()

	// --- --stop-before-workload escape hatch ---
	// Integration tests exit here: mgmt cluster is up on the provider,
	// kind is torn down, no workload churn. Re-run without the flag
	// (or send the same yage --print-command output) to provision the
	// workload.
	if cfg.Pivot.StopBeforeWorkload {
		if cfg.Pivot.Enabled {
			logx.Log("--stop-before-workload: mgmt cluster is up, no workload manifest applied. Done.")
		} else {
			logx.Log("--stop-before-workload: kind is the management plane (no pivot ran), no workload manifest applied. Done.")
		}
		return 0
	}

	// --- Apply workload cluster manifest ---
	ctx, phManifest := obs.StartPhase(ctx, "workload-manifest-apply",
		obs.Str("cluster", cfg.WorkloadClusterName),
	)
	MaybeInteractiveSelectWorkloadCluster(cfg)
	capimanifest.TryFillWorkloadInputsFromManagement(cfg)
	// Re-apply proxmox-bootstrap-config so snapshot keys beat live backfill.
	kindsync.MergeBootstrapSecretsFromKind(cfg)
	if cfg.Providers.Proxmox.TemplateID == "" {
		cfg.Providers.Proxmox.TemplateID = "104"
	}
	TryLoadCAPIManifestFromSecret(cfg)
	clusterctlCfgPath = SyncClusterctlConfigFile(cfg)
	capimanifest.GenerateWorkloadManifestIfMissing(cfg,
		func() bool { return WorkloadClusterctlIsStale(cfg) },
		func() string { return clusterctlCfgPath },
		func() { _ = kindsync.SyncBootstrapConfigToKind(cfg) },
	)
	_ = capimanifest.PatchProxmoxCSITopologyLabels(cfg)
	_ = capimanifest.PatchKubeadmSkipKubeProxyForCilium(cfg)
	_, _ = capimanifest.PatchProxmoxMachineTemplateSpecRevisions(cfg)
	capimanifest.DiscoverWorkloadClusterIdentity(cfg, cfg.CAPIManifest)
	_ = capimanifest.EnsureWorkloadClusterLabel(cfg, cfg.CAPIManifest, cfg.WorkloadClusterName)
	api.RefreshDerivedCiliumClusterID(cfg)
	_ = caaph.PatchClusterCAAPHHelmLabels(cfg, cfg.CAPIManifest)
	PushCAPIManifestToSecret(cfg)

	if cfg.BootstrapCAPIUseSecret {
		logx.Log("Applying workload manifest to management cluster (ephemeral file in this run; last pushed to Secret %s/%s)...",
			cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName)
	} else {
		logx.Log("Applying CAPI manifest %s...", cfg.CAPIManifest)
	}
	kubectl.WarnRegeneratedManifestImmutableRisk(cfg)
	for attempt := 1; attempt <= 3; attempt++ {
		if err := kubectl.ApplyWorkloadManifestToManagementCluster(cfg, cfg.CAPIManifest); err == nil {
			break
		}
		if attempt == 3 {
			logx.Die("Failed to apply %s after %d attempts.", cfg.CAPIManifest, attempt)
		}
		logx.Warn("Apply failed (attempt %d/3). Retrying in 10s while webhooks settle...", attempt)
		time.Sleep(10 * time.Second)
	}

	phManifest.End()

	ctx, phReadiness := obs.StartPhase(ctx, "workload-readiness",
		obs.Str("cluster", cfg.WorkloadClusterName),
	)
	opentofux.RecreateIdentitiesWorkloadCSISecrets(cfg)
	caaph.ApplyWorkloadCiliumHelmChartProxy(cfg)
	WaitForWorkloadClusterReady(cfg)
	caaph.ApplyWorkloadCiliumLBBToWorkload(cfg, func() (string, error) {
		return writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
	})

	// Metrics-server on workload.
	if cfg.EnableWorkloadMetricsServer {
		if cfg.ArgoCD.Enabled && cfg.ArgoCD.WorkloadEnabled {
			logx.Log("workload metrics-server: deploy with your app-of-apps repo (%s); ENABLE_WORKLOAD_METRICS_SERVER is informational when Argo delivers apps from Git.", cfg.ArgoCD.AppOfAppsGitURL)
		} else {
			InstallMetricsServerOnWorkload(cfg)
		}
	}

	// CSI driver Secrets on the workload.
	// LoadVarsFromConfig fills credential fields from the on-disk YAML
	// before Selector picks up the driver list; the fallback URL comes
	// from the Proxmox API endpoint when CSIURL is not set explicitly.
	// EnsureSecret owns kubeconfig-file cleanup via defer, so no
	// os.Remove calls are needed here.  ErrNotApplicable (disabled or
	// missing creds) is a silent skip; any other error is a warning.
	if cfg.ArgoCD.Enabled && cfg.ArgoCD.WorkloadEnabled {
		proxmoxcsi.LoadVarsFromConfig(cfg)
		if cfg.Providers.Proxmox.CSIURL == "" {
			cfg.Providers.Proxmox.CSIURL = api.APIJSONURL(cfg)
		}
		for _, d := range csi.Selector(cfg) {
			wk, err := writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
			if err != nil {
				logx.Warn("CSI driver %s: cannot get workload kubeconfig: %v", d.Name(), err)
				continue
			}
			if err := d.EnsureSecret(cfg, wk); err != nil {
				// applyConfigSecretToWorkload owns cleanup via defer when it
				// runs; for early returns (ErrNotApplicable, kubeconfig error)
				// we must clean up the temp file ourselves.
				os.Remove(wk)
				if !errors.Is(err, csi.ErrNotApplicable) {
					logx.Warn("CSI driver %s: EnsureSecret: %v", d.Name(), err)
				}
			}
		}
	}

	// Pre-install argocd-redis Secret on the workload.
	if cfg.ArgoCD.Enabled && cfg.ArgoCD.WorkloadEnabled {
		argocd.ApplyRedisSecretToWorkload(cfg, func() (string, error) {
			return writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
		})
	}
	phReadiness.End()

	// --- Argo CD on workload ---
	ctx, phArgo := obs.StartPhase(ctx, "argocd-bootstrap",
		obs.Bool("enabled", cfg.ArgoCD.Enabled),
		obs.Bool("workload", cfg.ArgoCD.WorkloadEnabled),
	)
	if cfg.ArgoCD.Enabled {
		logx.Log("Argo CD on the workload: Argo CD Operator + ArgoCD CR, then CAAPH argocd-apps (root Application name %s; see --workload-app-of-apps-git-*).", cfg.WorkloadClusterName)
		if cfg.ArgoCD.WorkloadEnabled {
			caaph.ApplyWorkloadArgoHelmProxies(cfg, func() {
				caaph.ApplyWorkloadArgoCDOperatorAndCR(cfg, func() (string, error) {
					return writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
				})
			})
			caaph.WaitWorkloadArgoCDServer(cfg, func() (string, error) {
				return writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
			})
			caaph.LogWorkloadArgoAppsStatus(cfg, func() (string, error) {
				return writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
			})
		}
	} else {
		logx.Warn("Argo CD disabled (--disable-argocd) — skipping CAAPH workload Argo and app-of-apps.")
	}
	phArgo.End()

	// --- Pivot teardown: delete the kind cluster after a successful pivot
	// (skipped when --pivot-keep-kind / --no-delete-kind / pivot disabled).
	if cfg.Pivot.Enabled {
		if err := pivot.TeardownKind(cfg); err != nil {
			logx.Warn("pivot: TeardownKind: %v", err)
		}
	}

	// Promote the bootstrap-config Secret from draft → realized now that
	// the full bootstrap (CAPI + Argo CD) has completed successfully.
	kindsync.PromoteBootstrapConfigToRealized(cfg)
	logx.Log("Done. CAPI: 'kubectl get clusters -A' and 'clusterctl describe cluster <name>'. For workload apps, rely on Argo CD sync (this script does not wait for all add-ons to be Healthy).")
	return 0
}

// validateMinVCPU enforces the CAPI/kubeadm + k3s minimum of 2 vCPUs
// per VM (sockets × cores) on every role that has at least one
// replica. Mgmt-worker is skipped when Mgmt.WorkerMachineCount is 0
// (the default — mgmt is single-node CP-only).
func validateMinVCPU(cfg *config.Config) error {
	const minVCPU = 2
	atoi := func(s string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(s))
		if n <= 0 {
			n = 1
		}
		return n
	}
	type role struct {
		name             string
		sockets, cores   string
		replicasNonZero  bool
	}
	roles := []role{
		{"workload control-plane", cfg.Providers.Proxmox.ControlPlaneNumSockets, cfg.Providers.Proxmox.ControlPlaneNumCores, atoi(cfg.ControlPlaneMachineCount) > 0},
		{"workload worker", cfg.Providers.Proxmox.WorkerNumSockets, cfg.Providers.Proxmox.WorkerNumCores, atoi(cfg.WorkerMachineCount) > 0},
	}
	if cfg.Pivot.Enabled {
		roles = append(roles,
			role{"mgmt control-plane", cfg.Providers.Proxmox.Mgmt.ControlPlaneNumSockets, cfg.Providers.Proxmox.Mgmt.ControlPlaneNumCores, atoi(cfg.Mgmt.ControlPlaneMachineCount) > 0},
			role{"mgmt worker", cfg.Providers.Proxmox.WorkerNumSockets, cfg.Providers.Proxmox.WorkerNumCores, atoi(cfg.Mgmt.WorkerMachineCount) > 0},
		)
	}
	var bad []string
	for _, r := range roles {
		if !r.replicasNonZero {
			continue
		}
		v := atoi(r.sockets) * atoi(r.cores)
		if v < minVCPU {
			bad = append(bad, fmt.Sprintf("%s: %s sockets × %s cores = %d vCPU (need ≥ %d — CAPI/kubeadm + k3s require at least 2 vCPUs per node)",
				r.name, r.sockets, r.cores, v, minVCPU))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("vCPU minimum not met:\n  %s", strings.Join(bad, "\n  "))
	}
	return nil
}

// preflightCapacity queries the active provider's Inventory and
// compares it against the configured cluster sizing × replicas.
// Returns nil when the plan fits inside cfg.Capacity.ResourceBudgetFraction
// × capacity. Returns a non-nil error otherwise — unless
// cfg.Capacity.AllowOvercommit is set, in which case the error is
// downgraded to a warning and nil is returned.
//
// Capacity acquisition lives behind the Provider interface
// (provider.For(cfg).Inventory). Providers that don't model
// capacity as flat Total/Used/Available (AWS, Azure, GCP, Hetzner,
// vSphere) return ErrNotApplicable and the preflight is skipped
// silently per §13.4 #1.
func preflightCapacity(cfg *config.Config) error {
	prov, err := provider.For(cfg)
	if err != nil {
		logx.Warn("capacity check skipped: provider lookup failed: %v", err)
		return nil
	}
	inv, err := prov.Inventory(cfg)
	if errors.Is(err, provider.ErrNotApplicable) {
		// Provider's quota model doesn't fit flat
		// Total/Used/Available — skip silently.
		return nil
	}
	if err != nil {
		// Don't block the run on a capacity-query failure — log and
		// proceed. The user can still hit a real cap on the API server.
		logx.Warn("capacity check skipped: %v", err)
		return nil
	}
	hc := hostCapacityFromInventory(inv)
	used := existingUsageFromInventory(inv)

	plan := capacity.PlanFor(cfg)
	threshold := cfg.Capacity.ResourceBudgetFraction
	if threshold <= 0 || threshold > 1 {
		threshold = capacity.DefaultThreshold
	}
	logx.Log("Capacity preflight (%.0f%% budget): nodes=%v, plan %d cores / %d MiB / %d GB vs host %d cores / %d MiB / %d GB",
		threshold*100, hc.Nodes,
		plan.CPUCores, plan.MemoryMiB, plan.StorageGB,
		hc.CPUCores, hc.MemoryMiB, hc.StorageGB)
	if hc.IsSmallEnv() && cfg.BootstrapMode != "k3s" {
		logx.Warn("Proxmox host is small (%d cores, %d MiB) — full kubeadm CAPI may be tight. Consider --bootstrap-mode k3s for a 1 vCPU / 1 GiB-per-node footprint.",
			hc.CPUCores, hc.MemoryMiB)
	}
	// Existing-VM awareness: provider.Inventory.Used carries the
	// structured aggregate; the human-readable census line is
	// already in inv.Notes (provider-defined). Surface both.
	if used.CPUCores > 0 || used.MemoryMiB > 0 || used.StorageGB > 0 {
		logx.Log("Existing-VM census: %d cores / %d MiB / %d GB already allocated",
			used.CPUCores, used.MemoryMiB, used.StorageGB)
	}
	tolerancePct := cfg.Capacity.OvercommitTolerancePct
	if tolerancePct <= 0 {
		tolerancePct = capacity.DefaultOvercommitTolerancePct
	}
	verdict, msg := capacity.CheckCombined(plan, hc, used, threshold, tolerancePct)
	switch verdict {
	case capacity.VerdictFits:
		// Silent — fits within soft budget.
		return nil
	case capacity.VerdictTight:
		logx.Warn("Capacity tight (above %.0f%% soft budget but inside %.0f%% overcommit tolerance): %s",
			threshold*100, tolerancePct, msg)
		return nil
	case capacity.VerdictAbort:
		if cfg.Capacity.AllowOvercommit {
			logx.Warn("ALLOW_RESOURCE_OVERCOMMIT=true — proceeding despite overcommit: %s", msg)
			return nil
		}
		// Suggest --bootstrap-mode k3s when (a) the user is running
		// kubeadm and (b) the same machine counts under k3s sizing
		// would fit the budget. Without (b) the suggestion is noise.
		hint := ""
		if cfg.BootstrapMode != "k3s" {
			if fits, k3sPlan := capacity.WouldFitAsK3s(cfg, hc, threshold); fits {
				hint = fmt.Sprintf(
					"\n\n  💡 The same %d machine(s) would fit under --bootstrap-mode k3s:\n"+
						"     k3s plan: %d cores / %d MiB / %d GB (vs kubeadm: %d cores / %d MiB / %d GB).\n"+
						"     k3s control plane / worker default to ~%d vCPU + %d MiB each.",
					totalReplicas(plan), k3sPlan.CPUCores, k3sPlan.MemoryMiB, k3sPlan.StorageGB,
					plan.CPUCores, plan.MemoryMiB, plan.StorageGB,
					capacity.K3sCPCores, capacity.K3sCPMemMiB)
			}
		}
		return fmt.Errorf("capacity preflight: %s\n  re-run with --allow-resource-overcommit to override, raise --overcommit-tolerance-pct, or shrink the plan.%s", msg, hint)
	}
	return nil
}

// hostCapacityFromInventory translates a provider.Inventory into the
// capacity.HostCapacity shape that capacity.CheckCombined /
// capacity.WouldFitAsK3s consume. Lives here rather than in the
// capacity package so capacity stays free of the provider import.
func hostCapacityFromInventory(inv *provider.Inventory) *capacity.HostCapacity {
	return &capacity.HostCapacity{
		CPUCores:  inv.Total.Cores,
		MemoryMiB: inv.Total.MemoryMiB,
		StorageGB: inv.Total.StorageGiB,
		StorageBy: inv.Total.StorageByClass,
		Nodes:     append([]string(nil), inv.Hosts...),
	}
}

// existingUsageFromInventory translates inv.Used into the
// capacity.ExistingUsage shape. VMCount and ByPool aren't carried
// in the Inventory's structured fields (they're Proxmox-display-
// only); CheckCombined doesn't read them so this is fine.
func existingUsageFromInventory(inv *provider.Inventory) *capacity.ExistingUsage {
	return &capacity.ExistingUsage{
		CPUCores:  inv.Used.Cores,
		MemoryMiB: inv.Used.MemoryMiB,
		StorageGB: inv.Used.StorageGiB,
	}
}

func totalReplicas(p capacity.Plan) int {
	n := 0
	for _, it := range p.Items {
		n += it.Replicas
	}
	return n
}

// ensureNonProxmoxClusterctlCredentials checks that clusterctl can
// authenticate to the selected cloud when not using the Proxmox +
// OpenTofu identity path.
func ensureNonProxmoxClusterctlCredentials(cfg *config.Config) {
	if cfg.ClusterctlCfgFilePresent() {
		return
	}
	switch cfg.InfraProvider {
	case "aws":
		if !cfg.HaveAWSCloudCreds() {
			logx.Die("AWS infrastructure selected but AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are not both set in the environment (CAPA / clusterctl need them). Set CLUSTERCTL_CFG if you use a different credential flow.")
		}
		logx.Log("AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY are set — skipping Proxmox OpenTofu identity bootstrap.")
	default:
		logx.Warn("Skipping Proxmox OpenTofu identity bootstrap for infrastructure provider %q — ensure the environment (or CLUSTERCTL_CFG) supplies the credentials clusterctl expects.", cfg.InfraProvider)
	}
}

// waitInfraProviderAfterClusterctlInit waits for the infra-provider
// controllers + webhooks that must be healthy before workload
// manifest generation (CAPMOX for Proxmox, CAPA for AWS, …).
func waitInfraProviderAfterClusterctlInit(cfg *config.Config, mgmtCli *k8sclient.Client) {
	switch cfg.InfraProvider {
	case "proxmox":
		logx.Log("Waiting for Proxmox provider (capmox-controller-manager) to become ready...")
		if err := waitDeploymentReady(mgmtCli, "capmox-system", "capmox-controller-manager", 300*time.Second); err != nil {
			logx.Die("capmox-controller-manager did not become Available: %v", err)
		}
		logx.Log("Waiting for CAPMOX mutating webhook endpoint (ProxmoxCluster apply)...")
		kubectl.WaitForServiceEndpoint("capmox-system", "capmox-webhook-service", 300*time.Second)
		opentofux.RecreateResyncCapmox(cfg)
	case "aws":
		logx.Log("Waiting for AWS provider (capa-controller-manager) to become ready...")
		if err := waitDeploymentReady(mgmtCli, "capa-system", "capa-controller-manager", 300*time.Second); err != nil {
			logx.Die("capa-controller-manager did not become Available: %v", err)
		}
		logx.Log("Waiting for CAPA webhook endpoint...")
		kubectl.WaitForServiceEndpoint("capa-system", "capa-webhook-service", 300*time.Second)
	default:
		logx.Warn("No provider-specific CAPI controller wait is implemented for %q — continuing after core CAPI webhooks.", cfg.InfraProvider)
	}
}

// rebindKindContextToMgmt writes a fresh KUBECONFIG file containing the
// management cluster's connection details aliased under the existing
// "kind-<name>" context, then sets the KUBECONFIG env var so subsequent
// k8sclient.ForContext("kind-"+cfg.KindClusterName) and ForCurrent calls
// hit the new management cluster instead of kind. The original kind
// cluster's kubeconfig is unaffected (it's still written by `kind` into
// the user's ~/.kube/config); pivot cuts over by overriding KUBECONFIG.
func rebindKindContextToMgmt(cfg *config.Config, mgmtKubeconfigPath string) error {
	raw, err := os.ReadFile(mgmtKubeconfigPath)
	if err != nil {
		return fmt.Errorf("read mgmt kubeconfig %s: %w", mgmtKubeconfigPath, err)
	}
	cc, err := clientcmd.Load(raw)
	if err != nil {
		return fmt.Errorf("parse mgmt kubeconfig: %w", err)
	}
	if len(cc.Contexts) == 0 {
		return fmt.Errorf("mgmt kubeconfig has no contexts")
	}
	// Pick the first (and usually only) context as the source.
	var srcName string
	var srcCtx *clientcmdapi.Context
	for k, v := range cc.Contexts {
		srcName, srcCtx = k, v
		break
	}
	newName := "kind-" + cfg.KindClusterName
	if srcName != newName {
		delete(cc.Contexts, srcName)
		cc.Contexts[newName] = srcCtx
	}
	cc.CurrentContext = newName

	out, err := clientcmd.Write(*cc)
	if err != nil {
		return fmt.Errorf("serialize rebinded kubeconfig: %w", err)
	}
	f, err := os.CreateTemp("", "post-pivot-kubeconfig-*.yaml")
	if err != nil {
		return fmt.Errorf("mktemp post-pivot kubeconfig: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(out); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("write post-pivot kubeconfig: %w", err)
	}
	if err := os.Setenv("KUBECONFIG", f.Name()); err != nil {
		return fmt.Errorf("set KUBECONFIG env: %w", err)
	}
	logx.Log("post-pivot KUBECONFIG=%s (context kind-%s aliased to mgmt cluster)",
		f.Name(), cfg.KindClusterName)
	return nil
}

// workloadKubeconfigSecretExists is a typed-client probe replacing the old
// `kubectl get secret <name>-kubeconfig` shell-out.
func workloadKubeconfigSecretExists(cfg *config.Config) bool {
	cli, err := k8sclient.ForContext("kind-" + cfg.KindClusterName)
	if err != nil {
		return false
	}
	_, err = cli.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
		Get(context.Background(), cfg.WorkloadClusterName+"-kubeconfig", metav1.GetOptions{})
	return err == nil
}