// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package config

// CapacityConfig groups all resource-budget and capacity-planning
// configuration.
// The env var and CLI flag names are unchanged — only the Go field paths moved.
type CapacityConfig struct {
	// ResourceBudgetFraction caps the share of available Proxmox host
	// CPU/memory/storage that the configured clusters may consume.
	// 0.75 by default — the remaining 25 % is reserved for the host
	// OS, hypervisor overhead, and slack for VM rollouts.
	ResourceBudgetFraction float64
	// AllowOvercommit, when true, downgrades the over-the-budget
	// capacity check to a warning instead of failing the run.
	AllowOvercommit bool
	// OvercommitTolerancePct caps how far above 100% of host capacity
	// the combined (existing-VM + planned) demand may go before the
	// orchestrator refuses to continue. 15 = "(existing + planned)
	// must be ≤ host × 1.15" — Proxmox supports memory overcommit
	// via ballooning + swap, but >15% drift starts to OOM under
	// load. Below the soft threshold (ResourceBudgetFraction) is
	// fine; between threshold and 1+tolerance is "tight, warn-and-
	// continue"; above 1+tolerance is "abort unless --allow-resource-
	// overcommit". Default 15. See capacity.CheckCombined.
	OvercommitTolerancePct float64
	// SystemAppsCPUMillicores / SystemAppsMemoryMiB define the cluster-
	// wide reserve for the system add-ons yage installs:
	// kyverno, cert-manager, proxmox-csi (controller), argocd (operator
	// + server + repo + redis), keycloak (SSO), external-secrets, and
	// infisical. The remainder of the workload cluster's worker capacity
	// is split into three equal buckets (db / observability / product).
	SystemAppsCPUMillicores int   // default 2000 = 2 cores
	SystemAppsMemoryMiB     int64 // default 4096 = 4 GiB
}
