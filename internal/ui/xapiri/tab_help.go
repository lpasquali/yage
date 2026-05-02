// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// tab_help.go — renderHelpTab.

import (
	"strings"
)

// ─── help tab ────────────────────────────────────────────────────────────────

func (m dashModel) renderHelpTab(w, h int) string {
	lines := []string{
		stHdr.Render(" Keyboard shortcuts"),
		stMuted.Render(strings.Repeat("─", w)),
		"",
		stBold.Render("  Tab switching"),
		"  " + stAccent.Render("1") + "                config (always)  " + stAccent.Render("2-8") + " other tabs (after config selected)",
		"  " + stAccent.Render("ctrl+← →") + "          cycle tabs (works from any context)",
		"  " + stAccent.Render("← →") + "               cycle tabs (when not in text field)",
		"",
		stBold.Render("  Config tab  (config selection)"),
		"  " + stAccent.Render("↑ ↓") + "           navigate list",
		"  " + stAccent.Render("enter") + "           select config (→ provision tab)",
		"  " + stAccent.Render("n") + "               new config",
		"  " + stAccent.Render("r") + "               refresh list",
		"",
		stBold.Render("  Provision tab  (edit form)"),
		"  " + stAccent.Render("↑ ↓") + "                    move between fields",
		"  " + stAccent.Render("space / enter") + "          toggle booleans",
		"  " + stAccent.Render("← →") + "                    cycle select options",
		"  " + stAccent.Render("ctrl+l") + "                 switch to logs tab",
		"",
		stBold.Render("  Costs tab"),
		"  " + stAccent.Render("↑ ↓") + "              scroll vendor list",
		"  " + stAccent.Render("[ / ]") + "            previous / next time window",
		"  " + stAccent.Render("c") + "                edit API credentials",
		"  " + stAccent.Render("tab / shift+tab") + "  move between credential fields",
		"  " + stAccent.Render("enter") + "            advance / save credentials",
		"",
		stBold.Render("  Deploy tab"),
		"  " + stAccent.Render("tab / ↑↓") + "         cycle between buttons",
		"  " + stAccent.Render("enter") + "            activate focused button",
		"",
		stBold.Render("  Logs tab"),
		"  " + stAccent.Render("↑ ↓") + "              scroll down / up",
		"  " + stAccent.Render("PgUp / PgDn") + "      scroll by 10 lines",
		"  " + stAccent.Render("g / G") + "            jump to top / bottom (follow)",
		"  " + stAccent.Render("/") + "                filter (vim-style, esc to clear)",
		"  " + stAccent.Render("ctrl+w") + "           toggle line-wrap",
		"",
		stBold.Render("  Global (any tab)"),
		"  " + stAccent.Render("ctrl+t") + "           open/focus embedded terminal pane (esc or ctrl+t = unfocus)",
		"  " + stAccent.Render("ctrl+alt+↑/↓") + "     resize terminal pane",
		"  " + stAccent.Render("ctrl+s") + "           save config and continue",
		"  " + stAccent.Render("esc / q") + "          abort (no changes written)",
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}
