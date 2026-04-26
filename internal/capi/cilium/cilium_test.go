// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cilium

import (
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestDefaultLBIPAMPoolCIDRFromNodes(t *testing.T) {
	cases := []struct {
		ranges string
		prefix string
		want   string
	}{
		// Bash: first IP of the first range + IP_PREFIX, masked to network.
		{"10.27.192.21-10.27.192.30", "24", "10.27.192.0/24"},
		{"10.27.192.21-10.27.192.30,10.27.193.21-10.27.193.30", "24", "10.27.192.0/24"},
		{"192.168.1.42", "16", "192.168.0.0/16"},
		// Invalid inputs return "".
		{"", "24", ""},
		{"10.27.192.21", "", ""},
		{"not-an-ip", "24", ""},
		{"10.27.192.21", "33", ""}, // prefix > 32 for IPv4
	}
	for _, c := range cases {
		cfg := &config.Config{NodeIPRanges: c.ranges, IPPrefix: c.prefix}
		got := DefaultLBIPAMPoolCIDRFromNodes(cfg)
		if got != c.want {
			t.Errorf("ranges=%q prefix=%q: got %q want %q", c.ranges, c.prefix, got, c.want)
		}
	}
}