// Package bootstrap is the orchestrator. The bash script's top-level flow
// (phases 1-10, roughly L7700-L8509) is the model we're porting piecewise.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/lpasquali/bootstrap-capi/internal/argocdx"
	"github.com/lpasquali/bootstrap-capi/internal/caaph"
	"github.com/lpasquali/bootstrap-capi/internal/capacity"
	"github.com/lpasquali/bootstrap-capi/internal/capimanifest"
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/csix"
	"github.com/lpasquali/bootstrap-capi/internal/installer"
	"github.com/lpasquali/bootstrap-capi/internal/k8sclient"
	"github.com/lpasquali/bootstrap-capi/internal/kind"
	"github.com/lpasquali/bootstrap-capi/internal/kindsync"
	"github.com/lpasquali/bootstrap-capi/internal/kubectlx"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/opentofux"
	"github.com/lpasquali/bootstrap-capi/internal/pivot"
	"github.com/lpasquali/bootstrap-capi/internal/promptx"
	"github.com/lpasquali/bootstrap-capi/internal/proxmox"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
	"github.com/lpasquali/bootstrap-capi/internal/yamlx"
)

// Run executes the top-level bootstrap flow. Returns an exit code.
func Run(cfg *config.Config) int {
	// Apply the KIND_CLUSTER_NAME default the bash main() does at L7713.
	if cfg.KindClusterName == "" {
		if cfg.ClusterName != "" {
			cfg.KindClusterName = cfg.ClusterName
		} else {
			cfg.KindClusterName = "capi-provisioner"
		}
	}
	// ALLOWED_NODES falls back to PROXMOX_NODE once parsing is done (L8266).
	if cfg.AllowedNodes == "" {
		cfg.AllowedNodes = cfg.ProxmoxNode
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
	// Standalone: kind backup / restore (bash L7746-L7760)
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
	// Standalone: --workload-rollout (bash L7860-L7941)
	// -------------------------------------------------------------------------
	if cfg.WorkloadRolloutStandalone {
		shell.RequireCmd("kubectl")
		kindsync.MergeProxmoxBootstrapSecretsFromKind(cfg)
		_ = kindsync.SyncBootstrapConfigToKind(cfg)
		_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)
		if ctx, ok := kubectlx.ResolveBootstrapContext(cfg); ok {
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
			_ = argocdx.StandaloneDiscoverWorkloadKubeconfigRef(cfg)
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
			kindsync.MergeProxmoxBootstrapSecretsFromKind(cfg)
			if cfg.ProxmoxTemplateID == "" {
				cfg.ProxmoxTemplateID = "104"
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
			kubectlx.WarnRegeneratedManifestImmutableRisk(cfg)
			for attempt := 1; attempt <= 3; attempt++ {
				if err := kubectlx.ApplyWorkloadManifestToManagementCluster(cfg, cfg.CAPIManifest); err == nil {
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
			if !cfg.ArgoCDEnabled {
				logx.Die("ARGOCD_ENABLED is false — cannot use argocd rollout. Use --workload-rollout capi, or set ARGOCD_ENABLED=true.")
			}
			if !cfg.WorkloadArgoCDEnabled {
				logx.Die("WORKLOAD_ARGOCD_ENABLED is false — no workload Argo.")
			}
			logx.Log("workload-rollout: CAAPH + app-of-apps Git — re-sync from the workload Argo CD (e.g. `argocd app sync %s` with workload kubeconfig, or refresh the root/child Applications in the UI).", cfg.WorkloadClusterName)
		}
		logx.Log("workload-rollout: done.")
		return 0
	}

	// -------------------------------------------------------------------------
	// Standalone: --argocd-print-access / --argocd-port-forward (L7943-L7968)
	// -------------------------------------------------------------------------
	if cfg.ArgoCDPrintAccessStandalone || cfg.ArgoCDPortForwardStandalone {
		shell.RequireCmd("kubectl")
		kindsync.MergeProxmoxBootstrapSecretsFromKind(cfg)
		_ = kindsync.SyncBootstrapConfigToKind(cfg)
		_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)
		if ctx, ok := kubectlx.ResolveBootstrapContext(cfg); ok {
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
			_ = argocdx.StandaloneDiscoverWorkloadKubeconfigRef(cfg)
		}
		if cfg.ArgoCDPrintAccessStandalone {
			argocdx.PrintAccessInfo(cfg)
		}
		if cfg.ArgoCDPortForwardStandalone {
			argocdx.RunPortForwards(cfg)
		}
		return 0
	}

	// -------------------------------------------------------------------------
	// Pre-phase: ensure CAPI manifest path (bash L7970)
	// -------------------------------------------------------------------------
	EnsureCAPIManifestPath(cfg)

	// --- Purge (bash L7972-L7979) ---
	if cfg.Purge {
		if !cfg.Force {
			if !promptx.Confirm("Purge generated files and Terraform state before continuing?") {
				logx.Die("Purge cancelled by user.")
			}
		}
		PurgeGeneratedArtifacts(cfg)
	}

	// --- CLUSTER_SET_ID + identity suffix (bash L7981-L8007) ---
	if cfg.RecreateProxmoxIdentities {
		logx.Log("Re-creation mode: identity parameters are resolved in Phase 2 (Terraform state or CAPI/CSI token IDs in kind / env).")
		if cfg.ClusterSetID != "" && cfg.ProxmoxIdentitySuffix == "" {
			cfg.ProxmoxIdentitySuffix = proxmox.DeriveIdentitySuffix(cfg.ClusterSetID)
		}
		if cfg.ClusterSetID != "" {
			proxmox.ValidateClusterSetIDFormat(cfg)
		}
		if cfg.ProxmoxIdentitySuffix != "" {
			logx.Log("Using Proxmox identity suffix: %s", cfg.ProxmoxIdentitySuffix)
		}
	} else {
		if cfg.ClusterSetID == "" {
			cfg.ClusterSetID = proxmox.GenerateUUIDv4()
			logx.Log("Generated CLUSTER_SET_ID: %s", cfg.ClusterSetID)
		}
		if cfg.ProxmoxIdentitySuffix == "" {
			cfg.ProxmoxIdentitySuffix = proxmox.DeriveIdentitySuffix(cfg.ClusterSetID)
		}
		proxmox.ValidateClusterSetIDFormat(cfg)
		logx.Log("Using Proxmox identity suffix: %s", cfg.ProxmoxIdentitySuffix)
	}

	// =========================================================================
	// PHASE 1: Install all dependencies (bash L8009-L8123)
	// =========================================================================
	logx.Log("Phase 1: Installing all dependencies...")

	if err := installer.SystemDependencies(); err != nil {
		logx.Die("ensure_system_dependencies failed: %v", err)
	}
	shell.RequireCmd("git")
	shell.RequireCmd("curl")
	shell.RequireCmd("python3")

	// Docker (bash L8020-L8034)
	installer.Docker()

	// kubectl first pass, then merge secrets from kind (may update ClusterctlVersion
	// and image pins), then re-run kubectl in case the pinned version changed.
	if err := installer.Kubectl(cfg); err != nil {
		logx.Die("ensure_kubectl failed: %v", err)
	}
	kindsync.MergeProxmoxBootstrapSecretsFromKind(cfg)
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
	if cfg.ArgoCDEnabled {
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
	if cfg.ArgoCDEnabled {
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

	kindsync.MergeProxmoxBootstrapSecretsFromKind(cfg)
	_ = kindsync.SyncBootstrapConfigToKind(cfg)
	_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)

	// Determine whether to skip heavy maintenance (upgrade Docker, BPG provider).
	// Matches bash PHASE1_SKIP_HEAVY_MAINTENANCE logic (L8086-L8093).
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
	if !skipHeavy {
		if err := opentofux.InstallBPGProvider(cfg); err != nil {
			logx.Die("install_bpg_proxmox_provider failed: %v", err)
		}
	}

	EnsureKindConfig(cfg)
	logx.Log("Phase 1 complete: all dependencies installed.")

	// =========================================================================
	// PHASE 2: Bootstrap (bash L8125-L8509)
	// =========================================================================
	logx.Log("Phase 2: Running bootstrap...")

	// --- 0. Proxmox identity bootstrap (bash L8133-L8211) ---
	recreateOpenTofuDone := false
	phase0IdentityBootstrap := false
	if !cfg.ClusterctlCfgFilePresent() && !cfg.HaveClusterctlCredsInEnv() {
		phase0IdentityBootstrap = true
	}
	if cfg.ProxmoxCSIConfig != "" {
		if _, err := os.Stat(cfg.ProxmoxCSIConfig); err != nil {
			phase0IdentityBootstrap = true
		}
	} else if cfg.ProxmoxCSITokenID == "" || cfg.ProxmoxCSITokenSecret == "" {
		phase0IdentityBootstrap = true
	}

	if phase0IdentityBootstrap {
		logx.Warn("Clusterctl API identity and/or CSI credentials are not satisfied from env or an explicit local clusterctl file — checking further.")
		proxmox.RefreshDerivedIdentityTokenIDs(cfg)
		opentofux.WriteClusterctlConfigIfMissing(cfg)
		opentofux.WriteCSIConfigIfMissing(cfg)

		needTerraform := false
		if !cfg.ClusterctlCfgFilePresent() && !cfg.HaveClusterctlCredsInEnv() {
			needTerraform = true
		}
		if cfg.ProxmoxCSIConfig != "" {
			if _, err := os.Stat(cfg.ProxmoxCSIConfig); err != nil {
				if cfg.ProxmoxCSITokenID == "" || cfg.ProxmoxCSITokenSecret == "" {
					needTerraform = true
				}
			}
		} else if cfg.ProxmoxCSITokenID == "" || cfg.ProxmoxCSITokenSecret == "" {
			needTerraform = true
		}

		if needTerraform {
			logx.Warn("CLI/env values are insufficient — running Terraform bootstrap for CAPI/CSI identities.")
			if cfg.ProxmoxURL == "" || cfg.ProxmoxAdminUsername == "" || cfg.ProxmoxAdminToken == "" {
				EnsureProxmoxAdminConfig(cfg,
					func() { kindsync.MergeProxmoxBootstrapSecretsFromKind(cfg) },
					func() { _ = kindsync.SyncBootstrapConfigToKind(cfg) },
					func() { _ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg) })
			}
			var missingAdmin []string
			if cfg.ProxmoxURL == "" {
				missingAdmin = append(missingAdmin, "PROXMOX_URL")
			}
			if cfg.ProxmoxAdminUsername == "" {
				missingAdmin = append(missingAdmin, "PROXMOX_ADMIN_USERNAME")
			}
			if cfg.ProxmoxAdminToken == "" {
				missingAdmin = append(missingAdmin, "PROXMOX_ADMIN_TOKEN")
			}
			if len(missingAdmin) > 0 {
				logx.Die("Missing admin Proxmox configuration: %v. Cannot run Terraform bootstrap without admin credentials.", missingAdmin)
			}
			if err := proxmox.ResolveRegionAndNodeFromAdminAPI(cfg); err != nil {
				logx.Warn("resolve_proxmox_region_and_node_from_admin_api: %v", err)
			}
			_ = proxmox.ResolveAvailableClusterSetIDForRoles(cfg)
			proxmox.CheckAdminAPIConnectivity(cfg)
			if cfg.RecreateProxmoxIdentities {
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

	if cfg.RecreateProxmoxIdentities && !recreateOpenTofuDone {
		if cfg.ProxmoxURL == "" || cfg.ProxmoxAdminUsername == "" || cfg.ProxmoxAdminToken == "" {
			EnsureProxmoxAdminConfig(cfg,
				func() { kindsync.MergeProxmoxBootstrapSecretsFromKind(cfg) },
				func() { _ = kindsync.SyncBootstrapConfigToKind(cfg) },
				func() { _ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg) })
		}
		if err := proxmox.ResolveRegionAndNodeFromAdminAPI(cfg); err != nil {
			logx.Warn("%v", err)
		}
		_ = proxmox.ResolveAvailableClusterSetIDForRoles(cfg)
		proxmox.CheckAdminAPIConnectivity(cfg)
		if err := opentofux.RecreateIdentities(cfg); err != nil {
			logx.Die("%v", err)
		}
	}

	// --- 1. Ensure clusterctl credentials (bash L8213-L8277) ---
	if !cfg.ClusterctlCfgFilePresent() && !cfg.HaveClusterctlCredsInEnv() {
		logx.Warn("Proxmox clusterctl API identity is not in the environment and no explicit local CLUSTERCTL_CFG is set.")
		if promptx.Confirm("Enter Proxmox API values interactively now?") {
			fmt.Fprint(os.Stderr, "\033[1;36m[?]\033[0m Proxmox VE URL (e.g. https://pve.example:8006): ")
			cfg.ProxmoxURL = promptx.ReadLine()
			fmt.Fprint(os.Stderr, "\033[1;36m[?]\033[0m Proxmox API TokenID (e.g. capmox@pve!capi): ")
			cfg.ProxmoxToken = promptx.ReadLine()
			fmt.Fprint(os.Stderr, "\033[1;36m[?]\033[0m Proxmox API Token secret (UUID): ")
			cfg.ProxmoxSecret = promptx.ReadLine()
			_ = kindsync.SyncBootstrapConfigToKind(cfg)
			_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)
			logx.Log("Proxmox API identity updated in kind when the management cluster is reachable. clusterctl on disk is not used by default (temp file for CLI only).")
		} else {
			logx.Warn("Skipping interactive creation. Set PROXMOX_URL, PROXMOX_TOKEN, and PROXMOX_SECRET, or add them to %s on kind, or set CLUSTERCTL_CFG to a local YAML you maintain.",
				cfg.ProxmoxBootstrapSecretNamespace)
			logx.Warn("Expected format:")
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, `  PROXMOX_URL: "https://pve.example:8006"`)
			fmt.Fprintln(os.Stderr, `  PROXMOX_TOKEN: "capmox@pve!capi"`)
			fmt.Fprintln(os.Stderr, `  PROXMOX_SECRET: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"`)
			fmt.Fprintln(os.Stderr)
			fmt.Fprint(os.Stderr, "\033[1;33m[?]\033[0m Press ENTER once you have set env vars or kind Secrets, or a CLUSTERCTL_CFG file...")
			_ = promptx.ReadLine()
			kindsync.MergeProxmoxBootstrapSecretsFromKind(cfg)
			if !cfg.ClusterctlCfgFilePresent() && !cfg.HaveClusterctlCredsInEnv() {
				logx.Die("Proxmox API identity still unset: not in kind Secrets, not in the environment, and no usable CLUSTERCTL_CFG. Aborting.")
			}
			logx.Log("Continuing with Proxmox credentials from env, kind, or explicit CLUSTERCTL_CFG file.")
		}
	}

	// Fill URL/Token/Secret from an explicit local clusterctl file if not
	// already populated from kind (matches bash L8255-L8259).
	if !cfg.ProxmoxBootstrapKindSecretUsed && !cfg.ProxmoxKindCAPMOXActive && cfg.ClusterctlCfgFilePresent() {
		if cfg.ProxmoxURL == "" {
			cfg.ProxmoxURL = yamlx.GetValue(cfg.ClusterctlCfg, "PROXMOX_URL")
		}
		if cfg.ProxmoxToken == "" {
			cfg.ProxmoxToken = yamlx.GetValue(cfg.ClusterctlCfg, "PROXMOX_TOKEN")
		}
		if cfg.ProxmoxSecret == "" {
			cfg.ProxmoxSecret = yamlx.GetValue(cfg.ClusterctlCfg, "PROXMOX_SECRET")
		}
	}
	cfg.ProxmoxSecret = proxmox.NormalizeTokenSecret(cfg.ProxmoxSecret, cfg.ProxmoxToken)
	proxmox.ValidateTokenSecret("PROXMOX_SECRET", cfg.ProxmoxSecret)
	proxmox.RefreshDerivedIdentityTokenIDs(cfg)
	if cfg.ProxmoxTemplateID == "" {
		cfg.ProxmoxTemplateID = "104"
	}
	if cfg.AllowedNodes == "" {
		cfg.AllowedNodes = cfg.ProxmoxNode
	}

	var missingCreds []string
	if cfg.ProxmoxURL == "" {
		missingCreds = append(missingCreds, "PROXMOX_URL")
	}
	if cfg.ProxmoxToken == "" {
		missingCreds = append(missingCreds, "PROXMOX_TOKEN")
	}
	if cfg.ProxmoxSecret == "" {
		missingCreds = append(missingCreds, "PROXMOX_SECRET")
	}
	if len(missingCreds) > 0 {
		logx.Warn("Missing Proxmox configuration: %v", missingCreds)
		logx.Die("Configure Proxmox credentials before running this script.")
	}

	// Test Proxmox API connectivity with the clusterctl token (bash L8279-L8289).
	logx.Log("Testing Proxmox API connectivity at %s (clusterctl token)...", proxmox.HostBaseURL(cfg))
	if err := proxmox.ResolveRegionAndNodeFromClusterctlAPI(cfg); err != nil {
		// ResolveRegionAndNodeFromClusterctlAPI already does the connectivity
		// check internally. For the explicit HTTP status code branch mirror
		// (401/000/other) we catch the error here.
		logx.Die("Proxmox API connectivity check failed: %v. Verify PROXMOX_URL, PROXMOX_TOKEN, and PROXMOX_SECRET.", err)
	}
	logx.Log("Proxmox API reachable.")

	// Bootstrap the ephemeral or explicit clusterctl config file (bash L8293).
	clusterctlCfgPath := SyncClusterctlConfigFile(cfg)
	_ = clusterctlCfgPath

	// --- 4. Check for existing kind clusters (bash L8295-L8315) ---
	kindClusterReused := false
	logx.Log("Checking for existing kind clusters...")
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
			logx.Log("No existing cluster found; purging any leftover networking state before fresh bootstrap.")
			PurgeStaleHostNetworking()
		}
	}

	// --- 5. Resolve CAPMOX image tag (bash L8317-L8335) ---
	logx.Log("Resolving CAPMOX image tag...")
	capmoxTag := cfg.CAPMOXVersion
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
	capmoxImage := cfg.CAPMOXImageRepo + ":" + capmoxTag

	// --- 6. Create kind cluster (bash L8337-L8365) ---
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

	// Load arm64 images or build them when the registry doesn't have arm64 (bash L8358-L8365).
	if kindClusterReused {
		logx.Log("Reusing existing kind cluster — skipping arm64 image checks and kind-load (images already present from previous bootstrap).")
	} else {
		logx.Log("Checking arm64 availability for all provider/CAPI images and loading into kind...")
		ipamTag := ""
		if i := strings.LastIndex(cfg.IPAMImage, ":"); i >= 0 {
			ipamTag = cfg.IPAMImage[i+1:]
		}
		_ = installer.BuildIfNoArm64(cfg, capmoxImage, cfg.CAPMOXRepo, capmoxTag, cfg.CAPMOXBuildDir, cfg.KindClusterName)
		_ = installer.BuildIfNoArm64(cfg, cfg.CAPICoreImage, cfg.CAPICoreRepo, cfg.ClusterctlVersion, "./cluster-api", cfg.KindClusterName)
		_ = installer.BuildIfNoArm64(cfg, cfg.CAPIBootstrapImage, cfg.CAPICoreRepo, cfg.ClusterctlVersion, "./cluster-api", cfg.KindClusterName)
		_ = installer.BuildIfNoArm64(cfg, cfg.CAPIControlplaneImage, cfg.CAPICoreRepo, cfg.ClusterctlVersion, "./cluster-api", cfg.KindClusterName)
		_ = installer.BuildIfNoArm64(cfg, cfg.IPAMImage, cfg.IPAMRepo, ipamTag, "./cluster-api-ipam-provider-in-cluster", cfg.KindClusterName)
	}
	_ = kindsync.SyncBootstrapConfigToKind(cfg)
	_ = kindsync.SyncProxmoxBootstrapLiteralCredentialsToKind(cfg)

	// --- 7. Management cluster CNI (bash L8369-L8372) ---
	logx.Log("Using kind's default CNI (kindnet) on the management cluster; skipping Cilium install.")

	// --- 8. Initialize Cluster API (bash L8374-L8423) ---
	InstallMetricsServerOnKindManagement(cfg)

	logx.Log("Initializing Cluster API (infrastructure=%s, ipam=%s, addon=helm)...", cfg.InfraProvider, cfg.IPAMProvider)
	clusterctlCfgPath = SyncClusterctlConfigFile(cfg)
	initArgs := []string{
		"clusterctl", "init",
		"--config", clusterctlCfgPath,
		"--infrastructure", cfg.InfraProvider,
		"--ipam", cfg.IPAMProvider,
		"--addon", "helm",
	}
	// --bootstrap-mode k3s swaps the kubeadm control-plane + bootstrap
	// providers for the K3s ones. kubeadm is the default and clusterctl
	// installs it implicitly when --control-plane / --bootstrap are
	// omitted.
	if cfg.BootstrapMode == "k3s" {
		logx.Log("BOOTSTRAP_MODE=k3s — initializing CAPI with K3s control-plane + bootstrap providers (instead of kubeadm)")
		initArgs = append(initArgs, "--control-plane", "k3s", "--bootstrap", "k3s")
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

	// Wait for CAAPH add-on provider (bash L8387-L8394).
	logx.Log("Waiting for CAAPH (add-on provider Helm) to become ready, if present...")
	mgmtCli, mgmtErr := k8sclient.ForCurrent()
	if mgmtErr != nil {
		logx.Die("kube client for current context: %v", mgmtErr)
	}
	bgInit := context.Background()
	if dl, err := mgmtCli.Typed.AppsV1().Deployments("caaph-system").List(bgInit, metav1.ListOptions{}); err == nil && dl != nil {
		for _, d := range dl.Items {
			_ = waitDeploymentReady(mgmtCli, "caaph-system", d.Name, 300*time.Second)
		}
	} else {
		logx.Warn("caaph-system not found after clusterctl init --addon helm — verify CAAPH; HelmChartProxy may fail without it.")
	}

	// Wait for core CAPI controllers (bash L8396-L8408).
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

	// Wait for webhook service endpoints (bash L8410-L8412).
	logx.Log("Waiting for CAPI webhook service endpoints...")
	kubectlx.WaitForServiceEndpoint("capi-kubeadm-bootstrap-system", "capi-kubeadm-bootstrap-webhook-service", 300*time.Second)
	kubectlx.WaitForServiceEndpoint("capi-kubeadm-control-plane-system", "capi-kubeadm-control-plane-webhook-service", 300*time.Second)

	// Wait for CAPMOX (bash L8414-L8421).
	logx.Log("Waiting for Proxmox provider (capmox-controller-manager) to become ready...")
	if err := waitDeploymentReady(mgmtCli, "capmox-system", "capmox-controller-manager", 300*time.Second); err != nil {
		logx.Die("capmox-controller-manager did not become Available: %v", err)
	}
	logx.Log("Waiting for CAPMOX mutating webhook endpoint (ProxmoxCluster apply)...")
	kubectlx.WaitForServiceEndpoint("capmox-system", "capmox-webhook-service", 300*time.Second)

	opentofux.RecreateResyncCapmox(cfg)

	// --- Capacity check: confirm the planned clusters fit inside
	// cfg.ResourceBudgetFraction (default 0.75) of the available
	// Proxmox host CPU/memory/storage. Aborts before any VM is created.
	if err := preflightCapacity(cfg); err != nil {
		logx.Die("%v", err)
	}

	// --- Pre-create Proxmox pools (organizational + ACL grouping).
	// CAPMOX rejects a pool reference that doesn't exist, so we
	// create the workload pool here and (when --pivot is enabled) the
	// mgmt pool too. Idempotent: existing pools are silently kept.
	if cfg.ProxmoxPool != "" {
		if err := proxmox.EnsurePool(cfg, cfg.ProxmoxPool); err != nil {
			logx.Warn("Proxmox pool %s: %v — VMs may fail to register; create it manually if needed.", cfg.ProxmoxPool, err)
		} else {
			logx.Log("Proxmox pool '%s' ensured (workload).", cfg.ProxmoxPool)
		}
	}
	if cfg.PivotEnabled && cfg.MgmtProxmoxPool != "" {
		if err := proxmox.EnsurePool(cfg, cfg.MgmtProxmoxPool); err != nil {
			logx.Warn("Proxmox pool %s: %v — mgmt VMs may fail to register; create it manually if needed.", cfg.MgmtProxmoxPool, err)
		} else {
			logx.Log("Proxmox pool '%s' ensured (management).", cfg.MgmtProxmoxPool)
		}
	}

	// --- 9.5 Pivot to a Proxmox-hosted management cluster (bash L8848-L8884) ---
	// CAPI bootstrap-and-pivot pattern. When PivotEnabled is true: kind
	// provisions a single-node management cluster on Proxmox, clusterctl
	// init runs against it, clusterctl move migrates CAPI inventory from
	// kind to mgmt, the proxmox-bootstrap-system Secrets are mirrored, the
	// kind context is rebound to point at the mgmt cluster, and downstream
	// phases run against mgmt. The kind cluster is torn down at the end
	// (unless --pivot-keep-kind / --no-delete-kind).
	if cfg.PivotEnabled {
		if cfg.PivotDryRun {
			logx.Log("Phase 2.95: PIVOT DRY-RUN — provisioning mgmt cluster + clusterctl-init, then `clusterctl move --dry-run`. Workload stays on kind; no state moves.")
		} else {
			logx.Log("Phase 2.95: pivoting CAPI from kind → Proxmox-hosted management cluster...")
		}
		mgmtKubeconfig, err := pivot.EnsureManagementCluster(cfg)
		if err != nil {
			logx.Die("pivot: EnsureManagementCluster: %v", err)
		}
		if err := pivot.InstallCAPIOnManagement(cfg, mgmtKubeconfig); err != nil {
			logx.Die("pivot: InstallCAPIOnManagement: %v", err)
		}
		if err := pivot.MoveCAPIState(cfg, mgmtKubeconfig); err != nil {
			logx.Die("pivot: MoveCAPIState: %v", err)
		}
		if cfg.PivotDryRun {
			logx.Log("pivot: dry-run complete. Inspect the management cluster:")
			logx.Log("  KUBECONFIG=%s kubectl get nodes", mgmtKubeconfig)
			logx.Log("  KUBECONFIG=%s kubectl -n capi-system get pods", mgmtKubeconfig)
			logx.Log("Re-run without --pivot-dry-run to perform the real move + handoff + kind teardown.")
			logx.Log("(kind cluster '%s' has been left intact; workload is still managed by it.)", cfg.KindClusterName)
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

	// --- 9. Apply workload cluster manifest (bash L8425-L8494) ---
	MaybeInteractiveSelectWorkloadCluster(cfg)
	capimanifest.TryFillWorkloadInputsFromManagement(cfg)
	// Re-apply proxmox-bootstrap-config so snapshot keys beat live backfill.
	kindsync.MergeProxmoxBootstrapSecretsFromKind(cfg)
	if cfg.ProxmoxTemplateID == "" {
		cfg.ProxmoxTemplateID = "104"
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
	proxmox.RefreshDerivedCiliumClusterID(cfg)
	_ = caaph.PatchClusterCAAPHHelmLabels(cfg, cfg.CAPIManifest)
	PushCAPIManifestToSecret(cfg)

	if cfg.BootstrapCAPIUseSecret {
		logx.Log("Applying workload manifest to management cluster (ephemeral file in this run; last pushed to Secret %s/%s)...",
			cfg.CAPIManifestSecretNamespace, cfg.CAPIManifestSecretName)
	} else {
		logx.Log("Applying CAPI manifest %s...", cfg.CAPIManifest)
	}
	kubectlx.WarnRegeneratedManifestImmutableRisk(cfg)
	for attempt := 1; attempt <= 3; attempt++ {
		if err := kubectlx.ApplyWorkloadManifestToManagementCluster(cfg, cfg.CAPIManifest); err == nil {
			break
		}
		if attempt == 3 {
			logx.Die("Failed to apply %s after %d attempts.", cfg.CAPIManifest, attempt)
		}
		logx.Warn("Apply failed (attempt %d/3). Retrying in 10s while webhooks settle...", attempt)
		time.Sleep(10 * time.Second)
	}

	opentofux.RecreateIdentitiesWorkloadCSISecrets(cfg)
	caaph.ApplyWorkloadCiliumHelmChartProxy(cfg)
	WaitForWorkloadClusterReady(cfg)
	caaph.ApplyWorkloadCiliumLBBToWorkload(cfg, func() (string, error) {
		return writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
	})

	// Metrics-server on workload (bash L8472-L8478).
	if cfg.EnableWorkloadMetricsServer {
		if cfg.ArgoCDEnabled && cfg.WorkloadArgoCDEnabled {
			logx.Log("workload metrics-server: deploy with your app-of-apps repo (%s); ENABLE_WORKLOAD_METRICS_SERVER is informational when Argo delivers apps from Git.", cfg.WorkloadAppOfAppsGitURL)
		} else {
			InstallMetricsServerOnWorkload(cfg)
		}
	}

	// Proxmox CSI config Secret on the workload (bash L8481-L8489).
	if cfg.ProxmoxCSIEnabled && cfg.ArgoCDEnabled && cfg.WorkloadArgoCDEnabled {
		csix.LoadVarsFromConfig(cfg)
		if cfg.ProxmoxCSIURL == "" {
			cfg.ProxmoxCSIURL = proxmox.APIJSONURL(cfg)
		}
		if cfg.ProxmoxCSIURL != "" && cfg.ProxmoxCSITokenID != "" &&
			cfg.ProxmoxCSITokenSecret != "" && cfg.ProxmoxRegion != "" {
			csix.ApplyConfigSecretToWorkload(cfg, func() (string, error) {
				return writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
			})
		} else {
			logx.Warn("Proxmox CSI token material incomplete — push %s-proxmox-csi-config to the workload yourself before syncing the CSI app.", cfg.WorkloadClusterName)
		}
	}

	// Pre-install argocd-redis Secret on the workload (bash L8492-L8494).
	if cfg.ArgoCDEnabled && cfg.WorkloadArgoCDEnabled {
		argocdx.ApplyRedisSecretToWorkload(cfg, func() (string, error) {
			return writeWorkloadKubeconfig(cfg, "kind-"+cfg.KindClusterName)
		})
	}

	// --- 10. Argo CD on workload (bash L8496-L8508) ---
	if cfg.ArgoCDEnabled {
		logx.Log("Argo CD on the workload: Argo CD Operator + ArgoCD CR, then CAAPH argocd-apps (root Application name %s; see --workload-app-of-apps-git-*).", cfg.WorkloadClusterName)
		if cfg.WorkloadArgoCDEnabled {
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

	// --- Pivot teardown: delete the kind cluster after a successful pivot
	// (skipped when --pivot-keep-kind / --no-delete-kind / pivot disabled).
	if cfg.PivotEnabled {
		if err := pivot.TeardownKind(cfg); err != nil {
			logx.Warn("pivot: TeardownKind: %v", err)
		}
	}

	logx.Log("Done. CAPI: 'kubectl get clusters -A' and 'clusterctl describe cluster <name>'. For workload apps, rely on Argo CD sync (this script does not wait for all add-ons to be Healthy).")
	return 0
}

// validateMinVCPU enforces the CAPI/kubeadm + k3s minimum of 2 vCPUs
// per VM (sockets × cores) on every role that has at least one
// replica. Mgmt-worker is skipped when MgmtWorkerMachineCount is 0
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
		{"workload control-plane", cfg.ControlPlaneNumSockets, cfg.ControlPlaneNumCores, atoi(cfg.ControlPlaneMachineCount) > 0},
		{"workload worker", cfg.WorkerNumSockets, cfg.WorkerNumCores, atoi(cfg.WorkerMachineCount) > 0},
	}
	if cfg.PivotEnabled {
		roles = append(roles,
			role{"mgmt control-plane", cfg.MgmtControlPlaneNumSockets, cfg.MgmtControlPlaneNumCores, atoi(cfg.MgmtControlPlaneMachineCount) > 0},
			role{"mgmt worker", cfg.WorkerNumSockets, cfg.WorkerNumCores, atoi(cfg.MgmtWorkerMachineCount) > 0},
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

// preflightCapacity queries the Proxmox host capacity and compares it
// against the configured cluster sizing × replicas. Returns nil when
// the plan fits inside cfg.ResourceBudgetFraction × capacity. Returns a
// non-nil error otherwise — unless cfg.AllowResourceOvercommit is set,
// in which case the error is downgraded to a warning and nil is
// returned.
func preflightCapacity(cfg *config.Config) error {
	hc, err := capacity.FetchHostCapacity(cfg)
	if err != nil {
		// Don't block the run on a capacity-query failure — log and
		// proceed. The user can still hit a real cap on the API server.
		logx.Warn("capacity check skipped: %v", err)
		return nil
	}
	plan := capacity.PlanFor(cfg)
	threshold := cfg.ResourceBudgetFraction
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
	// Existing-VM awareness: read what's already provisioned and
	// fold it into the verdict alongside the plan. Soft budget +
	// overcommit tolerance from cfg.
	used, _ := capacity.FetchExistingUsage(cfg)
	if used != nil && used.VMCount > 0 {
		logx.Log("Existing-VM census: %d VMs, %d cores / %d MiB / %d GB already allocated",
			used.VMCount, used.CPUCores, used.MemoryMiB, used.StorageGB)
	}
	tolerancePct := cfg.OvercommitTolerancePct
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
		if cfg.AllowResourceOvercommit {
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

func totalReplicas(p capacity.Plan) int {
	n := 0
	for _, it := range p.Items {
		n += it.Replicas
	}
	return n
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
