// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package logx provides log/warn/die helpers with emoji-prefixed
// output that yage uses for all user-facing console messages.
//
// Output shapes:
//
//	Log  -> "✅ 🎉 <msg>"  (stdout)
//	Warn -> "⚠️ 🙈 <msg>"  (stderr)
//	Die  -> "❌ 💩 <msg>"  (stderr, then os.Exit(1))
package logx

import (
	"fmt"
	"os"
)

// Log prints a success message to stdout.
func Log(format string, a ...any) {
	fmt.Printf("✅ 🎉 %s\n", fmt.Sprintf(format, a...))
}

// Warn prints a warning message to stderr.
func Warn(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "⚠️ 🙈 %s\n", fmt.Sprintf(format, a...))
}

// Err prints an error message to stderr without exiting.
func Err(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "❌ 💩 %s\n", fmt.Sprintf(format, a...))
}

// Die prints an error and exits with status 1.
func Die(format string, a ...any) {
	Err(format, a...)
	os.Exit(1)
}