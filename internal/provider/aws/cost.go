package aws

import (
	"fmt"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
)

// awsPriceTable is the on-demand monthly cost (us-east-1, Linux,
// 730 hours/month) for the EC2 instance types we commonly suggest.
// Numbers are deliberately approximate — they're a napkin estimator
// surfaced in the dry-run plan, not a billing source of truth.
//
// Instance types missing from the table fall back to a heuristic
// based on the family + size suffix; if even that doesn't match,
// we report 0 and add a "(unknown)" tag in the breakdown.
//
// Source for the seed values: AWS public on-demand pricing as of
// late 2024; refresh as needed. ARM (Graviton) pricing is ~20 %
// cheaper than the x86 equivalent.
var awsPriceTable = map[string]float64{
	// Burstable / general-purpose (x86)
	"t3.nano":   3.80,
	"t3.micro":  7.59,
	"t3.small":  15.18,
	"t3.medium": 30.37,
	"t3.large":  60.74,
	"t3.xlarge": 121.47,
	"t3.2xlarge": 242.94,

	// Burstable Graviton (ARM, ~20 % cheaper)
	"t4g.nano":   3.07,
	"t4g.micro":  6.13,
	"t4g.small":  12.26,
	"t4g.medium": 24.53,
	"t4g.large":  49.06,
	"t4g.xlarge": 98.11,

	// General-purpose m5 family
	"m5.large":    70.08,
	"m5.xlarge":   140.16,
	"m5.2xlarge":  280.32,
	"m5.4xlarge":  560.64,
	"m5.8xlarge":  1121.28,
	"m5.16xlarge": 2242.56,
	"m5.24xlarge": 3363.84,

	// Compute-optimised c5
	"c5.large":   62.05,
	"c5.xlarge":  124.10,
	"c5.2xlarge": 248.20,
	"c5.4xlarge": 496.40,

	// Memory-optimised r5
	"r5.large":   91.98,
	"r5.xlarge":  183.96,
	"r5.2xlarge": 367.92,
	"r5.4xlarge": 735.84,
}

// ebsGp3PerGBMonth is the per-GB-month cost of gp3 EBS storage
// (us-east-1). 3000 IOPS + 125 MB/s baseline are bundled in the
// base price.
const ebsGp3PerGBMonth = 0.08

// EstimateMonthlyCostUSD computes a napkin AWS bill for the planned
// cluster. EC2 + EBS only — does not count NAT Gateway, ELB,
// CloudWatch, Route53, data egress (those are workload-shape
// dependent and out of scope for a planning estimate).
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpType := orDefault(cfg.AWSControlPlaneMachineType, "t3.large")
	wkType := orDefault(cfg.AWSNodeMachineType, "t3.medium")

	cpPrice := lookupOrEstimate(cpType)
	wkPrice := lookupOrEstimate(wkType)

	cpDiskGB := atoiOr(cfg.ControlPlaneBootVolumeSize, 30)
	wkDiskGB := atoiOr(cfg.WorkerBootVolumeSize, 40)
	cpDiskCost := float64(cpDiskGB) * ebsGp3PerGBMonth
	wkDiskCost := float64(wkDiskGB) * ebsGp3PerGBMonth

	items := []provider.CostItem{}
	if cp > 0 {
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload control-plane (%s)", cpType),
			UnitUSDMonthly: cpPrice,
			Qty:            cp,
			SubtotalUSD:    cpPrice * float64(cp),
		})
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("CP boot volumes (%d GB gp3 each)", cpDiskGB),
			UnitUSDMonthly: cpDiskCost,
			Qty:            cp,
			SubtotalUSD:    cpDiskCost * float64(cp),
		})
	}
	if wk > 0 {
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload workers (%s)", wkType),
			UnitUSDMonthly: wkPrice,
			Qty:            wk,
			SubtotalUSD:    wkPrice * float64(wk),
		})
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("worker boot volumes (%d GB gp3 each)", wkDiskGB),
			UnitUSDMonthly: wkDiskCost,
			Qty:            wk,
			SubtotalUSD:    wkDiskCost * float64(wk),
		})
	}
	if cfg.PivotEnabled {
		mcp := atoiOr(cfg.MgmtControlPlaneMachineCount, 1)
		// Mgmt typically reuses the CP instance type unless the user
		// overrides; we don't track a separate mgmt instance type
		// today, so estimate at a smaller default tier.
		mgmtType := "t3.medium"
		mgmtPrice := lookupOrEstimate(mgmtType)
		mgmtDisk := atoiOr(cfg.MgmtControlPlaneBootVolumeSize, 30)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("mgmt control-plane (%s)", mgmtType),
			UnitUSDMonthly: mgmtPrice,
			Qty:            mcp,
			SubtotalUSD:    mgmtPrice * float64(mcp),
		})
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("mgmt boot volumes (%d GB gp3 each)", mgmtDisk),
			UnitUSDMonthly: float64(mgmtDisk) * ebsGp3PerGBMonth,
			Qty:            mcp,
			SubtotalUSD:    float64(mgmtDisk) * ebsGp3PerGBMonth * float64(mcp),
		})
	}

	total := 0.0
	for _, it := range items {
		total += it.SubtotalUSD
	}

	return provider.CostEstimate{
		TotalUSDMonthly: total,
		Items:           items,
		Note:            "us-east-1 on-demand prices (730 h/month), gp3 EBS only. Excludes NAT Gateway, ELB, data transfer, CloudWatch, Route53. Spot pricing typically saves 60-70 %.",
	}, nil
}

// lookupOrEstimate returns the price-table value for itype, or a
// rough family-based fallback when the exact size isn't in the
// table. Returns 0 (and a "(unknown)" via caller display) when
// nothing matches.
func lookupOrEstimate(itype string) float64 {
	if v, ok := awsPriceTable[itype]; ok {
		return v
	}
	// Heuristic: parse "<family>.<size>" and approximate.
	parts := strings.SplitN(itype, ".", 2)
	if len(parts) != 2 {
		return 0
	}
	// Pick a family base price (per-vCPU per-month rough scale).
	familyPerVCPU := 35.0 // m5/general
	switch {
	case strings.HasPrefix(parts[0], "t4g") || strings.HasPrefix(parts[0], "a1"):
		familyPerVCPU = 12.0 // ARM burstable
	case strings.HasPrefix(parts[0], "t3") || strings.HasPrefix(parts[0], "t2"):
		familyPerVCPU = 15.0 // x86 burstable
	case strings.HasPrefix(parts[0], "c5") || strings.HasPrefix(parts[0], "c6"):
		familyPerVCPU = 30.0
	case strings.HasPrefix(parts[0], "r5") || strings.HasPrefix(parts[0], "r6"):
		familyPerVCPU = 45.0
	}
	// Size suffix → vCPUs (rough table; AWS sizes double with each step).
	vcpu := map[string]int{
		"nano": 1, "micro": 1, "small": 1, "medium": 2,
		"large": 2, "xlarge": 4, "2xlarge": 8, "4xlarge": 16,
		"8xlarge": 32, "12xlarge": 48, "16xlarge": 64, "24xlarge": 96,
	}[parts[1]]
	if vcpu == 0 {
		return 0
	}
	return float64(vcpu) * familyPerVCPU
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func atoiOr(s string, def int) int {
	var n int
	for _, r := range s {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
			continue
		}
		break
	}
	if n == 0 {
		return def
	}
	return n
}
