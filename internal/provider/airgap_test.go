// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package provider

import (
	"errors"
	"testing"
)

func TestAirgapCompatible(t *testing.T) {
	yes := []string{"proxmox", "openstack", "vsphere", "capd"}
	no := []string{"aws", "azure", "gcp", "hetzner", "digitalocean", "linode", "oci", "ibmcloud"}
	for _, n := range yes {
		if !AirgapCompatible(n) {
			t.Errorf("AirgapCompatible(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if AirgapCompatible(n) {
			t.Errorf("AirgapCompatible(%q) = true, want false", n)
		}
	}
}

func TestAirgapFilter(t *testing.T) {
	in := []string{"proxmox", "aws", "vsphere", "gcp", "openstack"}
	t.Run("airgapped=true filters cloud providers", func(t *testing.T) {
		got := AirgapFilter(in, true)
		want := []string{"proxmox", "vsphere", "openstack"}
		if !equalStrings(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("airgapped=false returns input unchanged", func(t *testing.T) {
		got := AirgapFilter(in, false)
		if !equalStrings(got, in) {
			t.Fatalf("got %v, want %v", got, in)
		}
	})
}

func TestAirgapAwareForName_Cloud(t *testing.T) {
	_, err := AirgapAwareForName("aws", true)
	if !errors.Is(err, ErrAirgapped) {
		t.Fatalf("got err=%v, want wrapping ErrAirgapped", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}