// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package config

// PivotConfig groups all pivot-orchestration configuration.
// The env var and CLI flag names are unchanged — only the Go field paths moved.
type PivotConfig struct {
	// Enabled: when true, the bootstrap follows the standard CAPI
	// "bootstrap-and-pivot" pattern: kind provisions a management cluster
	// on Proxmox, clusterctl-moves CAPI state into it, the
	// yage-system Secrets are mirrored, the workload cluster
	// is created from the management cluster, and the kind cluster is
	// torn down once the management cluster is verified to carry the
	// same state.
	//
	// Default sizing is intentionally smaller than the workload defaults:
	// the management cluster runs only CAPI controllers, CAAPH, capmox,
	// in-cluster IPAM, and the bootstrap-state Secrets — no application
	// workload. 1 socket / 2 cores / 4 GiB is enough; one CP endpoint
	// VIP and a 2-IP node range (so a rollout can land a replacement VM
	// before draining the original).
	//
	// CNI: Cilium with Hubble enabled but LB-IPAM disabled (the
	// management cluster has no Services that need LoadBalancer IPs).
	// CSI: disabled by default (the management cluster is stateless
	// unless explicitly opted-in via MGMT_PROXMOX_CSI_ENABLED=true).
	//
	// The cluster shape itself lives in cfg.Mgmt; Proxmox-only sizing /
	// pool / CSI knobs live in cfg.Providers.Proxmox.Mgmt.
	Enabled bool
	// KeepKind, when true, skips the final `kind delete cluster`
	// after a successful pivot — useful for debugging.
	KeepKind bool
	// VerifyTimeout caps how long we wait for the management
	// cluster to look "identical" to kind before declaring success.
	VerifyTimeout string
	// DryRun stops after provisioning + clusterctl-init on the
	// management cluster, runs `clusterctl move --dry-run` so the user
	// can inspect the planned hand-off without executing it, and
	// returns. The workload cluster stays managed by kind. Useful for
	// validating mgmt connectivity / sizing before committing to the
	// move.
	DryRun bool
	// StopBeforeWorkload exits the orchestrator after the pivot
	// completes (mgmt cluster up, clusterctl moved, kind torn down)
	// but before the workload manifest is applied. Useful for
	// integration tests that inspect a clean managed CAPI plane on
	// the provider with no workload churn. Env: YAGE_STOP_BEFORE_WORKLOAD.
	StopBeforeWorkload bool
}
