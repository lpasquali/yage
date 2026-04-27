// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package hetzner

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// Hetzner overhead is **shape**, not money. Tier counts (LB sizes,
// floating IPs, extra-volume budget) are user-visible architecture
// choices that stay constant across price changes. Every $/month
// number in this file comes from internal/pricing — Hetzner Cloud
// API for server + volume pricing, EUR→USD via env-overridable rate.

// hetznerOverheadCounts is the bundled component shape for a tier.
// dev favors a single small LB (most clusters need at least one
// for ingress); prod doubles up to LB21 + a floating IP for stable
// external addressing; enterprise adds a second LB, a second
// floating IP, and a 5 TB extra-volume budget.
type hetznerOverheadCounts struct {
	lb11s             int
	lb21s             int
	lb31s             int
	floatingIPs       int
	extraVolumeGB     int
	backupsPercentage float64 // 0 or 0.20
}

func hetznerOverheadDefaults(tier string) hetznerOverheadCounts {
	switch tier {
	case "dev":
		return hetznerOverheadCounts{
			lb11s:             1,
			lb21s:             0,
			lb31s:             0,
			floatingIPs:       0,
			extraVolumeGB:     0,
			backupsPercentage: 0,
		}
	case "enterprise":
		return hetznerOverheadCounts{
			lb11s:             0,
			lb21s:             2, // ingress + platform
			lb31s:             0,
			floatingIPs:       2, // ingress VIP + egress source
			extraVolumeGB:     5000,
			backupsPercentage: 0.20,
		}
	default: // prod
		return hetznerOverheadCounts{
			lb11s:             0,
			lb21s:             1, // single ingress LB
			lb31s:             0,
			floatingIPs:       1,
			extraVolumeGB:     0,
			backupsPercentage: 0,
		}
	}
}

// EstimateMonthlyCostUSD computes a Hetzner Cloud bill for the
// planned cluster: server (with bundled disk + 20 TB egress) +
// optional volume storage + service overhead (LB, floating IPs).
// All monetary numbers come from the live Hetzner Cloud API; if the
// API is unreachable we return ErrNotApplicable so the orchestrator
// surfaces "estimate unavailable" rather than fabricate a number.
//
// CAPHV is unmanaged-only (no Hetzner-managed Kubernetes today),
// so there's no managed-mode switch.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	region := orDefault(cfg.Providers.Hetzner.Location, "fsn1")
	cp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wk := atoiOr(cfg.WorkerMachineCount, 0)
	cpType := orDefault(cfg.Providers.Hetzner.ControlPlaneMachineType, "cx23")
	wkType := orDefault(cfg.Providers.Hetzner.NodeMachineType, "cx23")

	items := []provider.CostItem{}

	// Control plane: Hetzner servers (boot disk bundled).
	if cp > 0 {
		cpPrice, err := liveServerMonthly(cpType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: hetzner: %v", provider.ErrNotApplicable, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload control-plane (%s, boot disk bundled)", cpType),
			UnitUSDMonthly: cpPrice,
			Qty:            cp,
			SubtotalUSD:    cpPrice * float64(cp),
		})
	}

	// Workers: Hetzner servers (boot disk bundled).
	if wk > 0 {
		wkPrice, err := liveServerMonthly(wkType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: hetzner: %v", provider.ErrNotApplicable, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("workload workers (%s, boot disk bundled)", wkType),
			UnitUSDMonthly: wkPrice,
			Qty:            wk,
			SubtotalUSD:    wkPrice * float64(wk),
		})
	}

	// Optional management cluster (pivot retains it).
	if cfg.PivotEnabled {
		mcp := atoiOr(cfg.Mgmt.ControlPlaneMachineCount, 1)
		mgmtType := "cx23"
		mgmtPrice, err := liveServerMonthly(mgmtType, region)
		if err != nil {
			return provider.CostEstimate{}, fmt.Errorf("%w: hetzner: %v", provider.ErrNotApplicable, err)
		}
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("mgmt control-plane (%s)", mgmtType),
			UnitUSDMonthly: mgmtPrice,
			Qty:            mcp,
			SubtotalUSD:    mgmtPrice * float64(mcp),
		})
	}

	// Service overhead — load balancers, floating IPs, optional
	// extra volumes, optional backups. All money figures live.
	var err error
	items, err = addHetznerOverhead(items, cfg, region)
	if err != nil {
		return provider.CostEstimate{}, fmt.Errorf("%w: hetzner overhead: %v", provider.ErrNotApplicable, err)
	}

	// Grand total.
	total := 0.0
	for _, it := range items {
		total += it.SubtotalUSD
	}

	tierLabel := orDefault(cfg.Providers.Hetzner.OverheadTier, "prod")
	totalServers := cp + wk
	if cfg.PivotEnabled {
		totalServers += atoiOr(cfg.Mgmt.ControlPlaneMachineCount, 1)
	}
	note := fmt.Sprintf(
		"Hetzner Cloud monthly caps (live api.hetzner.cloud, EUR-native; "+
			"FX-converted only when taller != EUR), region %s, %s overhead tier "+
			"(LB + floating IPs + volume budget). Boot disk + 20 TB egress bundled "+
			"per server (%d total). Hetzner is genuinely cheap — sanity-check the "+
			"line items via the bill split, not the headline total.",
		region, tierLabel, totalServers,
	)
	return provider.CostEstimate{
		TotalUSDMonthly: total,
		Items:           items,
		Note:            note,
	}, nil
}

// liveServerMonthly hits the live Hetzner pricing fetcher. Returns
// the monthly cap (USD) for the (server_type, location) pair.
func liveServerMonthly(sku, region string) (float64, error) {
	it, err := pricing.Fetch("hetzner", sku, region)
	if err != nil {
		return 0, err
	}
	return it.USDPerMonth, nil
}

// liveVolumeUSDPerGBMonth fetches the live per-GB-month volume
// price (auth-free). Cached in-process for the run via
// pricing.HetznerVolumeUSDPerGBMonth.
func liveVolumeUSDPerGBMonth() (float64, error) {
	return pricing.HetznerVolumeUSDPerGBMonth()
}

// liveLoadBalancerMonthly fetches the cap price of a Hetzner LB
// type. Hetzner doesn't price LBs in the server_types catalog;
// they're a separate product. The hetzner/v1/pricing endpoint
// returns them, but parsing that for one row is overkill — we
// model LB cost by mapping the LB type's vCPU/feature equivalent
// to a server type that *is* priced live, which is fragile. As a
// principled compromise, we look up the LB cap via the same
// pricing endpoint via a dedicated helper.
//
// To keep the diff focused, this PR returns ErrUnavailable when
// the pricing endpoint cannot be reached, and the caller surfaces
// "load balancer cost: estimate unavailable" for that one line
// rather than fail the whole estimate.
func liveLoadBalancerMonthly(lbType string) (float64, error) {
	return pricing.HetznerLoadBalancerUSDPerMonth(lbType)
}

// liveFloatingIPMonthly fetches the live floating-IPv4 monthly cap.
func liveFloatingIPMonthly() (float64, error) {
	return pricing.HetznerFloatingIPUSDPerMonth()
}

// addHetznerOverhead appends overhead CostItems. Component counts
// per tier are shape (constant); $/unit comes from live API.
func addHetznerOverhead(items []provider.CostItem, cfg *config.Config, region string) ([]provider.CostItem, error) {
	tier := orDefault(cfg.Providers.Hetzner.OverheadTier, "prod")
	d := hetznerOverheadDefaults(tier)

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

	addLB := func(label, lbType string, qty int) error {
		if qty <= 0 {
			return nil
		}
		price, err := liveLoadBalancerMonthly(lbType)
		if err != nil {
			// LB price unavailable: surface as a 0 line item so the
			// user notices, but don't fail the whole estimate.
			items = append(items, provider.CostItem{
				Name:           label + " (price unavailable: " + err.Error() + ")",
				UnitUSDMonthly: 0,
				Qty:            qty,
				SubtotalUSD:    0,
			})
			return nil
		}
		add(label, qty, price)
		return nil
	}

	if err := addLB("Hetzner Load Balancer LB11 (ingress)", "lb11", d.lb11s); err != nil {
		return items, err
	}
	if err := addLB("Hetzner Load Balancer LB21 (ingress)", "lb21", d.lb21s); err != nil {
		return items, err
	}
	if err := addLB("Hetzner Load Balancer LB31 (ingress)", "lb31", d.lb31s); err != nil {
		return items, err
	}

	if d.floatingIPs > 0 {
		fipPrice, err := liveFloatingIPMonthly()
		if err != nil {
			items = append(items, provider.CostItem{
				Name:           "Floating IPv4 (price unavailable: " + err.Error() + ")",
				UnitUSDMonthly: 0,
				Qty:            d.floatingIPs,
				SubtotalUSD:    0,
			})
		} else {
			add("Floating IPv4", d.floatingIPs, fipPrice)
		}
	}

	// Extra persistent volumes beyond the bundled boot disks.
	if d.extraVolumeGB > 0 {
		volPrice, err := liveVolumeUSDPerGBMonth()
		if err != nil {
			return items, fmt.Errorf("hetzner volume price: %w", err)
		}
		volCost := float64(d.extraVolumeGB) * volPrice
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("extra Cloud Volumes (%d GB total)", d.extraVolumeGB),
			UnitUSDMonthly: volCost,
			Qty:            1,
			SubtotalUSD:    volCost,
		})
	}

	// Backups: optional 20% surcharge applied to every server line.
	if d.backupsPercentage > 0 {
		serverTotal := 0.0
		for _, it := range items {
			n := strings.ToLower(it.Name)
			if strings.Contains(n, "control-plane") || strings.Contains(n, "workers") {
				serverTotal += it.SubtotalUSD
			}
		}
		if serverTotal > 0 {
			backupCost := serverTotal * d.backupsPercentage
			items = append(items, provider.CostItem{
				Name:           fmt.Sprintf("Backups (+%d%% surcharge on servers)", int(d.backupsPercentage*100)),
				UnitUSDMonthly: backupCost,
				Qty:            1,
				SubtotalUSD:    backupCost,
			})
		}
	}

	return items, nil
}

// errSentinel keeps unused-import ergonomics happy in case future
// code paths want to compare against pricing.ErrUnavailable directly.
var _ = errors.Is

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