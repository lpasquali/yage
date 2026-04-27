// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package logx provides log/warn/die helpers with emoji-prefixed
// output that yage uses for all user-facing console messages.
//
// Output shapes (produced by obs.PrettyHandler):
//
//	Log  -> "✅ 🎉 <msg>"  (stdout)
//	Warn -> "⚠️ 🙈 <msg>"  (stderr)
//	Err  -> "❌ 💩 <msg>"  (stderr)
//	Die  -> "❌ 💩 <msg>"  (stderr, then os.Exit(1))
//
// The emoji prefixes are injected by obs.PrettyHandler — callers must not
// add them manually.  All output is routed through obs.Global() so that the
// backend can be swapped without changing call sites.
package logx

import (
	"fmt"
	"os"

	"github.com/lpasquali/yage/internal/obs"
)

// Log prints a success message to stdout.
func Log(format string, a ...any) {
	obs.Global().Info(fmt.Sprintf(format, a...))
}

// Warn prints a warning message to stderr.
func Warn(format string, a ...any) {
	obs.Global().Warn(fmt.Sprintf(format, a...))
}

// Err prints an error message to stderr without exiting.
func Err(format string, a ...any) {
	obs.Global().Error(fmt.Sprintf(format, a...), nil)
}

// Die prints an error and exits with status 1.
func Die(format string, a ...any) {
	obs.Global().Error(fmt.Sprintf(format, a...), nil)
	os.Exit(1)
}
