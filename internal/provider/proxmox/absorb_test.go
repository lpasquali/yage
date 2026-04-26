package proxmox

import (
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

// TestAbsorbConfigYAML covers the inverse of KindSyncFields:
// reading PROXMOX_* uppercase keys out of a Secret-shaped map back
// into cfg, with "fill empty only" semantics. Was the body of the
// kindsync.fillEmptyFromMap switch before the Phase D dispatcher
// rewrite.
func TestAbsorbConfigYAML(t *testing.T) {
	p := &Provider{}
	cfg := &config.Config{}
	cfg.Providers.Proxmox.URL = "https://existing-do-not-overwrite"

	in := map[string]string{
		"PROXMOX_URL":                "https://from-secret-should-not-win",
		"PROXMOX_TOKEN":              "from-secret",
		"PROXMOX_REGION":             "from-secret-region",
		"PROXMOX_CSI_STORAGE":        "from-secret-storage",
		"PROXMOX_CAPI_USER_ID":       "capi-from-secret",
		"PROXMOX_CSI_TOPOLOGY_LABELS": "topology=foo",
		"UNKNOWN_KEY":                "ignored",
	}

	if !p.AbsorbConfigYAML(cfg, in) {
		t.Fatal("expected at least one assignment, got none")
	}

	// Existing non-empty value must NOT be overwritten.
	if cfg.Providers.Proxmox.URL != "https://existing-do-not-overwrite" {
		t.Errorf("URL was overwritten: %q", cfg.Providers.Proxmox.URL)
	}
	// Empty fields must be filled.
	tcs := []struct {
		name, got, want string
	}{
		{"Token", cfg.Providers.Proxmox.Token, "from-secret"},
		{"Region", cfg.Providers.Proxmox.Region, "from-secret-region"},
		{"CSIStorage", cfg.Providers.Proxmox.CSIStorage, "from-secret-storage"},
		{"CAPIUserID", cfg.Providers.Proxmox.CAPIUserID, "capi-from-secret"},
		{"CSITopologyLabels", cfg.Providers.Proxmox.CSITopologyLabels, "topology=foo"},
	}
	for _, tc := range tcs {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestAbsorbConfigYAML_NoOp confirms an empty map returns false
// (no assignments) and doesn't touch cfg.
func TestAbsorbConfigYAML_NoOp(t *testing.T) {
	p := &Provider{}
	cfg := &config.Config{}
	if got := p.AbsorbConfigYAML(cfg, map[string]string{}); got {
		t.Errorf("got true for empty map, want false")
	}
}
