// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// tab_logs.go — updateLogsTab, renderLogsTab.

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// updateLogsTab handles key events on the logs tab (scroll + filter).
func (m dashModel) updateLogsTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When filter input is open, route all keys there except Esc/Enter.
	if m.logFiltering {
		switch msg.Type {
		case tea.KeyEsc:
			m.logFiltering = false
			m.logFilter = ""
			m.logFilterInput.SetValue("")
			m.logFilterInput.Blur()
			return m, nil
		case tea.KeyEnter:
			m.logFiltering = false
			m.logFilter = m.logFilterInput.Value()
			m.logFilterInput.Blur()
			return m, nil
		default:
			var cmd tea.Cmd
			m.logFilterInput, cmd = m.logFilterInput.Update(msg)
			m.logFilter = m.logFilterInput.Value()
			return m, cmd
		}
	}

	key := msg.Type
	keyStr := msg.String()
	switch {
	case keyStr == "/":
		// Open filter bar (vim-style).
		m.logFiltering = true
		m.logFilterInput.SetValue("")
		m.logFilter = ""
		cmd := m.logFilterInput.Focus()
		return m, cmd
	case keyStr == "esc" || key == tea.KeyEsc:
		// Clear active filter.
		m.logFilter = ""
		m.logFilterInput.SetValue("")
	case key == tea.KeyUp:
		m.logScroll++
	case key == tea.KeyDown:
		if m.logScroll > 0 {
			m.logScroll--
		}
	case key == tea.KeyPgUp:
		m.logScroll += 10
	case key == tea.KeyPgDown:
		if m.logScroll > 10 {
			m.logScroll -= 10
		} else {
			m.logScroll = 0
		}
	case keyStr == "g":
		// Top.
		m.logScroll = len(m.logLines)
	case keyStr == "G":
		// Bottom (follow).
		m.logScroll = 0
	case key == tea.KeyCtrlW:
		m.logWrap = !m.logWrap
	}
	return m, nil
}

// ─── logs tab ────────────────────────────────────────────────────────────────

func (m dashModel) renderLogsTab(w, h int) string {
	lines := m.logLines

	// Apply filter (case-insensitive substring).
	if m.logFilter != "" {
		pat := strings.ToLower(m.logFilter)
		var filtered []string
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), pat) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}

	// Header row + optional filter bar + separator.
	showFilterBar := m.logFiltering || m.logFilter != ""
	hdrRows := 2
	if showFilterBar {
		hdrRows = 3
	}
	contentH := h - hdrRows
	if contentH < 1 {
		contentH = 1
	}

	// Apply scroll: logScroll=0 means pinned to bottom.
	total := len(lines)
	end := total - m.logScroll
	if end > total {
		end = total
	}
	if end < 0 {
		end = 0
	}
	start := end - contentH
	if start < 0 {
		start = 0
	}

	var out []string
	hdrText := fmt.Sprintf(" Logs  [%d lines]", total)
	if m.logFilter != "" {
		hdrText += fmt.Sprintf("  filter:%q", m.logFilter)
	}
	if m.logScroll > 0 {
		hdrText += fmt.Sprintf("  scroll↑ %d", m.logScroll)
	} else {
		hdrText += "  (following)"
	}
	if m.logWrap {
		hdrText += "  [wrap]"
	}
	out = append(out, stHdr.Render(hdrText))

	// Filter bar: active input or static hint when a filter is set.
	if showFilterBar {
		if m.logFiltering {
			out = append(out, m.logFilterInput.View())
		} else {
			out = append(out, stMuted.Render(" /"+m.logFilter+"  (/ to edit, esc to clear)"))
		}
	}

	out = append(out, stMuted.Render(strings.Repeat("─", w)))

	colW := w - 2
	if colW < 10 {
		colW = 10
	}
	if total == 0 {
		if m.logFilter != "" {
			out = append(out, stMuted.Render("  no matching lines"))
		} else {
			out = append(out, stMuted.Render("  no log output yet"))
		}
	} else {
		for _, l := range lines[start:end] {
			if m.logWrap && len(l) > colW {
				// Break into colW-wide chunks.
				for len(l) > colW {
					out = append(out, "  "+l[:colW])
					l = l[colW:]
				}
				if len(l) > 0 {
					out = append(out, "  "+l)
				}
			} else {
				if len(l) > colW {
					l = l[:colW] + "…"
				}
				out = append(out, "  "+l)
			}
		}
	}

	for len(out) < h {
		out = append(out, "")
	}
	return strings.Join(out[:min(len(out), h)], "\n")
}
