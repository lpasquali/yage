// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard_overlays.go — full-screen overlay renderers that draw on top of
// normal tab content. Currently only the token re-prompt overlay.

import (
	"fmt"
	"strings"
)

// renderTokenPromptOverlay renders a focused single-field prompt asking for
// the Proxmox admin token. Shown after a profile load when AdminUsername is
// set but AdminToken is empty (token was intentionally excluded from the kind
// Secret).
func (m dashModel) renderTokenPromptOverlay(w int) string {
	var lines []string
	lines = append(lines, stHdr.Render(" Proxmox admin token required"))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  Profile loaded for admin user: %s", stAccent.Render(m.cfg.Providers.Proxmox.AdminUsername)))
	lines = append(lines, "")
	lines = append(lines, stMuted.Render("  The admin token is not saved to the kind Secret (security policy)."))
	lines = append(lines, stMuted.Render("  Enter it now for this session, or press esc to skip."))
	lines = append(lines, "")
	lines = append(lines, "  "+m.tokenPromptInput.View())
	lines = append(lines, "")
	lines = append(lines, stMuted.Render("  enter  confirm    esc  skip"))
	return strings.Join(lines, "\n")
}

