// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cost

import (
	"fmt"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// EffectiveFootprint returns the operator-overridden InClusterFootprint for
// svc, falling back to SubstituteFootprint per-field when the override is 0.
// A partial override (e.g. only CPU set) is honoured: unset fields keep the
// SubstituteFootprint default.
func EffectiveFootprint(cfg *config.Config, svc ManagedService) InClusterFootprint {
	base := SubstituteFootprint(svc)
	switch svc {
	case MSMessageQueue:
		if cfg.MQCPUMillicoresOverride > 0 {
			base.CPUMillicores = cfg.MQCPUMillicoresOverride
		}
		if cfg.MQMemoryMiBOverride > 0 {
			base.MemoryMiB = cfg.MQMemoryMiBOverride
		}
		if cfg.MQVolumeGBOverride > 0 {
			base.PersistentGB = cfg.MQVolumeGBOverride
		}
	case MSObjectStore:
		if cfg.ObjStoreCPUMillicoresOverride > 0 {
			base.CPUMillicores = cfg.ObjStoreCPUMillicoresOverride
		}
		if cfg.ObjStoreMemoryMiBOverride > 0 {
			base.MemoryMiB = cfg.ObjStoreMemoryMiBOverride
		}
		if cfg.ObjStoreVolumeGBOverride > 0 {
			base.PersistentGB = cfg.ObjStoreVolumeGBOverride
		}
	case MSCache:
		if cfg.CacheCPUMillicoresOverride > 0 {
			base.CPUMillicores = cfg.CacheCPUMillicoresOverride
		}
		if cfg.CacheMemoryMiBOverride > 0 {
			base.MemoryMiB = cfg.CacheMemoryMiBOverride
		}
	}
	return base
}

// AddonCostItem returns a CostItem for one add-on slot using an in-cluster
// substitute forecast based on market-rate compute costs. Returns
// (item, false, nil) when the add-on is disabled in cfg.Workload.
//
// The forecast uses approximate blended AWS/GCP rates:
//   - $0.048 / vCPU-hour (≈ t3.medium blended on-demand, non-EC2 platforms are similar)
//   - $0.006 / GiB-hour  (memory)
//   - $0.08  / GB-month  (persistent volume, ≈ EBS gp3)
//
// These are labelled as estimates in the returned item name. Vendors that
// offer a priced SaaS equivalent (per vendorOffers) will get a more accurate
// line when per-service catalog integration lands; this fallback ensures the
// user's resource values always move the cost comparison.
func AddonCostItem(cfg *config.Config, _ string, _ string, svc ManagedService) (provider.CostItem, bool, error) {
	switch svc {
	case MSMessageQueue:
		if !cfg.Workload.HasQueue {
			return provider.CostItem{}, false, nil
		}
	case MSObjectStore:
		if !cfg.Workload.HasObjStore {
			return provider.CostItem{}, false, nil
		}
	case MSCache:
		if !cfg.Workload.HasCache {
			return provider.CostItem{}, false, nil
		}
	default:
		return provider.CostItem{}, false, nil
	}

	fp := EffectiveFootprint(cfg, svc)

	const (
		vcpuHourUSD    = 0.048
		gibHourUSD     = 0.006
		hoursPerMonth  = 730.0
		gbPerMonthUSD  = 0.08
	)

	cpuMonthly := float64(fp.CPUMillicores) / 1000.0 * vcpuHourUSD * hoursPerMonth
	memMonthly := float64(fp.MemoryMiB) / 1024.0 * gibHourUSD * hoursPerMonth
	volMonthly := float64(fp.PersistentGB) * gbPerMonthUSD

	total := cpuMonthly + memMonthly + volMonthly
	if total == 0 {
		return provider.CostItem{}, false, nil
	}

	label := addonLabel(svc)
	name := fmt.Sprintf("%s (in-cluster est.: %dm CPU / %d MiB / %d GB)", label, fp.CPUMillicores, fp.MemoryMiB, fp.PersistentGB)
	return provider.CostItem{
		Name:           name,
		UnitUSDMonthly: total,
		Qty:            1,
		SubtotalUSD:    total,
	}, true, nil
}

func addonLabel(svc ManagedService) string {
	switch svc {
	case MSMessageQueue:
		return "message queue"
	case MSObjectStore:
		return "object storage"
	case MSCache:
		return "in-memory cache"
	}
	return string(svc)
}
