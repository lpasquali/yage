// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package ibmcloud

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// EstimateMonthlyCostUSD computes an IBM Cloud VPC Gen2 bill from
// live Global Catalog data. Profile IDs look like "bx2-2x8" (general
// purpose, 2 vCPU, 8 GB) or "cx2-4x8" (compute optimised).
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.Providers.IBMCloud.Region, "us-south")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpProfile := orDefault(cfg.Providers.IBMCloud.ControlPlaneProfile, "bx2-2x8")
	wkProfile := orDefault(cfg.Providers.IBMCloud.NodeProfile, "bx2-2x8")

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
	// Managed Postgres substitution — IBM Cloud doesn't currently
	// model cnpg as an explicit cost line, so suppression is implicit.
	if mPG, fired, err := managedPostgresItem(cfg, "ibmcloud", region); err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: ibmcloud managed postgres: %v", provider.ErrNotApplicable, err)
	} else if fired {
		items = append(items, mPG)
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