// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package capacity

import (
	"github.com/lpasquali/yage/internal/config"
)

// WorkloadAllocations describes how the workload cluster's worker
// capacity is divided up at planning time. The four buckets:
//
//   1. SystemApps   — fixed reserve for kyverno / cert-manager /
//                     proxmox-csi / argocd / keycloak / external-secrets
//                     / infisical (the add-ons yage installs).
//   2. Database     — 1/3 of the remainder: cnpg, postgres operators,
//                     anything stateful needing a chunk of RAM + IOPS.
//   3. Observability — 1/3 of the remainder: vmsingle / victoria-metrics,
//                      OpenTelemetry collector, Grafana, Loki etc.
//   4. Product      — 1/3 of the remainder: the user's applications,
//                     Argo CD-deployed workloads. yage has
//                     no opinion on what lives here.
//
// These are PLANNING numbers — they tell the operator what fits, and
// can drive ResourceQuota generation in the workload-app-of-apps repo
// (follow-up). The cluster API server still admits anything Argo CD
// applies; a real ResourceQuota stops over-allocation at admission.
type WorkloadAllocations struct {
	// TotalCPUMillicores / TotalMemoryMiB are the sum of all worker
	// machines' (sockets × cores × 1000) and memory. Control-plane
	// VMs are not counted — by default the kubeadm scheduler taints
	// them NoSchedule and only system pods land there.
	TotalCPUMillicores int
	TotalMemoryMiB     int64

	SystemAppsCPUMillicores int
	SystemAppsMemoryMiB     int64

	RemainCPUMillicores int
	RemainMemoryMiB     int64

	// Each bucket gets RemainCPUMillicores / 3 and RemainMemoryMiB / 3.
	// Stored as a single Bucket value applied to all three.
	BucketCPUMillicores int
	BucketMemoryMiB     int64
}

// AllocationsFor computes the workload-cluster-side allocation plan.
// Worker machine count drives the total; system reserve is taken from
// cfg.SystemAppsCPUMillicores / SystemAppsMemoryMiB; remainder splits
// into thirds.
//
// Negative remainders indicate over-reserve: the system-apps bucket
// alone exceeds total worker capacity. Caller surfaces that as a
// warning ("system reserve > workers — pin add-ons to control-plane
// or grow worker count").
func AllocationsFor(cfg *config.Config) WorkloadAllocations {
	workers := atoiOr(cfg.WorkerMachineCount, 0)
	cpuPer := atoiOr(cfg.Providers.Proxmox.WorkerNumSockets, 1) * atoiOr(cfg.Providers.Proxmox.WorkerNumCores, 1) * 1000
	memPer := atoi64Or(cfg.Providers.Proxmox.WorkerMemoryMiB, 0)

	totalCPU := cpuPer * workers
	totalMem := memPer * int64(workers)

	sysCPU := cfg.SystemAppsCPUMillicores
	sysMem := cfg.SystemAppsMemoryMiB
	if sysCPU == 0 {
		sysCPU = 2000
	}
	if sysMem == 0 {
		sysMem = 4096
	}

	remainCPU := totalCPU - sysCPU
	remainMem := totalMem - sysMem

	// Floor remain at 0 in the public struct to avoid a misleading
	// "negative" bucket; the caller checks remain<0 separately to
	// report over-reserve.
	bucketCPU := 0
	bucketMem := int64(0)
	if remainCPU > 0 {
		bucketCPU = remainCPU / 3
	}
	if remainMem > 0 {
		bucketMem = remainMem / 3
	}

	return WorkloadAllocations{
		TotalCPUMillicores:      totalCPU,
		TotalMemoryMiB:          totalMem,
		SystemAppsCPUMillicores: sysCPU,
		SystemAppsMemoryMiB:     sysMem,
		RemainCPUMillicores:     remainCPU,
		RemainMemoryMiB:         remainMem,
		BucketCPUMillicores:     bucketCPU,
		BucketMemoryMiB:         bucketMem,
	}
}

// IsOverReserved reports whether the system-apps reserve alone
// exceeds the workload cluster's total worker capacity.
func (a WorkloadAllocations) IsOverReserved() bool {
	return a.RemainCPUMillicores < 0 || a.RemainMemoryMiB < 0
}