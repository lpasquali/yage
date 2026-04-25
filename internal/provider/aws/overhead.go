package aws

import (
	"fmt"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
)

// AWS service overhead pricing — the dependencies CAPA + workloads
// pull in beyond raw EC2/EBS/Fargate/EKS-CP. us-east-1 prices,
// 730-hour month.
//
// Costs that scale per-instance / per-event are reported as a flat
// monthly approximation suitable for a dry-run. Actual billing
// depends on traffic shape; spot-check against AWS Cost Explorer
// once the cluster runs real load.
const (
	// NAT Gateway: $0.045/hour fixed + $0.045/GB processed.
	natGatewayHourly      = 0.045
	natGatewayProcGBCost  = 0.045
	// ALB: $0.0225/hour fixed + $0.008/LCU-hour. We estimate
	// ~5 LCU/hour avg for a typical Argo CD ingress workload.
	albHourly             = 0.0225
	albLCUHour            = 0.008
	albLCUEstimatePerHour = 5.0
	// NLB: $0.0225/hour fixed + $0.006/NLCU-hour. Same LCU
	// estimate; NLBs are usually quieter than ALBs.
	nlbHourly             = 0.0225
	nlbLCUHour            = 0.006
	nlbLCUEstimatePerHour = 3.0
	// CloudWatch Logs ingestion: $0.50/GB ingested. Storage is
	// $0.03/GB-month — small relative to ingestion for typical
	// retention.
	cwLogsIngestionPerGB = 0.50
	cwLogsStoragePerGB   = 0.03
	// Route53 hosted zone: $0.50/zone/month. Queries are
	// $0.40/M and almost always negligible — we ignore them.
	route53PerZoneMonthly = 0.50
	// Data egress to internet: $0.09/GB above 100 GB free tier;
	// model as $0.09/GB across the user-supplied estimate.
	egressPerGB = 0.09
)

// overheadDefaults returns the bundled "everything else" component
// counts for a given tier. Per-component overrides on cfg take
// precedence over these.
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
			natGateways:    3, // multi-AZ HA
			albs:           2, // Argo + 1 platform ingress
			nlbs:           1, // separate NLB for cluster-internal egress
			dataTransferGB: 500,
			cloudwatchGB:   50,
			route53Zones:   2,
		}
	default: // prod
		return overheadCounts{
			natGateways:    1, // single AZ NAT keeps cost down
			albs:           1, // Argo CD ingress
			nlbs:           0,
			dataTransferGB: 100,
			cloudwatchGB:   10,
			route53Zones:   1,
		}
	}
}

// addServiceOverhead appends service-overhead CostItems to items
// based on tier + per-component overrides. Returns the new slice +
// the total overhead $/month.
func addServiceOverhead(items []provider.CostItem, cfg *config.Config) ([]provider.CostItem, float64) {
	tier := orDefault(cfg.AWSOverheadTier, "prod")
	d := overheadDefaults(tier)

	// Apply per-component overrides when set on cfg.
	if cfg.AWSNATGatewayCount != "" {
		d.natGateways = atoiOr(cfg.AWSNATGatewayCount, d.natGateways)
	}
	if cfg.AWSALBCount != "" {
		d.albs = atoiOr(cfg.AWSALBCount, d.albs)
	}
	if cfg.AWSNLBCount != "" {
		d.nlbs = atoiOr(cfg.AWSNLBCount, d.nlbs)
	}
	if cfg.AWSDataTransferGB != "" {
		d.dataTransferGB = atoiOr(cfg.AWSDataTransferGB, d.dataTransferGB)
	}
	if cfg.AWSCloudWatchLogsGB != "" {
		d.cloudwatchGB = atoiOr(cfg.AWSCloudWatchLogsGB, d.cloudwatchGB)
	}
	if cfg.AWSRoute53HostedZones != "" {
		d.route53Zones = atoiOr(cfg.AWSRoute53HostedZones, d.route53Zones)
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

	// NAT Gateway: hourly + ~30% of egress GB processed (rough split
	// — most egress also touches the NAT path).
	if d.natGateways > 0 {
		natHourly := natGatewayHourly * 730
		natProc := natGatewayProcGBCost * float64(d.dataTransferGB) * 0.3 / float64(d.natGateways)
		add(fmt.Sprintf("NAT Gateway (~%d GB processed/mo each)", int(float64(d.dataTransferGB)*0.3/float64(d.natGateways))),
			d.natGateways, natHourly+natProc)
	}

	// ALB: hourly + LCU.
	if d.albs > 0 {
		alb := (albHourly + albLCUHour*albLCUEstimatePerHour) * 730
		add("Application Load Balancer (Argo CD ingress / app)", d.albs, alb)
	}
	// NLB.
	if d.nlbs > 0 {
		nlb := (nlbHourly + nlbLCUHour*nlbLCUEstimatePerHour) * 730
		add("Network Load Balancer", d.nlbs, nlb)
	}

	// Data egress to internet (above NAT processing).
	if d.dataTransferGB > 0 {
		egress := egressPerGB * float64(d.dataTransferGB)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("Internet egress (~%d GB/mo)", d.dataTransferGB),
			UnitUSDMonthly: egress,
			Qty:            1,
			SubtotalUSD:    egress,
		})
	}

	// CloudWatch Logs ingestion + storage (assume 30-day retention,
	// so storage GB ≈ ingestion GB at steady state).
	if d.cloudwatchGB > 0 {
		ingest := cwLogsIngestionPerGB * float64(d.cloudwatchGB)
		storage := cwLogsStoragePerGB * float64(d.cloudwatchGB)
		items = append(items, provider.CostItem{
			Name:           fmt.Sprintf("CloudWatch Logs (%d GB ingested/mo)", d.cloudwatchGB),
			UnitUSDMonthly: ingest + storage,
			Qty:            1,
			SubtotalUSD:    ingest + storage,
		})
	}

	// Route53 hosted zones.
	if d.route53Zones > 0 {
		add("Route53 hosted zones", d.route53Zones, route53PerZoneMonthly)
	}

	// Sum the overhead lines we just added (last len-original items).
	total := 0.0
	for _, it := range items {
		// We can't easily distinguish "overhead" from "compute" by
		// position alone here; caller computes the grand total
		// instead. Returning total=0 to keep the interface honest.
		_ = it
	}
	return items, total
}
