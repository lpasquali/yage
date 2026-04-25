package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Oracle Cloud price list — auth-free.
//   GET https://apexapps.oracle.com/pls/apex/cetools/api/v1/products/?currencyCode=USD
// Returns every SKU with currencyCodeLocalizations[].prices[].value
// in USD/hour. Compute SKUs key by partNumber and have a displayName
// like "Compute - VM Standard - E4 - OCPU Per Hour" — we match by
// substring of the shape name (E4, A1, etc.) instead of exact equality.
//
// For Flex shapes we get a per-OCPU price + a per-GB price and need
// to multiply by the user's OCPU count + GB count. To keep this in
// the same Item shape as other vendors we model at "1 OCPU, 16 GB"
// (the CAPOCI default) — operators with bigger flex shapes scale
// the line item externally. Future: parameterize via cfg.
const ociPriceURL = "https://apexapps.oracle.com/pls/apex/cetools/api/v1/products/?currencyCode=USD"

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

func ociPayAsYouGo(p ociProduct) float64 {
	for _, c := range p.CurrencyCodeLocalizations {
		if c.CurrencyCode != "USD" {
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
	req, _ := http.NewRequest("GET", ociPriceURL, nil)
	req.Header.Set("User-Agent", "bootstrap-capi/pricing")
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
	// distinctive token (E4 / A1 / E5 etc.) AND a relevant metric
	// (OCPU Per Hour for flex, Per Hour for fixed).
	upper := strings.ToUpper(shape)
	isFlex := strings.HasSuffix(upper, ".FLEX")
	tokens := strings.Split(upper, ".")
	var family string
	for _, t := range tokens {
		if t == "VM" || t == "STANDARD" || t == "DENSEIO" || t == "FLEX" {
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
		if !strings.EqualFold(p.ServiceCategory, "Compute") {
			continue
		}
		dn := strings.ToUpper(p.DisplayName)
		if !strings.Contains(dn, family) {
			continue
		}
		mn := strings.ToUpper(p.MetricName)
		switch {
		case strings.Contains(dn, "OCPU") || strings.Contains(mn, "OCPU"):
			if v := ociPayAsYouGo(p); v > 0 && (ocpuPrice == 0 || v < ocpuPrice) {
				ocpuPrice = v
			}
		case strings.Contains(dn, "MEMORY") || strings.Contains(mn, "GB"):
			if v := ociPayAsYouGo(p); v > 0 && (memPrice == 0 || v < memPrice) {
				memPrice = v
			}
		case strings.Contains(mn, "PER HOUR"):
			if v := ociPayAsYouGo(p); v > 0 && (fixedPrice == 0 || v < fixedPrice) {
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
	return Item{
		USDPerHour:  hourly,
		USDPerMonth: hourly * MonthlyHours,
		FetchedAt:   time.Now(),
	}, nil
}
