package ibmcloud

import (
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// EstimateMonthlyCostUSD computes an IBM Cloud VPC Gen2 bill from
// live Global Catalog data. Profile IDs look like "bx2-2x8" (general
// purpose, 2 vCPU, 8 GB) or "cx2-4x8" (compute optimised).
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.IBMCloudRegion, "us-south")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpProfile := orDefault(cfg.IBMCloudControlPlaneProfile, "bx2-2x8")
	wkProfile := orDefault(cfg.IBMCloudNodeProfile, "bx2-2x8")

	items := []provider.CostItem{}
	if cp > 0 {
		price, err := liveIBMMonthly(cpProfile, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: ibmcloud cp %s/%s: %v",
				provider.ErrNotApplicable, cpProfile, region, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload control-plane (%s)", cpProfile),
			UnitUSDMonthly: price,
			Qty:            cp,
			SubtotalUSD:    price * float64(cp),
		})
	}
	if wk > 0 {
		price, err := liveIBMMonthly(wkProfile, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: ibmcloud worker %s/%s: %v",
				provider.ErrNotApplicable, wkProfile, region, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload workers (%s)", wkProfile),
			UnitUSDMonthly: price,
			Qty:            wk,
			SubtotalUSD:    price * float64(wk),
		})
	}
	total := 0.0
	for _, it := range items {
		total += it.SubtotalUSD
	}
	return provider.CostEstimate{
		TotalUSDMonthly: total,
		Items:           items,
		Note:            fmt.Sprintf("region %s, profiles priced live via IBM Cloud Global Catalog (IBMCLOUD_API_KEY). Block volumes / LBs / FloatingIP overhead not modeled yet.", region),
	}, nil
}

func liveIBMMonthly(profile, region string) (float64, error) {
	it, err := pricing.Fetch("ibmcloud", profile, region)
	if err != nil {
		return 0, err
	}
	return it.USDPerMonth, nil
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
