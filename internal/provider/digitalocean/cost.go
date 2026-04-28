// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package digitalocean

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// EstimateMonthlyCostUSD computes a DigitalOcean droplet bill from
// live /v2/sizes data via internal/pricing. Block storage and load
// balancers are not folded in yet (overhead-tier wiring would mirror
// AWS/Azure/GCP — TODO when needed).
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.Providers.DigitalOcean.Region, "nyc3")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpType := orDefault(cfg.Providers.DigitalOcean.ControlPlaneSize, "s-2vcpu-4gb")
	wkType := orDefault(cfg.Providers.DigitalOcean.NodeSize, "s-2vcpu-4gb")

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

	// Managed Postgres substitution — DigitalOcean doesn't currently
	// model cnpg as an explicit cost line, so suppression is implicit.
	if mPG, fired, err := managedPostgresItem(cfg, "digitalocean", region); err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: digitalocean managed postgres: %v", provider.ErrNotApplicable, err)
	} else if fired {
		items = append(items, mPG)
	}

	for _, svc := range []cost.ManagedService{cost.MSMessageQueue, cost.MSObjectStore, cost.MSCache} {
		if item, fired, _ := cost.AddonCostItem(cfg, "digitalocean", region, svc); fired {
			items = append(items, item)
		}
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

// managedPostgresItem dispatches to the per-vendor managed-PG
// helper when the operator hasn't opted out and the vendor offers
// the SaaS. ErrNotApplicable means "not wired yet" — fall back to
// in-cluster cnpg silently.
func managedPostgresItem(cfg *config.Config, vendor, region string) (provider.CostItem, bool, error) {
	if !cfg.UseManagedPostgres {
		return provider.CostItem{}, false, nil
	}
	if !cost.VendorOffersManaged(vendor, cost.MSPostgres) {
		return provider.CostItem{}, false, nil
	}
	tier := pgTierFromEnv(cfg.Workload.Environment)
	mp, err := cost.ManagedPostgresUSDPerMonth(vendor, region, tier, cfg.Workload.DatabaseGB)
	if err != nil {
		if errors.Is(err, provider.ErrNotApplicable) {
			return provider.CostItem{}, false, nil
		}
		return provider.CostItem{}, false, err
	}
	return provider.CostItem{
		Name:           fmt.Sprintf("Managed Postgres (%s, %s)", mp.SKU, tier),
		UnitUSDMonthly: mp.MonthlyUSD,
		Qty:            1,
		SubtotalUSD:    mp.MonthlyUSD,
	}, true, nil
}

func pgTierFromEnv(env string) cost.PostgresTier {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "prod":
		return cost.PostgresProd
	case "staging":
		return cost.PostgresStaging
	default:
		return cost.PostgresDev
	}
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