// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// IBM Cloud Databases for PostgreSQL — billed as RAM-hours +
// disk-hours per cluster member, with vCPU implied by the RAM
// pick. The Global Catalog parent entry has name
// "databases-for-postgresql"; per-region pricing lives either on
// the parent's deployments[] or on the data-center child
// entries (same shape as VPC instance profiles).
//
// Metric IDs vary slightly across catalog snapshots — IBM uses
// "MEMORY_HOURS" / "memory-hours" for RAM-hours and
// "DISK_HOURS" / "disk-hours" for storage. We match either form
// case-insensitively after replacing underscores with hyphens.
//
// HTTP requests use ibmHTTPClient (defined in ibmcloud.go): an
// &http.Client{} with nil Transport that inherits
// http.DefaultTransport, keeping the airgap shim effective.
// IAM token exchange uses IamAuthenticator (also defined in
// ibmcloud.go) with the same nil-Transport HTTP client.

// ibmCloudPostgresService is the Global Catalog entry name for the
// IBM Cloud Databases for PostgreSQL parent.
const ibmCloudPostgresService = "databases-for-postgresql"

// ibmCloudPostgresFallbackRAMUSDPerHour is the public list price
// per GB of RAM per hour for IBM Cloud Databases for PostgreSQL,
// used when the Global Catalog returns empty pricing arrays.
// Source: https://cloud.ibm.com/databases/databases-for-postgresql/create
const ibmCloudPostgresFallbackRAMUSDPerHour = 0.0148

// ibmCloudPostgresFallbackDiskUSDPerHour is the public list price
// per GB of disk per hour. Same source as the RAM rate.
const ibmCloudPostgresFallbackDiskUSDPerHour = 0.000625

// ibmCloudPostgresPickSKU translates a requested DB size into the
// (RAM GB, disk GB) tuple the catalog and the fallback both bill
// against. RAM is the primary scaling axis; vCPU follows
// automatically from the IBM-side template selection.
func ibmCloudPostgresPickSKU(dbGB int) (ramGB, diskGB int) {
	switch {
	case dbGB <= 25:
		// Smallest cluster shape — IBM enforces a 5 GB per-member
		// disk floor, so a "0 GB" request still pays for 5.
		return 4, 5
	case dbGB <= 100:
		return 8, dbGB
	case dbGB <= 500:
		return 16, dbGB
	default:
		return 32, dbGB
	}
}

// IBMCloudPostgresUSDPerMonth returns the monthly USD cost of an
// IBM Cloud Databases for PostgreSQL cluster sized for dbGB. When
// multiMember is true a 3-member HA topology is priced (RAM and
// disk both multiplied — each member runs on its own node).
//
// Walks the Global Catalog parent entry first, then the per-DC
// child entries, looking for MEMORY_HOURS and DISK_HOURS metrics
// in the requested region. Falls back to the published list
// prices when the catalog response is gated to empty pricing.
func IBMCloudPostgresUSDPerMonth(region string, dbGB int, multiMember bool) (string, float64, error) {
	ramGB, diskGB := ibmCloudPostgresPickSKU(dbGB)
	members := 1
	if multiMember {
		members = 3
	}
	label := fmt.Sprintf("IBM Cloud Databases for PostgreSQL %dGB RAM × %d + %d GB disk",
		ramGB, members, diskGB)

	ramHourly, diskHourly, err := ibmCloudPostgresLiveRates(region)
	if err != nil || ramHourly <= 0 || diskHourly <= 0 {
		ramHourly, diskHourly = ibmCloudPostgresFallbackUSDPerMonth()
	}

	// Cluster cost is per-member RAM + per-member disk, scaled by
	// the cluster size (each HA member is fully provisioned).
	monthly := (float64(ramGB)*ramHourly + float64(diskGB)*diskHourly) *
		float64(members) * MonthlyHours
	return label, monthly, nil
}

// ibmCloudPostgresFallbackUSDPerMonth returns the per-GB-hour list
// prices used when the Global Catalog can't be reached or returns
// gated empty pricing arrays. Returned in (RAM $/GB-hr, disk
// $/GB-hr) order so the caller can apply MonthlyHours itself.
func ibmCloudPostgresFallbackUSDPerMonth() (float64, float64) {
	return ibmCloudPostgresFallbackRAMUSDPerHour, ibmCloudPostgresFallbackDiskUSDPerHour
}

// ibmCloudPostgresLiveRates queries the Global Catalog for the
// per-GB-hour memory and disk rates in `region`, returning
// (ramUSDPerHour, diskUSDPerHour). Either value can be 0 if the
// catalog response is gated; callers should treat that as a
// fallback signal.
//
// Uses ibmHTTPClient (nil Transport) and IamAuthenticator for
// airgap-compatible authentication.
func ibmCloudPostgresLiveRates(region string) (float64, float64, error) {
	apiKey := ibmAPIKey()
	if apiKey == "" {
		return 0, 0, fmt.Errorf("ibmcloud: IBMCLOUD_API_KEY not set")
	}
	auth := newIBMAuthenticator(apiKey)
	token, err := ibmAuthorize(auth)
	if err != nil {
		return 0, 0, fmt.Errorf("ibmcloud iam: %w", err)
	}

	q := url.Values{}
	q.Set("q", ibmCloudPostgresService)
	q.Set("include", "pricing")
	endpoint := ibmCatalogBaseURL + "?" + q.Encode()
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := ibmHTTPClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("ibmcloud catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("ibmcloud catalog: HTTP %d", resp.StatusCode)
	}
	var cr ibmCatalogResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return 0, 0, fmt.Errorf("ibmcloud decode: %w", err)
	}

	for _, entry := range cr.Resources {
		if !strings.EqualFold(entry.Name, ibmCloudPostgresService) {
			continue
		}
		ram, disk := ibmCloudPostgresMetricsFromEntry(entry, region)
		if ram > 0 && disk > 0 {
			return ram, disk, nil
		}
		// Drill into child entries for per-DC pricing when the
		// parent's deployments[] is empty (same gating behaviour
		// as VPC instance profiles).
		children, cerr := ibmFetchChildrenWithPricing(token, entry.ID)
		if cerr != nil {
			continue
		}
		dcSuffix := ibmRegionToDCSuffix[strings.ToLower(strings.TrimSpace(region))]
		for _, child := range children {
			if dcSuffix != "" && !strings.HasSuffix(strings.ToLower(child.Name), "-"+dcSuffix) {
				continue
			}
			r, d := ibmCloudPostgresMetricsFromEntry(child, region)
			if r > 0 {
				ram = r
			}
			if d > 0 {
				disk = d
			}
			if ram > 0 && disk > 0 {
				return ram, disk, nil
			}
		}
		if ram > 0 || disk > 0 {
			return ram, disk, nil
		}
	}
	return 0, 0, fmt.Errorf("ibmcloud postgres: no live pricing for %q", region)
}

// ibmCloudPostgresMetricsFromEntry pulls the RAM and disk hourly
// rates out of a catalog entry, preferring deployment metrics
// scoped to `region` and falling back to the entry-wide metrics.
// Returns (0, 0) when nothing matched — caller decides whether to
// drill into child entries or fall back to the list price.
func ibmCloudPostgresMetricsFromEntry(entry ibmEntry, region string) (float64, float64) {
	var metrics []ibmMetric
	for _, dep := range entry.Pricing.Deployments {
		if strings.EqualFold(dep.Location, region) {
			metrics = dep.Metrics
			break
		}
	}
	if len(metrics) == 0 {
		metrics = entry.Pricing.Metrics
	}
	var ram, disk float64
	for _, m := range metrics {
		id := strings.ToLower(m.MetricID + " " + m.PartRef)
		id = strings.ReplaceAll(id, "_", "-")
		isMemory := strings.Contains(id, "memory-hour")
		isDisk := strings.Contains(id, "disk-hour")
		if !isMemory && !isDisk {
			continue
		}
		price := ibmCloudPostgresPriceFromAmounts(m.Amounts)
		if price <= 0 {
			continue
		}
		if isMemory && ram == 0 {
			ram = price
		}
		if isDisk && disk == 0 {
			disk = price
		}
	}
	return ram, disk
}

// ibmCloudPostgresPriceFromAmounts returns the first non-zero
// price across the amounts list, preferring the active taller
// currency and falling back to USD. IBM's amounts[] is a flat
// per-country list, so we just scan it.
func ibmCloudPostgresPriceFromAmounts(amounts []ibmAmount) float64 {
	taller := strings.ToUpper(strings.TrimSpace(TallerCurrency()))
	preferred := taller
	if preferred == "" {
		preferred = "USD"
	}
	for _, want := range []string{preferred, "USD"} {
		for _, a := range amounts {
			if !strings.EqualFold(a.Currency, want) {
				continue
			}
			for _, p := range a.Prices {
				if p.Price > 0 {
					return p.Price
				}
			}
		}
		if want == "USD" {
			break
		}
	}
	return 0
}

// ensure time import is used (via MonthlyHours constant context)
var _ = time.Now
