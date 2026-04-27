// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"fmt"
)

// gcp_postgres.go — live Cloud SQL for PostgreSQL pricing.
//
// Cloud SQL Enterprise edition bills compute as two separate
// catalog line items per instance: a per-vCPU-hour rate and a
// per-GB-RAM-hour rate. Storage is a per-GB-month SSD rate.
// Regional (HA) instances run a synchronous standby in a second
// zone and bill compute + storage at 2× zonal — that doubling is
// applied here rather than expecting the catalog to expose
// distinct "regional" SKUs (which it doesn't, in a stable shape).
//
// Description shape on the catalog drifts ("Cloud SQL: DB Custom
// PostgreSQL vCPU running in <region>", "Cloud SQL Enterprise -
// PostgreSQL: vCPU - <region>", etc.). We try several substring
// combinations, then fall back to the public list price so the
// estimator surfaces a number rather than failing the row.

// gcpCloudSQLShape captures the per-shape vCPU/RAM split used to
// size a Cloud SQL Enterprise instance. The machine-type name is
// purely cosmetic for the bill label — pricing is computed from
// the vCPU count + RAM GB against the catalog's per-vCPU-hour and
// per-GB-RAM-hour SKUs.
type gcpCloudSQLShape struct {
	machineType string
	vCPU        int
	ramGB       float64
}

// gcpCloudSQLShapeFor picks the smallest Enterprise shape that
// comfortably hosts dbGB. Thresholds match the spec: ≤25, ≤100,
// ≤500, else 8/30.
func gcpCloudSQLShapeFor(dbGB int) gcpCloudSQLShape {
	switch {
	case dbGB <= 25:
		return gcpCloudSQLShape{"db-custom-1-3840", 1, 3.75}
	case dbGB <= 100:
		return gcpCloudSQLShape{"db-custom-2-7680", 2, 7.5}
	case dbGB <= 500:
		return gcpCloudSQLShape{"db-custom-4-15360", 4, 15}
	default:
		return gcpCloudSQLShape{"db-custom-8-30720", 8, 30}
	}
}

// GCPCloudSQLPostgresUSDPerMonth returns the monthly $ for a
// Cloud SQL for PostgreSQL Enterprise instance sized to dbGB,
// in `region`, optionally regional (HA). The label embeds the
// shape, HA flag and storage so it's directly usable as a bill-
// line SKU.
func GCPCloudSQLPostgresUSDPerMonth(region string, dbGB int, regional bool) (string, float64, error) {
	shape := gcpCloudSQLShapeFor(dbGB)
	storageRate := gcpCloudSQLSSDRate(regional)
	storageMonthly := storageRate * float64(dbGB)

	haTag := ""
	if regional {
		haTag = " HA"
	}
	label := fmt.Sprintf("Cloud SQL PostgreSQL %dvCPU/%gGB%s + %d GB SSD",
		shape.vCPU, shape.ramGB, haTag, dbGB)

	key := gcpAPIKey()
	if key == "" {
		// No API key — surface the public list price so the
		// estimator stays useful. Mirrors GCPLoadBalancerUSDPerMonth
		// / GCPCloudDNSZoneUSDPerMonth.
		monthly := gcpCloudSQLPostgresFallbackUSDPerMonth(shape, dbGB, regional)
		return label, monthly, nil
	}

	vcpuHourly, okV := gcpResolveCloudSQLPGRate(region, key, true /*vCPU*/)
	ramHourly, okR := gcpResolveCloudSQLPGRate(region, key, false /*RAM*/)
	if !okV || !okR {
		// Catalog mismatch — drop to list price rather than
		// blocking the bill on an SKU rename.
		monthly := gcpCloudSQLPostgresFallbackUSDPerMonth(shape, dbGB, regional)
		return label, monthly, nil
	}

	computeMonthly := (vcpuHourly*float64(shape.vCPU) + ramHourly*shape.ramGB) * MonthlyHours
	if regional {
		// HA standby in a second zone doubles compute too.
		computeMonthly *= 2
	}
	return label, computeMonthly + storageMonthly, nil
}

// gcpCloudSQLSSDRate returns the per-GB-month SSD rate. Regional
// (HA) Cloud SQL replicates storage to a second zone, so the
// effective per-GB rate is 2× zonal.
func gcpCloudSQLSSDRate(regional bool) float64 {
	if regional {
		return 0.34
	}
	return 0.17
}

// gcpResolveCloudSQLPGRate scans the Cloud SQL catalog for the
// per-vCPU-hour or per-GB-RAM-hour rate of PostgreSQL Enterprise
// SKUs. Description shapes drift across catalog revisions; we
// try several substring combinations against the workload region
// first, then any region.
//
// The "PostgreSQL" qualifier is what disambiguates from the MySQL
// and SQL Server line items on the same service ID. We also exclude
// "Enterprise Plus" (a separate, pricier tier).
func gcpResolveCloudSQLPGRate(region, key string, wantVCPU bool) (float64, bool) {
	var descStrategies [][]string
	mustNotContain := []string{
		"mysql", "sql server", "sqlserver",
		"enterprise plus",
		"replica", "ha", // HA-tagged SKUs duplicate base rate; we double it ourselves
	}
	if wantVCPU {
		// Catalog uses "vCPU" verbatim; older revisions wrote "Core".
		descStrategies = [][]string{
			{"postgresql", "vcpu", "running"},
			{"postgres", "vcpu", "running"},
			{"cloud sql", "postgresql", "vcpu"},
			{"cloud sql", "postgres", "vcpu"},
			{"postgresql", "core", "running"},
			{"postgres", "core", "running"},
			{"db custom", "postgresql", "vcpu"},
		}
	} else {
		descStrategies = [][]string{
			{"postgresql", "ram", "running"},
			{"postgres", "ram", "running"},
			{"cloud sql", "postgresql", "ram"},
			{"cloud sql", "postgres", "ram"},
			{"db custom", "postgresql", "ram"},
			{"postgresql", "memory", "running"},
		}
	}

	regions := []string{region, ""}
	for _, desc := range descStrategies {
		for _, reg := range regions {
			v, err := gcpFindSku(gcpCloudSQLService, reg, key, desc, mustNotContain)
			if err == nil && v > 0 {
				return v, true
			}
		}
	}
	return 0, false
}

// gcpCloudSQLPostgresFallbackUSDPerMonth returns the public
// list-price estimate for a Cloud SQL for PostgreSQL Enterprise
// instance. Reference: https://cloud.google.com/sql/pricing
// (us-central1 Enterprise, on-demand). Used when the API key is
// missing or the catalog matched no PostgreSQL SKU.
//
// Representative per-shape monthly compute prices (zonal):
//
//	db-custom-1-3840    ≈ $50/mo  (1 vCPU + 3.75 GB)
//	db-custom-2-7680    ≈ $99/mo  (2 vCPU + 7.5 GB)
//	db-custom-4-15360   ≈ $198/mo (4 vCPU + 15 GB)
//	db-custom-8-30720   ≈ $396/mo (8 vCPU + 30 GB)
//
// Regional (HA) doubles compute. Storage is added separately at
// $0.17/GB-month zonal or $0.34/GB-month regional.
func gcpCloudSQLPostgresFallbackUSDPerMonth(shape gcpCloudSQLShape, dbGB int, regional bool) float64 {
	var compute float64
	switch shape.machineType {
	case "db-custom-1-3840":
		compute = 50
	case "db-custom-2-7680":
		compute = 99
	case "db-custom-4-15360":
		compute = 198
	case "db-custom-8-30720":
		compute = 396
	default:
		compute = 99
	}
	if regional {
		compute *= 2
	}
	storage := gcpCloudSQLSSDRate(regional) * float64(dbGB)
	return compute + storage
}
