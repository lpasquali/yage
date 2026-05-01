// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/feasibility"
)

// TestProperCase_RegisteredProviders covers every provider name that
// appears in the registry today. The mapping has to stay in sync with
// the Providers struct's exported field names so reflection in
// providerSubStruct can walk into the right sub-config.
func TestProperCase_RegisteredProviders(t *testing.T) {
	cases := map[string]string{
		"aws":          "AWS",
		"azure":        "Azure",
		"gcp":          "GCP",
		"hetzner":      "Hetzner",
		"digitalocean": "DigitalOcean",
		"linode":       "Linode",
		"oci":          "OCI",
		"ibmcloud":     "IBMCloud",
		"proxmox":      "Proxmox",
		"openstack":    "OpenStack",
		"vsphere":      "Vsphere",
	}
	for in, want := range cases {
		if got := properCase(in); got != want {
			t.Errorf("properCase(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestProperCase_FallbackOnUnknown: an unknown name falls back to
// "first-letter-uppercased" so a newly-registered provider doesn't
// silently disappear from applyGeoRegionDefaults.
func TestProperCase_FallbackOnUnknown(t *testing.T) {
	if got := properCase("acmecloud"); got != "Acmecloud" {
		t.Errorf("properCase(acmecloud) = %q, want fallback Acmecloud", got)
	}
	if got := properCase(""); got != "" {
		t.Errorf("properCase(\"\") = %q, want empty string", got)
	}
}

// TestProperCase_ResolvesViaReflection: the strings properCase
// returns must match a real exported field on cfg.Providers, or
// providerSubStruct returns (zero, false) and geo fills silently skip.
// This is the load-bearing invariant.
func TestProperCase_ResolvesViaReflection(t *testing.T) {
	cfg := &config.Config{}
	for _, name := range []string{
		"aws", "azure", "gcp", "hetzner", "digitalocean",
		"linode", "oci", "ibmcloud", "proxmox", "openstack", "vsphere",
	} {
		field := properCase(name)
		v := reflect.ValueOf(&cfg.Providers).Elem().FieldByName(field)
		if !v.IsValid() {
			t.Errorf("properCase(%q) → %q which is NOT a field on Providers — geo fill would silently skip", name, field)
		}
	}
}

// TestForkType_String: the printable names used in user-visible
// review output and persisted Secret values.
func TestForkType_String(t *testing.T) {
	cases := map[forkType]string{
		forkOnPrem:  "on-prem",
		forkCloud:   "cloud",
		forkUnknown: "unknown",
	}
	for f, want := range cases {
		if got := f.String(); got != want {
			t.Errorf("forkType(%d).String() = %q, want %q", f, got, want)
		}
	}
}

// TestFeasibilityVerdict_SymbolAndString: the unicode tick / warn /
// cross plus their human strings, used in the dashboard feasibility
// display and persisted Secret. Stable visible API.
func TestFeasibilityVerdict_SymbolAndString(t *testing.T) {
	cases := []struct {
		v       FeasibilityVerdict
		symbol  string
		stringS string
	}{
		{FeasibilityComfortable, "✓", "comfortable"},
		{FeasibilityTight, "⚠", "tight"},
		{FeasibilityInfeasible, "✗", "infeasible"},
		{FeasibilityUnchecked, "?", "unchecked"},
	}
	for _, c := range cases {
		if got := c.v.symbol(); got != c.symbol {
			t.Errorf("verdict %d symbol() = %q, want %q", c.v, got, c.symbol)
		}
		if got := c.v.String(); got != c.stringS {
			t.Errorf("verdict %d String() = %q, want %q", c.v, got, c.stringS)
		}
	}
}

// TestRun_NilCfgReturnsTwo: defensive guard at the top of Run() —
// nil cfg shouldn't panic, it should print a message and return 2.
func TestRun_NilCfgReturnsTwo(t *testing.T) {
	var buf bytes.Buffer
	got := Run(&buf, nil)
	if got != 2 {
		t.Errorf("Run(nil) returned %d, want 2", got)
	}
	if !bytes.Contains(buf.Bytes(), []byte("xapiri")) {
		t.Errorf("Run(nil) output missing \"xapiri\" prefix: %q", buf.String())
	}
}

// TestNewState_HeadroomDefault: every state starts with the §23.4
// default headroom of 20% so the cost-compare uses the same gate
// as the feasibility check itself.
func TestNewState_HeadroomDefault(t *testing.T) {
	cfg := &config.Config{}
	s := newState(&bytes.Buffer{}, cfg)
	if s.headroomPct != 0.20 {
		t.Errorf("newState headroomPct = %v, want 0.20", s.headroomPct)
	}
}

func TestSyncWorkloadShapeToCfg_CloudFork(t *testing.T) {
	cfg := &config.Config{BudgetUSDMonth: 300}
	w := workloadShape{
		Apps:       []appBucket{{Count: 4, Template: "medium"}},
		DBGB:       2,
		EgressGBMo: 4,
	}
	syncWorkloadShapeToCfg(cfg, w, resilienceHA, envStaging, forkCloud)
	if len(cfg.Workload.Apps) != 1 || cfg.Workload.Apps[0].Count != 4 || cfg.Workload.Apps[0].Template != "medium" {
		t.Fatalf("Workload.Apps = %+v", cfg.Workload.Apps)
	}
	if cfg.Workload.DatabaseGB != 2 || cfg.Workload.EgressGBMonth != 4 {
		t.Fatalf("DatabaseGB=%d EgressGBMonth=%d", cfg.Workload.DatabaseGB, cfg.Workload.EgressGBMonth)
	}
	if cfg.Workload.Resilience != "ha" || cfg.Workload.Environment != "staging" {
		t.Fatalf("Resilience=%q Environment=%q", cfg.Workload.Resilience, cfg.Workload.Environment)
	}
	if _, err := feasibility.Check(cfg); err != nil {
		t.Fatalf("feasibility.Check after sync: %v", err)
	}
}

func TestSyncWorkloadShapeToCfg_OnPremEgressDefault(t *testing.T) {
	cfg := &config.Config{BudgetUSDMonth: 100}
	w := workloadShape{
		Apps:       []appBucket{{Count: 2, Template: "light"}},
		DBGB:       5,
		EgressGBMo: 0,
	}
	syncWorkloadShapeToCfg(cfg, w, resilienceSingle, envDev, forkOnPrem)
	if cfg.Workload.EgressGBMonth != 10 {
		t.Fatalf("on-prem implicit egress = db×2: got %d want 10", cfg.Workload.EgressGBMonth)
	}
}

func TestGeoNearestRegionID_nearLondon(t *testing.T) {
	// Roughly London — does not require provider.Registered() (test
	// binary may not link every provider init).
	if got := geoNearestRegionID("aws", 51.5, -0.12); got != "eu-west-2" {
		t.Fatalf("aws nearest=%q want eu-west-2", got)
	}
	if got := geoNearestRegionID("azure", 51.5, -0.12); got != "uksouth" {
		t.Fatalf("azure nearest=%q want uksouth", got)
	}
}

// TestRenderField_SecretFieldsNeverLeakValue verifies that secret fields
// never emit their cleartext value in the unfocused render path. This is a
// regression guard for ADR 0013 (secret display policy).
func TestRenderField_SecretFieldsNeverLeakValue(t *testing.T) {
	const sentinel = "SENTINEL-SECRET-DEADBEEF-12345"

	cfg := &config.Config{}
	cfg.Providers.Proxmox.AdminToken = sentinel
	cfg.IssuingCARootCert = sentinel
	cfg.IssuingCARootKey = sentinel

	s := newState(&bytes.Buffer{}, cfg)
	m := newDashModel(cfg, s)

	cases := []struct {
		name string
		fid  int
	}{
		{"tiProxmoxAdminToken", focProxmoxAdminToken},
		{"tiIssuingCACert", focIssuingCACert},
		{"tiIssuingCAKey", focIssuingCAKey},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rendered := m.renderField(c.fid, false, 120)
			if bytes.Contains([]byte(rendered), []byte(sentinel)) {
				t.Errorf("renderField(%s, focused=false) leaked the secret value in: %q", c.name, rendered)
			}
			// Confirm the indicator is present.
			if !bytes.Contains([]byte(rendered), []byte("set")) {
				t.Errorf("renderField(%s, focused=false) missing set/not-set indicator in: %q", c.name, rendered)
			}
		})
	}
}
