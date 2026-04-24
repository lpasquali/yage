// Package bootstrap is the orchestrator. The bash script's top-level flow
// (phases 1-10, roughly L7700-L8509) is the model we're porting piecewise.
//
// Current state: only the dependency-installer phase and the kind
// backup/restore standalone branches are wired. Everything else emits a
// "not yet ported" message with a pointer to the bash line range.
package bootstrap

import (
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/installer"
	"github.com/lpasquali/bootstrap-capi/internal/kind"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
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

	// --- Standalone branches (bash: short-circuit before main phases) ---
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

	// --- Phase 0: host-level dependencies + CLI toolchain ---
	//
	// Matches the early block in bash main() that runs ensure_system_dependencies
	// and every ensure_* before any clusters are touched.
	if err := installer.SystemDependencies(); err != nil {
		logx.Die("ensure_system_dependencies failed: %v", err)
	}
	if err := installer.Kind(cfg); err != nil {
		logx.Die("ensure_kind failed: %v", err)
	}
	if err := installer.Kubectl(cfg); err != nil {
		logx.Die("ensure_kubectl failed: %v", err)
	}
	if err := installer.Clusterctl(cfg); err != nil {
		logx.Die("ensure_clusterctl failed: %v", err)
	}
	if err := installer.CiliumCLI(cfg); err != nil {
		logx.Die("ensure_cilium_cli failed: %v", err)
	}
	if cfg.ArgoCDEnabled {
		if err := installer.ArgoCDCLI(cfg); err != nil {
			logx.Die("ensure_argocd_cli failed: %v", err)
		}
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
	if !cfg.NoDeleteKind {
		if err := installer.OpenTofu(cfg); err != nil {
			logx.Die("ensure_opentofu failed: %v", err)
		}
	}

	// --- Phases 1-10: unported ---
	//
	// The remaining orchestration — kind cluster selection / create / reuse,
	// Proxmox identity Terraform apply, secret sync, clusterctl init,
	// workload manifest regen/apply, Cilium HelmChartProxy, Argo CD Operator,
	// CAAPH app-of-apps, metrics-server, post-sync hooks — lives in
	// bootstrap-capi.sh from ~L7700 to L8509. None of it is ported yet.
	logx.Warn("bootstrap: phases 1-10 (cluster lifecycle, CAPI init, apply) are not yet ported; see bootstrap-capi.sh L7700-L8509.")
	return 0
}
