// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// tab_about.go — renderAboutTab.

import (
	"runtime/debug"
	"strings"
)

// ─── about tab ───────────────────────────────────────────────────────────────

func (m dashModel) renderAboutTab(w, h int) string {
	version := "unknown"
	commit := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		version = info.Main.Version
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				commit = s.Value[:7]
			}
		}
	}

	lines := []string{
		stHdr.Render(" About yage xapiri"),
		stMuted.Render(strings.Repeat("─", w)),
		"",
		"  " + stBold.Render("yage") + "  — Yet Another GitOps Engine",
		"",
		"  " + stMuted.Render("version:") + "  " + stAccent.Render(version),
		"  " + stMuted.Render("commit: ") + "  " + stAccent.Render(commit),
		"",
		"  " + stMuted.Render("license:") + "  Apache-2.0",
		"  " + stMuted.Render("project:") + "  https://github.com/lpasquali/yage",
		"",
		stMuted.Render("  xapiri are sacred spirits in the Yanomami people's cosmology."),
		stMuted.Render("  yage runs xapiri to get help from the spirits to create a"),
		stMuted.Render("  visionary deployment."),
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}
