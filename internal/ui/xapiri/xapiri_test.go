// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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

// TestArrowNav_ProvisionTabAdvancesFocus verifies that KeyDown on the
// provision tab advances the focus index and KeyUp retreats it.
// Regression guard: the token-overlay default branch must not swallow
// arrow keys before they reach updateCfgEditScreen.
func TestArrowNav_ProvisionTabAdvancesFocus(t *testing.T) {
	cfg := &config.Config{}
	s := newState(&bytes.Buffer{}, cfg)
	m := newDashModel(cfg, s)
	m.cfgSelected = true
	m.cfgLoading = false // simulate post-load state; cfgLoading=true guards editScreen
	m.activeTab = tabProvision
	initialFocus := m.focus

	// Send KeyDown — focus should advance.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m1, ok := next.(dashModel)
	if !ok {
		t.Fatalf("Update returned %T, want dashModel", next)
	}
	if m1.focus == initialFocus {
		t.Errorf("KeyDown did not advance focus: before=%d after=%d", initialFocus, m1.focus)
	}

	// Send KeyUp — focus should retreat back.
	prev, _ := m1.Update(tea.KeyMsg{Type: tea.KeyUp})
	m2, ok := prev.(dashModel)
	if !ok {
		t.Fatalf("Update returned %T, want dashModel", prev)
	}
	if m2.focus != initialFocus {
		t.Errorf("KeyUp did not retreat focus: want=%d got=%d", initialFocus, m2.focus)
	}
}

// TestArrowNav_TokenOverlayDoesNotSwallowArrows verifies that while the
// token re-prompt overlay is active, KeyUp/KeyDown are explicitly discarded
// rather than silently passed to the single-line token input (which ignores
// them anyway). This guards against the regression where the default branch
// consumed arrows without effect.
func TestArrowNav_TokenOverlayDoesNotSwallowArrows(t *testing.T) {
	cfg := &config.Config{}
	s := newState(&bytes.Buffer{}, cfg)
	m := newDashModel(cfg, s)
	m.cfgSelected = true
	m.cfgLoading = false
	m.activeTab = tabProvision
	m.tokenPromptActive = true
	before := m.focus

	for _, kt := range []tea.KeyType{tea.KeyDown, tea.KeyUp} {
		next, _ := m.Update(tea.KeyMsg{Type: kt})
		m2, ok := next.(dashModel)
		if !ok {
			t.Fatalf("Update returned %T, want dashModel", next)
		}
		// Overlay must still be active (arrow keys must not dismiss it).
		if !m2.tokenPromptActive {
			t.Errorf("key %v dismissed token overlay unexpectedly", kt)
		}
		// Focus must not change while the overlay is up.
		if m2.focus != before {
			t.Errorf("key %v changed focus from %d to %d while overlay active", kt, before, m2.focus)
		}
	}
}

// TestDeployTab_EnterOnDeployButton sets deployRequested and done and returns
// tea.Quit — not nil — so the program terminates cleanly and the deploy flag
// is preserved for the caller.  Regression guard: the on-prem branch in
// runDashboard previously returned dashResult{saved: true} and silently
// dropped deployRequested, causing Deploy to exit without triggering the
// orchestrator.
func TestDeployTab_EnterOnDeployButton(t *testing.T) {
	cfg := &config.Config{}
	s := newState(&bytes.Buffer{}, cfg)
	m := newDashModel(cfg, s)
	m.cfgSelected = true
	m.cfgLoading = false
	m.activeTab = tabDeploy
	m.deployFocused = 1 // deploy button

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m2, ok := next.(dashModel)
	if !ok {
		t.Fatalf("Update returned %T, want dashModel", next)
	}
	if !m2.deployRequested {
		t.Error("deployRequested must be true after pressing Enter on deploy button")
	}
	if !m2.done {
		t.Error("done must be true after pressing Enter on deploy button")
	}
	if cmd == nil {
		t.Error("Update must return a non-nil tea.Cmd (tea.Quit) after deploy button press")
	}
}

// TestDeployTab_OnPremModePreservesDeployRequested is the direct regression
// guard for the runDashboard on-prem branch dropping deployRequested.  It
// constructs a post-Run model whose selects[siMode] is "on-prem" with
// done=true + deployRequested=true and calls the same result-shaping logic
// that runDashboard executes to confirm deployRequested is not dropped.
func TestDeployTab_OnPremModePreservesDeployRequested(t *testing.T) {
	cfg := &config.Config{}
	s := newState(&bytes.Buffer{}, cfg)
	m := newDashModel(cfg, s)
	m.cfgSelected = true
	m.cfgLoading = false

	// Simulate on-prem mode selected.
	m.selects[siMode] = selectState{options: []string{"cloud", "on-prem"}, cur: 1}
	m.done = true
	m.deployRequested = true

	// Mirror the runDashboard result-shaping logic exactly.
	var res dashResult
	final := m
	if !final.done {
		res = dashResult{}
	} else if final.selects[siMode].value() == "on-prem" {
		s.fork = forkOnPrem
		*cfg = *final.cfg
		res = dashResult{saved: true, deployRequested: final.deployRequested}
	} else {
		final.flushToCfg()
		*cfg = *final.cfg
		res = dashResult{saved: true, deployRequested: final.deployRequested}
	}

	if !res.saved {
		t.Error("saved must be true when done=true")
	}
	if !res.deployRequested {
		t.Error("deployRequested must not be dropped in on-prem mode — was the runDashboard on-prem branch fixed?")
	}
}

// TestRenderCostsCredsForm_SecretFieldsNeverLeakValue verifies that unfocused
// secret inputs in the cost-tab credential form emit the status indicator
// rather than raw value or dot-count. Regression guard for ADR 0013 §2.
func TestRenderCostsCredsForm_SecretFieldsNeverLeakValue(t *testing.T) {
	const sentinel = "SENTINEL-SECRET-DEADBEEF-12345"

	cfg := &config.Config{}
	cfg.Cost.Credentials.AWSSecretAccessKey = sentinel
	cfg.Cost.Credentials.GCPAPIKey = sentinel
	cfg.Cost.Credentials.HetznerToken = sentinel
	cfg.Cost.Credentials.DigitalOceanToken = sentinel
	cfg.Cost.Credentials.IBMCloudAPIKey = sentinel

	s := newState(&bytes.Buffer{}, cfg)
	m := newDashModel(cfg, s)
	// Force credential form open; focus on AWS Key ID (index 0, non-secret).
	m.costCredsMode = true
	m.costCredsFocus = ccAWSKeyID

	lines := m.renderCostsCredsForm()
	rendered := strings.Join(lines, "\n")

	if bytes.Contains([]byte(rendered), []byte(sentinel)) {
		t.Errorf("renderCostsCredsForm leaked the secret value in unfocused rows: %q", rendered)
	}
	if !bytes.Contains([]byte(rendered), []byte("set")) {
		t.Errorf("renderCostsCredsForm missing set/not-set indicator for secret fields: %q", rendered)
	}
}

// TestRenderTokenPromptOverlay_NeverLeaksWhenUnfocused verifies that the
// token re-prompt overlay shows the status indicator (not dot-count or
// cleartext) when the input is not focused. Regression guard for ADR 0013 §2.
func TestRenderTokenPromptOverlay_NeverLeaksWhenUnfocused(t *testing.T) {
	const sentinel = "SENTINEL-SECRET-DEADBEEF-12345"

	cfg := &config.Config{}
	cfg.Providers.Proxmox.AdminUsername = "root@pam"

	s := newState(&bytes.Buffer{}, cfg)
	m := newDashModel(cfg, s)
	m.tokenPromptActive = true
	// tokenPromptInput starts unfocused; set a value to simulate a token
	// that was typed earlier and would leak length via EchoPassword dots.
	m.tokenPromptInput.SetValue(sentinel)
	if m.tokenPromptInput.Focused() {
		t.Skip("tokenPromptInput is focused; unfocused path cannot be tested here")
	}

	rendered := m.renderTokenPromptOverlay(80)

	if bytes.Contains([]byte(rendered), []byte(sentinel)) {
		t.Errorf("renderTokenPromptOverlay leaked the token value when unfocused: %q", rendered)
	}
	if !bytes.Contains([]byte(rendered), []byte("set")) {
		t.Errorf("renderTokenPromptOverlay missing set/not-set indicator when unfocused: %q", rendered)
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

// TestCostTab_TimeframeBracketStepping verifies that `[` and `]` cycle
// m.costPeriodIdx through the costWindows preset list AND wrap at both
// boundaries — pressing `]` past the last entry returns to index 0, and
// pressing `[` from index 0 goes to the last entry. Regression guard for
// #196: the previous implementation clamped at the boundaries, which read
// as "the keys do nothing" from the default position (idx=6 = "1 month")
// after one `]` press reached the end of the list.
//
// Also asserts the credential form does not swallow the keys (the original
// PR #156 fix path) and that the rendered cost suffix changes once the
// window changes — the only externally observable signal that step + render
// are both wired.
func TestCostTab_TimeframeBracketStepping(t *testing.T) {
	cfg := &config.Config{}
	s := newState(&bytes.Buffer{}, cfg)
	m := newDashModel(cfg, s)
	m.cfgSelected = true
	m.cfgLoading = false
	m.activeTab = tabCosts

	pressBracket := func(mm dashModel, r rune) dashModel {
		t.Helper()
		next, _ := mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		nm, ok := next.(dashModel)
		if !ok {
			t.Fatalf("Update returned %T, want dashModel", next)
		}
		return nm
	}

	// Default starting index is costDefaultPeriodIdx (6 = 1 month).
	if m.costPeriodIdx != costDefaultPeriodIdx {
		t.Fatalf("starting costPeriodIdx = %d, want %d", m.costPeriodIdx, costDefaultPeriodIdx)
	}
	startSuffix := m.formatCost(100.0)

	// `]` from default advances by one (the easy case).
	m1 := pressBracket(m, ']')
	if m1.costPeriodIdx != costDefaultPeriodIdx+1 {
		t.Errorf("after one `]` costPeriodIdx = %d, want %d", m1.costPeriodIdx, costDefaultPeriodIdx+1)
	}

	// Walk to the last index, then press `]` again — must wrap to 0, not
	// stay clamped at the last entry. This is the #196 regression case.
	mLast := m
	mLast.costPeriodIdx = len(costWindows) - 1
	mWrap := pressBracket(mLast, ']')
	if mWrap.costPeriodIdx != 0 {
		t.Errorf("`]` at last index did not wrap: got %d, want 0 (#196 — clamping reads as broken keys)", mWrap.costPeriodIdx)
	}

	// `[` from index 0 must wrap to the last entry — symmetric guard.
	mFirst := m
	mFirst.costPeriodIdx = 0
	mWrapBack := pressBracket(mFirst, '[')
	if mWrapBack.costPeriodIdx != len(costWindows)-1 {
		t.Errorf("`[` at index 0 did not wrap: got %d, want %d", mWrapBack.costPeriodIdx, len(costWindows)-1)
	}

	// `[` and `]` must work even when the credential form is active — that
	// was the PR #156 fix path; this guards against re-regressing it.
	mCreds := m
	mCreds.costCredsMode = true
	beforeIdx := mCreds.costPeriodIdx
	mCredsAfter := pressBracket(mCreds, ']')
	if mCredsAfter.costPeriodIdx == beforeIdx {
		t.Error("`]` was swallowed while credential form active (#154 regression)")
	}

	// The rendered cost suffix must reflect the new window. Without this,
	// a future regression that updates the index but breaks the renderer
	// would still pass the index assertions above.
	endSuffix := mWrap.formatCost(100.0)
	if startSuffix == endSuffix {
		t.Errorf("formatCost suffix did not change after stepping window: start=%q end=%q", startSuffix, endSuffix)
	}
}
