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
	RetailPrice    float64 `json:"retailPrice"`
	UnitOfMeasure  string  `json:"unitOfMeasure"`
	ProductName    string  `json:"productName"`
	SkuName        string  `json:"skuName"`
	ArmSkuName     string  `json:"armSkuName"`
	ArmRegionName  string  `json:"armRegionName"`
	MeterName      string  `json:"meterName"`
	Type           string  `json:"type"`
}

type azureResp struct {
	Items        []azureItem `json:"Items"`
	NextPageLink string      `json:"NextPageLink"`
}

func (a *azureFetcher) Fetch(sku, region string) (Item, error) {
	filter := fmt.Sprintf(
		"serviceName eq 'Virtual Machines' and priceType eq 'Consumption' and armRegionName eq '%s' and armSkuName eq '%s'",
		region, sku,
	)
	q := url.Values{}
	q.Set("$filter", filter)
	endpoint := azureRetailURL + "?" + q.Encode()

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return Item{}, err
	}
	req.Header.Set("User-Agent", "bootstrap-capi/pricing")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("azure: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Item{}, fmt.Errorf("azure: HTTP %d", resp.StatusCode)
	}
	var ar azureResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return Item{}, fmt.Errorf("azure decode: %w", err)
	}

	// Filter: skip Spot, Low Priority, and Windows; we want
	// Linux on-demand consumption price (the cheapest, most
	// representative number).
	var best float64
	found := false
	for _, it := range ar.Items {
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
	return Item{
		USDPerHour:  best,
		USDPerMonth: best * MonthlyHours,
		FetchedAt:   time.Now(),
	}, nil
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
	req.Header.Set("User-Agent", "bootstrap-capi/pricing")
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
		return 0, 0, fmt.Errorf("azure nat gw: no full price in %s (hourly=%v gb=%v)", region, hourly, gb)
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
		return 0, 0, fmt.Errorf("azure lb std: incomplete price in %s", region)
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
// Azure DNS public zones in region (region is the meter's
// effectiveRegion; Azure DNS pricing is global but the catalog
// still keys by region).
func AzureDNSZoneUSDPerMonth(region string) (float64, error) {
	// Azure DNS armRegionName is "global" in the catalog; let
	// the caller override via env BOOTSTRAP_CAPI_AZURE_DNS_REGION.
	dnsRegion := region
	if v := os.Getenv("BOOTSTRAP_CAPI_AZURE_DNS_REGION"); v != "" {
		dnsRegion = v
	}
	filter := fmt.Sprintf(
		"serviceName eq 'Azure DNS' and priceType eq 'Consumption' and armRegionName eq '%s'",
		dnsRegion,
	)
	items, err := azureRetail(filter)
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		nm := strings.ToLower(it.MeterName + " " + it.ProductName)
		if !strings.Contains(nm, "zone") {
			continue
		}
		if strings.Contains(strings.ToLower(it.UnitOfMeasure), "month") {
			if it.RetailPrice > 0 {
				return it.RetailPrice, nil
			}
		}
	}
	return 0, fmt.Errorf("azure dns: no zone-month price")
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
	req.Header.Set("User-Agent", "bootstrap-capi/pricing")
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
