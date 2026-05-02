// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// tab_deploy.go — updateDeployTab, renderDeployTab.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
)

// updateDeployTab handles key events on the deploy tab.
func (m dashModel) updateDeployTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	switch {
	case key == tea.KeyTab || key == tea.KeyDown:
		m.deployFocused = (m.deployFocused + 1) % 2
	case key == tea.KeyShiftTab || key == tea.KeyUp:
		m.deployFocused = (m.deployFocused - 1 + 2) % 2
	case key == tea.KeyEnter || key == tea.KeySpace:
		switch m.deployFocused {
		case 0:
			if !m.saveKindLoading {
				m.flushToCfg()
				m.saveKindLoading = true
				m.saveKindDone = false
				m.saveKindErr = nil
				cfg := m.cfg
				return m, func() tea.Msg {
					return saveKindMsg{err: kindsync.WriteBootstrapConfigSecret(cfg)}
				}
			}
		case 1:
			m.flushToCfg()
			m.deployRequested = true
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// ─── deploy tab ──────────────────────────────────────────────────────────────

func (m dashModel) renderDeployTab(w, h int) string {
	var lines []string
	lines = append(lines, stHdr.Render(" Actions"))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))
	lines = append(lines, "")

	btnSave := "[Save to Kind]"
	btnDeploy := "[Start Deploy]"
	if m.deployFocused == 0 {
		btnSave = stAccent.Render("▸ " + btnSave)
		btnDeploy = stMuted.Render("  " + btnDeploy)
	} else {
		btnSave = stMuted.Render("  " + btnSave)
		btnDeploy = stAccent.Render("▸ " + btnDeploy)
	}
	lines = append(lines, btnSave)
	lines = append(lines, "")
	lines = append(lines, btnDeploy)
	lines = append(lines, "")

	var statusLine string
	switch {
	case m.saveKindLoading:
		statusLine = stWarn.Render("  ● saving…")
	case m.saveKindDone && m.saveKindErr != nil:
		statusLine = stBad.Render("  ✗ " + m.saveKindErr.Error())
	case m.saveKindDone:
		statusLine = stOK.Render("  ✓ saved to kind")
	default:
		statusLine = stMuted.Render("  ● Ready")
	}
	lines = append(lines, "Status: "+statusLine)

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}
