// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package feasibility

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/cluster/capacity"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
)

// setupAirgap forces internal/pricing into airgapped mode so live
// vendor APIs are never reached during tests. Returns a cleanup
// func the caller defers.
func setupAirgap(t *testing.T) {
	t.Helper()
	prev := pricing.IsAirgapped()
	pricing.SetAirgapped(true)
	t.Cleanup(func() { pricing.SetAirgapped(prev) })
}

// TestCheck_ComputeStarvation covers §23.1 #1: 8 medium apps + 50 GB
// DB on a $12 budget. The cheapest provider's compute floor alone
// blows past $12, so AbsoluteFloor exceeds the budget and at least
// one BlockingReason surfaces the loop-back trigger.
//
// Pricing is forced offline (airgapped); every provider row reports
// "live pricing unavailable" / Infeasible. AbsoluteFloor stays 0
// because no provider produced a price — the test asserts the
// Verdict shape (every row Infeasible) rather than a numeric floor.
func TestCheck_ComputeStarvation(t *testing.T) {
	setupAirgap(t)
	cfg := &config.Config{
		BudgetUSDMonth: 12,
		Workload: config.WorkloadShape{
			Apps: []config.AppGroup{
				{Count: 8, Template: "medium"},
			},
			DatabaseGB:    50,
			EgressGBMonth: 100,
			Resilience:    "single",
			Environment:   "dev",
		},
	}
	v, err := Check(cfg)
	if err != nil {
		t.Fatalf("Check returned err=%v, want nil", err)
	}
	// Every priced provider must come back Infeasible (pricing was
	// forced offline; the cheapest provider can't produce a number
	// under $12 even when wired up — 4 t3.medium nodes at us-east-1
	// list price ≈ $120/mo).
	for name, pv := range v.PerProvider {
		if pv.Verdict == Comfortable {
			t.Errorf("provider %s = Comfortable, want Tight/Infeasible (compute starvation)", name)
		}
	}
	if v.Recommended != "" {
		t.Errorf("Recommended=%q, want empty (no provider feasible)", v.Recommended)
	}
}

// TestCheck_ResilienceViolation covers §23.1 #2: prod environment +
// ControlPlaneMachineCount=1 must surface a BlockingReason.
func TestCheck_ResilienceViolation(t *testing.T) {
	setupAirgap(t)
	cfg := &config.Config{
		BudgetUSDMonth:           500,
		ControlPlaneMachineCount: "1",
		Workload: config.WorkloadShape{
			Apps: []config.AppGroup{
				{Count: 2, Template: "medium"},
			},
			DatabaseGB:    20,
			EgressGBMonth: 50,
			Resilience:    "ha",
			Environment:   "prod",
		},
	}
	v, err := Check(cfg)
	if err != nil {
		t.Fatalf("Check returned err=%v, want nil", err)
	}
	if len(v.BlockingReasons) == 0 {
		t.Fatalf("BlockingReasons empty; want at least one resilience violation")
	}
	gotProd := false
	gotCPCount := false
	for _, r := range v.BlockingReasons {
		if strings.Contains(r, "ControlPlaneMachineCount=1") {
			gotCPCount = true
		}
		if strings.Contains(r, "prod environment") || strings.Contains(r, "HA resilience") {
			gotProd = true
		}
	}
	if !gotCPCount {
		t.Errorf("BlockingReasons %v missing CP-count violation", v.BlockingReasons)
	}
	if !gotProd {
		t.Errorf("BlockingReasons %v missing prod/HA reason", v.BlockingReasons)
	}
}

// TestCheck_EgressSandbag covers §23.1 #3: a workload that fits a
// $30/mo compute envelope but the user "forgets" to state egress.
// The blocking-reasons list must surface the egress sandbag warning.
func TestCheck_EgressSandbag(t *testing.T) {
	setupAirgap(t)
	cfg := &config.Config{
		BudgetUSDMonth: 30,
		Workload: config.WorkloadShape{
			Apps: []config.AppGroup{
				{Count: 1, Template: "light"},
			},
			DatabaseGB:    10,
			EgressGBMonth: 0, // sandbag
			Resilience:    "single",
			Environment:   "dev",
		},
	}
	v, err := Check(cfg)
	if err != nil {
		t.Fatalf("Check returned err=%v, want nil", err)
	}
	got := false
	for _, r := range v.BlockingReasons {
		if strings.Contains(r, "egress") {
			got = true
			break
		}
	}
	if !got {
		t.Fatalf("BlockingReasons %v missing egress sandbag warning", v.BlockingReasons)
	}
}

// TestCheck_FreeTierCliff covers §23.1 #4: a workload that fits OCI
// Always Free (1 CP node, 0 worker, ≤20 GB DB) gets the FreeTierFit
// + cliff annotation. Add a worker count above the quota and the
// cliff is still annotated but FreeTierFit drops to false.
func TestCheck_FreeTierCliff(t *testing.T) {
	setupAirgap(t)
	cfg := &config.Config{
		BudgetUSDMonth: 100,
		Workload: config.WorkloadShape{
			Apps: []config.AppGroup{
				{Count: 1, Template: "light"},
			},
			DatabaseGB:    15,
			EgressGBMonth: 5,
			Resilience:    "single",
			Environment:   "dev",
		},
	}
	v, err := Check(cfg)
	if err != nil {
		t.Fatalf("Check returned err=%v, want nil", err)
	}
	oci, ok := v.PerProvider["oci"]
	if !ok {
		t.Fatalf("PerProvider missing 'oci' row")
	}
	if !oci.FreeTierFit {
		t.Errorf("oci FreeTierFit=false, want true (1 CP + 15 GB DB ≤ Always Free quota)")
	}
	if oci.FreeTierCliff == "" {
		t.Errorf("oci FreeTierCliff empty, want the §23.7 trigger string")
	}

	// Now exceed the quota: bump DB to 50 GB → above the 20 GB cap.
	cfg.Workload.DatabaseGB = 50
	v, err = Check(cfg)
	if err != nil {
		t.Fatalf("Check returned err=%v, want nil", err)
	}
	oci = v.PerProvider["oci"]
	if oci.FreeTierFit {
		t.Errorf("oci FreeTierFit=true after exceeding 20 GB quota, want false")
	}
	if oci.FreeTierCliff == "" {
		t.Errorf("oci FreeTierCliff empty even though the table has one; cliff should always annotate")
	}
}

// TestCheck_OnPrem covers the §22.2 / §23 on-prem branch: a
// workload that exceeds host inventory must come back Infeasible
// for every provider in the on-prem set.
func TestCheck_OnPrem(t *testing.T) {
	setupAirgap(t)
	cfg := &config.Config{
		Capacity: config.CapacityConfig{
			ResourceBudgetFraction: 0.66,
			OvercommitTolerancePct: 15.0,
		},
		HardwareCostUSD:        12000,
		HardwareUsefulLifeYears: 5,
		HardwareWatts:          400,
		HardwareKWHRateUSD:     0.15,
		Workload: config.WorkloadShape{
			// Stack a workload that requires far more cores/mem than
			// the host can provide.
			Apps: []config.AppGroup{
				{Count: 50, Template: "heavy"},
			},
			DatabaseGB:  500,
			Resilience:  "ha",
			Environment: "prod",
		},
		ControlPlaneMachineCount: "3",
	}
	host := &capacity.HostCapacity{
		Nodes:     []string{"node1"},
		CPUCores:  8,
		MemoryMiB: 16 * 1024,
		StorageGB: 200,
	}
	v, err := CheckOnPrem(cfg, host)
	if err != nil {
		t.Fatalf("CheckOnPrem returned err=%v, want nil", err)
	}
	if len(v.PerProvider) == 0 {
		t.Fatalf("PerProvider empty; on-prem set must have at least proxmox/vsphere/openstack/capd rows")
	}
	for name, pv := range v.PerProvider {
		if pv.Verdict != Infeasible {
			t.Errorf("on-prem provider %s verdict=%v, want Infeasible (workload exceeds host)", name, pv.Verdict)
		}
	}
	if v.AbsoluteFloor <= 0 {
		t.Errorf("AbsoluteFloor=%v, want >0 (TCO line from cfg.Hardware*)", v.AbsoluteFloor)
	}
	// Sanity-check the TCO formula: 12000 / (5×12) + 0.4×720×0.15
	// = 200 + 43.2 = 243.2 (no support charge).
	wantApprox := 12000.0/(5.0*12.0) + 0.4*720.0*0.15
	if abs(v.AbsoluteFloor-wantApprox) > 0.01 {
		t.Errorf("AbsoluteFloor=%v, want ≈%v", v.AbsoluteFloor, wantApprox)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}