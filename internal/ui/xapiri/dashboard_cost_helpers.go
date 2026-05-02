// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard_cost_helpers.go — cost time-window presets, kickRefreshCmd,
// sortedCostRows, and per-period scaling helpers.
//
// costWindowPreset, costWindows, costDefaultPeriodIdx, costMonthSecs are
// declared here (not in dashboard.go) so the cost-render functions in
// tab_costs.go can reference them without a circular dependency.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/pricing"
)

// ─── cost time-window presets ─────────────────────────────────────────────────

type costWindowPreset struct {
	d     time.Duration
	label string // human-readable full name, shown in the selector
	short string // compact suffix used in tables and bottom bar
}

// costWindows is the ordered list of time-window presets. Index 6 (1 month)
// is the default. The user cycles through them with [ / ] in the costs tab.
var costWindows = []costWindowPreset{
	{time.Second, "1 second", "/sec"},
	{time.Minute, "1 minute", "/min"},
	{time.Hour, "1 hour", "/hr"},
	{8 * time.Hour, "8 hours", "/8h"},
	{24 * time.Hour, "1 day", "/day"},
	{7 * 24 * time.Hour, "1 week", "/wk"},
	{30 * 24 * time.Hour, "1 month", "/mo"},   // default (index 6)
	{365 * 24 * time.Hour, "1 year", "/yr"},
}

const costDefaultPeriodIdx = 6 // 1 month

// costMonthSecs is the number of seconds in the reference month (30 days).
const costMonthSecs = 30 * 24 * 3600.0


// kickRefreshCmd launches streaming cost fetches and returns a cmd that
// delivers the first result. Subsequent results chain via waitForCostRowCmd.
// Returns nil when cost estimation is disabled.
func (m dashModel) kickRefreshCmd() tea.Cmd {
	if !m.cfg.CostCompareEnabled {
		return nil
	}
	snap := m.buildSnapshotCfg()
	// Cost comparison always queries every credentialled provider, regardless
	// of which provider the user has selected in the config tab. The provider
	// select is a deployment choice, not a cost-filter. Clear InfraProvider
	// so StreamWithRegions does not narrow to a single provider.
	snap.InfraProvider = ""
	snap.InfraProviderDefaulted = true
	// Capture credentials at dispatch time: pricing.SetCredentials is a
	// process-global set before kind is connected, so it may not include
	// credentials loaded later from the cost-compare-config Secret.
	c := m.cfg.Cost.Credentials
	s := m.s
	cfg := m.cfg
	return func() tea.Msg {
		pricing.SetCredentials(pricing.Credentials{
			AWSAccessKeyID:     c.AWSAccessKeyID,
			AWSSecretAccessKey: c.AWSSecretAccessKey,
			GCPAPIKey:          c.GCPAPIKey,
			HetznerToken:       c.HetznerToken,
			DigitalOceanToken:  c.DigitalOceanToken,
			IBMCloudAPIKey:     c.IBMCloudAPIKey,
		})

		// Determine geo lat/lon for nearest-region ranking.
		// Source priority: --geoip outbound IP > DataCenterLocation centroid.
		var geoLat, geoLon float64
		geoOK := false
		if cfg.GeoIPEnabled {
			s.ensureGeoLookup()
			geoLat, geoLon, geoOK = s.geoLat, s.geoLon, s.geoOK
		}
		if !geoOK {
			dc := strings.ToUpper(strings.TrimSpace(snap.Cost.Currency.DataCenterLocation))
			if dc != "" {
				if lat, lon, ok := pricing.CountryCentroid(dc); ok {
					geoLat, geoLon, geoOK = lat, lon, true
				}
			}
		}

		// Build per-provider region list (up to 4 nearest). When geo is
		// unavailable, regionsByProvider stays nil and StreamWithRegions
		// falls back to the region already in snap per provider.
		var regionsByProvider map[string][]string
		if geoOK {
			regionsByProvider = map[string][]string{}
			for _, name := range []string{"aws", "azure", "gcp", "hetzner", "digitalocean", "linode", "oci", "ibmcloud"} {
				ranked := geoRankedRegions(name, geoLat, geoLon, 4)
				if len(ranked) > 0 {
					regionsByProvider[name] = ranked
				}
			}
		}

		ch := make(chan cost.CloudCost, 64)
		cost.StreamWithRegions(&snap, cost.ScopeCloudOnly, regionsByProvider, globalLogRing, ch)
		row, ok := <-ch
		return costRowMsg{row: row, ch: ch, done: !ok}
	}
}

func (m dashModel) sortedCostRows() []cost.CloudCost {
	sorted := make([]cost.CloudCost, len(m.costRows))
	copy(sorted, m.costRows)
	sort.Slice(sorted, func(i, j int) bool {
		ei := sorted[i].Err != nil
		ej := sorted[j].Err != nil
		if ei != ej {
			return !ei // errors go last
		}
		return sorted[i].Estimate.TotalUSDMonthly < sorted[j].Estimate.TotalUSDMonthly
	})
	return sorted
}

// ─── cost period helpers ──────────────────────────────────────────────────────

// activeCostWindow returns the currently selected time window preset.
func (m dashModel) activeCostWindow() costWindowPreset {
	if m.costPeriodIdx < 0 || m.costPeriodIdx >= len(costWindows) {
		return costWindows[costDefaultPeriodIdx]
	}
	return costWindows[m.costPeriodIdx]
}

// costForPeriod scales a monthly USD figure to the active window.
func (m dashModel) costForPeriod(monthly float64) float64 {
	w := m.activeCostWindow()
	return monthly * w.d.Seconds() / costMonthSecs
}

// formatCost renders an amount with the active window's short suffix.
func (m dashModel) formatCost(monthly float64) string {
	if monthly == 0 {
		return "n/a"
	}
	amt := m.costForPeriod(monthly)
	w := m.activeCostWindow()
	switch {
	case amt < 0.01:
		return fmt.Sprintf("$%.4f%s", amt, w.short)
	case amt < 1:
		return fmt.Sprintf("$%.3f%s", amt, w.short)
	case amt < 100:
		return fmt.Sprintf("$%.2f%s", amt, w.short)
	default:
		return fmt.Sprintf("$%.0f%s", amt, w.short)
	}
}
