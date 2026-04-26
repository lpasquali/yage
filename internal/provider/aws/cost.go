package aws

import (
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// AWS overhead is **shape**, not money. Component counts (NAT GWs,
// ALBs, NLBs, dataTransferGB, cloudwatchGB, route53Zones) are
// architecture choices that stay constant. Every $/month number in
// this file comes from internal/pricing — AWS Bulk Pricing JSON,
// region-aware, on-demand Linux/Shared/NA tenancy SKUs.

// EstimateMonthlyCostUSD computes an AWS bill for the planned
// cluster against live Bulk Pricing JSON. Returns ErrNotApplicable
// when any priced dimension is unreachable — the orchestrator
// surfaces "AWS estimate unavailable" rather than fabricate.
//
// Switches on cfg.Providers.AWS.Mode:
//   - "unmanaged" (default): self-managed Kubernetes on EC2.
//   - "eks": EKS-managed control plane (live priced) + EC2 workers.
//   - "eks-fargate": EKS CP + Fargate workers (live vCPU/GB-hour).
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.Providers.AWS.Region, "us-east-1")
	mode := orDefault(cfg.Providers.AWS.Mode, "unmanaged")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpType := orDefault(cfg.Providers.AWS.ControlPlaneMachineType, "t3.large")
	wkType := orDefault(cfg.Providers.AWS.NodeMachineType, "t3.medium")

	cpDiskGB := atoiOr(cfg.Providers.Proxmox.ControlPlaneBootVolumeSize, 30)
	wkDiskGB := atoiOr(cfg.Providers.Proxmox.WorkerBootVolumeSize, 40)

	gp3GB, err := pricing.Fetch("aws", "ebs:gp3", region)
	if err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: aws ebs gp3 %s: %v", provider.ErrNotApplicable, region, err)
	}
	cpDiskCost := float64(cpDiskGB) * gp3GB.USDPerMonth
	wkDiskCost := float64(wkDiskGB) * gp3GB.USDPerMonth

	items := []provider.CostItem{}

	switch mode {
	case "eks", "eks-fargate":
		eks, err := pricing.AWSEKSControlPlaneUSDPerMonth(region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: aws eks cp %s: %v", provider.ErrNotApplicable, region, err)
		}
		items = append(items, provider.CostItem{
			Name:           "EKS managed control plane (flat per cluster, live)",
			UnitUSDMonthly: eks,
			Qty:            1,
			SubtotalUSD:    eks,
		})
	default: // unmanaged
		if cp > 0 {
			cpPrice, err := liveEC2Monthly(cpType, region)
			if err != nil {
				return provider.CostEstimate{}, fmt.Errorf("%w: aws cp %s/%s: %v", provider.ErrNotApplicable, cpType, region, err)
			}
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
	}

	// Workers: Fargate per-pod (live), or EC2 (live).
	if mode == "eks-fargate" {
		pods := atoiOr(cfg.Providers.AWS.FargatePodCount, 10)
		vcpuPer, gbPer := parseFargateSize(cfg)
		fgVCPUHr, fgGBHr, err := pricing.AWSFargatePerHour(region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: aws fargate %s: %v", provider.ErrNotApplicable, region, err)
		}
		fgVCPU := vcpuPer * pricing.MonthlyHours * fgVCPUHr
		fgMem := gbPer * pricing.MonthlyHours * fgGBHr
		perPod := fgVCPU + fgMem
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("Fargate pods (%.2g vCPU / %.2g GiB each)", vcpuPer, gbPer),
			UnitUSDMonthly: perPod,
			Qty:            pods,
			SubtotalUSD:    perPod * float64(pods),
		})
	} else if wk > 0 {
		wkPrice, err := liveEC2Monthly(wkType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: aws worker %s/%s: %v", provider.ErrNotApplicable, wkType, region, err)
		}
		label := "workload workers"
		if mode == "eks" {
			label = "workload workers (EKS Managed Node Group)"
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("%s (%s)", label, wkType),
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
		mcp := atoiOr(cfg.Mgmt.ControlPlaneMachineCount, 1)
		mgmtType := "t3.medium"
		mgmtPrice, err := liveEC2Monthly(mgmtType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: aws mgmt %s/%s: %v", provider.ErrNotApplicable, mgmtType, region, err)
		}
		mgmtDisk := atoiOr(cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize, 30)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("mgmt control-plane (%s)", mgmtType),
			UnitUSDMonthly: mgmtPrice,
			Qty:            mcp,
			SubtotalUSD:    mgmtPrice * float64(mcp),
		})
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("mgmt boot volumes (%d GB gp3 each)", mgmtDisk),
			UnitUSDMonthly: float64(mgmtDisk) * gp3GB.USDPerMonth,
			Qty:            mcp,
			SubtotalUSD:    float64(mgmtDisk) * gp3GB.USDPerMonth * float64(mcp),
		})
	}

	// Service overhead.
	items, err = addServiceOverhead(items, cfg, region)
	if err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: aws overhead: %v", provider.ErrNotApplicable, err)
	}

	total := 0.0
	for _, it := range items {
		total += it.SubtotalUSD
	}

	tierLabel := orDefault(cfg.Providers.AWS.OverheadTier, "prod")
	noteBase := fmt.Sprintf("region %s, %s overhead tier (NAT/ALB/CloudWatch/Route53/egress).", region, tierLabel)
	note := ""
	switch mode {
	case "eks":
		note = noteBase + " EKS CP + Managed Node Group EC2 + EBS gp3 (live AWS Bulk Pricing JSON)."
	case "eks-fargate":
		note = noteBase + " EKS CP + Fargate per-pod-hour (live AWS Bulk Pricing JSON)."
	default:
		note = noteBase + " Self-managed CP + EC2 workers + EBS gp3 (live AWS Bulk Pricing JSON)."
	}
	return provider.CostEstimate{
		TotalUSDMonthly: total,
		Items:           items,
		Note:            note,
	}, nil
}

func liveEC2Monthly(instanceType, region string) (float64, error) {
	it, err := pricing.Fetch("aws", instanceType, region)
	if err != nil {
		return 0, err
	}
	return it.USDPerMonth, nil
}

func parseFargateSize(cfg *config.Config) (vcpu, gib float64) {
	vcpu = parseFloatOr(cfg.Providers.AWS.FargatePodCPU, 0.5)
	gib = parseFloatOr(cfg.Providers.AWS.FargatePodMemoryGiB, 1.0)
	return
}

func parseFloatOr(s string, def float64) float64 {
	if strings.TrimSpace(s) == "" {
		return def
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil || f <= 0 {
		return def
	}
	return f
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
