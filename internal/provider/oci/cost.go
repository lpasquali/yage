// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package oci

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// EstimateMonthlyCostUSD computes an OCI compute bill from the live
// auth-free Cost Estimator JSON. Flex shapes (VM.Standard.E4.Flex)
// are priced per-OCPU + per-GB-hour and we model them at the user-
// supplied OCPU/memory; fixed shapes price per-hour.
func (p *Provider) EstimateMonthlyCostUSD(ctx context.Context, cfg *config.Config) (provider.CostEstimate, error) {
	pf := pricing.FetcherFrom(ctx)
	region := orDefault(cfg.Providers.OCI.Region, "us-ashburn-1")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpShape := orDefault(cfg.Providers.OCI.ControlPlaneShape, "VM.Standard.E4.Flex")
	wkShape := orDefault(cfg.Providers.OCI.NodeShape, "VM.Standard.E4.Flex")

	items := []provider.CostItem{}
	if cp > 0 {
		price, err := liveOCIShapeMonthly(ctx, pf, cpShape, region)
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
		price, err := liveOCIShapeMonthly(ctx, pf, wkShape, region)
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
	// Managed Postgres substitution — OCI doesn't currently model
	// cnpg as an explicit cost line, so suppression is implicit.
	if mPG, fired, err := managedPostgresItem(cfg, "oci", region); err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: oci managed postgres: %v", provider.ErrNotApplicable, err)
	} else if fired {
		items = append(items, mPG)
	}

	for _, svc := range []cost.ManagedService{cost.MSMessageQueue, cost.MSObjectStore, cost.MSCache} {
		if item, fired, _ := cost.AddonCostItem(cfg, "oci", region, svc); fired {
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
		Note:            fmt.Sprintf("region %s, shapes priced live via OCI Cost Estimator JSON (auth-free). Block volumes / LBs / egress not modeled yet.", region),
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

func liveOCIShapeMonthly(ctx context.Context, pf pricing.Fetcher, shape, region string) (float64, error) {
	it, err := pf.Fetch(ctx, "oci", shape, region)
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
