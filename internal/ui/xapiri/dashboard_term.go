// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard_term.go — embedded terminal pane helpers.
//
// watchPTYCmd, processTermBytes, keyMsgToBytes, stripNonSGR, termRawToLines,
// and renderTermPane are kept together because they form one coherent PTY
// read/render pipeline. Splitting them would obscure the data-flow.

import (
	"bytes"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ─── embedded terminal helpers ───────────────────────────────────────────────

const termPaneHDefault = 12 // initial terminal pane height; user can resize with ctrl+alt+↑/↓
const termPaneHMin = 4

// watchPTYCmd reads one chunk from the PTY master fd and returns a ptyOutputMsg.
// Re-scheduled after every ptyOutputMsg while termRunning is true.
func (m dashModel) watchPTYCmd() tea.Cmd {
	f := m.termPTY
	return func() tea.Msg {
		buf := make([]byte, 4096)
		n, err := f.Read(buf)
		if n > 0 {
			return ptyOutputMsg{data: buf[:n]}
		}
		if err != nil {
			return ptyExitMsg{err: err}
		}
		return ptyOutputMsg{}
	}
}

// processTermBytes appends raw PTY output to the ring buffer.
func (m *dashModel) processTermBytes(data []byte) {
	const maxRaw = 64 * 1024
	m.termRaw = append(m.termRaw, data...)
	if len(m.termRaw) > maxRaw {
		m.termRaw = m.termRaw[len(m.termRaw)-maxRaw:]
	}
}

// keyMsgToBytes converts a bubbletea key message to the raw bytes sent to the PTY.
func keyMsgToBytes(msg tea.KeyMsg) []byte {
	if msg.Type == tea.KeyRunes {
		if msg.Alt {
			b := []byte{0x1b}
			return append(b, []byte(string(msg.Runes))...)
		}
		return []byte(string(msg.Runes))
	}
	switch msg.Type {
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyDelete:
		return []byte{'\x1b', '[', '3', '~'}
	case tea.KeyUp:
		return []byte{'\x1b', '[', 'A'}
	case tea.KeyDown:
		return []byte{'\x1b', '[', 'B'}
	case tea.KeyRight:
		return []byte{'\x1b', '[', 'C'}
	case tea.KeyLeft:
		return []byte{'\x1b', '[', 'D'}
	case tea.KeyHome:
		return []byte{'\x1b', '[', 'H'}
	case tea.KeyEnd:
		return []byte{'\x1b', '[', 'F'}
	case tea.KeyPgUp:
		return []byte{'\x1b', '[', '5', '~'}
	case tea.KeyPgDown:
		return []byte{'\x1b', '[', '6', '~'}
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyCtrlA:
		return []byte{1}
	case tea.KeyCtrlB:
		return []byte{2}
	case tea.KeyCtrlC:
		return []byte{3}
	case tea.KeyCtrlD:
		return []byte{4}
	case tea.KeyCtrlE:
		return []byte{5}
	case tea.KeyCtrlF:
		return []byte{6}
	case tea.KeyCtrlK:
		return []byte{11}
	case tea.KeyCtrlL:
		return []byte{12}
	case tea.KeyCtrlN:
		return []byte{14}
	case tea.KeyCtrlP:
		return []byte{16}
	case tea.KeyCtrlR:
		return []byte{18}
	case tea.KeyCtrlU:
		return []byte{21}
	case tea.KeyCtrlW:
		return []byte{23}
	case tea.KeyCtrlZ:
		return []byte{26}
	default:
		return nil
	}
}

// stripNonSGR strips all ANSI escape sequences from data except SGR (color/style,
// ending in 'm'). This makes PTY output safe for lipgloss rendering while preserving
// colours.
func stripNonSGR(data []byte) string {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		b := data[i]
		if b != 0x1b {
			if b >= 0x20 || b == '\t' {
				out = append(out, b)
			}
			i++
			continue
		}
		i++
		if i >= len(data) {
			break
		}
		switch data[i] {
		case '[': // CSI
			i++
			start := i
			for i < len(data) && !(data[i] >= 0x40 && data[i] <= 0x7e) {
				i++
			}
			if i < len(data) {
				final := data[i]
				i++
				if final == 'm' {
					out = append(out, '\x1b', '[')
					out = append(out, data[start:i]...)
				}
			}
		case ']': // OSC — skip until BEL or ST
			i++
			for i < len(data) {
				if data[i] == 0x07 {
					i++
					break
				}
				if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default:
			i++ // skip ESC + one byte
		}
	}
	return string(out)
}

// termRawToLines converts the PTY raw buffer to display-ready lines.
// It splits by \n, strips trailing \r (from \r\n sequences), handles
// progress-bar overwrites (last segment after mid-line \r), and strips
// non-SGR ANSI sequences.
func (m dashModel) termRawToLines(maxLines int) []string {
	if len(m.termRaw) == 0 {
		return nil
	}
	parts := bytes.Split(m.termRaw, []byte{'\n'})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		for len(part) > 0 && part[len(part)-1] == '\r' {
			part = part[:len(part)-1]
		}
		if idx := bytes.LastIndexByte(part, '\r'); idx >= 0 {
			part = part[idx+1:]
		}
		result = append(result, stripNonSGR(part))
	}
	// Trim trailing empty lines.
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	if len(result) > maxLines {
		return result[len(result)-maxLines:]
	}
	return result
}

// renderTermPane renders the embedded terminal pane (always below tab content
// when the terminal is running).  Returns "" when not running.
func (m dashModel) renderTermPane(w int) string {
	if !m.termRunning {
		return ""
	}
	var lines []string
	sep := stMuted.Render(strings.Repeat("─", w))
	if m.termFocused {
		lines = append(lines, sep)
		lines = append(lines, stAccent.Render("▸ Terminal")+stMuted.Render("  esc=unfocus  ctrl+alt+↑/↓=resize"))
	} else {
		lines = append(lines, sep)
		lines = append(lines, stMuted.Render("  Terminal")+stMuted.Render("  ctrl+t=focus  ctrl+alt+↑/↓=resize"))
	}
	contentH := m.termH - 2
	for _, l := range m.termRawToLines(contentH) {
		lines = append(lines, "  "+l)
	}
	for len(lines) < m.termH {
		lines = append(lines, "")
	}
	return strings.Join(lines[:m.termH], "\n")
}
