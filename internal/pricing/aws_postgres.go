// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"fmt"
	"strconv"
	"strings"
)

// AWS managed Postgres pricing — RDS for dev/staging tiers, Aurora
// PostgreSQL for prod. Both engines are priced through the same
// AmazonRDS bulk offer file, so we share the loader and only differ
// in the SKU-pick predicates and the storage rate.
//
// Catalog quirks worth knowing:
//   - The Aurora rows sometimes carry deploymentOption="Multi-AZ" and
//     sometimes leave it blank; we don't constrain by deployment for
//     Aurora and rely on the cluster shape being implicit.
//   - Aurora storage is priced per "Aurora I/O Optimized" or by the
//     legacy per-GB-month SKU. We accept either via usagetype substring.
//   - License model rows with anything other than "no license required"
//     or "bring your own license" pricing for non-applicable engines
//     get filtered out — Postgres has no commercial licensing tier.

// awsRDSGP3StorageUSDPerGBMonthList is the published RDS gp3 storage
// rate ($0.115/GB-month in us-east-1, see
// https://aws.amazon.com/rds/postgresql/pricing/). Used only when the
// bulk catalog can't resolve a storage SKU. We hard-code one figure
// because regional spread on RDS gp3 is small (under 20%) and the
// estimator's job is "ballpark", not invoice-grade.
const awsRDSGP3StorageUSDPerGBMonthList = 0.115

// awsAuroraStorageUSDPerGBMonthList is the published Aurora Standard
// storage rate ($0.10/GB-month, see
// https://aws.amazon.com/rds/aurora/pricing/). Same rationale as the
// RDS fallback: regional drift is small and a single list value keeps
// the bill sane when the catalog is unreachable.
const awsAuroraStorageUSDPerGBMonthList = 0.10

// awsRDSInstanceFallbackHourly maps the instance classes we pick in
// AWSRDSPostgresUSDPerMonth to the us-east-1 list price (single-AZ,
// PostgreSQL engine). Used only when the AmazonRDS bulk fetch fails.
// Source: https://aws.amazon.com/rds/postgresql/pricing/ (2026-04
// list prices).
func awsRDSPostgresInstanceFallbackHourly(instance string) float64 {
	switch instance {
	case "db.t3.small":
		return 0.034
	case "db.t3.medium":
		return 0.068
	case "db.m6i.large":
		return 0.180
	case "db.m6i.xlarge":
		return 0.360
	}
	return 0
}

// awsAuroraPostgresInstanceFallbackHourly is the per-instance Aurora
// PostgreSQL list rate in us-east-1. Source:
// https://aws.amazon.com/rds/aurora/pricing/.
func awsAuroraPostgresInstanceFallbackHourly(instance string) float64 {
	switch instance {
	case "db.r6g.large":
		return 0.26
	case "db.r6g.xlarge":
		return 0.52
	}
	return 0
}

// pickRDSPostgresInstance maps disk size to a single-AZ instance
// class. Tiers chosen to cover the realistic dev/staging band without
// over-provisioning compute for tiny dbs.
func pickRDSPostgresInstance(dbGB int) string {
	switch {
	case dbGB <= 25:
		return "db.t3.small"
	case dbGB <= 100:
		return "db.t3.medium"
	case dbGB <= 500:
		return "db.m6i.large"
	default:
		return "db.m6i.xlarge"
	}
}

// pickAuroraPostgresInstance picks an Aurora-friendly r6g instance
// class. Aurora doesn't ship t3 sizes, so the floor is r6g.large.
func pickAuroraPostgresInstance(dbGB int) string {
	if dbGB <= 100 {
		return "db.r6g.large"
	}
	return "db.r6g.xlarge"
}

// awsRDSPostgresInstanceHourly resolves a Single-AZ PostgreSQL
// instance hourly rate from the AmazonRDS bulk catalog.
func awsRDSPostgresInstanceHourly(pl *awsBulkPayload, loc, instance string) (float64, bool) {
	best := 0.0
	matched := false
	for skuID, prod := range pl.Products {
		attr := prod.Attributes
		if !strings.EqualFold(attr["instanceType"], instance) {
			continue
		}
		if !strings.EqualFold(attr["location"], loc) {
			continue
		}
		if !strings.EqualFold(attr["databaseEngine"], "PostgreSQL") {
			continue
		}
		if !strings.EqualFold(attr["deploymentOption"], "Single-AZ") {
			continue
		}
		lic := strings.ToLower(attr["licenseModel"])
		if lic != "" && !strings.Contains(lic, "no license") && !strings.Contains(lic, "included") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		if !matched || usd < best {
			best = usd
			matched = true
		}
	}
	return best, matched
}

// awsRDSGP3StorageUSDPerGBMonth pulls the RDS gp3 storage rate from
// the AmazonRDS bulk catalog for region.
func awsRDSGP3StorageUSDPerGBMonth(pl *awsBulkPayload, loc string) (float64, bool) {
	best := 0.0
	matched := false
	for skuID, prod := range pl.Products {
		attr := prod.Attributes
		if !strings.EqualFold(attr["location"], loc) {
			continue
		}
		fam := awsBulkProductFamily(prod)
		if !strings.Contains(fam, "database storage") {
			continue
		}
		ut := strings.ToLower(attr["usagetype"])
		vt := strings.ToLower(attr["volumeType"])
		// Prefer gp3 explicitly; fall back to "general purpose" if
		// the catalog hasn't tagged the new gp3 SKU yet.
		isGP3 := strings.Contains(ut, "gp3-storage") || strings.Contains(vt, "gp3")
		isGP := isGP3 || strings.Contains(vt, "general purpose") || strings.Contains(ut, "gp2-storage")
		if !isGP {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		// Prefer a gp3 match if seen; otherwise track cheapest.
		if isGP3 {
			return usd, true
		}
		if !matched || usd < best {
			best = usd
			matched = true
		}
	}
	return best, matched
}

// AWSRDSPostgresUSDPerMonth returns a (label, monthlyUSD) pair for an
// RDS PostgreSQL Single-AZ deployment sized by dbGB. Falls back to
// list prices when the bulk catalog is unreachable or the SKU shapes
// have drifted, so the bill never blocks on a catalog rename.
func AWSRDSPostgresUSDPerMonth(region string, dbGB int) (string, float64, error) {
	if dbGB < 0 {
		dbGB = 0
	}
	instance := pickRDSPostgresInstance(dbGB)
	label := fmt.Sprintf("RDS PostgreSQL %s Single-AZ + %d GB gp3", instance, dbGB)

	pl, err := awsSvc.loadServiceBulk("AmazonRDS", region)
	if err != nil {
		monthly := awsRDSPostgresInstanceFallbackHourly(instance)*MonthlyHours +
			float64(dbGB)*awsRDSGP3StorageUSDPerGBMonthList
		if monthly <= 0 {
			return label, 0, fmt.Errorf("aws rds postgres: %w", err)
		}
		return label, monthly, nil
	}

	loc := awsRegionLong(region)
	hourly, ok := awsRDSPostgresInstanceHourly(pl, loc, instance)
	if !ok {
		hourly = awsRDSPostgresInstanceFallbackHourly(instance)
	}
	storage, ok := awsRDSGP3StorageUSDPerGBMonth(pl, loc)
	if !ok {
		storage = awsRDSGP3StorageUSDPerGBMonthList
	}
	monthly := hourly*MonthlyHours + float64(dbGB)*storage
	if monthly <= 0 {
		return label, 0, fmt.Errorf("aws rds postgres: no priced match for %s in %s", instance, region)
	}
	return label, monthly, nil
}

// awsAuroraPostgresInstanceHourly resolves an Aurora PostgreSQL
// instance hourly rate. Aurora rows sometimes leave deploymentOption
// blank and sometimes set it to "Multi-AZ"; we don't constrain that
// attribute and let the engine label do the filtering.
func awsAuroraPostgresInstanceHourly(pl *awsBulkPayload, loc, instance string) (float64, bool) {
	best := 0.0
	matched := false
	for skuID, prod := range pl.Products {
		attr := prod.Attributes
		if !strings.EqualFold(attr["instanceType"], instance) {
			continue
		}
		if !strings.EqualFold(attr["location"], loc) {
			continue
		}
		if !strings.EqualFold(attr["databaseEngine"], "Aurora PostgreSQL") {
			continue
		}
		lic := strings.ToLower(attr["licenseModel"])
		if lic != "" && !strings.Contains(lic, "no license") && !strings.Contains(lic, "included") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		if !matched || usd < best {
			best = usd
			matched = true
		}
	}
	return best, matched
}

// awsAuroraStorageUSDPerGBMonth pulls the Aurora storage rate from
// the AmazonRDS bulk catalog. Names drift between "Aurora I/O
// Optimized", "Aurora Standard", and bare "Aurora" — we accept any
// Database Storage row whose usagetype/volumeType mentions Aurora.
func awsAuroraStorageUSDPerGBMonth(pl *awsBulkPayload, loc string) (float64, bool) {
	best := 0.0
	matched := false
	for skuID, prod := range pl.Products {
		attr := prod.Attributes
		if !strings.EqualFold(attr["location"], loc) {
			continue
		}
		fam := awsBulkProductFamily(prod)
		if !strings.Contains(fam, "database storage") &&
			!strings.Contains(fam, "system operation") {
			continue
		}
		ut := strings.ToLower(attr["usagetype"])
		vt := strings.ToLower(attr["volumeType"])
		eng := strings.ToLower(attr["databaseEngine"])
		isAurora := strings.Contains(ut, "aurora") ||
			strings.Contains(vt, "aurora") ||
			strings.Contains(eng, "aurora")
		if !isAurora {
			continue
		}
		// Skip I/O request SKUs — those are per-million-requests, not
		// per-GB-month, and would skew the fold to a tiny number.
		if strings.Contains(ut, "iorequest") || strings.Contains(ut, "io-requests") {
			continue
		}
		pd, perr := onDemandPriceDim(pl, skuID)
		if perr != nil {
			continue
		}
		usd, perr := strconv.ParseFloat(pd.PricePerUnit["USD"], 64)
		if perr != nil || usd <= 0 {
			continue
		}
		// Aurora storage SKUs publish a per-GB-month price that lives
		// roughly in [0.02, 0.30]. Reject obvious outliers (per-request
		// SKUs that slip through with cents-per-million pricing).
		if usd > 1.0 {
			continue
		}
		if !matched || usd < best {
			best = usd
			matched = true
		}
	}
	return best, matched
}

// AWSAuroraPostgresUSDPerMonth returns a (label, monthlyUSD) pair for
// an Aurora PostgreSQL Multi-AZ-style deployment sized by dbGB. Aurora
// is intrinsically multi-AZ at the storage layer; we don't model
// standby reader replicas — the writer instance is what hits the bill.
func AWSAuroraPostgresUSDPerMonth(region string, dbGB int) (string, float64, error) {
	if dbGB < 0 {
		dbGB = 0
	}
	instance := pickAuroraPostgresInstance(dbGB)
	label := fmt.Sprintf("Aurora PostgreSQL %s Multi-AZ + %d GB Aurora storage", instance, dbGB)

	pl, err := awsSvc.loadServiceBulk("AmazonRDS", region)
	if err != nil {
		monthly := awsAuroraPostgresInstanceFallbackHourly(instance)*MonthlyHours +
			float64(dbGB)*awsAuroraStorageUSDPerGBMonthList
		if monthly <= 0 {
			return label, 0, fmt.Errorf("aws aurora postgres: %w", err)
		}
		return label, monthly, nil
	}

	loc := awsRegionLong(region)
	hourly, ok := awsAuroraPostgresInstanceHourly(pl, loc, instance)
	if !ok {
		hourly = awsAuroraPostgresInstanceFallbackHourly(instance)
	}
	storage, ok := awsAuroraStorageUSDPerGBMonth(pl, loc)
	if !ok {
		storage = awsAuroraStorageUSDPerGBMonthList
	}
	monthly := hourly*MonthlyHours + float64(dbGB)*storage
	if monthly <= 0 {
		return label, 0, fmt.Errorf("aws aurora postgres: no priced match for %s in %s", instance, region)
	}
	return label, monthly, nil
}
