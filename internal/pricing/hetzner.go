// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

// Hetzner Cloud API — requires a project API token for catalog
// reads (Hetzner closed the unauthenticated endpoint a while back).
// Uses the official hcloud-go/v2 SDK; the underlying HTTP client is
// passed as &http.Client{} (nil Transport) so it inherits whatever
// http.DefaultTransport is at call time — including the airgap shim
// applied by defaulttransport_airgap_test.go.
//
// SDK docs: https://pkg.go.dev/github.com/hetznercloud/hcloud-go/v2/hcloud
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

// newHCloudClient creates a Hetzner Cloud API client using the given
// token. The HTTP client is intentionally &http.Client{} (nil
// Transport) so callers inherit http.DefaultTransport — this keeps
// the airgap test shim effective.
func newHCloudClient(token string) *hcloud.Client {
	return hcloud.NewClient(
		hcloud.WithToken(token),
		hcloud.WithHTTPClient(&http.Client{}),
	)
}

type hetznerFetcher struct{}

func init() {
	Register("hetzner", &hetznerFetcher{})
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
	client := newHCloudClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serverTypes, err := client.ServerType.All(ctx)
	if err != nil {
		return Item{}, fmt.Errorf("hetzner: %w", err)
	}

	tryNames := []string{sku}
	if alt, ok := hetznerDeprecatedServerTypes[strings.ToLower(strings.TrimSpace(sku))]; ok && alt != sku {
		tryNames = append(tryNames, alt)
	}
	for _, name := range tryNames {
		for _, st := range serverTypes {
			if st.Name != name {
				continue
			}
			for _, p := range st.Pricings {
				if p.Location == nil || p.Location.Name != region {
					continue
				}
				hourEUR, _ := strconv.ParseFloat(p.Hourly.Gross, 64)
				monthEUR, _ := strconv.ParseFloat(p.Monthly.Gross, 64)
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

// fetchHetznerPricing uses the Pricing SDK client to retrieve volume,
// load balancer, and floating IP pricing from /v1/pricing. The HTTP
// client is &http.Client{} (nil Transport) to inherit DefaultTransport.
func fetchHetznerPricing() (*hcloud.Pricing, error) {
	token := hetznerToken()
	if token == "" {
		return nil, fmt.Errorf("hetzner: HCLOUD_TOKEN not set")
	}
	client := newHCloudClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p, _, err := client.Pricing.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("hetzner pricing: %w", err)
	}
	return &p, nil
}

// HetznerVolumeUSDPerGBMonth fetches the per-GB-month price of
// a Hetzner Cloud Volume. Hetzner publishes volume pricing on
// a separate /pricing endpoint; we use the SDK and convert EUR → USD
// via the standard pricing.toUSD path.
func HetznerVolumeUSDPerGBMonth() (float64, error) {
	p, err := fetchHetznerPricing()
	if err != nil {
		return 0, err
	}
	eur, err := strconv.ParseFloat(p.Volume.PerGBMonthly.Gross, 64)
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
	for _, t := range p.LoadBalancerTypes {
		if t.LoadBalancerType == nil || t.LoadBalancerType.Name != lbType {
			continue
		}
		var bestEUR float64
		found := false
		for _, lp := range t.Pricings {
			eur, err := strconv.ParseFloat(lp.Monthly.Gross, 64)
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
	// Use the legacy FloatingIP field (IPv4 only); hcloud-go still
	// populates it alongside FloatingIPs for backward compatibility.
	eur, err := strconv.ParseFloat(p.FloatingIP.Monthly.Gross, 64)
	if err != nil {
		return 0, fmt.Errorf("hetzner pricing fip: parse: %w", err)
	}
	return toUSD(eur, "EUR")
}
