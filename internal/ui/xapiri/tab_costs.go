// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// tab_costs.go — updateCostsTab, updateCostsCredsForm, saveCostCredsCmd,
// renderCostsTab, renderCostsCredsForm, renderCostRow, renderVendorRow.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	lipgloss "github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/pricing"
)

// updateCostsTab handles key events on the costs tab.
func (m dashModel) updateCostsTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	keyStr := msg.String()

	// Intercept [ / ] before any inner form so they always navigate the
	// timeframe window — even when the credential form is active (#154).
	// Wrap at both ends so repeated presses always step (#196): the previous
	// clamp made the keys read as "broken" once the user hit either boundary
	// (default idx=6 → one press of ] reaches end, further presses no-op).
	switch keyStr {
	case "[":
		m.costPeriodIdx = (m.costPeriodIdx - 1 + len(costWindows)) % len(costWindows)
		return m, nil
	case "]":
		m.costPeriodIdx = (m.costPeriodIdx + 1) % len(costWindows)
		return m, nil
	}

	if m.costCredsMode {
		return m.updateCostsCredsForm(msg)
	}
	switch {
	case keyStr == "c" || keyStr == "e":
		m.costCredsMode = true
		m.costCredsInputs[m.costCredsFocus].Focus()
		return m, textinput.Blink
	case key == tea.KeyUp:
		if len(m.costRows) > 0 {
			m.costVendor = (m.costVendor - 1 + len(m.costRows)) % len(m.costRows)
		}
	case key == tea.KeyDown:
		if len(m.costRows) > 0 {
			m.costVendor = (m.costVendor + 1) % len(m.costRows)
		}
	}
	return m, nil
}

// updateCostsCredsForm handles key events inside the credential entry form.
func (m dashModel) updateCostsCredsForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	keyStr := msg.String()

	switch {
	case key == tea.KeyEnter:
		if m.costCredsFocus == ccCount-1 {
			// Last field: submit. Evaluate saveCostCredsCmd first so
			// pointer-receiver mutations (costCredsMode=false) are
			// visible in the returned model.
			cmd := m.saveCostCredsCmd()
			return m, cmd
		}
		// Advance to next field.
		m.costCredsInputs[m.costCredsFocus].Blur()
		m.costCredsFocus++
		m.costCredsInputs[m.costCredsFocus].Focus()
		return m, textinput.Blink

	case key == tea.KeyTab:
		m.costCredsInputs[m.costCredsFocus].Blur()
		m.costCredsFocus = (m.costCredsFocus + 1) % ccCount
		m.costCredsInputs[m.costCredsFocus].Focus()
		return m, textinput.Blink

	case key == tea.KeyShiftTab:
		m.costCredsInputs[m.costCredsFocus].Blur()
		m.costCredsFocus = (m.costCredsFocus - 1 + ccCount) % ccCount
		m.costCredsInputs[m.costCredsFocus].Focus()
		return m, textinput.Blink

	case keyStr == "ctrl+s":
		cmd := m.saveCostCredsCmd()
		return m, cmd

	default:
		ti, cmd := m.costCredsInputs[m.costCredsFocus].Update(msg)
		m.costCredsInputs[m.costCredsFocus] = ti
		return m, cmd
	}
}

// saveCostCredsCmd applies the credential form values to cfg, wires the
// pricing package, and asynchronously persists to the kind Secret.
func (m *dashModel) saveCostCredsCmd() tea.Cmd {
	m.cfg.Cost.Credentials.AWSAccessKeyID = strings.TrimSpace(m.costCredsInputs[ccAWSKeyID].Value())
	m.cfg.Cost.Credentials.AWSSecretAccessKey = strings.TrimSpace(m.costCredsInputs[ccAWSSecret].Value())
	m.cfg.Cost.Credentials.GCPAPIKey = strings.TrimSpace(m.costCredsInputs[ccGCPKey].Value())
	m.cfg.Cost.Credentials.HetznerToken = strings.TrimSpace(m.costCredsInputs[ccHetznerTok].Value())
	m.cfg.Cost.Credentials.DigitalOceanToken = strings.TrimSpace(m.costCredsInputs[ccDOTok].Value())
	m.cfg.Cost.Credentials.IBMCloudAPIKey = strings.TrimSpace(m.costCredsInputs[ccIBMKey].Value())

	pricing.SetCredentials(pricing.Credentials{
		AWSAccessKeyID:     m.cfg.Cost.Credentials.AWSAccessKeyID,
		AWSSecretAccessKey: m.cfg.Cost.Credentials.AWSSecretAccessKey,
		GCPAPIKey:          m.cfg.Cost.Credentials.GCPAPIKey,
		HetznerToken:       m.cfg.Cost.Credentials.HetznerToken,
		DigitalOceanToken:  m.cfg.Cost.Credentials.DigitalOceanToken,
		IBMCloudAPIKey:     m.cfg.Cost.Credentials.IBMCloudAPIKey,
	})
	m.cfg.CostCompareEnabled = true
	m.costCredsMode = false
	m.costCredsInputs[m.costCredsFocus].Blur()

	cfg := m.cfg
	return tea.Batch(
		m.markDirty(),
		func() tea.Msg {
			creds := map[string]string{
				"aws-access-key-id":     cfg.Cost.Credentials.AWSAccessKeyID,
				"aws-secret-access-key": cfg.Cost.Credentials.AWSSecretAccessKey,
				"gcp-api-key":           cfg.Cost.Credentials.GCPAPIKey,
				"hetzner-token":         cfg.Cost.Credentials.HetznerToken,
				"digitalocean-token":    cfg.Cost.Credentials.DigitalOceanToken,
				"ibmcloud-api-key":      cfg.Cost.Credentials.IBMCloudAPIKey,
			}
			return saveCostCredsMsg{err: kindsync.WriteCostCompareSecret(cfg, creds)}
		},
	)
}

// ─── costs tab ───────────────────────────────────────────────────────────────

// renderCostsTab renders the full comparison table + ASCII bar chart.
func (m dashModel) renderCostsTab(w, h int) string {
	var lines []string

	win := m.activeCostWindow()
	windowSel := stMuted.Render("[") + win.label + stMuted.Render("]") +
		stMuted.Render("  ◄[ ]►")
	title := stHdr.Render(" Provider cost comparison") + "  " + windowSel
	if m.costLoading {
		title += stMuted.Render("  refreshing…")
	}
	lines = append(lines, title)
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))

	if m.cfg.CostCompareEnabled && !m.cfg.GeoIPEnabled && m.cfg.Cost.Currency.DataCenterLocation == "" {
		lines = append(lines, stMuted.Render("  DataCenter location unset — cost comparison unavailable."))
		lines = append(lines, stMuted.Render("  Set YAGE_DATA_CENTER_LOCATION or enable GeoIP to activate region-aware pricing."))
		lines = append(lines, "")
	}

	if m.costCredsMode {
		lines = append(lines, m.renderCostsCredsForm()...)
	} else if len(m.costRows) == 0 {
		lines = append(lines, stMuted.Render("  computing…"))
	} else {
		sorted := m.sortedCostRows()

		// Find max and cheapest for bar-chart normalization.
		maxTotal := 0.0
		cheapest := 0.0
		for _, r := range sorted {
			if r.Err == nil {
				if cheapest == 0 {
					cheapest = r.Estimate.TotalUSDMonthly
				}
				if r.Estimate.TotalUSDMonthly > maxTotal {
					maxTotal = r.Estimate.TotalUSDMonthly
				}
			}
		}

		budget := m.cfg.BudgetUSDMonth
		if budget == 0 {
			if f, err := strconv.ParseFloat(strings.TrimSpace(m.textInputs[tiBudget].Value()), 64); err == nil && f > 0 {
				if u, ferr := pricing.FromTaller(f); ferr == nil {
					budget = u
				} else {
					budget = f
				}
			}
		}

		// Clamp selected vendor.
		if m.costVendor >= len(sorted) {
			m.costVendor = len(sorted) - 1
		}
		if m.costVendor < 0 {
			m.costVendor = 0
		}

		// Table header. The provider/region column is 22 chars wide to
		// accommodate "digitalocean nyc3" and similar combined labels.
		hdr := fmt.Sprintf("  %-22s %10s  %s", "provider/region", m.activeCostWindow().short, "bar chart")
		lines = append(lines, stHdr.Render(hdr))

		barW := w - 40 // chars available for bar (2+22+1+10+2 = 37 fixed + margin)
		if barW < 10 {
			barW = 10
		}

		for i, r := range sorted {
			selected := i == m.costVendor
			lines = append(lines, m.renderCostRow(r, selected, cheapest, maxTotal, budget, barW))
		}

		// Detail block for selected vendor.
		lines = append(lines, "")
		if m.costVendor < len(sorted) {
			sel := sorted[m.costVendor]
			detailLabel := sel.ProviderName
			if sel.Region != "" {
				detailLabel = sel.ProviderName + " " + sel.Region
			}
			lines = append(lines, stMuted.Render(fmt.Sprintf(" ─ %s detail ─", detailLabel)))
			if sel.Err != nil {
				lines = append(lines, stBad.Render("  "+sel.Err.Error()))
			} else {
				for _, it := range sel.Estimate.Items {
					name := it.Name
					maxNameW := w - 16
					if maxNameW < 10 {
						maxNameW = 10
					}
					if len(name) > maxNameW {
						name = name[:maxNameW] + "…"
					}
					lineStr := fmt.Sprintf("  %-*s %10s", maxNameW, name, m.formatCost(it.SubtotalUSD))
					lines = append(lines, lineStr)
				}
			}
			if budget > 0 && sel.Err == nil {
				lines = append(lines, "")
				scaledTotal := m.costForPeriod(sel.Estimate.TotalUSDMonthly)
				scaledBudget := m.costForPeriod(budget)
				w := m.activeCostWindow()
				if scaledTotal <= scaledBudget {
					lines = append(lines, stOK.Render(fmt.Sprintf("  ✓ within budget (%s / %s%s)",
						m.formatCost(sel.Estimate.TotalUSDMonthly),
						fmt.Sprintf("$%.0f", scaledBudget), w.short)))
				} else {
					lines = append(lines, stBad.Render(fmt.Sprintf("  ✗ over budget (%s / %s%s)",
						m.formatCost(sel.Estimate.TotalUSDMonthly),
						fmt.Sprintf("$%.0f", scaledBudget), w.short)))
				}
			}
		}
	}

	if !m.costCredsMode {
		// Footer: allow re-entering credential form.
		statusLine := stMuted.Render("  c = edit credentials")
		if m.costCredsStatus != "" {
			if m.costCredsStatus == "saved" {
				statusLine = stOK.Render("  ✓ credentials saved")
			} else {
				statusLine = stWarn.Render("  ⚠ " + m.costCredsStatus)
			}
		}
		// Insert footer before padding.
		lines = append(lines, "", statusLine)
	}

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// renderCostsCredsForm renders the API credential entry form for the costs tab.
func (m dashModel) renderCostsCredsForm() []string {
	var lines []string
	lines = append(lines, stHdr.Render("  API Credentials"))
	lines = append(lines, stMuted.Render("  Enter keys for the providers you want priced. Leave blank to skip (Azure, Linode, OCI use public APIs)."))
	lines = append(lines, stMuted.Render("  tab / shift+tab = move  ·  enter on last field = save  ·  ctrl+s = save"))
	lines = append(lines, "")
	for i := 0; i < ccCount; i++ {
		lbl := fmt.Sprintf("  %-22s", ccLabels[i])
		isSecret := i != ccAWSKeyID
		focused := i == m.costCredsFocus
		var input string
		switch {
		case focused:
			// User is actively typing — EchoPassword masking is acceptable
			// per ADR 0013 §3 (length-leak is ephemeral and user is the secret-knower).
			input = m.costCredsInputs[i].View()
		case isSecret:
			// Unfocused secret: emit status indicator only (ADR 0013 §2).
			if m.costCredsInputs[i].Value() == "" {
				input = stMuted.Render("[ ] not set")
			} else {
				input = stOK.Render("[✓] set")
			}
		default:
			input = m.costCredsInputs[i].View()
		}
		cursor := " "
		if focused {
			cursor = stAccent.Render("▌")
		}
		lines = append(lines, cursor+lbl+" "+input)
	}
	if m.costCredsStatus != "" && m.costCredsStatus != "saved" {
		lines = append(lines, "")
		lines = append(lines, stWarn.Render("  ⚠ "+m.costCredsStatus))
	}
	return lines
}

// renderCostRow renders a single provider+region row with bar chart.
func (m dashModel) renderCostRow(r cost.CloudCost, selected bool, cheapest, maxTotal, budget float64, barW int) string {
	prefix := "  "
	if selected {
		prefix = stAccent.Render("▌ ")
	}

	// Combined provider/region label (max 22 chars).
	label := r.ProviderName
	if r.Region != "" {
		label = r.ProviderName + " " + r.Region
	}
	if len(label) > 22 {
		label = label[:21] + "…"
	}

	if r.Err != nil {
		row := prefix + stMuted.Render(fmt.Sprintf("%-22s  n/a", label))
		return row
	}

	total := r.Estimate.TotalUSDMonthly
	totalStr := fmt.Sprintf("%10s", m.formatCost(total))

	// Budget comparison uses period-scaled values so the budget field
	// (which is always monthly) is also scaled before the check.
	scaledBudget := m.costForPeriod(budget)
	scaledTotal := m.costForPeriod(total)
	var style lipgloss.Style
	switch {
	case scaledBudget > 0 && scaledTotal > scaledBudget:
		style = stBad
	case cheapest > 0 && total <= cheapest:
		style = stOK
	case cheapest > 0 && total > cheapest*1.5:
		style = stWarn
	default:
		style = lipgloss.NewStyle()
	}

	// ASCII bar chart using █.
	barLen := 0
	if maxTotal > 0 {
		barLen = int(float64(barW) * total / maxTotal)
	}
	if barLen < 1 && total > 0 {
		barLen = 1
	}
	bar := strings.Repeat("█", barLen)
	bar = style.Render(bar)

	nameStr := fmt.Sprintf("%-22s", label)
	if selected {
		nameStr = stAccent.Render(nameStr)
		totalStr = style.Bold(true).Render(totalStr)
	} else {
		nameStr = style.Render(nameStr)
		totalStr = style.Render(totalStr)
	}

	return prefix + nameStr + " " + totalStr + "  " + bar
}

// renderVendorRow is kept for backward compatibility (used by bottom strip logic).
func (m dashModel) renderVendorRow(r cost.CloudCost, selected bool, cheapest, budget float64, w int) string {
	prefix := "  "
	if selected {
		prefix = stAccent.Render("▌ ")
	}

	name := fmt.Sprintf("%-12s", r.ProviderName)

	if r.Err != nil {
		row := prefix + stMuted.Render(name) + stMuted.Render(" n/a")
		return row
	}

	total := r.Estimate.TotalUSDMonthly
	totalStr := fmt.Sprintf("$%8.2f", total)

	var badge string
	var style lipgloss.Style
	switch {
	case budget > 0 && total > budget:
		style = stBad
		badge = stBad.Render(" ✗ over")
	case cheapest > 0 && total <= cheapest:
		style = stOK
		badge = stOK.Render(" ✓ low")
	case cheapest > 0 && total > cheapest*1.5:
		style = stWarn
		badge = stWarn.Render(" ▲ hi")
	default:
		style = lipgloss.NewStyle()
		badge = ""
	}

	if selected {
		name = stAccent.Render(fmt.Sprintf("%-12s", r.ProviderName))
		totalStr = style.Bold(true).Render(totalStr)
	} else {
		name = style.Render(fmt.Sprintf("%-12s", r.ProviderName))
		totalStr = style.Render(totalStr)
	}

	return prefix + name + " " + totalStr + badge
}

