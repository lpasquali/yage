// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package feasibility

import (
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

// TestTemplateOf_KnownNames covers the §22.2 named-template table:
// "light" / "medium" / "heavy" each map to the canonical preset.
func TestTemplateOf_KnownNames(t *testing.T) {
	cases := []struct {
		name     string
		template string
		want     Template
	}{
		{"light", "light", TemplateLight},
		{"medium", "medium", TemplateMedium},
		{"heavy", "heavy", TemplateHeavy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := templateOf(config.AppGroup{Template: c.template})
			if got != c.want {
				t.Errorf("templateOf(%q) = %+v, want %+v", c.template, got, c.want)
			}
		})
	}
}

// TestTemplateOf_DefaultsToMedium: empty string and unknown-name
// both fall back to medium per §22.2 ("I didn't say" → medium).
func TestTemplateOf_DefaultsToMedium(t *testing.T) {
	for _, name := range []string{"", "EXTRA", "tiny", "potato"} {
		got := templateOf(config.AppGroup{Template: name})
		if got != TemplateMedium {
			t.Errorf("templateOf(%q) = %+v, want medium fallback %+v", name, got, TemplateMedium)
		}
	}
}

// TestTemplateOf_Custom_UsesGroupValues: custom template propagates
// the AppGroup's CustomCores + CustomMemMiB.
func TestTemplateOf_Custom_UsesGroupValues(t *testing.T) {
	got := templateOf(config.AppGroup{
		Template:     "custom",
		CustomCores:  750,
		CustomMemMiB: 512,
	})
	if got.Name != "custom" {
		t.Errorf("custom template Name = %q, want \"custom\"", got.Name)
	}
	if got.CoresMilli != 750 || got.MemMiB != 512 {
		t.Errorf("custom template values = (%d, %d), want (750, 512)", got.CoresMilli, got.MemMiB)
	}
}

// TestTemplateOf_Custom_ZeroFallsBackToMedium: a custom group with
// 0 cores or 0 mem is a programmer error elsewhere — feasibility's
// defensive floor falls back to medium for whichever field was zero.
func TestTemplateOf_Custom_ZeroFallsBackToMedium(t *testing.T) {
	t.Run("zero cores", func(t *testing.T) {
		got := templateOf(config.AppGroup{Template: "custom", CustomCores: 0, CustomMemMiB: 999})
		if got.CoresMilli != TemplateMedium.CoresMilli {
			t.Errorf("zero-cores fallback CoresMilli = %d, want %d", got.CoresMilli, TemplateMedium.CoresMilli)
		}
		if got.MemMiB != 999 {
			t.Errorf("zero-cores fallback MemMiB = %d, want 999 (mem not zeroed)", got.MemMiB)
		}
	})
	t.Run("zero mem", func(t *testing.T) {
		got := templateOf(config.AppGroup{Template: "custom", CustomCores: 333, CustomMemMiB: 0})
		if got.MemMiB != TemplateMedium.MemMiB {
			t.Errorf("zero-mem fallback MemMiB = %d, want %d", got.MemMiB, TemplateMedium.MemMiB)
		}
		if got.CoresMilli != 333 {
			t.Errorf("zero-mem fallback CoresMilli = %d, want 333 (cores not zeroed)", got.CoresMilli)
		}
	})
}

// TestCpNodesFor: §23.3 control-plane node count keyed on resilience.
func TestCpNodesFor(t *testing.T) {
	cases := []struct {
		resilience string
		want       int
	}{
		{"", 1},
		{"single", 1},
		{"ha", 3},
		{"ha-mr", 3},
		{"unknown-tier", 1}, // unknown → single (fail-cheap, not fail-paranoid)
	}
	for _, c := range cases {
		got := cpNodesFor(c.resilience)
		if got != c.want {
			t.Errorf("cpNodesFor(%q) = %d, want %d", c.resilience, got, c.want)
		}
	}
}

// TestDbResourcesFor_NoDB: zero / negative DatabaseGB returns (0, 0).
func TestDbResourcesFor_NoDB(t *testing.T) {
	for _, gb := range []int{0, -1, -100} {
		c, m := dbResourcesFor(gb)
		if c != 0 || m != 0 {
			t.Errorf("dbResourcesFor(%d) = (%d, %d), want (0, 0)", gb, c, m)
		}
	}
}

// TestDbResourcesFor_FloorsAtTwoCoresTwoGiB: very small DBs still get
// the §23.3 minimum (2 cores, 2 GiB). 50 GB hits the cores threshold
// exactly; below that the floor kicks in.
func TestDbResourcesFor_FloorsAtTwoCoresTwoGiB(t *testing.T) {
	for _, gb := range []int{1, 10, 49, 50} {
		c, m := dbResourcesFor(gb)
		if c < 2000 {
			t.Errorf("dbResourcesFor(%d) cores = %d millicores, want >= 2000 (2-core floor)", gb, c)
		}
		if m < 2048 {
			t.Errorf("dbResourcesFor(%d) mem = %d MiB, want >= 2048 (2 GiB floor)", gb, m)
		}
	}
}

// TestDbResourcesFor_LargeDB_ScalesPerHeuristic: big DBs follow the
// "1 core per 50 GB, 100 MiB per GB" heuristic from §23.3.
func TestDbResourcesFor_LargeDB_ScalesPerHeuristic(t *testing.T) {
	c, m := dbResourcesFor(500)
	// 500 / 50 = 10 cores; mem = 500 × 100 = 50_000 MiB
	if c != 10000 {
		t.Errorf("dbResourcesFor(500) cores = %d, want 10000 (10 cores × 1000 millicores)", c)
	}
	if m != 50000 {
		t.Errorf("dbResourcesFor(500) mem = %d, want 50000", m)
	}
}

// TestAppResourcesFor_Empty: no apps → zero compute footprint.
func TestAppResourcesFor_Empty(t *testing.T) {
	c, m := appResourcesFor(nil)
	if c != 0 || m != 0 {
		t.Errorf("appResourcesFor(nil) = (%d, %d), want (0, 0)", c, m)
	}
	c, m = appResourcesFor([]config.AppGroup{})
	if c != 0 || m != 0 {
		t.Errorf("appResourcesFor([]) = (%d, %d), want (0, 0)", c, m)
	}
}

// TestAppResourcesFor_SkipsZeroCount: a group with Count <= 0 is a
// no-op (it's how the TUI persists "removed this app row").
func TestAppResourcesFor_SkipsZeroCount(t *testing.T) {
	c, m := appResourcesFor([]config.AppGroup{
		{Count: 0, Template: "heavy"},
		{Count: -5, Template: "heavy"},
	})
	if c != 0 || m != 0 {
		t.Errorf("zero-count groups not skipped: got (%d, %d)", c, m)
	}
}

// TestAppResourcesFor_SumsAcrossGroups: the workload's footprint is
// the sum of every group's (count × template).
func TestAppResourcesFor_SumsAcrossGroups(t *testing.T) {
	apps := []config.AppGroup{
		{Count: 3, Template: "light"},  // 3 × (100m, 128 MiB) = 300m, 384 MiB
		{Count: 2, Template: "medium"}, // 2 × (200m, 256 MiB) = 400m, 512 MiB
		{Count: 1, Template: "heavy"},  // 1 × (500m, 1024 MiB) = 500m, 1024 MiB
	}
	c, m := appResourcesFor(apps)
	wantC := int64(300 + 400 + 500)
	wantM := int64(384 + 512 + 1024)
	if c != wantC || m != wantM {
		t.Errorf("appResourcesFor sums = (%d, %d), want (%d, %d)", c, m, wantC, wantM)
	}
}

// TestAppResourcesFor_CustomMixedWithNamed: custom groups participate
// in the sum just like named ones.
func TestAppResourcesFor_CustomMixedWithNamed(t *testing.T) {
	apps := []config.AppGroup{
		{Count: 1, Template: "medium"},                                       // 200m, 256 MiB
		{Count: 2, Template: "custom", CustomCores: 1000, CustomMemMiB: 512}, // 2 × (1000m, 512) = 2000m, 1024 MiB
	}
	c, m := appResourcesFor(apps)
	if c != 2200 || m != 1280 {
		t.Errorf("custom+named sum = (%d, %d), want (2200, 1280)", c, m)
	}
}

// TestConstants_FrozenForDocsAlignment: the §22.2 / §23.3 / §23.4
// numeric constants are part of the public docs surface — flag any
// silent change so the docs stay aligned.
func TestConstants_FrozenForDocsAlignment(t *testing.T) {
	if TemplateLight.CoresMilli != 100 || TemplateLight.MemMiB != 128 {
		t.Errorf("light template drifted from §22.2: %+v", TemplateLight)
	}
	if TemplateMedium.CoresMilli != 200 || TemplateMedium.MemMiB != 256 {
		t.Errorf("medium template drifted from §22.2: %+v", TemplateMedium)
	}
	if TemplateHeavy.CoresMilli != 500 || TemplateHeavy.MemMiB != 1024 {
		t.Errorf("heavy template drifted from §22.2: %+v", TemplateHeavy)
	}
	if SystemCoresMilli != 2000 || SystemMemMiB != 4096 {
		t.Errorf("system reserve drifted from §23.3: cores=%d mem=%d", SystemCoresMilli, SystemMemMiB)
	}
	if CPCoresMilli != 2000 || CPMemMiB != 4096 {
		t.Errorf("CP per-node overhead drifted from §23.3: cores=%d mem=%d", CPCoresMilli, CPMemMiB)
	}
	if SchedulingFragmentationFactor != 1.33 {
		t.Errorf("fragmentation factor drifted from §23.3: %v", SchedulingFragmentationFactor)
	}
	if DefaultHeadroomPct != 0.20 {
		t.Errorf("default headroom drifted from §23.4: %v", DefaultHeadroomPct)
	}
	if ComfortableThreshold != 0.60 || TightThreshold != 0.90 {
		t.Errorf("verdict thresholds drifted from §23.4: comfortable=%v tight=%v", ComfortableThreshold, TightThreshold)
	}
}
