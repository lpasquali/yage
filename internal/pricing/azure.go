// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Azure Retail Prices API — auth-free, region-aware.
// GET https://prices.azure.com/api/retail/prices?$filter=...
// Filter dimensions used here:
//   serviceName eq 'Virtual Machines'    — VM compute SKUs
//   armRegionName eq '<region>'          — eastus, westeurope, ...
//   armSkuName eq '<sku>'                — Standard_D2s_v3, ...
//   priceType eq 'Consumption'           — exclude reservations
//
// The endpoint returns Items[] with retailPrice (USD/hour for
// VMs, USD/GB-month for managed disks). We pick the lowest
// retailPrice that's NOT a Spot/Low Priority/Windows entry —
// the catalog returns multiple rows per sku (Linux, Windows,
// Spot tiers). Linux on-demand is the conservative default
// for a Linux-based CAPI workload.
const azureRetailURL = "https://prices.azure.com/api/retail/prices"

type azureFetcher struct{ httpClient *http.Client }

func init() {
	Register("azure", &azureFetcher{httpClient: &http.Client{Timeout: 15 * time.Second}})
}

type azureItem struct {
	RetailPrice         float64 `json:"retailPrice"`
	TierMinimumUnits    float64 `json:"tierMinimumUnits"`
	UnitOfMeasure       string  `json:"unitOfMeasure"`
	ProductName         string  `json:"productName"`
	SkuName             string  `json:"skuName"`
	ArmSkuName          string  `json:"armSkuName"`
	ArmRegionName       string  `json:"armRegionName"`
	MeterName           string  `json:"meterName"`
	Type                string  `json:"type"`
	CurrencyCode        string  `json:"currencyCode"`
}

// azureSupportedCurrencies is the subset of ISO-4217 codes the
// Azure Retail Prices API responds to via ?currencyCode=. Anything
// outside this set falls back to USD; we then convert via FX.
// Source: learn.microsoft.com/en-us/rest/api/cost-management/retail-prices/azure-retail-prices.
var azureSupportedCurrencies = map[string]bool{
	"USD": true, "EUR": true, "GBP": true, "CAD": true, "AUD": true,
	"BRL": true, "CHF": true, "CNY": true, "DKK": true, "HKD": true,
	"IDR": true, "INR": true, "JPY": true, "KRW": true, "MXN": true,
	"MYR": true, "NOK": true, "NZD": true, "RUB": true, "SAR": true,
	"SEK": true, "TWD": true, "ZAR": true, "ARS": true,
}

type azureResp struct {
	Items        []azureItem `json:"Items"`
	NextPageLink string      `json:"NextPageLink"`
}

func (a *azureFetcher) Fetch(sku, region string) (Item, error) {
	items, currency, err := azureRetailWithCurrency(fmt.Sprintf(
		"serviceName eq 'Virtual Machines' and priceType eq 'Consumption' and armRegionName eq '%s' and armSkuName eq '%s'",
		region, sku,
	), preferredVendorCurrency(azureSupportedCurrencies))
	if err != nil {
		return Item{}, err
	}

	// Filter: skip Spot, Low Priority, and Windows; we want
	// Linux on-demand consumption price (the cheapest, most
	// representative number).
	var best float64
	found := false
	for _, it := range items {
		nm := strings.ToLower(it.SkuName + " " + it.ProductName + " " + it.MeterName)
		if strings.Contains(nm, "spot") || strings.Contains(nm, "low priority") {
			continue
		}
		if strings.Contains(nm, "windows") {
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
		return Item{}, fmt.Errorf("azure: no Linux on-demand price for %q in %q", sku, region)
	}
	return buildVendorItem(best, currency)
}

// preferredVendorCurrency returns the active taller currency when
// the vendor supports it, else "USD". Generic helper used by every
// fetcher that has a "currencyCode" knob (Azure, OCI, …) so the
// "ask in target currency when possible" policy lives in one place.
func preferredVendorCurrency(supported map[string]bool) string {
	taller := strings.ToUpper(strings.TrimSpace(TallerCurrency()))
	if taller == "" || taller == "USD" {
		return "USD"
	}
	if supported[taller] {
		return taller
	}
	return "USD"
}

// buildVendorItem normalizes a (perHourInVendorCurrency, currency)
// pair into a fully-populated Item. When currency != USD, the
// canonical USDPerHour/USDPerMonth fields hold the FX-converted
// USD-equivalent (best-effort; FX failure leaves them at zero — the
// display layer falls back to the native value via FormatTaller).
func buildVendorItem(perHour float64, currency string) (Item, error) {
	c := strings.ToUpper(strings.TrimSpace(currency))
	if c == "" {
		c = "USD"
	}
	monthly := perHour * MonthlyHours
	usdHour, usdMonth := perHour, monthly
	if c != "USD" {
		usdHour = 0
		usdMonth = 0
		if v, err := toUSD(perHour, c); err == nil {
			usdHour = v
			usdMonth = v * MonthlyHours
		}
	}
	return Item{
		USDPerHour:     usdHour,
		USDPerMonth:    usdMonth,
		NativeCurrency: c,
		NativeAmount:   monthly,
		FetchedAt:      time.Now(),
	}, nil
}

// azureRetailWithCurrency is azureRetail() with an explicit
// currency code passed via the API's ?currencyCode= parameter.
// Returns the items + the actual currency they're in (which falls
// back to USD when Azure ignored our preference for an unsupported
// SKU/region pair).
func azureRetailWithCurrency(filter, currency string) ([]azureItem, string, error) {
	q := url.Values{}
	q.Set("$filter", filter)
	if currency != "" && strings.ToUpper(currency) != "USD" {
		q.Set("currencyCode", strings.ToUpper(currency))
	}
	endpoint := azureRetailURL + "?" + q.Encode()
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, "USD", err
	}
	req.Header.Set("User-Agent", "yage/pricing")
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, "USD", fmt.Errorf("azure: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "USD", fmt.Errorf("azure: HTTP %d", resp.StatusCode)
	}
	var ar azureResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, "USD", fmt.Errorf("azure decode: %w", err)
	}
	// Determine the effective currency from the first row that has
	// one set. Azure echoes the per-row currency; if it matches our
	// request we got native pricing, otherwise it silently fell back.
	gotCurrency := "USD"
	for _, it := range ar.Items {
		if c := strings.ToUpper(strings.TrimSpace(it.CurrencyCode)); c != "" {
			gotCurrency = c
			break
		}
	}
	return ar.Items, gotCurrency, nil
}

// azureRetail performs a generic Retail Prices API query and
// returns the matching items. Used by the AzureXxx() helpers
// below to extract specific overhead SKUs.
func azureRetail(filter string) ([]azureItem, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	q := url.Values{}
	q.Set("$filter", filter)
	endpoint := azureRetailURL + "?" + q.Encode()
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("azure retail: HTTP %d", resp.StatusCode)
	}
	var ar azureResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, err
	}
	return ar.Items, nil
}

// AzureAKSUSDPerMonth fetches the live monthly fee for AKS Standard
// tier ("uptime SLA"). The Retail Prices catalog publishes it as
// USD/hour on the "Azure Kubernetes Service" service.
func AzureAKSUSDPerMonth(region string) (float64, error) {
	filter := fmt.Sprintf(
		"serviceName eq 'Azure Kubernetes Service' and priceType eq 'Consumption' and armRegionName eq '%s'",
		region,
	)
	items, err := azureRetail(filter)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		nm := strings.ToLower(it.MeterName + " " + it.SkuName + " " + it.ProductName)
		if !strings.Contains(nm, "uptime") && !strings.Contains(nm, "standard") {
			continue
		}
		if it.RetailPrice <= 0 {
			continue
		}
		// AKS uptime SLA is priced per-hour per-cluster.
		return it.RetailPrice * MonthlyHours, nil
	}
	return 0, fmt.Errorf("azure aks: no Standard-tier price in %s", region)
}

// azureNATGatewayRetailAPIGapFallback returns Microsoft list-price NAT
// Gateway rates when the Azure Retail Prices API returns no meters
// (a long-standing catalog gap for NAT in many regions). Values match
// the public calculator for East US tier-1 regions (~$0.045/hr gateway,
// ~$0.045/GB processed). Prefer live rows from the API when present.
func azureNATGatewayRetailAPIGapFallback() (hourly, perGB float64) {
	return 0.045, 0.045
}

// azureStandardLBRetailAPIGapFallback returns headline Microsoft list
// prices for Standard SKU load balancing when the Retail API omits
// meters (observed for eastus and other regions as of 2026).
func azureStandardLBRetailAPIGapFallback() (hourly, perGB float64) {
	return 0.025, 0.005
}

// AzureNATGatewayHourlyAndProcGB returns the live hourly fee +
// per-GB processed fee for an Azure NAT Gateway in region.
func AzureNATGatewayHourlyAndProcGB(region string) (hourly, gb float64, err error) {
	filter := fmt.Sprintf(
		"serviceName eq 'Virtual Network' and priceType eq 'Consumption' and armRegionName eq '%s'",
		region,
	)
	items, err := azureRetail(filter)
	if err != nil {
		return 0, 0, err
	}
	for _, it := range items {
		nm := strings.ToLower(it.MeterName + " " + it.ProductName)
		if !strings.Contains(nm, "nat gateway") {
			continue
		}
		uom := strings.ToLower(it.UnitOfMeasure)
		if strings.Contains(uom, "hour") && hourly == 0 {
			hourly = it.RetailPrice
		} else if strings.Contains(uom, "gb") && gb == 0 {
			gb = it.RetailPrice
		}
	}
	if hourly == 0 || gb == 0 {
		fh, fg := azureNATGatewayRetailAPIGapFallback()
		return fh, fg, nil
	}
	return hourly, gb, nil
}

// AzureStandardLBHourlyAndProcGB returns hourly + per-GB processed
// for the Standard SKU Load Balancer in region.
func AzureStandardLBHourlyAndProcGB(region string) (hourly, gb float64, err error) {
	filter := fmt.Sprintf(
		"serviceName eq 'Load Balancer' and priceType eq 'Consumption' and armRegionName eq '%s'",
		region,
	)
	items, err := azureRetail(filter)
	if err != nil {
		return 0, 0, err
	}
	// Two candidate Standard meters exist: one hourly per "rule"
	// (tiered for first/over 5 rules), one per GB processed. Pick
	// the cheapest in each unit class.
	hourlyBest := 0.0
	gbBest := 0.0
	hF, gF := false, false
	for _, it := range items {
		nm := strings.ToLower(it.MeterName + " " + it.ProductName + " " + it.SkuName)
		if !strings.Contains(nm, "standard") {
			continue
		}
		uom := strings.ToLower(it.UnitOfMeasure)
		if it.RetailPrice <= 0 {
			continue
		}
		if strings.Contains(uom, "hour") && (!hF || it.RetailPrice < hourlyBest) {
			hourlyBest = it.RetailPrice
			hF = true
		}
		if strings.Contains(uom, "gb") && (!gF || it.RetailPrice < gbBest) {
			gbBest = it.RetailPrice
			gF = true
		}
	}
	if !hF || !gF {
		fh, fg := azureStandardLBRetailAPIGapFallback()
		return fh, fg, nil
	}
	return hourlyBest, gbBest, nil
}

// AzurePublicIPHourly returns the per-hour rate for a Standard SKU
// Public IPv4 in region.
func AzurePublicIPHourly(region string) (float64, error) {
	filter := fmt.Sprintf(
		"serviceName eq 'Virtual Network' and priceType eq 'Consumption' and armRegionName eq '%s'",
		region,
	)
	items, err := azureRetail(filter)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		nm := strings.ToLower(it.MeterName + " " + it.ProductName + " " + it.SkuName)
		if !strings.Contains(nm, "ip address") {
			continue
		}
		if !strings.Contains(nm, "standard") {
			continue
		}
		if !strings.Contains(strings.ToLower(it.UnitOfMeasure), "hour") {
			continue
		}
		if it.RetailPrice > 0 {
			return it.RetailPrice, nil
		}
	}
	return 0, fmt.Errorf("azure public ip: no standard hourly in %s", region)
}

// AzureLogAnalyticsUSDPerGB returns the per-GB ingestion rate for
// Pay-As-You-Go Log Analytics in region.
func AzureLogAnalyticsUSDPerGB(region string) (float64, error) {
	filter := fmt.Sprintf(
		"serviceName eq 'Log Analytics' and priceType eq 'Consumption' and armRegionName eq '%s'",
		region,
	)
	items, err := azureRetail(filter)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		nm := strings.ToLower(it.MeterName + " " + it.ProductName + " " + it.SkuName)
		if !strings.Contains(nm, "pay-as-you-go") && !strings.Contains(nm, "ingestion") && !strings.Contains(nm, "data ingest") {
			continue
		}
		if !strings.Contains(strings.ToLower(it.UnitOfMeasure), "gb") {
			continue
		}
		if it.RetailPrice > 0 {
			return it.RetailPrice, nil
		}
	}
	return 0, fmt.Errorf("azure log analytics: no PAYG/GB in %s", region)
}

// AzureDNSZoneUSDPerMonth returns the per-zone monthly fee for
// Azure DNS public zones in region (region is the workload
// location, e.g. eastus). The Retail Prices catalog keys public
// DNS under bandwidth-style geo zones ("Zone 1", …), not RM
// region names — see resolveAzureDNSArmRegionForDNSPricing.
func AzureDNSZoneUSDPerMonth(region string) (float64, error) {
	if v := os.Getenv("YAGE_AZURE_DNS_REGION"); v != "" {
		return azureDNSZoneUSDInArmRegion(resolveAzureDNSArmRegionForDNSPricing(v))
	}
	return azureDNSZoneUSDInArmRegion(resolveAzureDNSArmRegionForDNSPricing(region))
}

// azureDNSRetailGeoArmRegions are the armRegionName values used in
// the Retail API for Azure DNS public hosted zones (not eastus/westeurope).
func azureDNSRetailGeoArmRegions() map[string]struct{} {
	return map[string]struct{}{
		"Zone 1":        {},
		"Zone 2":        {},
		"Zone 3":        {},
		"Zone 4":        {},
		"US Gov Zone 1": {},
		"DE Gov Zone 2": {},
	}
}

func resolveAzureDNSArmRegionForDNSPricing(workloadOrCatalog string) string {
	s := strings.TrimSpace(workloadOrCatalog)
	if s == "" {
		return "Zone 1"
	}
	if _, ok := azureDNSRetailGeoArmRegions()[s]; ok {
		return s
	}
	return azureDNSCatalogArmRegionFromWorkload(s)
}

// azureDNSCatalogArmRegionFromWorkload maps an Azure RM location to
// the DNS pricing geo used in the Retail catalog (Microsoft FAQ:
// https://azure.microsoft.com/en-us/pricing/details/dns/).
func azureDNSCatalogArmRegionFromWorkload(workload string) string {
	r := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(workload, " ", "")))
	switch {
	case strings.HasPrefix(r, "usgov"):
		return "US Gov Zone 1"
	case r == "germanycentral" || r == "germanynortheast":
		return "DE Gov Zone 2"
	}
	if z, ok := azureDNSWorkloadToGeoZone[r]; ok {
		return z
	}
	return "Zone 1"
}

// azureDNSWorkloadToGeoZone lists commercial regions; anything else
// defaults to Zone 1 (same headline per-zone rate as Zone 2–4 today).
var azureDNSWorkloadToGeoZone = map[string]string{
	// Zone 1 (excerpt from Microsoft DNS pricing FAQ).
	"australiacentral": "Zone 1", "australiacentral2": "Zone 1",
	"canadacentral": "Zone 1", "canadaeast": "Zone 1",
	"northeurope": "Zone 1", "westeurope": "Zone 1",
	"francecentral": "Zone 1", "francesouth": "Zone 1",
	"germanynorth": "Zone 1", "germanywestcentral": "Zone 1",
	"norwayeast": "Zone 1", "norwaywest": "Zone 1",
	"switzerlandnorth": "Zone 1", "switzerlandwest": "Zone 1",
	"uksouth": "Zone 1", "ukwest": "Zone 1",
	"centralus": "Zone 1", "eastus": "Zone 1", "eastus2": "Zone 1",
	"northcentralus": "Zone 1", "southcentralus": "Zone 1",
	"westus": "Zone 1", "westus2": "Zone 1", "westus3": "Zone 1", "westcentralus": "Zone 1",
	"italynorth": "Zone 1", "polandcentral": "Zone 1", "spaincentral": "Zone 1",
	"swedencentral": "Zone 1", "swedensouth": "Zone 1",
	"israelcentral": "Zone 1", "qatarcentral": "Zone 1",
	"mexicocentral": "Zone 1", "chilecentral": "Zone 1",
	"austriaeast": "Zone 1", "belgiumcentral": "Zone 1", "denmarkeast": "Zone 1",
	"newzealandnorth": "Zone 1", "indonesiacentral": "Zone 1", "malaysiawest": "Zone 1",
	// Zone 2.
	"eastasia": "Zone 2", "southeastasia": "Zone 2",
	"australiaeast": "Zone 2", "australiasoutheast": "Zone 2",
	"centralindia": "Zone 2", "southindia": "Zone 2", "westindia": "Zone 2",
	"japaneast": "Zone 2", "japanwest": "Zone 2",
	"koreacentral": "Zone 2", "koreasouth": "Zone 2",
	// Zone 3.
	"brazilsouth": "Zone 3", "brazilsoutheast": "Zone 3",
	"southafricanorth": "Zone 3", "southafricawest": "Zone 3",
	"uaecentral": "Zone 3", "uaenorth": "Zone 3",
}

func azureDNSZoneUSDInArmRegion(catalogArmRegion string) (float64, error) {
	filter := fmt.Sprintf(
		"serviceName eq 'Azure DNS' and priceType eq 'Consumption' and armRegionName eq '%s'",
		catalogArmRegion,
	)
	items, err := azureRetail(filter)
	if err != nil {
		return 0, err
	}
	var best float64
	bestTier := 1e9
	found := false
	for _, it := range items {
		if !strings.EqualFold(strings.TrimSpace(it.MeterName), "Public Zone") {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(it.SkuName), "Public") {
			continue
		}
		if it.RetailPrice <= 0 {
			continue
		}
		tier := it.TierMinimumUnits
		if !found || tier < bestTier {
			found = true
			bestTier = tier
			best = it.RetailPrice
			continue
		}
		if tier == bestTier && it.RetailPrice < best {
			best = it.RetailPrice
		}
	}
	if !found {
		return 0, fmt.Errorf("azure dns: no public zone price in %s", catalogArmRegion)
	}
	return best, nil
}

// AzureEgressUSDPerGB returns the per-GB egress rate for outbound
// internet traffic from region.
func AzureEgressUSDPerGB(region string) (float64, error) {
	filter := fmt.Sprintf(
		"serviceName eq 'Bandwidth' and priceType eq 'Consumption' and armRegionName eq '%s'",
		region,
	)
	items, err := azureRetail(filter)
	if err != nil {
		// Bandwidth is often priced under "Bandwidth" service with
		// armRegionName=Zone1 or zone-based. Fall back to global filter.
		fallback := "serviceName eq 'Bandwidth' and priceType eq 'Consumption'"
		items, err = azureRetail(fallback)
		if err != nil {
			return 0, err
		}
	}
	best := 0.0
	found := false
	for _, it := range items {
		nm := strings.ToLower(it.MeterName + " " + it.ProductName)
		if !strings.Contains(nm, "data transfer out") && !strings.Contains(nm, "internet egress") {
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
		return 0, fmt.Errorf("azure egress: no per-GB rate")
	}
	return best, nil
}

// AzureManagedDiskUSDPerGBMonth fetches the per-GB-month price
// of an Azure managed disk tier (e.g. "Standard SSD" / "Premium SSD")
// in a given region, by filtering the Retail Prices API.
//
// Returns the lowest matching retailPrice for the given productName
// substring — there are multiple meters (LRS / ZRS / GRS) and we
// pick the cheapest (LRS) as the conservative default.
func AzureManagedDiskUSDPerGBMonth(region, productSubstr string) (float64, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	filter := fmt.Sprintf(
		"serviceName eq 'Storage' and priceType eq 'Consumption' and armRegionName eq '%s'",
		region,
	)
	q := url.Values{}
	q.Set("$filter", filter)
	endpoint := azureRetailURL + "?" + q.Encode()
	req, _ := http.NewRequest("GET", endpoint, nil)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("azure storage: HTTP %d", resp.StatusCode)
	}
	var ar azureResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return 0, err
	}
	want := strings.ToLower(productSubstr)
	var best float64
	found := false
	for _, it := range ar.Items {
		// Only managed disks priced per GB-Month
		if !strings.Contains(strings.ToLower(it.UnitOfMeasure), "gb") {
			continue
		}
		if !strings.Contains(strings.ToLower(it.ProductName), want) {
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
		return 0, fmt.Errorf("azure storage: no %q in %q", productSubstr, region)
	}
	return best, nil
}