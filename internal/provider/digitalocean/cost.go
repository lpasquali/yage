package digitalocean

import (
	"fmt"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/pricing"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
)

// EstimateMonthlyCostUSD computes a DigitalOcean droplet bill from
// live /v2/sizes data via internal/pricing. Block storage and load
// balancers are not folded in yet (overhead-tier wiring would mirror
// AWS/Azure/GCP — TODO when needed).
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.DigitalOceanRegion, "nyc3")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpType := orDefault(cfg.DigitalOceanControlPlaneSize, "s-2vcpu-4gb")
	wkType := orDefault(cfg.DigitalOceanNodeSize, "s-2vcpu-4gb")

	items := []provider.CostItem{}
	if cp > 0 {
		price, err := liveDropletMonthly(cpType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: digitalocean cp %s/%s: %v",
				provider.ErrNotApplicable, cpType, region, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload control-plane (%s)", cpType),
			UnitUSDMonthly: price,
			Qty:            cp,
			SubtotalUSD:    price * float64(cp),
		})
	}
	if wk > 0 {
		price, err := liveDropletMonthly(wkType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: digitalocean worker %s/%s: %v",
				provider.ErrNotApplicable, wkType, region, err)
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
		Note:            fmt.Sprintf("region %s, droplet sizes priced live via DigitalOcean /v2/sizes (DIGITALOCEAN_TOKEN). Block volumes / Load Balancers / Spaces overhead not modeled yet.", region),
	}, nil
}

func liveDropletMonthly(slug, region string) (float64, error) {
	it, err := pricing.Fetch("digitalocean", slug, region)
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
