// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
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
// EUR is Hetzner's "datacenter currency" — the same role USD plays
// for AWS/Azure/GCP/etc. The fetcher fills Item.NativeAmount and
// Item.NativeCurrency with the unmodified EUR figure, and converts
// to USD via the standard FX path (pricing.toUSD) for the canonical
// USDPerHour/USDPerMonth fields used by cross-vendor sort. There is
// no hetzner-specific FX knob — open.er-api.com supplies the EUR
// rate alongside every other currency.
const hetznerServerTypesURL = "https://api.hetzner.cloud/v1/server_types"

// hetznerDeprecatedServerTypes maps server type names removed from the
// public catalog (Hetzner Cloud changelog 2025-10-16) to their
// successors so existing configs and env vars keep pricing.
var hetznerDeprecatedServerTypes = map[string]string{
	"cx22": "cx23", "cx32": "cx33", "cx42": "cx43", "cx52": "cx53",
	"cpx11": "cpx12", "cpx21": "cpx22", "cpx31": "cpx32", "cpx41": "cpx42", "cpx51": "cpx52",
}

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

// nativeToUSDOrZero converts a hetzner native amount (EUR) to USD
// via the standard FX path. On FX failure we return 0; callers
// surface NativeAmount instead, and FormatTaller's USD-fallback
// chain ensures the UI still renders something.
func nativeToUSDOrZero(eurAmount float64) float64 {
	usd, err := toUSD(eurAmount, "EUR")
	if err != nil {
		return 0
	}
	return usd
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
	tryNames := []string{sku}
	if alt, ok := hetznerDeprecatedServerTypes[strings.ToLower(strings.TrimSpace(sku))]; ok && alt != sku {
		tryNames = append(tryNames, alt)
	}
	for _, name := range tryNames {
		for _, st := range list.ServerTypes {
			if st.Name != name {
				continue
			}
			for _, p := range st.Prices {
				if p.Location != region {
					continue
				}
				hourEUR, _ := strconv.ParseFloat(p.PriceHourly.Gross, 64)
				monthEUR, _ := strconv.ParseFloat(p.PriceMonthly.Gross, 64)
				return Item{
					USDPerHour:     nativeToUSDOrZero(hourEUR),
					USDPerMonth:    nativeToUSDOrZero(monthEUR),
					NativeCurrency: "EUR",
					NativeAmount:   monthEUR,
					FetchedAt:      time.Now(),
				}, nil
			}
			return Item{}, fmt.Errorf("hetzner: sku %q not priced in region %q", name, region)
		}
	}
	return Item{}, fmt.Errorf("hetzner: unknown server_type %q", sku)
}

// HetznerVolumeUSDPerGBMonth fetches the per-GB-month price of
// a Hetzner Cloud Volume. Hetzner publishes volume pricing on
// a separate /pricing endpoint; we use the public one and
// convert EUR → USD via the standard pricing.toUSD path.
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
	return toUSD(eur, "EUR")
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
		return toUSD(bestEUR, "EUR")
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
	return toUSD(eur, "EUR")
}