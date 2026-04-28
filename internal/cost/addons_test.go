// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cost

import (
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestEffectiveFootprint_Defaults(t *testing.T) {
	cfg := &config.Config{}
	for _, svc := range []ManagedService{MSMessageQueue, MSObjectStore, MSCache} {
		got := EffectiveFootprint(cfg, svc)
		want := SubstituteFootprint(svc)
		if got.CPUMillicores != want.CPUMillicores || got.MemoryMiB != want.MemoryMiB || got.PersistentGB != want.PersistentGB {
			t.Errorf("EffectiveFootprint(%s) with zero overrides = %+v, want %+v", svc, got, want)
		}
	}
}

func TestEffectiveFootprint_OverridesApplied(t *testing.T) {
	cfg := &config.Config{
		MQCPUMillicoresOverride:       2000,
		MQMemoryMiBOverride:           4096,
		MQVolumeGBOverride:            40,
		ObjStoreCPUMillicoresOverride: 1500,
		ObjStoreMemoryMiBOverride:     3072,
		ObjStoreVolumeGBOverride:      1000,
		CacheCPUMillicoresOverride:    800,
		CacheMemoryMiBOverride:        8192,
	}
	mq := EffectiveFootprint(cfg, MSMessageQueue)
	if mq.CPUMillicores != 2000 || mq.MemoryMiB != 4096 || mq.PersistentGB != 40 {
		t.Errorf("MQ overrides not applied: %+v", mq)
	}
	obj := EffectiveFootprint(cfg, MSObjectStore)
	if obj.CPUMillicores != 1500 || obj.MemoryMiB != 3072 || obj.PersistentGB != 1000 {
		t.Errorf("ObjStore overrides not applied: %+v", obj)
	}
	cache := EffectiveFootprint(cfg, MSCache)
	if cache.CPUMillicores != 800 || cache.MemoryMiB != 8192 {
		t.Errorf("Cache overrides not applied: %+v", cache)
	}
}

func TestEffectiveFootprint_PartialOverride(t *testing.T) {
	cfg := &config.Config{MQCPUMillicoresOverride: 3000}
	got := EffectiveFootprint(cfg, MSMessageQueue)
	base := SubstituteFootprint(MSMessageQueue)
	if got.CPUMillicores != 3000 {
		t.Errorf("CPU override not applied: got %d", got.CPUMillicores)
	}
	if got.MemoryMiB != base.MemoryMiB {
		t.Errorf("unset memory should fall back to default %d, got %d", base.MemoryMiB, got.MemoryMiB)
	}
}

func TestAddonCostItem_DisabledReturnsNoFire(t *testing.T) {
	cfg := &config.Config{
		Workload: config.WorkloadShape{HasQueue: false, HasObjStore: false, HasCache: false},
	}
	for _, svc := range []ManagedService{MSMessageQueue, MSObjectStore, MSCache} {
		_, fired, err := AddonCostItem(cfg, "aws", "us-east-1", svc)
		if err != nil {
			t.Errorf("AddonCostItem(%s) disabled: unexpected error %v", svc, err)
		}
		if fired {
			t.Errorf("AddonCostItem(%s) disabled: expected fired=false", svc)
		}
	}
}

func TestAddonCostItem_EnabledEmitsItem(t *testing.T) {
	cfg := &config.Config{
		Workload: config.WorkloadShape{HasQueue: true, HasObjStore: true, HasCache: true},
		MQCPUMillicoresOverride:       2000,
		MQMemoryMiBOverride:           4096,
		MQVolumeGBOverride:            20,
		ObjStoreCPUMillicoresOverride: 1000,
		ObjStoreMemoryMiBOverride:     2048,
		ObjStoreVolumeGBOverride:      500,
		CacheCPUMillicoresOverride:    500,
		CacheMemoryMiBOverride:        2048,
	}
	for _, svc := range []ManagedService{MSMessageQueue, MSObjectStore, MSCache} {
		item, fired, err := AddonCostItem(cfg, "aws", "us-east-1", svc)
		if err != nil {
			t.Errorf("AddonCostItem(%s): unexpected error %v", svc, err)
		}
		if !fired {
			t.Errorf("AddonCostItem(%s) enabled: expected fired=true", svc)
		}
		if item.SubtotalUSD <= 0 {
			t.Errorf("AddonCostItem(%s): expected positive cost, got %f", svc, item.SubtotalUSD)
		}
	}
}

func TestAddonCostItem_OverrideChangesTotal(t *testing.T) {
	base := &config.Config{Workload: config.WorkloadShape{HasQueue: true}}
	big := &config.Config{
		Workload:              config.WorkloadShape{HasQueue: true},
		MQCPUMillicoresOverride: 8000,
		MQMemoryMiBOverride:     16384,
		MQVolumeGBOverride:      200,
	}
	itemBase, _, _ := AddonCostItem(base, "aws", "us-east-1", MSMessageQueue)
	itemBig, _, _ := AddonCostItem(big, "aws", "us-east-1", MSMessageQueue)
	if itemBig.SubtotalUSD <= itemBase.SubtotalUSD {
		t.Errorf("larger override should produce higher cost: base=%f big=%f", itemBase.SubtotalUSD, itemBig.SubtotalUSD)
	}
}
