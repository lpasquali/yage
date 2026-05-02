// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// tab_config.go — updateConfigTab, updateCfgListScreen, updateCfgNewNameScreen,
// renderConfigTab, renderCfgListScreen, renderCfgNewNameScreen.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// updateConfigTab dispatches key events to the active config sub-screen.
func (m dashModel) updateConfigTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.cfgScreen {
	case cfgScreenList:
		return m.updateCfgListScreen(msg)
	case cfgScreenNewName:
		return m.updateCfgNewNameScreen(msg)
	}
	return m, nil
}

// updateCfgListScreen handles keys on the config-list screen.
func (m dashModel) updateCfgListScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.cfgLoading {
		return m, nil
	}
	key := msg.Type
	keyStr := msg.String()
	total := len(m.cfgCandidates) + 1 // +1 for the "[ + New config ]" sentinel
	switch {
	case key == tea.KeyUp:
		if m.cfgListCursor > 0 {
			m.cfgListCursor--
		}
	case key == tea.KeyDown:
		if m.cfgListCursor < total-1 {
			m.cfgListCursor++
		}
	case key == tea.KeyEnter:
		if m.cfgListCursor == len(m.cfgCandidates) {
			// New config sentinel selected.
			m.cfgScreen = cfgScreenNewName
			m.cfgNewInput.SetValue("")
			cmd := m.cfgNewInput.Focus()
			return m, cmd
		}
		c := m.cfgCandidates[m.cfgListCursor]
		m.cfgLoading = true
		m.cfgLoadErr = ""
		return m, m.loadCfgEntryCmd(c)
	case keyStr == "n":
		m.cfgScreen = cfgScreenNewName
		m.cfgNewInput.SetValue("")
		cmd := m.cfgNewInput.Focus()
		return m, cmd
	case keyStr == "r":
		m.cfgLoading = true
		m.cfgLoadErr = ""
		m.cfgCandidates = nil
		return m, m.loadCfgListCmd()
	}
	return m, nil
}

// updateCfgNewNameScreen handles keys while entering a new config name.
func (m dashModel) updateCfgNewNameScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	switch {
	case key == tea.KeyEsc:
		m.cfgScreen = cfgScreenList
		m.cfgNewInput.Blur()
		return m, nil
	case key == tea.KeyEnter:
		name := strings.TrimSpace(m.cfgNewInput.Value())
		m.cfgNewInput.Blur()
		if name != "" {
			m.cfg.ConfigName = name
			m.cfg.ConfigNameExplicit = true
		}
		m.cfgSelected = true
		m.activeTab = tabProvision
		return m, textinput.Blink
	default:
		var cmd tea.Cmd
		m.cfgNewInput, cmd = m.cfgNewInput.Update(msg)
		return m, cmd
	}
}

// ─── config tab ──────────────────────────────────────────────────────────────


// renderConfigTab dispatches to the active config sub-screen renderer.
func (m dashModel) renderConfigTab(w, h int) string {
	switch m.cfgScreen {
	case cfgScreenNewName:
		return m.renderCfgNewNameScreen(w, h)
	default:
		return m.renderCfgListScreen(w, h)
	}
}

// renderCfgListScreen renders the list of existing bootstrap configs.
func (m dashModel) renderCfgListScreen(w, h int) string {
	var lines []string
	title := fmt.Sprintf(" Configurations on kind cluster %q", m.cfg.KindClusterName)
	lines = append(lines, stHdr.Render(title))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))
	lines = append(lines, "")

	if m.cfgLoading {
		lines = append(lines, stMuted.Render("  loading…"))
	} else if m.cfgLoadErr != "" {
		lines = append(lines, stBad.Render("  "+m.cfgLoadErr))
		lines = append(lines, stMuted.Render("  r = retry"))
	} else {
		for i, c := range m.cfgCandidates {
			cursor := "  "
			if i == m.cfgListCursor {
				cursor = stAccent.Render("▸ ")
			}
			lines = append(lines, cursor+c.Label())
		}
		lines = append(lines, "")
		// "[ + New config ]" sentinel.
		sentinelIdx := len(m.cfgCandidates)
		newLabel := "[ + New config ]"
		if m.cfgListCursor == sentinelIdx {
			lines = append(lines, stAccent.Render("▸ ")+stAccent.Render(newLabel))
		} else {
			lines = append(lines, "  "+stMuted.Render(newLabel))
		}
	}

	lines = append(lines, "")
	if m.cfgLoading {
		lines = append(lines, stMuted.Render("  please wait…"))
	} else {
		lines = append(lines, stMuted.Render("  ↑/↓  navigate    enter  select    n  new config    r  refresh    esc/q  quit"))
	}
	lines = append(lines, "")
	if m.cfgSelected {
		lines = append(lines, stOK.Render("  config: "+m.cfg.ConfigName+"  — select another to switch, or press 2 to return to provision"))
	} else {
		lines = append(lines, stWarn.Render("  ⚠ Select a configuration to unlock all tabs"))
	}

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// renderCfgNewNameScreen renders the new config name input.
func (m dashModel) renderCfgNewNameScreen(w, h int) string {
	var lines []string
	lines = append(lines, stHdr.Render(" New configuration name"))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))
	lines = append(lines, "")
	lines = append(lines, stMuted.Render("  Enter a name for the new configuration."))
	lines = append(lines, stMuted.Render("  A dedicated namespace yage-<name> will be created on the kind cluster."))
	lines = append(lines, "  (leave blank to use the workload cluster name as the default)")
	lines = append(lines, "")
	lines = append(lines, "  "+m.cfgNewInput.View())
	lines = append(lines, "")
	lines = append(lines, stMuted.Render("  enter  confirm    esc  back to list"))

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}
