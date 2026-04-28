// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

//go:build windows

package xapiri

// editorFallbacks is the probe order on Windows when neither $VISUAL nor
// $EDITOR is set. notepad.exe is always present; the others require install.
var editorFallbacks = []string{
	"notepad++",   // popular freeware
	"notepad.exe", // guaranteed on every Windows install
}
