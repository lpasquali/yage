// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

//go:build !windows

package xapiri

// editorFallbacks is the probe order when neither $VISUAL nor $EDITOR is set.
// Each entry is tried with exec.LookPath; the first hit wins.
var editorFallbacks = []string{
	"nano", // friendly, present on most modern Linux/macOS
	"vim",
	"vi",    // POSIX-mandatory on any real Unix
	"ed",    // last resort: always present on POSIX
}
