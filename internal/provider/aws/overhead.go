package aws

import (
	"fmt"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// AWS overhead **shape** — counts, not money. Per-tier defaults
// describe an architecture (how many NAT GWs, ALBs, NLBs the
// workload assumes), and per-component cfg overrides (Providers.AWS.ALBCount,
// Providers.AWS.NATGatewayCount, ...) override individual counts. The actual
// $/unit comes from internal/pricing live AWS API at the moment of
// estimation.

type overheadCounts struct {
	natGateways    int
	albs           int
	nlbs           int
	dataTransferGB int
	cloudwatchGB   int
	route53Zones   int
}

func overheadDefaults(tier string) overheadCounts {
	switch tier {
	case "dev":
		return overheadCounts{
			natGateways:    0, // public subnets only — no NAT cost
			albs:           1, // Argo CD ingress
			nlbs:           0,
			dataTransferGB: 20,
			cloudwatchGB:   2,
			route53Zones:   0,
		}
	case "enterprise":
		return overheadCounts{
			natGateways:    3,
			albs:           2,
			nlbs:           1,
			dataTransferGB: 500,
			cloudwatchGB:   50,
			route53Zones:   2,
		}
	default: // prod
		return overheadCounts{
			natGateways:    1,
			albs:           1,
			nlbs:           0,
			dataTransferGB: 100,
			cloudwatchGB:   10,
			route53Zones:   1,
		}
	}
}

// addServiceOverhead appends overhead CostItems with live $/unit.
// Returns the new slice. Component counts come from tier defaults
// + per-component cfg overrides.
func addServiceOverhead(items []provider.CostItem, cfg *config.Config, region string) ([]provider.CostItem, error) {
	tier := orDefault(cfg.Providers.AWS.OverheadTier, "prod")
	d := overheadDefaults(tier)

	// Apply per-component overrides when set on cfg.
	if cfg.Providers.AWS.NATGatewayCount != "" {
		d.natGateways = atoiOr(cfg.Providers.AWS.NATGatewayCount, d.natGateways)
	}
	if cfg.Providers.AWS.ALBCount != "" {
		d.albs = atoiOr(cfg.Providers.AWS.ALBCount, d.albs)
	}
	if cfg.Providers.AWS.NLBCount != "" {
		d.nlbs = atoiOr(cfg.Providers.AWS.NLBCount, d.nlbs)
	}
	if cfg.Providers.AWS.DataTransferGB != "" {
		d.dataTransferGB = atoiOr(cfg.Providers.AWS.DataTransferGB, d.dataTransferGB)
	}
	if cfg.Providers.AWS.CloudWatchLogsGB != "" {
		d.cloudwatchGB = atoiOr(cfg.Providers.AWS.CloudWatchLogsGB, d.cloudwatchGB)
	}
	if cfg.Providers.AWS.Route53HostedZones != "" {
		d.route53Zones = atoiOr(cfg.Providers.AWS.Route53HostedZones, d.route53Zones)
	}

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

	if d.natGateways > 0 {
		hr, gb, err := pricing.AWSNATGatewayPricing(region)
		if err != nil {
			return items, fmt.Errorf("nat gw: %w", err)
		}
		natMonthly := hr*pricing.MonthlyHours + gb*float64(d.dataTransferGB)*0.3/float64(d.natGateways)
		add(fmt.Sprintf("NAT Gateway (~%d GB processed/mo each)",
			int(float64(d.dataTransferGB)*0.3/float64(d.natGateways))),
			d.natGateways, natMonthly)
	}

	if d.albs > 0 {
		hr, lcuHr, err := pricing.AWSApplicationLBPricing(region)
		if err != nil {
			return items, fmt.Errorf("alb: %w", err)
		}
		// LCU estimate: ~5 LCU/hour for typical Argo CD ingress.
		alb := (hr + lcuHr*5.0) * pricing.MonthlyHours
		add("Application Load Balancer (Argo CD ingress / app)", d.albs, alb)
	}

	if d.nlbs > 0 {
		hr, lcuHr, err := pricing.AWSNetworkLBPricing(region)
		if err != nil {
			return items, fmt.Errorf("nlb: %w", err)
		}
		nlb := (hr + lcuHr*3.0) * pricing.MonthlyHours
		add("Network Load Balancer", d.nlbs, nlb)
	}

	if d.dataTransferGB > 0 {
		egressGB, err := pricing.AWSEgressUSDPerGB(region)
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

	if d.cloudwatchGB > 0 {
		ingest, storage, err := pricing.AWSCloudWatchLogsPricing(region)
		if err != nil {
			return items, fmt.Errorf("cwl: %w", err)
		}
		cw := ingest*float64(d.cloudwatchGB) + storage*float64(d.cloudwatchGB)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("CloudWatch Logs (%d GB ingested/mo)", d.cloudwatchGB),
			UnitUSDMonthly: cw,
			Qty:            1,
			SubtotalUSD:    cw,
		})
	}

	if d.route53Zones > 0 {
		zoneM, err := pricing.AWSRoute53ZoneUSDPerMonth(region)
		if err != nil {
			return items, fmt.Errorf("route53: %w", err)
		}
		add("Route53 hosted zones", d.route53Zones, zoneM)
	}

	return items, nil
}
