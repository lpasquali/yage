// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
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
// by the geo-IP API (or by --data-center-location) to the ISO-4217
// currency code. Coverage targets the global top-50 currencies plus
// every Eurozone member; anything missing falls back to USD with a
// notice in the dry-run header. Update SupportedCurrencies() when
// adding entries here.
var countryToCurrency = map[string]string{
	"US": "USD", "CA": "CAD", "MX": "MXN", "BR": "BRL", "AR": "ARS",
	"GB": "GBP", "IE": "EUR", "FR": "EUR", "DE": "EUR", "IT": "EUR",
	"ES": "EUR", "PT": "EUR", "NL": "EUR", "BE": "EUR", "AT": "EUR",
	"FI": "EUR", "LU": "EUR", "GR": "EUR", "MT": "EUR", "CY": "EUR",
	"EE": "EUR", "LV": "EUR", "LT": "EUR", "SI": "EUR", "SK": "EUR",
	"HR": "EUR",
	"CH": "CHF", "SE": "SEK", "NO": "NOK", "DK": "DKK", "IS": "ISK",
	"PL": "PLN", "CZ": "CZK", "HU": "HUF", "RO": "RON", "BG": "BGN",
	"RU": "RUB", "TR": "TRY", "UA": "UAH", "KZ": "KZT",
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
// symbol. The set of keys here IS yage's supported-currency list:
// IsSupportedCurrency() returns true iff the code is in this map.
// When adding a code, also confirm it's quoted by open.er-api.com
// (it covers all major ISO codes) and ensure countryToCurrency
// reaches it from at least one country. Currencies outside this
// set surface UnsupportedCurrencyMessage() and the cost path falls
// back to USD.
var currencySymbol = map[string]string{
	"USD": "$", "CAD": "CA$", "AUD": "A$", "NZD": "NZ$", "MXN": "MX$",
	"HKD": "HK$", "SGD": "S$", "BRL": "R$", "ARS": "AR$", "CLP": "CLP$",
	"COP": "COP$", "VES": "Bs.", "PEN": "S/", "UYU": "$U",
	"EUR": "€", "GBP": "£", "JPY": "¥", "CNY": "¥", "TWD": "NT$",
	"KRW": "₩", "INR": "₹", "RUB": "₽", "TRY": "₺", "UAH": "₴",
	"PLN": "zł", "CZK": "Kč", "HUF": "Ft", "RON": "lei", "BGN": "лв",
	"KZT": "₸",
	"CHF": "CHF", "SEK": "kr", "NOK": "kr", "DKK": "kr", "ISK": "kr",
	"AED": "د.إ", "SAR": "﷼", "ILS": "₪", "EGP": "E£",
	"QAR": "QR", "KWD": "KD", "OMR": "OR", "BHD": "BD", "JOD": "JD",
	"ZAR": "R", "NGN": "₦", "KES": "KSh", "GHS": "₵",
	"MAD": "DH", "TND": "DT", "DZD": "DA",
	"PHP": "₱", "THB": "฿", "VND": "₫", "IDR": "Rp", "MYR": "RM",
	"PKR": "Rs", "BDT": "৳", "LKR": "Rs", "NPR": "रु",
}

var (
	tallerOnce       sync.Once
	tallerCurrency   string
	tallerNote       string // human-readable notice for the dry-run header
)

// resolveTallerCurrency runs once per process; returns the active
// taller code and a note string explaining how it was picked.
//
// Resolution order:
//  1. cfg.Cost.Currency.DisplayCurrency (explicit code via flag/env)
//  2. YAGE_TALLER_CURRENCY / YAGE_CURRENCY env (when SetCurrency
//     hasn't been called yet — tests, embedded use)
//  3. cfg.Cost.Currency.DataCenterLocation (--data-center-location):
//     country code → ISO currency
//  4. Geo-IP detection
//  5. USD fallback
//
// Anywhere along the chain, if the chosen code isn't in
// currencySymbol the resolver falls back to USD and surfaces
// UnsupportedCurrencyMessage in the note so the user can file an
// issue.
func resolveTallerCurrency() (string, string) {
	tallerOnce.Do(func() {
		pickOrFallback := func(code, source string) (string, string, bool) {
			c := strings.ToUpper(strings.TrimSpace(code))
			if c == "" {
				return "", "", false
			}
			if !IsSupportedCurrency(c) {
				return "USD", "taller currency: USD (" + source + " " + c + " unsupported — " +
					UnsupportedCurrencyMessage(c) + ")", true
			}
			return c, "taller currency: " + c + " (" + source + ")", true
		}
		if v, n, ok := pickOrFallback(prefs.DisplayCurrency, "cfg override"); ok {
			tallerCurrency, tallerNote = v, n
			return
		}
		if v, n, ok := pickOrFallback(os.Getenv("YAGE_TALLER_CURRENCY"), "env override"); ok {
			tallerCurrency, tallerNote = v, n
			return
		}
		if v, n, ok := pickOrFallback(os.Getenv("YAGE_CURRENCY"), "legacy env override"); ok {
			tallerCurrency, tallerNote = v, n
			return
		}
		// Step 3: --data-center-location → country → currency.
		if dc := strings.ToUpper(strings.TrimSpace(prefs.DataCenterLocation)); dc != "" {
			cur := CountryCurrency(dc)
			if cur == "" {
				tallerCurrency = "USD"
				tallerNote = "taller currency: USD (--data-center-location " + dc +
					" is not in our country→currency map; falling back)"
				return
			}
			if !IsSupportedCurrency(cur) {
				tallerCurrency = "USD"
				tallerNote = "taller currency: USD (--data-center-location " + dc +
					" → " + cur + " unsupported — " + UnsupportedCurrencyMessage(cur) + ")"
				return
			}
			tallerCurrency = cur
			tallerNote = "taller currency: " + cur + " (--data-center-location " + dc + ")"
			return
		}
		// Step 4: geolocation.
		cc, err := detectCountryCode()
		if err == nil {
			if cur, ok := countryToCurrency[cc]; ok {
				if !IsSupportedCurrency(cur) {
					tallerCurrency = "USD"
					tallerNote = "taller currency: USD (geo: " + cc +
						" → " + cur + " unsupported — " + UnsupportedCurrencyMessage(cur) + ")"
					return
				}
				tallerCurrency = cur
				tallerNote = "taller currency: " + cur + " (geo: " + cc + ")"
				return
			}
			tallerCurrency = "USD"
			tallerNote = "taller currency: USD (geo: " + cc + " not in currency map)"
			return
		}
		// Step 5: fallback.
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
	usd, err := toUSD(amount, nc)
	if err != nil {
		return 0, taller, err
	}
	if taller == "USD" {
		return usd, taller, nil
	}
	if fxRatesUSD == nil {
		return 0, taller, fmt.Errorf("fx unavailable: no rates loaded")
	}
	rate, ok := fxRatesUSD[taller]
	if !ok || rate <= 0 {
		return 0, taller, fmt.Errorf("fx: unknown taller currency %q", taller)
	}
	return usd * rate, taller, nil
}

// FromTaller converts an amount in the active taller currency to
// USD. The inverse of ToTaller — used when the UI accepts user
// input in local currency (e.g. budget) but downstream comparisons
// happen in USD. FX-failure callers should treat the input as
// already USD (the FormatTaller USD-fallback story).
func FromTaller(amount float64) (float64, error) {
	taller := TallerCurrency()
	if taller == "USD" {
		return amount, nil
	}
	return toUSD(amount, taller)
}

// toUSD converts amount in nativeCurrency to USD using the loaded
// FX rates. Pure helper used by ToTaller and the FormatTaller USD
// fallback path. Returns the original amount when nativeCurrency is
// already USD.
func toUSD(amount float64, nativeCurrency string) (float64, error) {
	nc := strings.ToUpper(strings.TrimSpace(nativeCurrency))
	if nc == "" || nc == "USD" {
		return amount, nil
	}
	if err := ensureFXLoaded(); err != nil {
		return 0, fmt.Errorf("fx unavailable: %w", err)
	}
	if fxRatesUSD == nil {
		return 0, fmt.Errorf("fx unavailable: no rates loaded")
	}
	rate, ok := fxRatesUSD[nc]
	if !ok || rate <= 0 {
		return 0, fmt.Errorf("fx: unknown source currency %q", nc)
	}
	return amount / rate, nil
}

// FormatTaller returns "<symbol><value>" using the active taller.
// Failure modes are layered so the UI never goes blank:
//   1. FX fully working          → display in active taller
//   2. taller FX missing         → display in USD with a "(FX unavailable, USD)" tag
//   3. source FX also missing    → display in source native currency with the same tag
// Step (2) is the user's "if for some reason a currency can't be
// converted, fall back to dollars" rule.
func FormatTaller(amount float64, nativeCurrency string) string {
	if v, _, err := ToTaller(amount, nativeCurrency); err == nil {
		return fmt.Sprintf("%s%.2f", TallerSymbol(), v)
	}
	// Step 2: try USD intermediate.
	if usd, err := toUSD(amount, nativeCurrency); err == nil {
		taller := TallerCurrency()
		if taller != "USD" {
			return fmt.Sprintf("$%.2f (FX %s unavailable, USD)", usd, taller)
		}
		return fmt.Sprintf("$%.2f", usd)
	}
	// Step 3: last-resort native display.
	return fmt.Sprintf("%s%.2f (FX unavailable, %s)",
		nativeSymbol(nativeCurrency), amount, strings.ToUpper(nativeCurrency))
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

// IsSupportedCurrency reports whether yage knows how to display
// (and FX-convert) a given ISO-4217 currency code. The supported
// set is the keys of currencySymbol; callers selecting a currency
// outside this set should print UnsupportedCurrencyMessage and fall
// back to USD.
func IsSupportedCurrency(code string) bool {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "" {
		return false
	}
	_, ok := currencySymbol[c]
	return ok
}

// SupportedCurrencies returns the sorted list of ISO-4217 codes yage
// can display today. Useful for --help output and the unsupported-
// currency message.
func SupportedCurrencies() []string {
	out := make([]string, 0, len(currencySymbol))
	for k := range currencySymbol {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// UnsupportedCurrencyMessage returns the user-facing notice we
// display when an operator selects a currency yage doesn't support
// yet — both the explanation and the issue-tracker link asking them
// to file a request. Currencies outside SupportedCurrencies() trigger
// this and fall back to USD.
func UnsupportedCurrencyMessage(code string) string {
	c := strings.ToUpper(strings.TrimSpace(code))
	if c == "" {
		c = "(empty)"
	}
	return fmt.Sprintf(
		"currency %s not yet supported (yage covers ~%d top global currencies); "+
			"falling back to USD. Please open an issue at "+
			"https://github.com/lpasquali/yage/issues to request it.",
		c, len(currencySymbol))
}

