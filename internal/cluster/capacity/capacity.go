// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package capacity computes resource budget plans (PlanFor /
// PlanForK3s / AllocationsFor) and verdicts (Check / CheckCombined
// / WouldFitAsK3s) used by the orchestrator's preflight and
// --dry-run to compare planned cluster sizing against host
// headroom.
//
// Cloud-specific inventory acquisition lives behind the Provider
// interface (provider.For(cfg).Inventory). The Proxmox-specific
// HTTP queries live in internal/provider/proxmox/inventory.go.
// This package consumes pre-built HostCapacity / ExistingUsage
// values supplied by the orchestrator (see
// orchestrator.hostCapacityFromInventory).
//
// Default budget threshold is 2/3 — clusters cannot use more than
// 66% of available host resources. The rest is reserved for the
// host OS, the hypervisor, and overhead.
package capacity

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lpasquali/yage/internal/config"
)

// DefaultThreshold is the fraction of host resources that may be
// claimed by all clusters combined. The remaining ~33 % is reserved
// for the host OS, hypervisor overhead, rollout slack, and the
// unknown-future workload that lands on the cluster after orchestrator.
const DefaultThreshold = 2.0 / 3.0

// SmallEnvCPUCores / SmallEnvMemoryGiB define the threshold below which
// the orchestrator suggests using K3s instead of full kubeadm. Anything
// at or below this size will struggle to run the default kubeadm sizing
// of two clusters; K3s typically fits in ~512 MiB per node.
const (
	SmallEnvCPUCores  = 8
	SmallEnvMemoryGiB = 16
)

// K3s recommended sizing — what the orchestrator would propose to fit
// the user's current machine counts inside a tight budget. CPU stays
// at 2 vCPUs per VM (CAPI requires ≥2); memory and disk drop hard.
//
// These are values the orchestrator suggests, not enforces — the user
// can override per-role with the existing CONTROL_PLANE_* / WORKER_*
// env vars after switching to --bootstrap-mode k3s.
const (
	K3sCPCores    = 2
	K3sCPMemMiB   = 1024
	K3sCPDiskGB   = 20
	K3sWorkerMem  = 1024
	K3sWorkerDisk = 15
)

// HostCapacity is the aggregate of CPU + memory + storage across all
// Proxmox nodes that are eligible for VM placement, after filtering by
// the configured AllowedNodes (or just ProxmoxNode when AllowedNodes is
// empty).
type HostCapacity struct {
	Nodes        []string
	CPUCores     int   // total physical cores
	MemoryMiB    int64 // total memory in MiB
	StorageGB    int64 // total storage capacity in GB across the cluster
	StorageBy    map[string]int64
}

// ExistingUsage is what's already provisioned on the Proxmox host
// before the planned bootstrap runs. Aggregated from the VM list
// (`/api2/json/cluster/resources?type=vm`) — every VM on the
// allowed nodes contributes its `maxcpu / maxmem / maxdisk` (the
// VM's *configured* size, not its current load).
//
// Used to surface "what's already running fits / overcommits /
// blocks the new cluster" in the dry-run plan, and to fail real
// runs when (existing + planned) overshoots host capacity beyond
// the configured overcommit tolerance.
type ExistingUsage struct {
	VMCount   int
	CPUCores  int
	MemoryMiB int64
	StorageGB int64
	// ByPool groups VMs by Proxmox pool so the dry-run can show
	// "5 VMs in 'capi-quickstart', 3 VMs in 'other-cluster'".
	ByPool map[string]int
}

// Plan is the aggregate of CPU + memory + storage that the configured
// workload + (optional) management clusters would consume.
type Plan struct {
	CPUCores  int
	MemoryMiB int64
	StorageGB int64
	Items     []PlanItem
}

// PlanItem is one line in the breakdown — a single role on a single
// cluster contributes one PlanItem.
type PlanItem struct {
	Name      string // "workload control-plane", "mgmt control-plane", ...
	Replicas  int
	CPUCores  int   // per replica
	MemoryMiB int64 // per replica
	DiskGB    int64 // per replica
}

// Total returns the sum of CPU/memory/disk across all replicas of the
// item.
func (p PlanItem) Total() (cpu int, mem, disk int64) {
	return p.CPUCores * p.Replicas, p.MemoryMiB * int64(p.Replicas), p.DiskGB * int64(p.Replicas)
}

// PlanFor builds the resource plan from cfg. Includes the workload
// cluster always, and the management cluster when PivotEnabled is true.
func PlanFor(cfg *config.Config) Plan {
	p := Plan{}
	add := func(name string, replicas int, sockets, cores, memMiB, diskGB string) {
		if replicas <= 0 {
			return
		}
		s := atoiOr(sockets, 1)
		c := atoiOr(cores, 1)
		mem := atoi64Or(memMiB, 0)
		disk := atoi64Or(diskGB, 0)
		item := PlanItem{
			Name: name, Replicas: replicas,
			CPUCores: s * c, MemoryMiB: mem, DiskGB: disk,
		}
		p.Items = append(p.Items, item)
		cpu, m, d := item.Total()
		p.CPUCores += cpu
		p.MemoryMiB += m
		p.StorageGB += d
	}
	wcp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wwk := atoiOr(cfg.WorkerMachineCount, 0)
	add("workload control-plane", wcp,
		cfg.Providers.Proxmox.ControlPlaneNumSockets, cfg.Providers.Proxmox.ControlPlaneNumCores,
		cfg.Providers.Proxmox.ControlPlaneMemoryMiB, cfg.Providers.Proxmox.ControlPlaneBootVolumeSize)
	add("workload worker", wwk,
		cfg.Providers.Proxmox.WorkerNumSockets, cfg.Providers.Proxmox.WorkerNumCores,
		cfg.Providers.Proxmox.WorkerMemoryMiB, cfg.Providers.Proxmox.WorkerBootVolumeSize)
	if cfg.PivotEnabled {
		mcp := atoiOr(cfg.Mgmt.ControlPlaneMachineCount, 1)
		mwk := atoiOr(cfg.Mgmt.WorkerMachineCount, 0)
		add("mgmt control-plane", mcp,
			cfg.Providers.Proxmox.Mgmt.ControlPlaneNumSockets, cfg.Providers.Proxmox.Mgmt.ControlPlaneNumCores,
			cfg.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB, cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize)
		add("mgmt worker", mwk,
			cfg.Providers.Proxmox.WorkerNumSockets, cfg.Providers.Proxmox.WorkerNumCores,
			cfg.Providers.Proxmox.WorkerMemoryMiB, cfg.Providers.Proxmox.WorkerBootVolumeSize)
	}
	return p
}

// PlanForK3s returns the resource plan if cfg's machine counts ran
// under K3s sizing instead of cfg's per-role sockets/cores/memory.
// Used to suggest --bootstrap-mode k3s when the kubeadm plan
// overflows the host budget but a k3s footprint would fit.
//
// Replica counts are taken from cfg as-is; only sizing is overridden.
func PlanForK3s(cfg *config.Config) Plan {
	p := Plan{}
	add := func(name string, replicas int, cpu int, memMiB, diskGB int64) {
		if replicas <= 0 {
			return
		}
		item := PlanItem{
			Name: name, Replicas: replicas,
			CPUCores: cpu, MemoryMiB: memMiB, DiskGB: diskGB,
		}
		p.Items = append(p.Items, item)
		c, m, d := item.Total()
		p.CPUCores += c
		p.MemoryMiB += m
		p.StorageGB += d
	}
	wcp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wwk := atoiOr(cfg.WorkerMachineCount, 0)
	add("workload control-plane (k3s)", wcp, K3sCPCores, K3sCPMemMiB, K3sCPDiskGB)
	add("workload worker (k3s)", wwk, K3sCPCores, K3sWorkerMem, K3sWorkerDisk)
	if cfg.PivotEnabled {
		mcp := atoiOr(cfg.Mgmt.ControlPlaneMachineCount, 1)
		mwk := atoiOr(cfg.Mgmt.WorkerMachineCount, 0)
		add("mgmt control-plane (k3s)", mcp, K3sCPCores, K3sCPMemMiB, K3sCPDiskGB)
		add("mgmt worker (k3s)", mwk, K3sCPCores, K3sWorkerMem, K3sWorkerDisk)
	}
	return p
}

// WouldFitAsK3s returns true when PlanForK3s(cfg) fits inside
// threshold × host. Caller already checked the kubeadm plan and got
// an overflow; a true result means switching to k3s + smaller sizing
// is a viable suggestion. Returns the k3s plan alongside for display.
func WouldFitAsK3s(cfg *config.Config, host *HostCapacity, threshold float64) (bool, Plan) {
	p := PlanForK3s(cfg)
	if err := Check(p, host, threshold); err == nil {
		return true, p
	}
	return false, p
}

// IsSmallEnv reports whether the host has fewer than SmallEnvCPUCores
// or SmallEnvMemoryGiB. Callers use this to suggest --bootstrap-mode k3s.
func (h *HostCapacity) IsSmallEnv() bool {
	if h == nil {
		return false
	}
	return h.CPUCores < SmallEnvCPUCores || h.MemoryMiB < int64(SmallEnvMemoryGiB)*1024
}

// DefaultOvercommitTolerancePct caps how far above 100% of host
// capacity the combined (existing + planned) demand may go before
// the orchestrator refuses to continue. 15 means "(existing+planned)
// ≤ host × 1.15 is acceptable; over that, abort". Memory is the
// dimension this matters for in practice — Proxmox supports memory
// overcommit via ballooning + swap, but >15% drift starts to cause
// OOMs under load.
const DefaultOvercommitTolerancePct = 15.0

// Verdict is the result of a combined existing-vs-planned capacity
// check. The orchestrator uses this to decide whether to continue,
// warn-and-continue, or abort.
type Verdict int

const (
	// VerdictFits means (existing + planned) fits inside the soft
	// budget (threshold × host). Proceed silently.
	VerdictFits Verdict = iota
	// VerdictTight means (existing + planned) exceeds the soft
	// budget but is within (1 + tolerance) × host. Proceed with
	// a warning — the host is overcommitted but inside the
	// operator-approved tolerance.
	VerdictTight
	// VerdictAbort means (existing + planned) exceeds (1 + tolerance)
	// × host. The orchestrator refuses to continue unless
	// --allow-resource-overcommit is set.
	VerdictAbort
)

func (v Verdict) String() string {
	switch v {
	case VerdictFits:
		return "fits"
	case VerdictTight:
		return "tight"
	}
	return "abort"
}

// CheckCombined evaluates (existing + planned) against host capacity
// at two thresholds: the soft budget (threshold) and the hard
// overcommit ceiling (1 + tolerance/100). Returns a Verdict and a
// human-readable message describing the math behind it.
//
// Either existing or threshold may be nil/zero — when existing is
// nil it's treated as zero usage (fresh host); when threshold is 0
// it falls back to DefaultThreshold; when tolerancePct is ≤ 0 it
// falls back to DefaultOvercommitTolerancePct.
func CheckCombined(plan Plan, host *HostCapacity, existing *ExistingUsage, threshold, tolerancePct float64) (Verdict, string) {
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultThreshold
	}
	if tolerancePct <= 0 {
		tolerancePct = DefaultOvercommitTolerancePct
	}
	usedCPU, usedMem, usedDisk := 0, int64(0), int64(0)
	if existing != nil {
		usedCPU = existing.CPUCores
		usedMem = existing.MemoryMiB
		usedDisk = existing.StorageGB
	}
	totalCPU := usedCPU + plan.CPUCores
	totalMem := usedMem + plan.MemoryMiB
	totalDisk := usedDisk + plan.StorageGB

	cpuFrac := float64(totalCPU) / float64(host.CPUCores)
	memFrac := float64(totalMem) / float64(host.MemoryMiB)
	diskFrac := 0.0
	if host.StorageGB > 0 {
		diskFrac = float64(totalDisk) / float64(host.StorageGB)
	}

	maxFrac := cpuFrac
	if memFrac > maxFrac {
		maxFrac = memFrac
	}
	if diskFrac > maxFrac {
		maxFrac = diskFrac
	}

	hardCeiling := 1.0 + tolerancePct/100.0

	msg := fmt.Sprintf("CPU %d+%d=%d/%d (%.0f%%), mem %d+%d=%d/%d MiB (%.0f%%), disk %d+%d=%d/%d GB (%.0f%%)",
		usedCPU, plan.CPUCores, totalCPU, host.CPUCores, cpuFrac*100,
		usedMem, plan.MemoryMiB, totalMem, host.MemoryMiB, memFrac*100,
		usedDisk, plan.StorageGB, totalDisk, host.StorageGB, diskFrac*100,
	)

	switch {
	case maxFrac <= threshold:
		return VerdictFits, msg + fmt.Sprintf(" — within soft budget %.0f%%", threshold*100)
	case maxFrac <= hardCeiling:
		return VerdictTight, msg + fmt.Sprintf(" — exceeds soft budget %.0f%% but within %.0f%% overcommit tolerance",
			threshold*100, tolerancePct)
	default:
		return VerdictAbort, msg + fmt.Sprintf(" — exceeds %.0f%% overcommit ceiling (%.0f%% > %.0f%%)",
			tolerancePct, maxFrac*100, hardCeiling*100)
	}
}

// Check returns nil when plan fits inside threshold * capacity, or an
// error describing the overflow. Threshold defaults to DefaultThreshold
// when 0.
func Check(plan Plan, host *HostCapacity, threshold float64) error {
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultThreshold
	}
	maxCPU := int(float64(host.CPUCores) * threshold)
	maxMem := int64(float64(host.MemoryMiB) * threshold)
	maxDisk := int64(float64(host.StorageGB) * threshold)
	var msgs []string
	if plan.CPUCores > maxCPU {
		msgs = append(msgs, fmt.Sprintf(
			"CPU: requested %d cores, capacity %d × %.0f%% = %d",
			plan.CPUCores, host.CPUCores, threshold*100, maxCPU))
	}
	if plan.MemoryMiB > maxMem {
		msgs = append(msgs, fmt.Sprintf(
			"memory: requested %d MiB, capacity %d × %.0f%% = %d MiB",
			plan.MemoryMiB, host.MemoryMiB, threshold*100, maxMem))
	}
	if host.StorageGB > 0 && plan.StorageGB > maxDisk {
		msgs = append(msgs, fmt.Sprintf(
			"storage: requested %d GB, capacity %d GB × %.0f%% = %d GB",
			plan.StorageGB, host.StorageGB, threshold*100, maxDisk))
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("planned clusters exceed %.0f%% of available Proxmox host resources:\n  %s",
		threshold*100, strings.Join(msgs, "\n  "))
}

// --- helpers ---

func atoiOr(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func atoi64Or(s string, def int64) int64 {
	return int64(atoiOr(s, int(def)))
}