package capacity

import (
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func tinyHost() *HostCapacity {
	return &HostCapacity{
		Nodes:     []string{"pve"},
		CPUCores:  12,
		MemoryMiB: 8 * 1024, // 8 GiB
		StorageGB: 200,
	}
}

func defaultishCfg() *config.Config {
	c := &config.Config{}
	c.Providers.Proxmox.ControlPlaneNumSockets = "1"
	c.Providers.Proxmox.ControlPlaneNumCores = "2"
	c.Providers.Proxmox.ControlPlaneMemoryMiB = "4096"
	c.Providers.Proxmox.ControlPlaneBootVolumeSize = "40"
	c.ControlPlaneMachineCount = "1"
	c.Providers.Proxmox.WorkerNumSockets = "1"
	c.Providers.Proxmox.WorkerNumCores = "2"
	c.Providers.Proxmox.WorkerMemoryMiB = "4096"
	c.Providers.Proxmox.WorkerBootVolumeSize = "40"
	c.WorkerMachineCount = "2"
	return c
}

// TestKubeadmOverflowK3sFits — host has 8 GiB / 12 cores. Plan budget
// is 2/3 = 5.33 GiB / 8 cores / 133 GB. The default kubeadm plan
// (12 GiB total) overflows memory but k3s (3-4 GiB total) fits.
func TestKubeadmOverflowK3sFits(t *testing.T) {
	cfg := defaultishCfg()
	hc := tinyHost()
	threshold := DefaultThreshold

	plan := PlanFor(cfg)
	if err := Check(plan, hc, threshold); err == nil {
		t.Fatalf("kubeadm Check should overflow on tiny host: plan=%+v host=%+v", plan, hc)
	}
	fits, k3sPlan := WouldFitAsK3s(cfg, hc, threshold)
	if !fits {
		t.Fatalf("k3s plan should fit on tiny host: %+v", k3sPlan)
	}
	if k3sPlan.MemoryMiB >= plan.MemoryMiB {
		t.Errorf("k3s memory %d should be < kubeadm %d", k3sPlan.MemoryMiB, plan.MemoryMiB)
	}
}

// TestK3sOverflowAlsoFails — when even k3s sizing would overflow (host
// truly tiny), WouldFitAsK3s returns false so the suggestion isn't
// surfaced.
func TestK3sOverflowAlsoFails(t *testing.T) {
	cfg := defaultishCfg()
	cfg.WorkerMachineCount = "20" // 20 workers won't fit even at k3s sizing
	hc := tinyHost()
	fits, _ := WouldFitAsK3s(cfg, hc, DefaultThreshold)
	if fits {
		t.Fatalf("k3s with 20 workers should NOT fit on tiny host")
	}
}

// TestAllocationsThirdsAfterReserve — with 2 workers × 2 cores × 4 GiB
// the cluster has 4 cores / 8 GiB total. Default reserve is 2 cores /
// 4 GiB → remainder 2 cores / 4 GiB → each of three buckets gets
// 666 mCPU / 1365 MiB.
func TestAllocationsThirdsAfterReserve(t *testing.T) {
	cfg := defaultishCfg()
	cfg.SystemAppsCPUMillicores = 2000
	cfg.SystemAppsMemoryMiB = 4096
	a := AllocationsFor(cfg)
	if a.TotalCPUMillicores != 4000 || a.TotalMemoryMiB != 8192 {
		t.Fatalf("total wrong: %+v", a)
	}
	if a.RemainCPUMillicores != 2000 || a.RemainMemoryMiB != 4096 {
		t.Fatalf("remainder wrong: %+v", a)
	}
	wantBucketCPU := 2000 / 3
	wantBucketMem := int64(4096 / 3)
	if a.BucketCPUMillicores != wantBucketCPU || a.BucketMemoryMiB != wantBucketMem {
		t.Fatalf("bucket wrong: got %d/%d want %d/%d",
			a.BucketCPUMillicores, a.BucketMemoryMiB, wantBucketCPU, wantBucketMem)
	}
	if a.IsOverReserved() {
		t.Errorf("default config should not be over-reserved")
	}
}

// TestAllocationsOverReserved — system reserve exceeds workers when
// the cluster is too small.
func TestAllocationsOverReserved(t *testing.T) {
	cfg := defaultishCfg()
	cfg.WorkerMachineCount = "1"
	cfg.SystemAppsCPUMillicores = 8000 // way more than 1 worker provides
	cfg.SystemAppsMemoryMiB = 16384
	a := AllocationsFor(cfg)
	if !a.IsOverReserved() {
		t.Fatalf("should be over-reserved: %+v", a)
	}
}

// TestPivotAddsMgmt — k3s plan includes mgmt cluster only when pivot
// is enabled.
func TestPivotAddsMgmt(t *testing.T) {
	cfg := defaultishCfg()
	cfg.PivotEnabled = true
	cfg.Mgmt.ControlPlaneMachineCount = "1"
	cfg.Mgmt.WorkerMachineCount = "0"
	p := PlanForK3s(cfg)
	hasMgmt := false
	for _, it := range p.Items {
		if it.Name == "mgmt control-plane (k3s)" {
			hasMgmt = true
		}
	}
	if !hasMgmt {
		t.Fatalf("PlanForK3s with PivotEnabled should include mgmt CP item: %+v", p.Items)
	}
}
