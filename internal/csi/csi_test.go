package csi_test

import (
	"sort"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"

	// Importing the driver packages registers their init() so the
	// Selector test sees a fully populated registry. Without these
	// imports a `go test ./internal/csi/` invocation would run with
	// an empty registry and the "default for aws" assertion would
	// fail.
	_ "github.com/lpasquali/yage/internal/csi/awsebs"
	_ "github.com/lpasquali/yage/internal/csi/azuredisk"
	_ "github.com/lpasquali/yage/internal/csi/gcppd"
)

func names(ds []csi.Driver) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Name())
	}
	return out
}

// TestSelectorEmptyCfgUsesProviderDefault: when cfg.CSI.Drivers is
// empty, Selector falls back to DefaultsFor(cfg.InfraProvider).
func TestSelectorEmptyCfgUsesProviderDefault(t *testing.T) {
	cases := []struct {
		provider string
		want     []string
	}{
		{"aws", []string{"aws-ebs"}},
		{"azure", []string{"azure-disk"}},
		{"gcp", []string{"gcp-pd"}},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			cfg := &config.Config{InfraProvider: c.provider}
			got := names(csi.Selector(cfg))
			if !equalStringSlices(got, c.want) {
				t.Errorf("Selector(%s) = %v, want %v", c.provider, got, c.want)
			}
		})
	}
}

// TestSelectorExplicitOverride: cfg.CSI.Drivers wins over
// DefaultsFor() and order is preserved.
func TestSelectorExplicitOverride(t *testing.T) {
	cfg := &config.Config{
		InfraProvider: "aws",
		CSI: config.CSIConfig{
			Drivers: []string{"gcp-pd", "azure-disk"},
		},
	}
	got := names(csi.Selector(cfg))
	want := []string{"gcp-pd", "azure-disk"}
	if !equalStringSlices(got, want) {
		t.Errorf("Selector explicit = %v, want %v", got, want)
	}
}

// TestSelectorUnknownNamesDropped: unregistered names are silently
// skipped (with a logx warning); known names still resolve.
func TestSelectorUnknownNamesDropped(t *testing.T) {
	cfg := &config.Config{
		InfraProvider: "aws",
		CSI: config.CSIConfig{
			Drivers: []string{"hcloud", "aws-ebs", "longhorn"},
		},
	}
	got := names(csi.Selector(cfg))
	want := []string{"aws-ebs"}
	if !equalStringSlices(got, want) {
		t.Errorf("Selector with unknowns = %v, want %v (only registered names survive)", got, want)
	}
}

// TestSelectorDeduplicates: repeated names collapse to a single
// entry (first occurrence wins).
func TestSelectorDeduplicates(t *testing.T) {
	cfg := &config.Config{
		CSI: config.CSIConfig{
			Drivers: []string{"aws-ebs", "aws-ebs", "azure-disk"},
		},
	}
	got := names(csi.Selector(cfg))
	want := []string{"aws-ebs", "azure-disk"}
	if !equalStringSlices(got, want) {
		t.Errorf("Selector dedupe = %v, want %v", got, want)
	}
}

// TestSelectorNoProviderNoExplicit: empty cfg → empty result, never
// panics.
func TestSelectorNoProviderNoExplicit(t *testing.T) {
	cfg := &config.Config{}
	got := names(csi.Selector(cfg))
	if len(got) != 0 {
		t.Errorf("Selector empty cfg = %v, want []", got)
	}
}

// TestRegisteredContainsScopedDrivers: the three drivers this
// commit ships should all be in Registered().
func TestRegisteredContainsScopedDrivers(t *testing.T) {
	got := csi.Registered()
	sort.Strings(got)
	for _, want := range []string{"aws-ebs", "azure-disk", "gcp-pd"} {
		found := false
		for _, n := range got {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Registered() missing %q (have %v)", want, got)
		}
	}
}

// TestDefaultsForOnlyImplementedProviders: DefaultsFor returns
// non-nil only for providers this commit ships drivers for.
func TestDefaultsForOnlyImplementedProviders(t *testing.T) {
	for _, p := range []string{"aws", "azure", "gcp"} {
		if got := csi.DefaultsFor(p); len(got) == 0 {
			t.Errorf("DefaultsFor(%q) = empty, expected at least one driver", p)
		}
	}
	// Unimplemented-this-commit providers get nil.
	for _, p := range []string{"hetzner", "linode", "oci", "digitalocean", "ibmcloud", "openstack", "vsphere", "proxmox"} {
		if got := csi.DefaultsFor(p); got != nil {
			t.Errorf("DefaultsFor(%q) = %v, expected nil (driver not yet shipped in Phase F scoped)", p, got)
		}
	}
}

func equalStringSlices(a, b []string) bool {
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
