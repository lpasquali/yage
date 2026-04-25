package equinix

import (
	"fmt"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/pricing"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
)

// EstimateMonthlyCostUSD computes an Equinix Metal bare-metal bill
// from live /metal/v1/plans data. Plan classes look like
// "c3.small.x86" or "m3.large.x86". Equinix prices on hourly bare-
// metal rate; monthly = hourly × 730.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	metro := orDefault(cfg.EquinixMetro, "ny")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpClass := orDefault(cfg.EquinixControlPlaneClass, "c3.small.x86")
	wkClass := orDefault(cfg.EquinixNodeClass, "c3.small.x86")

	items := []provider.CostItem{}
	if cp > 0 {
		price, err := liveEquinixMonthly(cpClass, metro)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: equinix cp %s/%s: %v",
				provider.ErrNotApplicable, cpClass, metro, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload control-plane (%s)", cpClass),
			UnitUSDMonthly: price,
			Qty:            cp,
			SubtotalUSD:    price * float64(cp),
		})
	}
	if wk > 0 {
		price, err := liveEquinixMonthly(wkClass, metro)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: equinix worker %s/%s: %v",
				provider.ErrNotApplicable, wkClass, metro, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload workers (%s)", wkClass),
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
		Note:            fmt.Sprintf("metro %s, plans priced live via Equinix Metal /metal/v1/plans (METAL_AUTH_TOKEN). Bandwidth / IPs / project tier overhead not modeled yet.", metro),
	}, nil
}

func liveEquinixMonthly(planClass, metro string) (float64, error) {
	it, err := pricing.Fetch("equinix", planClass, metro)
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
