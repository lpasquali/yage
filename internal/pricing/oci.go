// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Oracle Cloud price list — auth-free.
//   GET https://apexapps.oracle.com/pls/apex/cetools/api/v1/products/?currencyCode=<X>
// Returns every SKU with currencyCodeLocalizations[].prices[].value
// in the requested currency. Compute SKUs key by partNumber and have
// a displayName like "Compute - VM Standard - E4 - OCPU Per Hour" —
// we match by substring of the shape name (E4, A1, etc.) instead
// of exact equality.
//
// We call with the active taller currency when OCI supports it
// (ociSupportedCurrencies) so EU/UK/etc. operators see Oracle's
// published list price directly. Otherwise we fall back to USD and
// FX-convert at display.
//
// For Flex shapes we get a per-OCPU price + a per-GB price and need
// to multiply by the user's OCPU count + GB count. To keep this in
// the same Item shape as other vendors we model at "1 OCPU, 16 GB"
// (the CAPOCI default) — operators with bigger flex shapes scale
// the line item externally. Future: parameterize via cfg.
const ociPriceBaseURL = "https://apexapps.oracle.com/pls/apex/cetools/api/v1/products/"

// ociSupportedCurrencies are the ISO codes the cetools API answers
// to. The published list rotates occasionally; this is the
// well-supported subset (verified empirically). Anything outside
// this set falls back to USD.
var ociSupportedCurrencies = map[string]bool{
	"USD": true, "EUR": true, "GBP": true, "JPY": true, "AUD": true,
	"CAD": true, "BRL": true, "INR": true, "MXN": true, "CHF": true,
	"SEK": true, "NOK": true, "DKK": true, "ZAR": true, "SGD": true,
}

type ociFetcher struct{ httpClient *http.Client }

func init() {
	Register("oci", &ociFetcher{httpClient: &http.Client{Timeout: 30 * time.Second}})
}

type ociPriceEntry struct {
	Model string  `json:"model"` // "PAY_AS_YOU_GO", "MONTHLY_FLEX", ...
	Value float64 `json:"value"`
}

type ociCurrencyLocal struct {
	CurrencyCode string          `json:"currencyCode"`
	Prices       []ociPriceEntry `json:"prices"`
}

type ociProduct struct {
	PartNumber                  string             `json:"partNumber"`
	DisplayName                 string             `json:"displayName"`
	MetricName                  string             `json:"metricName"`
	ServiceCategory             string             `json:"serviceCategory"`
	CurrencyCodeLocalizations   []ociCurrencyLocal `json:"currencyCodeLocalizations"`
}

type ociResp struct {
	Items []ociProduct `json:"items"`
}

// ociPayAsYouGoIn returns the OCI "PAY_AS_YOU_GO" rate in the named
// currency (uppercase ISO-4217). Returns 0 when not present so the
// caller can fall back to USD.
func ociPayAsYouGoIn(p ociProduct, currency string) float64 {
	for _, c := range p.CurrencyCodeLocalizations {
		if !strings.EqualFold(c.CurrencyCode, currency) {
			continue
		}
		for _, pe := range c.Prices {
			if strings.EqualFold(pe.Model, "PAY_AS_YOU_GO") && pe.Value > 0 {
				return pe.Value
			}
		}
	}
	return 0
}

func (o *ociFetcher) Fetch(shape, region string) (Item, error) {
	currency := preferredVendorCurrency(ociSupportedCurrencies)
	url := ociPriceBaseURL + "?currencyCode=" + currency
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("oci: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Item{}, fmt.Errorf("oci: HTTP %d", resp.StatusCode)
	}
	var or ociResp
	if err := json.NewDecoder(resp.Body).Decode(&or); err != nil {
		return Item{}, fmt.Errorf("oci decode: %w", err)
	}

	// Shape lookup heuristic: split shape "VM.Standard.E4.Flex" into
	// tokens, look for a Compute SKU whose displayName contains the
	// distinctive family token (E4 / A1 / E5 etc.) AND the chassis
	// hint (Standard vs Dense I/O). The OCI catalog uses
	// serviceCategory like "Compute - Virtual Machine" / "Compute - GPU"
	// / "Compute - VMware" — we accept anything starting with
	// "compute" but exclude GPU and VMware lines for ordinary VMs.
	upper := strings.ToUpper(shape)
	isFlex := strings.HasSuffix(upper, ".FLEX")
	chassisHint := "STANDARD"
	if strings.Contains(upper, ".DENSEIO") || strings.Contains(upper, ".DENSE IO") {
		chassisHint = "DENSE I/O"
	}
	tokens := strings.Split(upper, ".")
	var family string
	for _, t := range tokens {
		if t == "VM" || t == "BM" || t == "STANDARD" || t == "DENSEIO" || t == "FLEX" {
			continue
		}
		family = t
		break
	}
	if family == "" {
		return Item{}, fmt.Errorf("oci: unable to extract family from %q", shape)
	}

	var ocpuPrice, memPrice, fixedPrice float64
	for _, p := range or.Items {
		cat := strings.ToLower(p.ServiceCategory)
		if !strings.HasPrefix(cat, "compute") {
			continue
		}
		// Exclude unrelated compute lines (GPU, VMware, bare-metal
		// commitments) when looking for ordinary VM pricing.
		if strings.Contains(cat, "gpu") || strings.Contains(cat, "vmware") {
			continue
		}
		dn := strings.ToUpper(p.DisplayName)
		if !strings.Contains(dn, family) {
			continue
		}
		// Restrict to the chassis (Standard vs Dense I/O) so we don't
		// mix prices across families that share the same generation.
		if !strings.Contains(dn, chassisHint) {
			continue
		}
		mn := strings.ToUpper(p.MetricName)
		// Try the requested currency first; fall back to USD per row
		// when the vendor only published USD for this SKU.
		fetch := func() float64 {
			if v := ociPayAsYouGoIn(p, currency); v > 0 {
				return v
			}
			return ociPayAsYouGoIn(p, "USD")
		}
		switch {
		case strings.Contains(dn, "OCPU") || strings.Contains(mn, "OCPU"):
			if v := fetch(); v > 0 && (ocpuPrice == 0 || v < ocpuPrice) {
				ocpuPrice = v
			}
		case strings.Contains(dn, "MEMORY") || strings.Contains(mn, "GIGABYTE") || strings.Contains(mn, "GB"):
			if v := fetch(); v > 0 && (memPrice == 0 || v < memPrice) {
				memPrice = v
			}
		case strings.Contains(mn, "PER HOUR"):
			if v := fetch(); v > 0 && (fixedPrice == 0 || v < fixedPrice) {
				fixedPrice = v
			}
		}
	}

	// Default flex sizing: 1 OCPU, 16 GB (CAPOCI quickstart). Future:
	// parameterize via cfg.OCIControlPlaneOCPUs / Memory.
	hourly := 0.0
	switch {
	case isFlex && ocpuPrice > 0:
		hourly = ocpuPrice*1 + memPrice*16
	case fixedPrice > 0:
		hourly = fixedPrice
	}
	if hourly <= 0 {
		return Item{}, fmt.Errorf("oci: no price for shape %q (family %s)", shape, family)
	}
	return buildVendorItem(hourly, currency)
}