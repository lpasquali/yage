// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package gcp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// GCP overhead is **shape**, not money. Component counts (Cloud
// NAT gateways, LBs, log GB, DNS zones, egress GB) are
// architecture choices that stay constant. Every $/month number in
// this file comes from internal/pricing — GCP Cloud Billing
// Catalog API, which requires a Google API key.
//
// API key: set GOOGLE_BILLING_API_KEY (or YAGE_GCP_API_KEY).
// When unset, EstimateMonthlyCostUSD returns provider.ErrNotApplicable
// so the orchestrator surfaces "GCP estimate unavailable: needs
// Cloud Billing API key" rather than fabricating a number.

// EstimateMonthlyCostUSD computes a GCP bill against the live
// Cloud Billing Catalog. Switches on cfg.Providers.GCP.Mode:
//   - "unmanaged" (default): self-managed Kubernetes on GCE.
//   - "gke": GKE Standard managed control plane (live-priced) +
//     GCE worker nodes.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.Providers.GCP.Region, "us-central1")
	mode := orDefault(cfg.Providers.GCP.Mode, "unmanaged")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpType := orDefault(cfg.Providers.GCP.ControlPlaneMachineType, "n2-standard-2")
	wkType := orDefault(cfg.Providers.GCP.NodeMachineType, "n2-standard-2")

	cpDiskGB := atoiOr(cfg.Providers.Proxmox.ControlPlaneBootVolumeSize, 30)
	wkDiskGB := atoiOr(cfg.Providers.Proxmox.WorkerBootVolumeSize, 40)

	pdBalanced, err := pricing.Fetch("gcp", "pd:balanced", region)
	if err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: gcp pd-balanced %s: %v", provider.ErrNotApplicable, region, err)
	}
	cpDiskCost := float64(cpDiskGB) * pdBalanced.USDPerMonth
	wkDiskCost := float64(wkDiskGB) * pdBalanced.USDPerMonth

	items := []provider.CostItem{}

	switch mode {
	case "gke":
		gke, err := pricing.GCPGKEControlPlaneUSDPerMonth(region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: gcp gke cp %s: %v", provider.ErrNotApplicable, region, err)
		}
		items = append(items, provider.CostItem{
			Name:           "GKE Standard managed control plane (flat per cluster, live)",
			UnitUSDMonthly: gke,
			Qty:            1,
			SubtotalUSD:    gke,
		})
	default: // unmanaged
		if cp > 0 {
			cpPrice, err := liveGCEMonthly(cpType, region)
			if err != nil {
				return provider.CostEstimate{}, fmt.Errorf("%w: gcp cp %s/%s: %v", provider.ErrNotApplicable, cpType, region, err)
			}
			items = append(items, provider.CostItem{
				Name:           fmt.Sprintf("workload control-plane (%s)", cpType),
				UnitUSDMonthly: cpPrice,
				Qty:            cp,
				SubtotalUSD:    cpPrice * float64(cp),
			})
			items = append(items, provider.CostItem{
				Name:           fmt.Sprintf("CP boot volumes (%d GB pd-balanced each)", cpDiskGB),
				UnitUSDMonthly: cpDiskCost,
				Qty:            cp,
				SubtotalUSD:    cpDiskCost * float64(cp),
			})
		}
	}

	if wk > 0 {
		wkPrice, err := liveGCEMonthly(wkType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: gcp worker %s/%s: %v", provider.ErrNotApplicable, wkType, region, err)
		}
		label := "workload workers"
		if mode == "gke" {
			label = "workload workers (GKE node pool)"
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("%s (%s)", label, wkType),
			UnitUSDMonthly: wkPrice,
			Qty:            wk,
			SubtotalUSD:    wkPrice * float64(wk),
		})
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("worker boot volumes (%d GB pd-balanced each)", wkDiskGB),
			UnitUSDMonthly: wkDiskCost,
			Qty:            wk,
			SubtotalUSD:    wkDiskCost * float64(wk),
		})
	}

	if cfg.Pivot.Enabled {
		mcp := atoiOr(cfg.Mgmt.ControlPlaneMachineCount, 1)
		mgmtType := "e2-medium"
		mgmtPrice, err := liveGCEMonthly(mgmtType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: gcp mgmt %s/%s: %v", provider.ErrNotApplicable, mgmtType, region, err)
		}
		mgmtDisk := atoiOr(cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize, 30)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("mgmt control-plane (%s)", mgmtType),
			UnitUSDMonthly: mgmtPrice,
			Qty:            mcp,
			SubtotalUSD:    mgmtPrice * float64(mcp),
		})
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("mgmt boot volumes (%d GB pd-balanced each)", mgmtDisk),
			UnitUSDMonthly: float64(mgmtDisk) * pdBalanced.USDPerMonth,
			Qty:            mcp,
			SubtotalUSD:    float64(mgmtDisk) * pdBalanced.USDPerMonth * float64(mcp),
		})
	}

	// Managed Postgres substitution — GCP doesn't currently model
	// cnpg as an explicit cost line, so suppression is implicit.
	if mPG, fired, err := managedPostgresItem(cfg, "gcp", region); err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: gcp managed postgres: %v", provider.ErrNotApplicable, err)
	} else if fired {
		items = append(items, mPG)
	}

	for _, svc := range []cost.ManagedService{cost.MSMessageQueue, cost.MSObjectStore, cost.MSCache} {
		if item, fired, _ := cost.AddonCostItem(cfg, "gcp", region, svc); fired {
			items = append(items, item)
		}
	}

	items, err = addServiceOverhead(items, cfg, region)
	if err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: gcp overhead: %v", provider.ErrNotApplicable, err)
	}

	total := 0.0
	for _, it := range items {
		total += it.SubtotalUSD
	}

	tierLabel := orDefault(cfg.Providers.GCP.OverheadTier, "prod")
	noteBase := fmt.Sprintf("region %s, %s overhead tier (Cloud NAT/LB/Cloud Logging/Cloud DNS/egress).", region, tierLabel)
	note := ""
	switch mode {
	case "gke":
		note = noteBase + " GKE CP + GCE node pool + pd-balanced (live GCP Cloud Billing Catalog)."
	default:
		note = noteBase + " Self-managed CP + GCE workers + pd-balanced (live GCP Cloud Billing Catalog)."
	}
	return provider.CostEstimate{
		TotalUSDMonthly: total,
		Items:           items,
		Note:            note,
	}, nil
}

// managedPostgresItem dispatches to the per-vendor managed-PG
// helper when the operator hasn't opted out and the vendor offers
// the SaaS. ErrNotApplicable from the dispatcher means "not wired
// yet" — fall back to in-cluster cnpg silently.
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

func liveGCEMonthly(machineType, region string) (float64, error) {
	it, err := pricing.Fetch("gcp", machineType, region)
	if err != nil {
		return 0, err
	}
	return it.USDPerMonth, nil
}

// --- GCP overhead: counts (shape) per tier; $/unit live. ---

type overheadCounts struct {
	cloudNATs      int
	loadBalancers  int
	dataTransferGB int
	loggingGB      int
	dnsZones       int
}

func overheadDefaults(tier string) overheadCounts {
	switch tier {
	case "dev":
		return overheadCounts{
			cloudNATs:      0,
			loadBalancers:  1,
			dataTransferGB: 20,
			loggingGB:      2,
			dnsZones:       0,
		}
	case "enterprise":
		return overheadCounts{
			cloudNATs:      3,
			loadBalancers:  3,
			dataTransferGB: 500,
			loggingGB:      50,
			dnsZones:       2,
		}
	default: // prod
		return overheadCounts{
			cloudNATs:      1,
			loadBalancers:  1,
			dataTransferGB: 100,
			loggingGB:      10,
			dnsZones:       1,
		}
	}
}

func addServiceOverhead(items []provider.CostItem, cfg *config.Config, region string) ([]provider.CostItem, error) {
	tier := orDefault(cfg.Providers.GCP.OverheadTier, "prod")
	d := overheadDefaults(tier)

	add := func(name string, qty int, unitMonthly float64) {
		if qty <= 0 || unitMonthly <= 0 {
			return
		}
		items = append(items, provider.CostItem{
			Name:           name,
			UnitUSDMonthly: unitMonthly,
			Qty:            qty,
			SubtotalUSD:    unitMonthly * float64(qty),
		})
	}

	if d.cloudNATs > 0 {
		hr, gb, err := pricing.GCPCloudNATPricing(region)
		if err != nil {
			return items, fmt.Errorf("cloud nat: %w", err)
		}
		natMonthly := hr*pricing.MonthlyHours +
			gb*float64(d.dataTransferGB)*0.3/float64(d.cloudNATs)
		add(fmt.Sprintf("Cloud NAT (~%d GB processed/mo each)",
			int(float64(d.dataTransferGB)*0.3/float64(d.cloudNATs))),
			d.cloudNATs, natMonthly)
	}

	if d.loadBalancers > 0 {
		lb, err := pricing.GCPLoadBalancerUSDPerMonth(region)
		if err != nil {
			return items, fmt.Errorf("lb: %w", err)
		}
		add("Load Balancer (Argo CD ingress / app)", d.loadBalancers, lb)
	}

	if d.dataTransferGB > 0 {
		egressGB, err := pricing.GCPEgressUSDPerGB(region)
		if err != nil {
			return items, fmt.Errorf("egress: %w", err)
		}
		egress := egressGB * float64(d.dataTransferGB)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("Internet egress (~%d GB/mo)", d.dataTransferGB),
			UnitUSDMonthly: egress,
			Qty:            1,
			SubtotalUSD:    egress,
		})
	}

	if d.loggingGB > 0 {
		logGB, err := pricing.GCPCloudLoggingUSDPerGB(region)
		if err != nil {
			return items, fmt.Errorf("logging: %w", err)
		}
		ingest := logGB * float64(d.loggingGB)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("Cloud Logging (%d GB ingested/mo)", d.loggingGB),
			UnitUSDMonthly: ingest,
			Qty:            1,
			SubtotalUSD:    ingest,
		})
	}

	if d.dnsZones > 0 {
		dnsZone, err := pricing.GCPCloudDNSZoneUSDPerMonth(region)
		if err != nil {
			return items, fmt.Errorf("dns: %w", err)
		}
		add("Cloud DNS hosted zones", d.dnsZones, dnsZone)
	}

	return items, nil
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