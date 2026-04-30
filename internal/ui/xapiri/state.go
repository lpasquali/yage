// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// state.go — shared state for the xapiri dashboard.
//
// We carry the user's answers across the dashboard lifecycle on a single
// *state value rather than threading a dozen positional args. The
// fields fall in three buckets:
//
//   - cfg / w: the inputs threaded in from Run(). cfg is the
//     resolved config we mutate; w is the writer the dashboard speaks on.
//   - fork / workload: dashboard-local answers that are not on
//     cfg. Stamp them into cfg fields the rest of yage already
//     reads, like ControlPlaneMachineCount.
//   - geo*: cached outbound-IP geolocation for region hints.

import (
	"io"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// forkType discriminates the on-prem vs cloud branches of the
// dashboard. forkUnknown is the "ask explicitly" outcome from
// auto-detection.
type forkType int

const (
	forkUnknown forkType = iota
	forkOnPrem
	forkCloud
)

func (f forkType) String() string {
	switch f {
	case forkOnPrem:
		return "on-prem"
	case forkCloud:
		return "cloud"
	}
	return "unknown"
}

// envTier captures the dev/staging/prod choice. Drives Argo /
// Hubble UI / monitoring add-ons and replica counts. Uses string
// values so the persisted Secret is human-readable.
type envTier string

const (
	envDev     envTier = "dev"
	envStaging envTier = "staging"
	envProd    envTier = "prod"
)

// resilienceTier captures the resilience choice. The set of
// valid values differs per fork (on-prem caps at HA-across-hosts;
// cloud goes up to HA-multi-region). Drives cp_nodes and (cloud-
// only) NAT GW / multi-AZ replica counts.
type resilienceTier string

const (
	resilienceSingle  resilienceTier = "single"   // single-host on-prem / single-AZ cloud
	resilienceHA      resilienceTier = "ha"        // HA across hosts (on-prem) / HA single-region (cloud)
	resilienceHAMulti resilienceTier = "ha-multi"  // cloud-only: HA across regions
)

// appBucket is one (count × template) pair. The user
// can mix multiple buckets ("6 medium + 2 heavy") so we store a
// slice on workloadShape.
type appBucket struct {
	Count    int
	Template string // "light" | "medium" | "heavy"
}

// workloadShape collects the workload sizing answers. Held local to xapiri
// rather than on cfg until cfg.Workload exists; once it does,
// these fields can move over to cfg.Workload and the feasibility
// shim wires up directly.
type workloadShape struct {
	Apps        []appBucket
	DBGB        int
	EgressGBMo  int  // 0 on on-prem; required on cloud
	HasQueue    bool
	HasObjStore bool
	HasCache    bool
	// Per-add-on resource sizing (0 = use cost.SubstituteFootprint default).
	// Stamped into cfg.MQ*/ObjStore*/Cache*Override by syncWorkloadShapeToCfg.
	QueueCPUMilli    int
	QueueMemMiB      int
	QueueVolGB       int
	ObjStoreCPUMilli int
	ObjStoreMemMiB   int
	ObjStoreVolGB    int
	CacheCPUMilli    int
	CacheMemMiB      int
}

// state is the dashboard's mutable bag.
type state struct {
	w   io.Writer
	cfg *config.Config

	fork     forkType
	env      envTier
	resil    resilienceTier
	workload workloadShape

	// Cloud-fork-only: the budget the user typed plus the headroom percent.
	// budgetAfterHeadroom = budget × (1 - hp).
	budgetUSDMonth      float64
	headroomPct         float64
	budgetAfterHeadroom float64

	// feasibilityErr, when non-nil, captures the most recent
	// feasibility-gate verdict the dashboard can't satisfy.
	feasibilityErr error

	// deployNow is set by the final confirm prompt. main can
	// read it via Run's return code (we still return 0, but the
	// caller has cfg in hand and the orchestrator picks up from
	// there on next run regardless).
	deployNow bool

	// geo* caches outbound-IP geolocation (GeoJS) once per run so
	// the dashboard can align blank provider regions for cost compare.
	// Disabled when airgapped or YAGE_XAPIRI_NO_GEO=1.
	geoDidLookup bool
	geoOK        bool
	geoLat       float64
	geoLon       float64
	geoLabel     string
}

// newState constructs a state for the dashboard.
func newState(w io.Writer, cfg *config.Config) *state {
	s := &state{
		w:           w,
		cfg:         cfg,
		headroomPct: 0.20,
	}
	s.initFromConfig(cfg)
	return s
}

// initFromConfig seeds dashboard-local state from a previously
// persisted cfg.Workload so that the dashboard opens with the saved
// values rather than zero/empty.
func (s *state) initFromConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	// Restore fork from saved InfraProvider so the dashboard opens in
	// the correct on-prem/cloud view without a manual mode switch.
	// When InfraProvider is not yet set, fall back to env-var heuristics.
	if cfg.InfraProvider == "" {
		s.fork = detectFork(cfg)
	} else if provider.AirgapCompatible(cfg.InfraProvider) {
		s.fork = forkOnPrem
	} else {
		s.fork = forkCloud
	}
	switch cfg.Workload.Environment {
	case "staging":
		s.env = envStaging
	case "prod":
		s.env = envProd
	case "dev":
		s.env = envDev
	}
	switch cfg.Workload.Resilience {
	case "ha":
		s.resil = resilienceHA
	case "ha-mr":
		s.resil = resilienceHAMulti
	case "single":
		s.resil = resilienceSingle
	}
	s.workload.DBGB = cfg.Workload.DatabaseGB
	s.workload.EgressGBMo = cfg.Workload.EgressGBMonth
	s.workload.HasQueue = cfg.Workload.HasQueue
	s.workload.HasObjStore = cfg.Workload.HasObjStore
	s.workload.HasCache = cfg.Workload.HasCache
	s.workload.QueueCPUMilli = cfg.MQCPUMillicoresOverride
	s.workload.QueueMemMiB = cfg.MQMemoryMiBOverride
	s.workload.QueueVolGB = cfg.MQVolumeGBOverride
	s.workload.ObjStoreCPUMilli = cfg.ObjStoreCPUMillicoresOverride
	s.workload.ObjStoreMemMiB = cfg.ObjStoreMemoryMiBOverride
	s.workload.ObjStoreVolGB = cfg.ObjStoreVolumeGBOverride
	s.workload.CacheCPUMilli = cfg.CacheCPUMillicoresOverride
	s.workload.CacheMemMiB = cfg.CacheMemoryMiBOverride
	s.workload.Apps = make([]appBucket, 0, len(cfg.Workload.Apps))
	for _, ag := range cfg.Workload.Apps {
		s.workload.Apps = append(s.workload.Apps, appBucket{Count: ag.Count, Template: ag.Template})
	}
}


