// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"math"
	"testing"
)

func TestResolveAzureDNSArmRegionForDNSPricing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"", "Zone 1"},
		{"eastus", "Zone 1"},
		{"italynorth", "Zone 1"},
		{"japaneast", "Zone 2"},
		{"brazilsouth", "Zone 3"},
		{"usgovvirginia", "US Gov Zone 1"},
		{"germanycentral", "DE Gov Zone 2"},
		{"Zone 2", "Zone 2"},
		{"US Gov Zone 1", "US Gov Zone 1"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got := resolveAzureDNSArmRegionForDNSPricing(tc.in)
			if got != tc.want {
				t.Fatalf("resolve(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAzureDNSZoneUSDPerMonth_liveEastUS(t *testing.T) {
	if testing.Short() {
		t.Skip("live Azure Retail API")
	}
	usd, err := AzureDNSZoneUSDPerMonth("eastus")
	if err != nil {
		t.Fatal(err)
	}
	if usd <= 0 || math.Abs(usd-0.5) > 0.02 {
		t.Fatalf("unexpected eastus public zone rate: %v", usd)
	}
}
