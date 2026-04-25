package linode

import (
	"fmt"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/pricing"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
)

// EstimateMonthlyCostUSD computes a Linode bill from live
// /v4/linode/types data. Type IDs look like "g6-standard-2".
// Region availability is global for compute (region only matters
// for transfer pricing); we ignore region in the catalog lookup.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.LinodeRegion, "us-east")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpType := orDefault(cfg.LinodeControlPlaneType, "g6-standard-2")
	wkType := orDefault(cfg.LinodeNodeType, "g6-standard-2")

	items := []provider.CostItem{}
	if cp > 0 {
		price, err := liveLinodeMonthly(cpType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: linode cp %s: %v",
				provider.ErrNotApplicable, cpType, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload control-plane (%s)", cpType),
			UnitUSDMonthly: price,
			Qty:            cp,
			SubtotalUSD:    price * float64(cp),
		})
	}
	if wk > 0 {
		price, err := liveLinodeMonthly(wkType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: linode worker %s: %v",
				provider.ErrNotApplicable, wkType, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload workers (%s)", wkType),
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
		Note:            fmt.Sprintf("region %s, types priced live via Linode /v4/linode/types (auth-free catalog). NodeBalancer / volume / transfer overhead not modeled yet.", region),
	}, nil
}

func liveLinodeMonthly(typeID, region string) (float64, error) {
	it, err := pricing.Fetch("linode", typeID, region)
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
