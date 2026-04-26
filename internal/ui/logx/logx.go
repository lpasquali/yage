// Package logx mirrors the bash log/warn/die helpers from the original bash port.
//
// Bash equivalents:
//
//	log()  { printf '✅ 🎉 %s\n' "$*"; }
//	warn() { printf '⚠️ 🙈 %s\n' "$*" >&2; }
//	die()  { printf '❌ 💩 %s\n' "$*" >&2; exit 1; }
//	err is used in some helpers as "printf '❌ ... %s\n' "$*" >&2" without exit.
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
// Matches the ad-hoc `err` usage scattered in the bash script.
func Err(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "❌ 💩 %s\n", fmt.Sprintf(format, a...))
}

// Die prints an error and exits with status 1.
func Die(format string, a ...any) {
	Err(format, a...)
	os.Exit(1)
}
