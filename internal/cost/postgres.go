// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cost

// postgres.go — managed Postgres cost dispatch.
//
// Each cloud vendor with a Postgres-compatible SaaS exposes per-
// vendor pricing helpers (RDS / Aurora / Cloud SQL / Azure DB for
// PG / DO / Linode / OCI / IBM). This file is the routing layer:
// callers ask for a tier (dev/staging/prod) + region + DB size,
// and get back a (price, sku-label) pair plus an error.
//
// The tier shapes the SKU choice. AWS specifically uses RDS for
// dev/staging (cheaper, single-AZ, smaller instance) and Aurora
// for prod (multi-AZ, higher throughput). Other vendors stay on
// their default managed-PG tier and scale the size by environment.

import (
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
)

// PostgresTier picks the SKU shape per environment. yage maps the
// xapiri envTier ("dev"/"staging"/"prod") onto this enum so the
// vendor-specific helpers don't need to know about envs.
type PostgresTier string

const (
	PostgresDev     PostgresTier = "dev"
	PostgresStaging PostgresTier = "staging"
	PostgresProd    PostgresTier = "prod"
)

// IsProd reports whether the tier should pick the higher-end SKU
// (Aurora on AWS, multi-AZ on others). Staging is treated as dev
// for cost purposes — operators wanting prod-grade staging can
// override the override-flags or pick prod explicitly.
func (t PostgresTier) IsProd() bool {
	return strings.EqualFold(string(t), string(PostgresProd))
}

// ManagedPostgres is the result of a SaaS Postgres price lookup.
// SKU is the human-readable label that goes into the bill line;
// MonthlyUSD is the live live-fetched per-instance figure.
type ManagedPostgres struct {
	SKU        string
	MonthlyUSD float64
}

// ManagedPostgresUSDPerMonth routes to the per-vendor helper for
// the active provider and returns (label, monthlyUSD). Returns
// provider.ErrNotApplicable when the vendor doesn't offer managed
// Postgres or the catalog can't be reached.
func ManagedPostgresUSDPerMonth(vendor, region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	switch vendor {
	case "aws":
		return awsManagedPostgres(region, tier, dbGB)
	case "azure":
		return azureManagedPostgres(region, tier, dbGB)
	case "gcp":
		return gcpManagedPostgres(region, tier, dbGB)
	case "digitalocean":
		return doManagedPostgres(region, tier, dbGB)
	case "linode":
		return linodeManagedPostgres(region, tier, dbGB)
	case "oci":
		return ociManagedPostgres(region, tier, dbGB)
	case "ibmcloud":
		return ibmcloudManagedPostgres(region, tier, dbGB)
	}
	return ManagedPostgres{}, fmt.Errorf("%w: managed postgres not offered on %q",
		provider.ErrNotApplicable, vendor)
}

// Per-vendor helpers below are stubs that return ErrNotApplicable
// until each vendor's pricing helper lands. The dispatcher routes
// through them now so per-provider cost.go integration can be
// written against the stable signature.
//
// When a vendor's helper lands, replace the stub body with the
// real implementation. Compile-time signature stays the same.

// awsManagedPostgres routes to Aurora when tier.IsProd() (multi-AZ
// flavored, higher throughput) or RDS otherwise (cheaper Single-AZ).
// The pricing layer reads the AmazonRDS bulk JSON and falls back to
// hard-coded list prices when the SKU shape can't be resolved.
func awsManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	if tier.IsProd() {
		label, monthly, err := pricing.AWSAuroraPostgresUSDPerMonth(region, dbGB)
		if err != nil {
			return ManagedPostgres{}, fmt.Errorf("%w: aws: %v",
				provider.ErrNotApplicable, err)
		}
		return ManagedPostgres{SKU: label, MonthlyUSD: monthly}, nil
	}
	label, monthly, err := pricing.AWSRDSPostgresUSDPerMonth(region, dbGB)
	if err != nil {
		return ManagedPostgres{}, fmt.Errorf("%w: aws: %v",
			provider.ErrNotApplicable, err)
	}
	return ManagedPostgres{SKU: label, MonthlyUSD: monthly}, nil
}

// azureManagedPostgres prices Azure Database for PostgreSQL Flexible
// Server. tier.IsProd() trips zone-redundant HA (compute + storage
// doubled to bill the standby). The pricing layer falls back to the
// public list when the Retail Prices API isn't reachable.
func azureManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	zoneRedundant := tier.IsProd()
	label, monthly, err := pricing.AzureFlexiblePostgresUSDPerMonth(region, dbGB, zoneRedundant)
	if err != nil {
		return ManagedPostgres{}, fmt.Errorf("%w: azure: %v",
			provider.ErrNotApplicable, err)
	}
	return ManagedPostgres{SKU: label, MonthlyUSD: monthly}, nil
}

// gcpManagedPostgres prices Cloud SQL for PostgreSQL Enterprise.
// tier.IsProd() trips the regional (HA) layout — compute and storage
// doubled to bill the cross-zone replica. The pricing layer falls
// back to the public list when the Cloud Billing Catalog isn't
// reachable.
func gcpManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	regional := tier.IsProd()
	label, monthly, err := pricing.GCPCloudSQLPostgresUSDPerMonth(region, dbGB, regional)
	if err != nil {
		return ManagedPostgres{}, fmt.Errorf("%w: gcp: %v",
			provider.ErrNotApplicable, err)
	}
	return ManagedPostgres{SKU: label, MonthlyUSD: monthly}, nil
}

// doManagedPostgres picks the smallest single-node DO Managed Postgres
// SKU that fits the requested DB size. DO doesn't expose a separate
// "prod" tier, so the prod path simply jumps one size class — the
// next-up VM is the closest thing to a multi-AZ-flavored upgrade DO's
// catalog offers (DO bills HA with per-standby surcharges that aren't
// modeled here). Falls back to the published list price when the
// /v2/databases/options endpoint isn't reachable.
func doManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	size := doPostgresSize(tier, dbGB)
	usd, err := pricing.DOManagedPostgresUSDPerMonth(size)
	if err != nil {
		return ManagedPostgres{}, fmt.Errorf("%w: digitalocean: %v",
			provider.ErrNotApplicable, err)
	}
	return ManagedPostgres{
		SKU:        fmt.Sprintf("DO Managed Postgres %s", size),
		MonthlyUSD: usd,
	}, nil
}

// doPostgresSize maps tier + dbGB onto the DO node-size slug. The
// dev/staging path picks the cheapest single-vCPU shape; prod jumps
// to the 2-vCPU/4-GB shape, which is the smallest instance DO bills
// at the production-grade price tier.
func doPostgresSize(tier PostgresTier, dbGB int) string {
	if tier.IsProd() || dbGB > 25 {
		return "db-s-2vcpu-4gb"
	}
	return "db-s-1vcpu-1gb"
}

// linodeManagedPostgres picks the Linode/Akamai DBaaS node size that
// fits the workload tier + storage. Linode also has no dedicated prod
// upgrade path beyond instance-size — prod and large-DB requests roll
// up to the standard-2 shape; everything else stays on nanode-1. The
// pricing helper falls back to the public list when the catalog
// endpoint is unavailable.
func linodeManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	size := linodePostgresSize(tier, dbGB)
	usd, err := pricing.LinodeManagedPostgresUSDPerMonth(size)
	if err != nil {
		return ManagedPostgres{}, fmt.Errorf("%w: linode: %v",
			provider.ErrNotApplicable, err)
	}
	return ManagedPostgres{
		SKU:        fmt.Sprintf("Linode Managed Postgres %s", size),
		MonthlyUSD: usd,
	}, nil
}

func linodePostgresSize(tier PostgresTier, dbGB int) string {
	if tier.IsProd() || dbGB > 25 {
		return "g6-standard-2"
	}
	return "g6-nanode-1"
}

// ociManagedPostgres prices OCI Database with PostgreSQL. tier.IsProd()
// trips the 3-node Multiple-AD HA layout; non-prod stays single-AD.
// The pricing helper transparently falls back to the public list when
// the cetools catalog can't be reached.
func ociManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	multiAD := tier.IsProd()
	label, monthly, err := pricing.OCIPostgresUSDPerMonth(region, dbGB, multiAD)
	if err != nil {
		return ManagedPostgres{}, fmt.Errorf("%w: oci: %v",
			provider.ErrNotApplicable, err)
	}
	return ManagedPostgres{SKU: label, MonthlyUSD: monthly}, nil
}

func ibmcloudManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	return ManagedPostgres{}, errNotImplemented("ibmcloud")
}

func errNotImplemented(vendor string) error {
	return fmt.Errorf("%w: managed postgres helper for %q not implemented yet",
		provider.ErrNotApplicable, vendor)
}

