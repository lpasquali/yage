// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// tab_provision.go — updateProvisionTab, updateCfgEditScreen,
// renderProvisionTab, renderCfgEditScreen.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// updateProvisionTab handles key events on the provision tab (full edit form).
func (m dashModel) updateProvisionTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	return m.updateCfgEditScreen(msg)
}


// updateCfgEditScreen handles key events in the full config edit form.
func (m dashModel) updateCfgEditScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Discard keystrokes while a config entry is loading to prevent input
	// from being silently lost when cfgEntryLoadMsg rebuilds all inputs.
	if m.cfgLoading {
		return m, nil
	}
	key := msg.Type
	keyStr := msg.String()

	switch {
	case keyStr == "ctrl+l":
		m.activeTab = tabLogs
		return m, nil

	case key == tea.KeyUp:
		m = m.moveFocus(false)
		return m, textinput.Blink

	case key == tea.KeyDown:
		m = m.moveFocus(true)
		return m, textinput.Blink
	}

	// Per-field-kind handling.
	meta := dashFields[m.focus]
	switch meta.kind {
	case fkToggle:
		if key == tea.KeySpace || key == tea.KeyEnter {
			m.toggles[meta.subIdx] = !m.toggles[meta.subIdx]
			if meta.costKey {
				return m, m.markDirty()
			}
		}

	case fkSelect:
		switch {
		case key == tea.KeyRight || key == tea.KeyEnter || keyStr == "l":
			m.selects[meta.subIdx].next()
			if meta.subIdx == siMode {
				m = m.rebuildProviderList()
			}
			if meta.costKey {
				return m, m.markDirty()
			}
		case key == tea.KeyLeft || keyStr == "h":
			m.selects[meta.subIdx].prev()
			if meta.subIdx == siMode {
				m = m.rebuildProviderList()
			}
			if meta.costKey {
				return m, m.markDirty()
			}
		}

	case fkText:
		if key == tea.KeyTab || key == tea.KeyShiftTab {
			return m, nil
		}
		ti, cmd := m.textInputs[meta.subIdx].Update(msg)
		m.textInputs[meta.subIdx] = ti
		if meta.costKey {
			return m, tea.Batch(cmd, m.markDirty())
		}
		return m, cmd
	}

	return m, nil
}



// renderProvisionTab renders the full interactive provision edit form.
func (m dashModel) renderProvisionTab(w, h int) string {
	return m.renderCfgEditScreen(w, h)
}



// renderCfgEditScreen renders the full interactive config edit form.
func (m dashModel) renderCfgEditScreen(w, h int) string {
	var lines []string
	lastSection := ""

	// Config name badge at the top.
	if name := m.cfg.ConfigName; name != "" {
		lines = append(lines, stMuted.Render(fmt.Sprintf("  config: %s", name)))
		lines = append(lines, "")
	}

	for fid := 0; fid < focCount; fid++ {
		if m.isHidden(fid) {
			continue
		}
		meta := dashFields[fid]
		// Section header.
		if meta.section != "" && meta.section != lastSection {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, stHdr.Render(" "+meta.section))
			lastSection = meta.section
		}

		focused := m.focus == fid
		lines = append(lines, m.renderField(fid, focused, w))
	}

	// Pad to height.
	for len(lines) < h {
		lines = append(lines, "")
	}

	return strings.Join(lines[:min(len(lines), h)], "\n")
}

