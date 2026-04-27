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

func awsManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	return ManagedPostgres{}, errNotImplemented("aws")
}

func azureManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	return ManagedPostgres{}, errNotImplemented("azure")
}

func gcpManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	return ManagedPostgres{}, errNotImplemented("gcp")
}

func doManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	return ManagedPostgres{}, errNotImplemented("digitalocean")
}

func linodeManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	return ManagedPostgres{}, errNotImplemented("linode")
}

func ociManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	return ManagedPostgres{}, errNotImplemented("oci")
}

func ibmcloudManagedPostgres(region string, tier PostgresTier, dbGB int) (ManagedPostgres, error) {
	return ManagedPostgres{}, errNotImplemented("ibmcloud")
}

func errNotImplemented(vendor string) error {
	return fmt.Errorf("%w: managed postgres helper for %q not implemented yet",
		provider.ErrNotApplicable, vendor)
}

// Compile-time guard so unused imports aren't dropped while the
// stubs are in place. Removed once helpers go live.
var _ = pricing.MonthlyHours
