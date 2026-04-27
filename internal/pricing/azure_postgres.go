// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"fmt"
	"strings"
)

// azure_postgres.go — Azure Database for PostgreSQL Flexible Server.
//
// Pricing flow:
//   1. Pick a SKU shape from dbGB (Burstable B1ms for dev/small,
//      General Purpose D{2,4,8}ds_v{4,5} for larger sizes).
//   2. Hit the Retail Prices API with serviceFamily filter for
//      "Azure Database for PostgreSQL Flexible Server" + the SKU
//      meter, take the cheapest hourly Consumption row.
//   3. Hit the same service for Premium SSD storage (per-GB-month).
//   4. When zone-redundant HA is requested, double both compute and
//      storage — Microsoft bills the standby on the same meter as
//      the primary, and the HA replica volume mirrors the primary
//      Premium SSD allocation. (See "Pricing model" on
//      https://azure.microsoft.com/en-us/pricing/details/postgresql/flexible-server/.)
//
// Retail API quirks observed:
//   - The "service" varies between rows: some carry serviceName
//     "Azure Database for PostgreSQL" with productName containing
//     "Flexible Server", others carry serviceName
//     "Azure Database for PostgreSQL Flexible Server" directly.
//     We match on productName substring to cover both shapes.
//   - meterName for compute is typically just the vCore SKU
//     ("D2ds v5", "B1ms"); skuName carries the full
//     "Standard_D2ds_v5" form. We try matching on either.
//   - There are v4 and v5 General Purpose meters in most regions;
//     when both are present we prefer the cheapest match.

// AzureFlexiblePostgresUSDPerMonth returns a (label, monthly USD)
// pair for an Azure Database for PostgreSQL Flexible Server sized
// to fit dbGB, optionally zone-redundant (HA).
func AzureFlexiblePostgresUSDPerMonth(region string, dbGB int, zoneRedundant bool) (label string, monthly float64, err error) {
	shape := pickAzureFlexPGShape(dbGB)

	computeMonthly, computeErr := azureFlexPGComputeUSDPerMonth(region, shape)
	storageMonthly, storageErr := azureFlexPGStorageUSDPerMonth(region, dbGB)

	if computeErr != nil || storageMonthly <= 0 || storageErr != nil || computeMonthly <= 0 {
		// Fall through to fallback list-prices when either component
		// failed; the caller should still get a useful number.
		fb, ok := azureFlexiblePostgresFallbackUSDPerMonth(shape, dbGB, zoneRedundant)
		if !ok {
			if computeErr != nil {
				return "", 0, computeErr
			}
			if storageErr != nil {
				return "", 0, storageErr
			}
			return "", 0, fmt.Errorf("azure flex pg: no live or fallback price for %q", shape.armSku)
		}
		return azureFlexPGLabel(shape, dbGB, zoneRedundant), fb, nil
	}

	if zoneRedundant {
		computeMonthly *= 2
		storageMonthly *= 2
	}
	return azureFlexPGLabel(shape, dbGB, zoneRedundant), computeMonthly + storageMonthly, nil
}

// azureFlexPGShape carries the meter-matching hints for a chosen
// Flexible Server SKU. armSku is what skuName looks like in the
// Retail catalog ("Standard_B1ms", "Standard_D2ds_v5"); meterMatch
// holds the lower-cased substrings any one of which must appear in
// either skuName or meterName for the row to be a candidate.
type azureFlexPGShape struct {
	human      string   // "Burstable B1ms", "General Purpose D2ds_v5"
	armSku     string   // "Standard_B1ms"
	meterMatch []string // ["b1ms"], ["d2ds v4", "d2ds v5", "d2ds_v4", "d2ds_v5"]
}

func pickAzureFlexPGShape(dbGB int) azureFlexPGShape {
	switch {
	case dbGB <= 25:
		return azureFlexPGShape{
			human:      "Burstable B1ms",
			armSku:     "Standard_B1ms",
			meterMatch: []string{"b1ms"},
		}
	case dbGB <= 100:
		return azureFlexPGShape{
			human:      "General Purpose D2ds_v5",
			armSku:     "Standard_D2ds_v5",
			meterMatch: []string{"d2ds v5", "d2ds_v5", "d2ds v4", "d2ds_v4"},
		}
	case dbGB <= 500:
		return azureFlexPGShape{
			human:      "General Purpose D4ds_v5",
			armSku:     "Standard_D4ds_v5",
			meterMatch: []string{"d4ds v5", "d4ds_v5", "d4ds v4", "d4ds_v4"},
		}
	default:
		return azureFlexPGShape{
			human:      "General Purpose D8ds_v5",
			armSku:     "Standard_D8ds_v5",
			meterMatch: []string{"d8ds v5", "d8ds_v5", "d8ds v4", "d8ds_v4"},
		}
	}
}

func azureFlexPGLabel(shape azureFlexPGShape, dbGB int, zoneRedundant bool) string {
	suffix := ""
	if zoneRedundant {
		suffix = " Zone-Redundant"
	}
	return fmt.Sprintf("Azure DB for PostgreSQL Flex %s%s + %d GB", shape.human, suffix, dbGB)
}

// azureFlexPGComputeUSDPerMonth returns the per-instance monthly cost
// of the Flexible Server compute meter matching shape. We pick the
// cheapest non-Windows, non-reserved Consumption row whose meterName
// or skuName matches one of shape.meterMatch.
func azureFlexPGComputeUSDPerMonth(region string, shape azureFlexPGShape) (float64, error) {
	filter := fmt.Sprintf(
		"priceType eq 'Consumption' and armRegionName eq '%s'",
		region,
	)
	items, err := azureRetail(filter)
	if err != nil {
		return 0, err
	}
	best := 0.0
	found := false
	for _, it := range items {
		if !isAzureFlexPGProduct(it.ProductName) {
			continue
		}
		// Compute meters are hourly per vCore SKU. Storage rows are
		// per GB-month and live in the same product family — skip
		// those by requiring an hour unit of measure.
		uom := strings.ToLower(it.UnitOfMeasure)
		if !strings.Contains(uom, "hour") {
			continue
		}
		nm := strings.ToLower(it.SkuName + " " + it.MeterName + " " + it.ArmSkuName)
		if strings.Contains(nm, "windows") {
			continue
		}
		hit := false
		for _, m := range shape.meterMatch {
			if strings.Contains(nm, m) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		if it.RetailPrice <= 0 {
			continue
		}
		if !found || it.RetailPrice < best {
			best = it.RetailPrice
			found = true
		}
	}
	if !found {
		return 0, fmt.Errorf("azure flex pg: no compute price for %q in %q", shape.armSku, region)
	}
	return best * MonthlyHours, nil
}

// azureFlexPGStorageUSDPerMonth returns the cost of dbGB of Premium
// SSD storage on a Flexible Server in region. Premium SSD is the
// default and only Flexible-Server-attached storage tier for the
// SKU shapes we pick.
func azureFlexPGStorageUSDPerMonth(region string, dbGB int) (float64, error) {
	filter := fmt.Sprintf(
		"priceType eq 'Consumption' and armRegionName eq '%s'",
		region,
	)
	items, err := azureRetail(filter)
	if err != nil {
		return 0, err
	}
	best := 0.0
	found := false
	for _, it := range items {
		if !isAzureFlexPGProduct(it.ProductName) {
			continue
		}
		nm := strings.ToLower(it.MeterName + " " + it.ProductName + " " + it.SkuName)
		if !strings.Contains(nm, "storage") {
			continue
		}
		// Skip backup-storage and IOPS meters — we want the per-GB
		// allocated volume rate.
		if strings.Contains(nm, "backup") || strings.Contains(nm, "iops") {
			continue
		}
		uom := strings.ToLower(it.UnitOfMeasure)
		if !strings.Contains(uom, "gb") {
			continue
		}
		if it.RetailPrice <= 0 {
			continue
		}
		if !found || it.RetailPrice < best {
			best = it.RetailPrice
			found = true
		}
	}
	if !found {
		return 0, fmt.Errorf("azure flex pg: no storage price in %q", region)
	}
	return best * float64(dbGB), nil
}

// isAzureFlexPGProduct returns true when productName looks like a
// PostgreSQL Flexible Server meter. The Retail catalog inconsistently
// uses "Azure Database for PostgreSQL Flexible Server" vs just
// "PostgreSQL Flexible Server" prefixed with the SKU family.
func isAzureFlexPGProduct(productName string) bool {
	p := strings.ToLower(productName)
	if !strings.Contains(p, "postgresql") {
		return false
	}
	return strings.Contains(p, "flexible server")
}

// azureFlexiblePostgresFallbackUSDPerMonth returns Microsoft list
// prices for the Flexible Server shapes we pick when the Retail
// Prices API is unreachable. Source:
// https://azure.microsoft.com/en-us/pricing/details/postgresql/flexible-server/
// (East US, Pay-As-You-Go, captured 2026-04). The values include
// Premium SSD storage at ~$0.115 per GB-month and zone-redundant
// HA doubles both the compute and storage components.
func azureFlexiblePostgresFallbackUSDPerMonth(shape azureFlexPGShape, dbGB int, zoneRedundant bool) (float64, bool) {
	// East US Pay-As-You-Go USD/hour list rates (compute only).
	hourly := map[string]float64{
		"Standard_B1ms":    0.0260, // ~$18.98/mo
		"Standard_D2ds_v5": 0.2230, // ~$162.79/mo
		"Standard_D4ds_v5": 0.4460, // ~$325.58/mo
		"Standard_D8ds_v5": 0.8920, // ~$651.16/mo
	}
	rate, ok := hourly[shape.armSku]
	if !ok {
		return 0, false
	}
	const storagePerGB = 0.115 // Premium SSD GB-month, US list price.
	compute := rate * MonthlyHours
	storage := storagePerGB * float64(dbGB)
	if zoneRedundant {
		compute *= 2
		storage *= 2
	}
	return compute + storage, true
}
