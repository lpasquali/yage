// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard_chrome.go — chrome renderers: tab bar, sysinfo widget, bottom
// cost strip, footer key hints, and related helpers.

import (
	"fmt"
	"strings"

	lipgloss "github.com/charmbracelet/lipgloss"
)

// renderTabBar renders the tab strip at the top with a right-aligned sysinfo widget.
func (m dashModel) renderTabBar() string {
	var parts []string
	for i := dashTab(0); i < tabCount; i++ {
		label := "[" + tabLabels[i] + "]"
		if i == m.activeTab {
			parts = append(parts, lipgloss.NewStyle().Bold(true).Underline(true).Foreground(colAccent).Render(label))
		} else {
			parts = append(parts, stMuted.Render(label))
		}
	}
	bar := strings.Join(parts, "  ")
	title := stBold.Render("yage") + "  "
	left := title + bar

	// Right-align the sysinfo widget; pad between left and widget.
	widget := m.renderSysWidget()
	// Strip ANSI for width math (widget uses lipgloss styles).
	wPlain := lipgloss.Width(widget)
	lPlain := lipgloss.Width(left)
	pad := m.width - lPlain - wPlain
	if pad < 1 {
		pad = 1
	}
	line := left + strings.Repeat(" ", pad) + widget
	return lipgloss.NewStyle().Width(m.width).Render(line) + "\n" +
		stMuted.Render(strings.Repeat("─", m.width))
}

// renderSysWidget returns a compact right-corner widget:
//
//	cpu 3%  128M  ↓1.2M ↑400B
func (m dashModel) renderSysWidget() string {
	s := m.sysStats
	if s.MemRSSBytes == 0 {
		return stMuted.Render("cpu –  – M  ↓–  ↑–")
	}

	// CPU colour: green < 20%, yellow < 60%, red ≥ 60%.
	cpuStr := fmt.Sprintf("%.0f%%", s.CPUPercent)
	var cpuStyle lipgloss.Style
	switch {
	case s.CPUPercent < 20:
		cpuStyle = stOK
	case s.CPUPercent < 60:
		cpuStyle = stWarn
	default:
		cpuStyle = stBad
	}

	// Memory: colour by absolute size.
	memStr := fmtBytes(s.MemRSSBytes)
	var memStyle lipgloss.Style
	switch {
	case s.MemRSSBytes < 256<<20: // < 256 MB
		memStyle = stOK
	case s.MemRSSBytes < 512<<20: // < 512 MB
		memStyle = stWarn
	default:
		memStyle = stBad
	}

	// Network: per-second rate; muted when idle, accent when data flowing.
	var rxRate, txRate uint64
	if s.DeltaDur > 0 {
		secs := s.DeltaDur.Seconds()
		rxRate = uint64(float64(s.NetRxDelta) / secs)
		txRate = uint64(float64(s.NetTxDelta) / secs)
	}
	netStyle := stMuted
	if rxRate > 1024 || txRate > 1024 {
		netStyle = stAccent
	}

	return stMuted.Render("cpu ") + cpuStyle.Render(cpuStr) +
		stMuted.Render("  ") + memStyle.Render(memStr) +
		stMuted.Render("  ↓") + netStyle.Render(fmtRate(rxRate)) +
		stMuted.Render(" ↑") + netStyle.Render(fmtRate(txRate))
}

// fmtBytes formats a byte count compactly: "128M", "4.2G", "512K", "64B".
func fmtBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// fmtRate formats bytes/s compactly: "1.2M/s", "400K/s", "64B/s", "0".
func fmtRate(bps uint64) string {
	if bps == 0 {
		return "0"
	}
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1fM/s", float64(bps)/float64(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0fK/s", float64(bps)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB/s", bps)
	}
}

// tabAtX returns the dashTab at visible column x in the tab bar title row (Y==0).
// The layout is: "yage  " (6 visible chars) then "[label]  " per tab
// separated by "  ". Returns (tab, true) when x falls inside a tab label.
func tabAtX(x int) (dashTab, bool) {
	col := len("yage  ") // 6 visible chars prefix
	for i := dashTab(0); i < tabCount; i++ {
		w := len(tabLabels[i]) + 2 // "[" + label + "]"
		if x >= col && x < col+w {
			return i, true
		}
		col += w + 2 // "  " separator between tabs
	}
	return 0, false
}

// renderBottomStrip renders the live cost tally always visible below tab content.
func (m dashModel) renderBottomStrip() string {
	line := stMuted.Render(strings.Repeat("─", m.width)) + "\n"
	if !m.cfg.CostCompareEnabled {
		return line + stMuted.Render("  cost estimation: go to [costs] tab (4) to enter API credentials")
	}
	if len(m.costRows) == 0 {
		var suffix string
		if m.costLoading {
			suffix = stMuted.Render("  fetching…")
		} else {
			suffix = stMuted.Render("  no cost data")
		}
		return line + suffix
	}
	sorted := m.sortedCostRows()

	// Find cheapest once.
	cheapest := 0.0
	for _, rr := range sorted {
		if rr.Err == nil && rr.Estimate.TotalUSDMonthly > 0 {
			cheapest = rr.Estimate.TotalUSDMonthly
			break
		}
	}
	budget := m.cfg.BudgetUSDMonth

	// Build fixed suffix first so we know how much room is left.
	suffix := ""
	if m.costLoading {
		suffix = "  (fetching…)"
	}
	if m.termRunning {
		if m.termFocused {
			suffix += "  [term:active]"
		} else {
			suffix += "  [term:bg]"
		}
	}

	// Greedy-fit: add provider tokens one by one until they no longer fit.
	// Each token is "  provider $NNN". Width budget = terminal width - 2
	// (leading indent) - suffix width. When a token doesn't fit we stop and
	// show "+N more" so the bar is always exactly one line.
	avail := m.width
	if avail <= 0 {
		avail = 120 // safe default before WindowSizeMsg
	}
	avail -= 2 // leading "  "
	avail -= len(suffix)

	var kept []string
	skipped := 0
	sep := "  "
	for _, r := range sorted {
		var token string
		label := r.ProviderName
		if r.Region != "" {
			label = r.ProviderName + "/" + r.Region
		}
		if r.Err != nil {
			token = label + " n/a"
		} else {
			token = label + " " + m.formatCost(r.Estimate.TotalUSDMonthly)
		}
		need := len(token)
		if len(kept) > 0 {
			need += len(sep)
		}
		if need > avail {
			skipped++
			continue
		}
		avail -= need
		kept = append(kept, token)
	}

	// Render kept tokens with colour.
	var renderedParts []string
	for _, tok := range kept {
		name := strings.Fields(tok)[0]
		// Find the original row to apply colour.
		var style lipgloss.Style
		for _, r := range sorted {
			if r.ProviderName != name {
				continue
			}
			if r.Err != nil {
				style = stMuted
			} else {
				total := r.Estimate.TotalUSDMonthly
				scaledTotal := m.costForPeriod(total)
				scaledBudget := m.costForPeriod(budget)
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
			}
			break
		}
		renderedParts = append(renderedParts, style.Render(tok))
	}

	content := "  " + strings.Join(renderedParts, stMuted.Render(sep))
	if skipped > 0 {
		content += stMuted.Render(fmt.Sprintf("  +%d more", skipped))
	}
	if suffix != "" {
		content += stMuted.Render(suffix)
	}
	return line + content
}

func (m dashModel) renderFooter() string {
	shellHint := stMuted.Render("ctrl+t") + " terminal  "
	tabHint := stMuted.Render("1-8/ctrl+◄►") + " tabs  "
	var keys string
	switch m.activeTab {
	case tabConfig:
		switch m.cfgScreen {
		case cfgScreenList:
			keys = stMuted.Render("↑/↓") + " navigate  " +
				stMuted.Render("enter") + " select  " +
				stMuted.Render("n") + " new  " +
				stMuted.Render("r") + " refresh  " +
				stMuted.Render("esc/q") + " quit"
		case cfgScreenNewName:
			keys = stMuted.Render("enter") + " confirm  " +
				stMuted.Render("esc") + " back to list  " +
				stMuted.Render("esc/q") + " quit"
		default:
			keys = stMuted.Render("esc/q") + " quit"
		}
	case tabProvision:
		keys = stMuted.Render("tab/⇧tab") + " navigate  " +
			stMuted.Render("space") + " toggle  " +
			stMuted.Render("◄ ►") + " select  " +
			shellHint +
			stMuted.Render("ctrl+l") + " logs  " +
			tabHint +
			stAccent.Render("ctrl+s") + " save  " +
			stMuted.Render("esc/q") + " abort"
	case tabLogs:
		keys = stMuted.Render("j/k") + " scroll  " +
			stMuted.Render("g/G") + " top/bottom  " +
			shellHint + tabHint +
			stAccent.Render("ctrl+s") + " save  " +
			stMuted.Render("esc/q") + " abort"
	case tabCosts:
		if m.costCredsMode {
			keys = stMuted.Render("tab/⇧tab") + " navigate  " +
				stMuted.Render("enter") + " save (last field)  " +
				shellHint + tabHint +
				stAccent.Render("ctrl+s") + " save+exit  " +
				stMuted.Render("esc/q") + " abort"
		} else {
			keys = stMuted.Render("j/k") + " scroll  " +
				stMuted.Render("[/]") + " time window  " +
				stMuted.Render("c") + " edit creds  " +
				shellHint + tabHint +
				stAccent.Render("ctrl+s") + " save  " +
				stMuted.Render("esc/q") + " abort"
		}
	case tabDeploy:
		keys = stMuted.Render("tab") + " focus  " +
			stMuted.Render("enter") + " activate  " +
			shellHint + tabHint +
			stAccent.Render("ctrl+s") + " save  " +
			stMuted.Render("esc/q") + " abort"
	case tabDeps:
		keys = stMuted.Render("tab") + " focus  " +
			stMuted.Render("enter") + " activate  " +
			shellHint + tabHint +
			stMuted.Render("esc/q") + " abort"
	default:
		keys = shellHint + tabHint +
			stAccent.Render("ctrl+s") + " save  " +
			stMuted.Render("esc/q") + " abort"
	}
	sep := stMuted.Render(strings.Repeat("─", m.width))
	if m.errMsg != "" {
		return sep + "\n" + stBad.Render("  "+m.errMsg) + "\n" + keys
	}
	return sep + "\n" + keys
}

