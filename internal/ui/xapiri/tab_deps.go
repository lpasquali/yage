// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// tab_deps.go — updateDepsTab, renderDepsTab.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lpasquali/yage/internal/platform/installer"
)

// ─── deps tab ────────────────────────────────────────────────────────────────

// updateDepsTab handles key events on the deps tab.
func (m dashModel) updateDepsTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	switch {
	case key == tea.KeyTab || key == tea.KeyDown:
		m.depsFocused = (m.depsFocused + 1) % 2
	case key == tea.KeyShiftTab || key == tea.KeyUp:
		m.depsFocused = (m.depsFocused - 1 + 2) % 2
	case key == tea.KeyEnter || key == tea.KeySpace:
		if m.depsRunning {
			return m, nil
		}
		cfg := m.cfg
		switch m.depsFocused {
		case 0: // check button
			m.depsRunning = true
			m.depsStatus = "checking…"
			return m, func() tea.Msg {
				return depsCheckMsg{
					tools:  installer.CheckDeps(cfg),
					images: installer.CheckProviderImages(cfg),
				}
			}
		case 1: // upgrade button
			m.depsRunning = true
			m.depsStatus = "upgrading…"
			return m, func() tea.Msg {
				return depsUpgradeMsg{err: installer.UpgradeDeps(cfg)}
			}
		}
	}
	return m, nil
}

func (m dashModel) renderDepsTab(w, h int) string {
	var lines []string
	lines = append(lines, stHdr.Render(" Dependencies"))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))
	lines = append(lines, "")

	// buttons
	btnCheck := "[Check]"
	btnUpgrade := "[Upgrade All]"
	if m.depsFocused == 0 {
		btnCheck = stAccent.Render("▸ " + btnCheck)
		btnUpgrade = stMuted.Render("  " + btnUpgrade)
	} else {
		btnCheck = stMuted.Render("  " + btnCheck)
		btnUpgrade = stAccent.Render("▸ " + btnUpgrade)
	}
	lines = append(lines, btnCheck+"  "+btnUpgrade)
	lines = append(lines, "")

	// status line
	if m.depsStatus != "" {
		lines = append(lines, stMuted.Render("  "+m.depsStatus))
		lines = append(lines, "")
	}

	if len(m.depsTools) > 0 {
		lines = append(lines, stBold.Render("  CLI tools"))
		for _, t := range m.depsTools {
			var badge string
			switch {
			case t.Skip:
				badge = stMuted.Render("  ◦")
			case t.OK:
				badge = stOK.Render("  ✓")
			default:
				badge = stBad.Render("  ✗")
			}
			have := t.Have
			if len(have) > 20 {
				have = have[:20]
			}
			line := fmt.Sprintf("%s %-12s  have: %-20s  want: %s", badge, t.Name, have, t.Want)
			lines = append(lines, line)
		}
		lines = append(lines, "")
	}

	if len(m.depsImages) > 0 {
		provLabel := m.cfg.InfraProvider
		if provLabel == "" {
			provLabel = "provider"
		}
		lines = append(lines, stBold.Render("  "+provLabel+" images"))
		archOK := func(ok bool) string {
			if ok {
				return stOK.Render("✓")
			}
			return stBad.Render("✗")
		}
		for _, img := range m.depsImages {
			ref := img.Image
			if len(ref) > 45 {
				ref = "…" + ref[len(ref)-44:]
			}
			var archStr string
			if img.Err {
				archStr = stWarn.Render("  ? [amd64:?] [arm64:?]")
			} else {
				archStr = fmt.Sprintf("  [amd64:%s] [arm64:%s]", archOK(img.Amd64), archOK(img.Arm64))
			}
			lines = append(lines, fmt.Sprintf("  %-18s %s  %s", img.Name, archStr, stMuted.Render(ref)))
		}
		lines = append(lines, "")
	}

	if len(m.depsTools) == 0 && len(m.depsImages) == 0 && m.depsStatus == "" {
		lines = append(lines, stMuted.Render("  Press [Check] to scan installed CLI versions and provider image availability."))
	}

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}
