package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Hetzner Cloud API — requires a project API token for catalog
// reads (Hetzner closed the unauthenticated endpoint a while back).
// GET https://api.hetzner.cloud/v1/server_types returns every
// server type with a prices[] array, one entry per location
// ("fsn1", "nbg1", "hel1", "ash", "hil"). Each entry exposes
// price_hourly.gross and price_monthly.gross in EUR (Hetzner
// caps monthly billing — that cap is what shows up here, not
// hourly × 730).
//
// Token: HCLOUD_TOKEN (also accepted: YAGE_HCLOUD_TOKEN).
// When unset, the fetcher returns ErrUnavailable and the cost
// path surfaces "Hetzner estimate unavailable: HCLOUD_TOKEN
// not set" rather than fabricate a number.
//
// USD conversion: env YAGE_EUR_USD overrides the
// default rate; we don't pull live FX (out of scope) but the
// rate is one knob, easy to override per-run.
const hetznerServerTypesURL = "https://api.hetzner.cloud/v1/server_types"

// hetznerToken returns the Hetzner Cloud project token used for
// pricing queries. Read order: cfg.Cost.Credentials (set by main
// from config.Load — also cross-filled with cfg.Providers.Hetzner.Token)
// → env-var fallback for cases where SetCredentials hasn't run yet.
func hetznerToken() string {
	if creds.HetznerToken != "" {
		return creds.HetznerToken
	}
	if v := os.Getenv("YAGE_HCLOUD_TOKEN"); v != "" {
		return v
	}
	return os.Getenv("HCLOUD_TOKEN")
}

type hetznerFetcher struct{ httpClient *http.Client }

func init() {
	Register("hetzner", &hetznerFetcher{httpClient: &http.Client{Timeout: 10 * time.Second}})
}

type hetznerPriceAmount struct {
	Net   string `json:"net"`
	Gross string `json:"gross"`
}

type hetznerLocPrice struct {
	Location     string             `json:"location"`
	PriceHourly  hetznerPriceAmount `json:"price_hourly"`
	PriceMonthly hetznerPriceAmount `json:"price_monthly"`
}

type hetznerServerType struct {
	Name   string            `json:"name"`
	Prices []hetznerLocPrice `json:"prices"`
}

type hetznerListResp struct {
	ServerTypes []hetznerServerType `json:"server_types"`
}

// eurToUSD returns the EUR→USD conversion rate. Read order:
// cfg.Cost.Currency.EURUSDOverride (via pricing.SetCurrency) →
// env-var fallback → hard-coded 1.08 default. See §16.
func eurToUSD() float64 {
	if prefs.EURUSDOverride != "" {
		if f, err := strconv.ParseFloat(prefs.EURUSDOverride, 64); err == nil && f > 0 {
			return f
		}
	}
	if v := os.Getenv("YAGE_EUR_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return 1.08
}

func (h *hetznerFetcher) Fetch(sku, region string) (Item, error) {
	token := hetznerToken()
	if token == "" {
		return Item{}, fmt.Errorf("hetzner: HCLOUD_TOKEN not set")
	}
	req, err := http.NewRequest("GET", hetznerServerTypesURL, nil)
	if err != nil {
		return Item{}, err
	}
	req.Header.Set("User-Agent", "yage/pricing")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("hetzner: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Item{}, fmt.Errorf("hetzner: HTTP %d", resp.StatusCode)
	}
	var list hetznerListResp
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return Item{}, fmt.Errorf("hetzner decode: %w", err)
	}
	for _, st := range list.ServerTypes {
		if st.Name != sku {
			continue
		}
		for _, p := range st.Prices {
			if p.Location != region {
				continue
			}
			hourEUR, _ := strconv.ParseFloat(p.PriceHourly.Gross, 64)
			monthEUR, _ := strconv.ParseFloat(p.PriceMonthly.Gross, 64)
			rate := eurToUSD()
			return Item{
				USDPerHour:  hourEUR * rate,
				USDPerMonth: monthEUR * rate,
				FetchedAt:   time.Now(),
			}, nil
		}
		return Item{}, fmt.Errorf("hetzner: sku %q not priced in region %q", sku, region)
	}
	return Item{}, fmt.Errorf("hetzner: unknown server_type %q", sku)
}

// HetznerVolumeUSDPerGBMonth fetches the per-GB-month price of
// a Hetzner Cloud Volume. Hetzner publishes volume pricing on
// a separate /pricing endpoint; we use the public one
// (auth-free) and convert EUR → USD using the same rate as
// server pricing.
const hetznerPricingURL = "https://api.hetzner.cloud/v1/pricing"

type hetznerVolumePrice struct {
	PricePerGBMonth hetznerPriceAmount `json:"price_per_gb_month"`
}

type hetznerLBTypePrice struct {
	Location     string             `json:"location"`
	PriceMonthly hetznerPriceAmount `json:"price_monthly"`
}

type hetznerLBType struct {
	Name   string               `json:"name"`
	Prices []hetznerLBTypePrice `json:"prices"`
}

type hetznerFloatingIPPrice struct {
	PriceMonthly hetznerPriceAmount `json:"price_monthly"`
}

type hetznerPricingPayload struct {
	Pricing struct {
		Volume               hetznerVolumePrice     `json:"volume"`
		LoadBalancerTypes    []hetznerLBType        `json:"load_balancer_types"`
		FloatingIP           hetznerFloatingIPPrice `json:"floating_ip"`
	} `json:"pricing"`
}

func fetchHetznerPricing() (*hetznerPricingPayload, error) {
	token := hetznerToken()
	if token == "" {
		return nil, fmt.Errorf("hetzner: HCLOUD_TOKEN not set")
	}
	c := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", hetznerPricingURL, nil)
	req.Header.Set("User-Agent", "yage/pricing")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("hetzner pricing: HTTP %d", resp.StatusCode)
	}
	var p hetznerPricingPayload
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

func HetznerVolumeUSDPerGBMonth() (float64, error) {
	p, err := fetchHetznerPricing()
	if err != nil {
		return 0, err
	}
	eur, err := strconv.ParseFloat(p.Pricing.Volume.PricePerGBMonth.Gross, 64)
	if err != nil {
		return 0, fmt.Errorf("hetzner pricing: parse: %w", err)
	}
	return eur * eurToUSD(), nil
}

// HetznerLoadBalancerUSDPerMonth fetches the live monthly cap for
// a Hetzner LB type ("lb11", "lb21", "lb31"). LB pricing per
// location varies very slightly; we return the lowest cap (EU
// locations are typically cheapest).
func HetznerLoadBalancerUSDPerMonth(lbType string) (float64, error) {
	p, err := fetchHetznerPricing()
	if err != nil {
		return 0, err
	}
	for _, t := range p.Pricing.LoadBalancerTypes {
		if t.Name != lbType {
			continue
		}
		var bestEUR float64
		found := false
		for _, lp := range t.Prices {
			eur, err := strconv.ParseFloat(lp.PriceMonthly.Gross, 64)
			if err != nil || eur <= 0 {
				continue
			}
			if !found || eur < bestEUR {
				bestEUR = eur
				found = true
			}
		}
		if !found {
			return 0, fmt.Errorf("hetzner LB %q: no priced location", lbType)
		}
		return bestEUR * eurToUSD(), nil
	}
	return 0, fmt.Errorf("hetzner LB %q: not in catalog", lbType)
}

// HetznerFloatingIPUSDPerMonth fetches the live monthly cap for
// a single static IPv4 floating IP.
func HetznerFloatingIPUSDPerMonth() (float64, error) {
	p, err := fetchHetznerPricing()
	if err != nil {
		return 0, err
	}
	eur, err := strconv.ParseFloat(p.Pricing.FloatingIP.PriceMonthly.Gross, 64)
	if err != nil {
		return 0, fmt.Errorf("hetzner pricing fip: parse: %w", err)
	}
	return eur * eurToUSD(), nil
}
