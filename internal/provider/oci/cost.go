package oci

import (
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// EstimateMonthlyCostUSD computes an OCI compute bill from the live
// auth-free Cost Estimator JSON. Flex shapes (VM.Standard.E4.Flex)
// are priced per-OCPU + per-GB-hour and we model them at the user-
// supplied OCPU/memory; fixed shapes price per-hour.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.OCIRegion, "us-ashburn-1")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpShape := orDefault(cfg.OCIControlPlaneShape, "VM.Standard.E4.Flex")
	wkShape := orDefault(cfg.OCINodeShape, "VM.Standard.E4.Flex")

	items := []provider.CostItem{}
	if cp > 0 {
		price, err := liveOCIShapeMonthly(cpShape, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: oci cp %s/%s: %v",
				provider.ErrNotApplicable, cpShape, region, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload control-plane (%s)", cpShape),
			UnitUSDMonthly: price,
			Qty:            cp,
			SubtotalUSD:    price * float64(cp),
		})
	}
	if wk > 0 {
		price, err := liveOCIShapeMonthly(wkShape, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: oci worker %s/%s: %v",
				provider.ErrNotApplicable, wkShape, region, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload workers (%s)", wkShape),
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
		Note:            fmt.Sprintf("region %s, shapes priced live via OCI Cost Estimator JSON (auth-free). Block volumes / LBs / egress not modeled yet.", region),
	}, nil
}

func liveOCIShapeMonthly(shape, region string) (float64, error) {
	it, err := pricing.Fetch("oci", shape, region)
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
