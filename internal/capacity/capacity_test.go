package capacity

import (
	"testing"

	"github.com/lpasquali/bootstrap-capi/internal/config"
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
	c.ControlPlaneNumSockets = "1"
	c.ControlPlaneNumCores = "2"
	c.ControlPlaneMemoryMiB = "4096"
	c.ControlPlaneBootVolumeSize = "40"
	c.ControlPlaneMachineCount = "1"
	c.WorkerNumSockets = "1"
	c.WorkerNumCores = "2"
	c.WorkerMemoryMiB = "4096"
	c.WorkerBootVolumeSize = "40"
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

// TestPivotAddsMgmt — k3s plan includes mgmt cluster only when pivot
// is enabled.
func TestPivotAddsMgmt(t *testing.T) {
	cfg := defaultishCfg()
	cfg.PivotEnabled = true
	cfg.MgmtControlPlaneMachineCount = "1"
	cfg.MgmtWorkerMachineCount = "0"
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
