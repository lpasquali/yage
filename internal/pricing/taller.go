// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// "Taller" is yage's internal placeholder currency.
// It IS whatever currency makes sense for the human running the
// program — by default the local currency where the host runs
// (geo-detected via IP), overridable via env to anything they
// prefer. Every monetary number yage displays is
// converted from each vendor's *native* currency (AWS/Azure/GCP
// price in USD, Hetzner in EUR) into the active taller via a
// LIVE FX rate fetched from a free, auth-less endpoint.
//
// Resolution order for the active taller currency code:
//   1. env YAGE_TALLER_CURRENCY  (explicit override)
//   2. env YAGE_CURRENCY         (legacy alias)
//   3. IP geolocation → ISO country → currency  (default)
//   4. "USD" fallback if both geolocation and the FX API fail
//
// FX endpoint: https://open.er-api.com/v6/latest/<base>
//   - auth-free, free tier
//   - returns rates from <base> to every ISO 4217 currency
//   - cached on disk in cacheDir() for 24h to keep repeated
//     dry-run plans consistent
//
// Geo endpoint: http://ip-api.com/json/?fields=countryCode
//   - auth-free, free tier (45 req/min — way more than we need)
//   - cached in-process for the run duration

const (
	tallerFXEndpoint  = "https://open.er-api.com/v6/latest/USD"
	tallerGeoEndpoint = "http://ip-api.com/json/?fields=status,countryCode"
	tallerFXCacheTTL  = 24 * time.Hour
)

// countryToCurrency maps the ISO-3166 alpha-2 country code returned
// by the geo-IP API to the ISO-4217 currency code. Only the codes
// most likely to host a yage user are listed; anything
// missing falls back to USD with a notice in the dry-run header.
var countryToCurrency = map[string]string{
	"US": "USD", "CA": "CAD", "MX": "MXN", "BR": "BRL", "AR": "ARS",
	"GB": "GBP", "IE": "EUR", "FR": "EUR", "DE": "EUR", "IT": "EUR",
	"ES": "EUR", "PT": "EUR", "NL": "EUR", "BE": "EUR", "AT": "EUR",
	"FI": "EUR", "LU": "EUR", "GR": "EUR", "MT": "EUR", "CY": "EUR",
	"EE": "EUR", "LV": "EUR", "LT": "EUR", "SI": "EUR", "SK": "EUR",
	"HR": "EUR",
	"CH": "CHF", "SE": "SEK", "NO": "NOK", "DK": "DKK", "IS": "ISK",
	"PL": "PLN", "CZ": "CZK", "HU": "HUF", "RO": "RON", "BG": "BGN",
	"RU": "RUB", "TR": "TRY", "UA": "UAH",
	"JP": "JPY", "CN": "CNY", "HK": "HKD", "TW": "TWD", "KR": "KRW",
	"IN": "INR", "PK": "PKR", "BD": "BDT", "LK": "LKR", "NP": "NPR",
	"SG": "SGD", "MY": "MYR", "TH": "THB", "VN": "VND", "ID": "IDR",
	"PH": "PHP",
	"AU": "AUD", "NZ": "NZD",
	"AE": "AED", "SA": "SAR", "IL": "ILS", "EG": "EGP", "QA": "QAR",
	"KW": "KWD", "OM": "OMR", "BH": "BHD", "JO": "JOD",
	"ZA": "ZAR", "NG": "NGN", "KE": "KES", "GH": "GHS", "MA": "MAD",
	"TN": "TND", "DZ": "DZD",
	"CL": "CLP", "CO": "COP", "PE": "PEN", "VE": "VES", "UY": "UYU",
}

// currencySymbol maps an ISO-4217 currency code to a display
// symbol. Falls back to the code itself for currencies not in the
// table — better to show "ZAR 42.13" than nothing.
var currencySymbol = map[string]string{
	"USD": "$", "CAD": "CA$", "AUD": "A$", "NZD": "NZ$", "MXN": "MX$",
	"HKD": "HK$", "SGD": "S$", "BRL": "R$", "ARS": "AR$", "CLP": "CLP$",
	"COP": "COP$", "VES": "Bs.",
	"EUR": "€", "GBP": "£", "JPY": "¥", "CNY": "¥", "TWD": "NT$",
	"KRW": "₩", "INR": "₹", "RUB": "₽", "TRY": "₺", "UAH": "₴",
	"PLN": "zł", "CZK": "Kč", "HUF": "Ft", "RON": "lei", "BGN": "лв",
	"CHF": "CHF", "SEK": "kr", "NOK": "kr", "DKK": "kr", "ISK": "kr",
	"AED": "د.إ", "SAR": "﷼", "ILS": "₪", "EGP": "E£",
	"ZAR": "R", "NGN": "₦", "KES": "KSh", "GHS": "₵",
	"PHP": "₱", "THB": "฿", "VND": "₫", "IDR": "Rp", "MYR": "RM",
}

var (
	tallerOnce       sync.Once
	tallerCurrency   string
	tallerNote       string // human-readable notice for the dry-run header
)

// resolveTallerCurrency runs once per process; returns the active
// taller code and a note string explaining how it was picked.
func resolveTallerCurrency() (string, string) {
	tallerOnce.Do(func() {
		// Step 1: explicit override.
		// Order: cfg.Cost.Currency.DisplayCurrency (set by main from
		// config.Load via pricing.SetCurrency) → env-var fallback
		// for cases where SetCurrency hasn't run yet. See §16.
		if v := strings.ToUpper(strings.TrimSpace(prefs.DisplayCurrency)); v != "" {
			tallerCurrency = v
			tallerNote = "taller currency: " + v + " (cfg override)"
			return
		}
		if v := strings.ToUpper(strings.TrimSpace(os.Getenv("YAGE_TALLER_CURRENCY"))); v != "" {
			tallerCurrency = v
			tallerNote = "taller currency: " + v + " (env override)"
			return
		}
		if v := strings.ToUpper(strings.TrimSpace(os.Getenv("YAGE_CURRENCY"))); v != "" {
			tallerCurrency = v
			tallerNote = "taller currency: " + v + " (legacy env override)"
			return
		}
		// Step 2: geolocation.
		cc, err := detectCountryCode()
		if err == nil {
			if cur, ok := countryToCurrency[cc]; ok {
				tallerCurrency = cur
				tallerNote = "taller currency: " + cur + " (geo: " + cc + ")"
				return
			}
			tallerCurrency = "USD"
			tallerNote = "taller currency: USD (geo: " + cc + " not in currency map)"
			return
		}
		// Step 3: fallback.
		tallerCurrency = "USD"
		tallerNote = "taller currency: USD (geo lookup failed: " + err.Error() + ")"
	})
	return tallerCurrency, tallerNote
}

// TallerCurrency returns the active ISO-4217 code for the taller.
// Cheap — resolved once and cached.
func TallerCurrency() string {
	c, _ := resolveTallerCurrency()
	return c
}

// TallerNote returns a one-line notice describing how the taller
// was selected. Intended for the dry-run plan header so the user
// sees which currency we converted into and why.
func TallerNote() string {
	_, n := resolveTallerCurrency()
	return n
}

// TallerSymbol returns the display symbol for the active taller.
// Falls back to the ISO code itself when there's no specific symbol.
func TallerSymbol() string {
	c := TallerCurrency()
	if s, ok := currencySymbol[c]; ok {
		return s
	}
	return c + " "
}

func detectCountryCode() (string, error) {
	c := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", tallerGeoEndpoint, nil)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("geo: HTTP %d", resp.StatusCode)
	}
	var p struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return "", err
	}
	if p.Status != "success" || p.CountryCode == "" {
		return "", fmt.Errorf("geo: unsuccessful response")
	}
	return strings.ToUpper(p.CountryCode), nil
}

// fxRates caches the live FX response so all callers in one run
// see consistent numbers. Cached on disk too (tallerFXCacheTTL)
// so back-to-back dry-run plans don't re-fetch.

type fxResponse struct {
	Result string             `json:"result"`
	Rates  map[string]float64 `json:"rates"`
}

var (
	fxOnce     sync.Once
	fxRatesUSD map[string]float64
	fxErr      error
)

func loadFXFromDiskCache() (map[string]float64, error) {
	path := fmt.Sprintf("%s/fx-USD.json", cacheDir())
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if time.Since(st.ModTime()) > tallerFXCacheTTL {
		return nil, fmt.Errorf("stale")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p fxResponse
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return p.Rates, nil
}

func saveFXToDiskCache(rates map[string]float64) {
	_ = os.MkdirAll(cacheDir(), 0o755)
	path := fmt.Sprintf("%s/fx-USD.json", cacheDir())
	raw, err := json.Marshal(fxResponse{Result: "success", Rates: rates})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o644)
}

func fetchFXLive() (map[string]float64, error) {
	c := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", tallerFXEndpoint, nil)
	req.Header.Set("User-Agent", "yage/pricing")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("fx: HTTP %d", resp.StatusCode)
	}
	var p fxResponse
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	if p.Result != "success" || len(p.Rates) == 0 {
		return nil, fmt.Errorf("fx: unsuccessful response")
	}
	return p.Rates, nil
}

func ensureFXLoaded() error {
	fxOnce.Do(func() {
		// Skip live fetch when pricing is globally disabled (CI / tests).
		if disabled {
			fxRatesUSD = map[string]float64{"USD": 1.0}
			return
		}
		if r, err := loadFXFromDiskCache(); err == nil {
			fxRatesUSD = r
			return
		}
		r, err := fetchFXLive()
		if err != nil {
			fxErr = err
			return
		}
		fxRatesUSD = r
		saveFXToDiskCache(r)
	})
	return fxErr
}

// ToTaller converts amount (in nativeCurrency) to the active taller.
// nativeCurrency is the ISO-4217 code returned by the vendor API
// ("USD" for AWS/Azure/GCP, "EUR" for Hetzner, etc.). Returns the
// converted amount + the active taller's ISO code.
//
// FX flow:
//   nativeCurrency -> USD via 1/rates[nativeCurrency]
//   USD -> taller   via rates[taller]
//
// (We anchor on USD because open.er-api.com bases on it; one
// round-trip through USD is fine for planning estimates.)
func ToTaller(amount float64, nativeCurrency string) (float64, string, error) {
	taller := TallerCurrency()
	nc := strings.ToUpper(nativeCurrency)
	if nc == "" {
		nc = "USD"
	}
	if taller == nc {
		return amount, taller, nil
	}
	if err := ensureFXLoaded(); err != nil {
		return 0, taller, fmt.Errorf("fx unavailable: %w", err)
	}
	if fxRatesUSD == nil {
		return 0, taller, fmt.Errorf("fx unavailable: no rates loaded")
	}
	// Convert source -> USD.
	usd := amount
	if nc != "USD" {
		rate, ok := fxRatesUSD[nc]
		if !ok || rate <= 0 {
			return 0, taller, fmt.Errorf("fx: unknown source currency %q", nc)
		}
		usd = amount / rate
	}
	// Convert USD -> taller.
	if taller == "USD" {
		return usd, taller, nil
	}
	rate, ok := fxRatesUSD[taller]
	if !ok || rate <= 0 {
		return 0, taller, fmt.Errorf("fx: unknown taller currency %q", taller)
	}
	return usd * rate, taller, nil
}

// FormatTaller returns "<symbol><value>" using the active taller.
// On FX failure, falls back to "<native amount> <native code>" so
// the dry-run still surfaces a number rather than blank — the
// vendor's native price is the source of truth.
func FormatTaller(amount float64, nativeCurrency string) string {
	v, _, err := ToTaller(amount, nativeCurrency)
	if err != nil {
		// Fallback to native currency, with a tag so the user
		// notices the FX path failed.
		return fmt.Sprintf("%s%.2f (FX unavailable, %s)",
			nativeSymbol(nativeCurrency), amount, strings.ToUpper(nativeCurrency))
	}
	return fmt.Sprintf("%s%.2f", TallerSymbol(), v)
}

// nativeSymbol returns the symbol for a vendor's native currency.
func nativeSymbol(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "" {
		c = "USD"
	}
	if s, ok := currencySymbol[c]; ok {
		return s
	}
	return c + " "
}