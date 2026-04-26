// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package azure

import (
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// Azure overhead is **shape**, not money. Component counts (NAT GW,
// LB, Public IP, Log Analytics GB, DNS zones, egress GB) are
// architecture choices that stay constant. Every $/month number
// in this file comes from internal/pricing — Azure Retail Prices
// API (auth-free), region-aware, Linux on-demand consumption SKUs.

// EstimateMonthlyCostUSD computes an Azure bill for the planned
// cluster against live Retail Prices. VM compute + managed disks +
// service overhead. Returns ErrNotApplicable wrapped if any priced
// dimension is unreachable — the orchestrator surfaces "Azure
// estimate unavailable" rather than fabricate a number.
//
// Switches on cfg.Providers.Azure.Mode:
//   - "unmanaged" (default): self-managed Kubernetes on Azure VMs.
//   - "aks": AKS-managed control plane (priced as a flat-fee SKU
//     in the Retail Prices catalog) plus worker VMs.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.Providers.Azure.Location, "eastus")
	mode := orDefault(cfg.Providers.Azure.Mode, "unmanaged")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpType := orDefault(cfg.Providers.Azure.ControlPlaneMachineType, "Standard_D2s_v3")
	wkType := orDefault(cfg.Providers.Azure.NodeMachineType, "Standard_D2s_v3")

	cpDiskGB := atoiOr(cfg.Providers.Proxmox.ControlPlaneBootVolumeSize, 128)
	wkDiskGB := atoiOr(cfg.Providers.Proxmox.WorkerBootVolumeSize, 128)

	premiumSSDGB, err := pricing.AzureManagedDiskUSDPerGBMonth(region, "Premium SSD Managed Disks")
	if err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: azure premium ssd %s: %v", provider.ErrNotApplicable, region, err)
	}
	stdSSDGB, err := pricing.AzureManagedDiskUSDPerGBMonth(region, "Standard SSD Managed Disks")
	if err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: azure standard ssd %s: %v", provider.ErrNotApplicable, region, err)
	}

	cpDiskCost := float64(cpDiskGB) * premiumSSDGB
	wkDiskCost := float64(wkDiskGB) * premiumSSDGB

	items := []provider.CostItem{}

	// Control plane: AKS-managed (flat per-cluster SKU pulled live)
	// or self-managed VMs + Premium SSD boot disks.
	switch mode {
	case "aks":
		aks, err := pricing.AzureAKSUSDPerMonth(region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: azure aks %s: %v", provider.ErrNotApplicable, region, err)
		}
		items = append(items, provider.CostItem{
			Name:           "AKS managed control plane (flat per cluster, live retail price)",
			UnitUSDMonthly: aks,
			Qty:            1,
			SubtotalUSD:    aks,
		})
	default: // unmanaged
		if cp > 0 {
			cpPrice, err := liveVMMonthly(cpType, region)
			if err != nil {
				return provider.CostEstimate{}, fmt.Errorf("%w: azure cp %s/%s: %v", provider.ErrNotApplicable, cpType, region, err)
			}
			items = append(items, provider.CostItem{
				Name:           fmt.Sprintf("workload control-plane (%s)", cpType),
				UnitUSDMonthly: cpPrice,
				Qty:            cp,
				SubtotalUSD:    cpPrice * float64(cp),
			})
			items = append(items, provider.CostItem{
				Name:           fmt.Sprintf("CP boot disks (%d GB Premium SSD each)", cpDiskGB),
				UnitUSDMonthly: cpDiskCost,
				Qty:            cp,
				SubtotalUSD:    cpDiskCost * float64(cp),
			})
		}
	}

	// Workers.
	if wk > 0 {
		wkPrice, err := liveVMMonthly(wkType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: azure worker %s/%s: %v", provider.ErrNotApplicable, wkType, region, err)
		}
		label := "workload workers"
		if mode == "aks" {
			label = "workload workers (AKS Node Pool)"
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("%s (%s)", label, wkType),
			UnitUSDMonthly: wkPrice,
			Qty:            wk,
			SubtotalUSD:    wkPrice * float64(wk),
		})
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("worker boot disks (%d GB Premium SSD each)", wkDiskGB),
			UnitUSDMonthly: wkDiskCost,
			Qty:            wk,
			SubtotalUSD:    wkDiskCost * float64(wk),
		})
	}

	if cfg.PivotEnabled {
		mcp := atoiOr(cfg.Mgmt.ControlPlaneMachineCount, 1)
		mgmtType := "Standard_B2s"
		mgmtPrice, err := liveVMMonthly(mgmtType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: azure mgmt %s/%s: %v", provider.ErrNotApplicable, mgmtType, region, err)
		}
		mgmtDisk := atoiOr(cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize, 64)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("mgmt control-plane (%s)", mgmtType),
			UnitUSDMonthly: mgmtPrice,
			Qty:            mcp,
			SubtotalUSD:    mgmtPrice * float64(mcp),
		})
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("mgmt boot disks (%d GB Standard SSD each)", mgmtDisk),
			UnitUSDMonthly: float64(mgmtDisk) * stdSSDGB,
			Qty:            mcp,
			SubtotalUSD:    float64(mgmtDisk) * stdSSDGB * float64(mcp),
		})
	}

	// Service overhead — counts are shape, $/unit comes live.
	items, err = addAzureServiceOverhead(items, cfg, region)
	if err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: azure overhead: %v", provider.ErrNotApplicable, err)
	}

	total := 0.0
	for _, it := range items {
		total += it.SubtotalUSD
	}

	tierLabel := orDefault(cfg.Providers.Azure.OverheadTier, "prod")
	noteBase := fmt.Sprintf("region %s, %s overhead tier (NAT GW/LB/Public IP/Log Analytics/DNS/egress).", region, tierLabel)
	note := ""
	switch mode {
	case "aks":
		note = noteBase + " AKS CP + Node Pool VMs + Premium SSD (live Azure Retail Prices API)."
	default:
		note = noteBase + " Self-managed CP + Azure VM workers + Premium SSD (live Azure Retail Prices API)."
	}
	return provider.CostEstimate{
		TotalUSDMonthly: total,
		Items:           items,
		Note:            note,
	}, nil
}

func liveVMMonthly(sku, region string) (float64, error) {
	it, err := pricing.Fetch("azure", sku, region)
	if err != nil {
		return 0, err
	}
	return it.USDPerMonth, nil
}

// --- Azure overhead: counts (shape) per tier; $/unit live. ---

type azureOverheadCounts struct {
	natGateways    int
	loadBalancers  int
	publicIPs      int
	dataTransferGB int
	logAnalyticsGB int
	dnsZones       int
}

func azureOverheadDefaults(tier string) azureOverheadCounts {
	switch tier {
	case "dev":
		return azureOverheadCounts{
			natGateways:    0,
			loadBalancers:  1,
			publicIPs:      1,
			dataTransferGB: 20,
			logAnalyticsGB: 2,
			dnsZones:       0,
		}
	case "enterprise":
		return azureOverheadCounts{
			natGateways:    3,
			loadBalancers:  2,
			publicIPs:      4,
			dataTransferGB: 500,
			logAnalyticsGB: 50,
			dnsZones:       2,
		}
	default: // prod
		return azureOverheadCounts{
			natGateways:    1,
			loadBalancers:  1,
			publicIPs:      2,
			dataTransferGB: 100,
			logAnalyticsGB: 10,
			dnsZones:       1,
		}
	}
}

func addAzureServiceOverhead(items []provider.CostItem, cfg *config.Config, region string) ([]provider.CostItem, error) {
	tier := orDefault(cfg.Providers.Azure.OverheadTier, "prod")
	d := azureOverheadDefaults(tier)

	if d.natGateways > 0 {
		hourly, gbProc, err := pricing.AzureNATGatewayHourlyAndProcGB(region)
		if err != nil {
			return items, fmt.Errorf("nat gateway: %w", err)
		}
		natMonthly := hourly*pricing.MonthlyHours +
			gbProc*float64(d.dataTransferGB)*0.3/float64(d.natGateways)
		items = append(items, provider.CostItem{
			Name: fmt.Sprintf("NAT Gateway (~%d GB processed/mo each)",
				int(float64(d.dataTransferGB)*0.3/float64(d.natGateways))),
			UnitUSDMonthly: natMonthly,
			Qty:            d.natGateways,
			SubtotalUSD:    natMonthly * float64(d.natGateways),
		})
	}

	if d.loadBalancers > 0 {
		hourly, gbProc, err := pricing.AzureStandardLBHourlyAndProcGB(region)
		if err != nil {
			return items, fmt.Errorf("lb: %w", err)
		}
		// Assume ~30 GB/mo crosses the LB on a typical Argo CD shape.
		lbMonthly := hourly*pricing.MonthlyHours + gbProc*30
		items = append(items, provider.CostItem{
			Name:           "Standard Load Balancer (Argo CD ingress / app)",
			UnitUSDMonthly: lbMonthly,
			Qty:            d.loadBalancers,
			SubtotalUSD:    lbMonthly * float64(d.loadBalancers),
		})
	}

	if d.publicIPs > 0 {
		hourly, err := pricing.AzurePublicIPHourly(region)
		if err != nil {
			return items, fmt.Errorf("public ip: %w", err)
		}
		pip := hourly * pricing.MonthlyHours
		items = append(items, provider.CostItem{
			Name:           "Public IP (Standard SKU)",
			UnitUSDMonthly: pip,
			Qty:            d.publicIPs,
			SubtotalUSD:    pip * float64(d.publicIPs),
		})
	}

	if d.dataTransferGB > 0 {
		egressGB, err := pricing.AzureEgressUSDPerGB(region)
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

	if d.logAnalyticsGB > 0 {
		laGB, err := pricing.AzureLogAnalyticsUSDPerGB(region)
		if err != nil {
			return items, fmt.Errorf("log analytics: %w", err)
		}
		ingest := laGB * float64(d.logAnalyticsGB)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("Log Analytics (%d GB ingested/mo)", d.logAnalyticsGB),
			UnitUSDMonthly: ingest,
			Qty:            1,
			SubtotalUSD:    ingest,
		})
	}

	if d.dnsZones > 0 {
		dnsZone, err := pricing.AzureDNSZoneUSDPerMonth(region)
		if err != nil {
			return items, fmt.Errorf("dns zone: %w", err)
		}
		items = append(items, provider.CostItem{
			Name:           "Azure DNS hosted zones",
			UnitUSDMonthly: dnsZone,
			Qty:            d.dnsZones,
			SubtotalUSD:    dnsZone * float64(d.dnsZones),
		})
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