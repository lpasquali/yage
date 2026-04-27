// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// OCI Database with PostgreSQL pricing.
//
// Catalog shape (verified against the public cetools API):
//   B99060 — "Database with PostgreSQL - X86" / metric "OCPU Per Hour"
//   B99062 — "Database Optimized Storage"     / metric "Gigabyte Storage Capacity Per Month"
// serviceCategory == "Database with PostgreSQL" for both. There is
// NO separate per-GB-RAM SKU in the catalog: OCI's managed-Postgres
// OCPU rate is bundled with the standard memory ratio (16 GB per
// OCPU as of the public pricing page). The fallback constants below
// keep a per-OCPU-hour and per-GB-RAM-hour split anyway so the
// estimate remains honest if Oracle ever re-splits the SKUs (their
// public pricing page lists them separately even though the catalog
// rolls them up).
//
// We always price block-volume-style storage off B99062 — the
// "Database Optimized Storage" SKU is the only storage line OCI
// publishes for managed Postgres in the catalog.
//
// HTTP requests use ociHTTPClient (defined in oci.go): an &http.Client{}
// with nil Transport that inherits http.DefaultTransport, keeping the
// airgap shim effective.

// Per-OCPU and per-GB-RAM ratio used to pre-bill memory when the
// catalog OCPU rate is the bundled flavor (no separate RAM SKU).
// 16 GB per OCPU mirrors OCI's default flex ratio.
const ociPostgresRAMPerOCPU = 16

// ociPostgresFallbackUSDPerMonth is the list-price safety net pulled
// from https://www.oracle.com/cloud/postgresql/pricing/ as published
// at writing. Used when the cetools API is unreachable.
//
// Headline rates:
//
//	OCPU       $0.0717 per OCPU-hour
//	Memory     $0.005  per GB-hour
//	Storage    $0.0255 per GB-month
func ociPostgresFallbackUSDPerMonth(ocpu, ramGB, dbGB int, multiAD bool) float64 {
	const (
		ocpuPerHr = 0.0717
		ramPerHr  = 0.005
		stGBMo    = 0.0255
	)
	nodes := 1
	if multiAD {
		nodes = 3
	}
	compute := (float64(ocpu)*ocpuPerHr + float64(ramGB)*ramPerHr) * MonthlyHours
	storage := float64(dbGB) * stGBMo
	return float64(nodes) * (compute + storage)
}

// ociPostgresShape maps requested DB size onto the (OCPU, RAM-GB)
// flavor we bill against. The thresholds match the dispatcher spec.
func ociPostgresShape(dbGB int) (ocpu, ramGB int) {
	switch {
	case dbGB <= 50:
		return 2, 16
	case dbGB <= 200:
		return 4, 32
	default:
		return 8, 64
	}
}

// OCIPostgresUSDPerMonth returns a (label, monthly USD) pair for
// the OCI Database with PostgreSQL service. dbGB drives the OCPU
// flavor pick; multiAD triples both compute and storage to model
// the 3-node Multiple-AD HA layout (each node has its own volume).
func OCIPostgresUSDPerMonth(region string, dbGB int, multiAD bool) (label string, monthly float64, err error) {
	ocpu, ramGB := ociPostgresShape(dbGB)
	suffix := ""
	if multiAD {
		suffix = " HA"
	}
	label = fmt.Sprintf("OCI PostgreSQL %dOCPU/%dGB%s + %d GB", ocpu, ramGB, suffix, dbGB)

	ocpuHourly, ramHourly, storageMonthly, fetchErr := fetchOCIPostgresRates()
	if fetchErr != nil || ocpuHourly <= 0 || storageMonthly <= 0 {
		// The catalog lookup failed; fall back to public list prices.
		return label, ociPostgresFallbackUSDPerMonth(ocpu, ramGB, dbGB, multiAD), nil
	}

	nodes := 1
	if multiAD {
		nodes = 3
	}
	// When the catalog lacks a separate RAM SKU (current state), the
	// OCPU rate is already the bundled flavor and ramHourly is 0; we
	// then ignore RAM in the math. If Oracle later splits the SKUs,
	// ramHourly > 0 will start contributing and ociPostgresRAMPerOCPU
	// keeps the math truthful per the catalog convention.
	computeHourly := float64(ocpu) * ocpuHourly
	if ramHourly > 0 {
		computeHourly += float64(ramGB) * ramHourly
	}
	compute := computeHourly * MonthlyHours
	storage := float64(dbGB) * storageMonthly
	monthly = float64(nodes) * (compute + storage)
	return label, monthly, nil
}

// fetchOCIPostgresRates pulls the per-OCPU-hour, per-GB-RAM-hour
// (when published as a separate SKU), and per-GB-month storage
// rates from the cetools catalog. Uses ociHTTPClient (nil Transport →
// inherits http.DefaultTransport) for airgap-compatibility.
// Returns positive values for the SKUs found and 0 for any not
// present so the caller can decide whether to trip the fallback.
func fetchOCIPostgresRates() (ocpuHourly, ramHourly, storageMonthly float64, err error) {
	req, reqErr := http.NewRequest("GET", ociPriceBaseURL+"?currencyCode=USD", nil)
	if reqErr != nil {
		return 0, 0, 0, fmt.Errorf("oci postgres: %w", reqErr)
	}
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := ociHTTPClient.Do(req)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("oci postgres: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, 0, 0, fmt.Errorf("oci postgres: HTTP %d", resp.StatusCode)
	}
	var or ociResp
	if err := json.NewDecoder(resp.Body).Decode(&or); err != nil {
		return 0, 0, 0, fmt.Errorf("oci postgres decode: %w", err)
	}
	for _, p := range or.Items {
		cat := strings.ToLower(p.ServiceCategory)
		dn := strings.ToLower(p.DisplayName)
		mn := strings.ToLower(p.MetricName)
		// Match either by serviceCategory or by displayName containing
		// PostgreSQL — Oracle has shifted both fields over time.
		if !strings.Contains(cat, "postgres") && !strings.Contains(dn, "postgres") {
			continue
		}
		v := ociPayAsYouGoIn(p, "USD")
		if v <= 0 {
			continue
		}
		switch {
		case strings.Contains(mn, "ocpu") && strings.Contains(mn, "hour"):
			if ocpuHourly == 0 || v < ocpuHourly {
				ocpuHourly = v
			}
		case (strings.Contains(mn, "memory") || strings.Contains(mn, "gigabyte memory")) && strings.Contains(mn, "hour"):
			if ramHourly == 0 || v < ramHourly {
				ramHourly = v
			}
		case strings.Contains(mn, "storage") || strings.Contains(mn, "gigabyte storage"):
			if storageMonthly == 0 || v < storageMonthly {
				storageMonthly = v
			}
		}
	}
	return ocpuHourly, ramHourly, storageMonthly, nil
}
