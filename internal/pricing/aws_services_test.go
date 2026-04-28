// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"testing"
)

func TestAwsBulkProductFamily(t *testing.T) {
	p := awsBulkProduct{
		ProductFamily: "Load Balancer-Application",
		Attributes:    map[string]string{},
	}
	if got := awsBulkProductFamily(p); got != "load balancer-application" {
		t.Fatalf("got %q", got)
	}
	p2 := awsBulkProduct{
		ProductFamily: "",
		Attributes:    map[string]string{"productFamily": "Foo"},
	}
	if got := awsBulkProductFamily(p2); got != "foo" {
		t.Fatalf("attr fallback got %q", got)
	}
}

func TestAWSApplicationLBPricing_usEast1Live(t *testing.T) {
	if testing.Short() {
		t.Skip("live AWS pricing fetch")
	}
	if !PricingCredsConfigured("aws") {
		t.Skip("no AWS credentials configured (set via pricing.SetCredentials or --cost-compare-config)")
	}
	h, lcu, err := AWSApplicationLBPricing("us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if h <= 0 || lcu <= 0 {
		t.Fatalf("alb incomplete hr=%v lcu=%v", h, lcu)
	}
}
