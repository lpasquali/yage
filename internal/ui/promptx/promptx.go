// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package promptx hosts the interactive-prompt helpers. The
// bootstrap uses these for yes/no confirmations and numeric menu
// selection when a kind cluster already exists, when destructive
// operations are about to run, etc.
package promptx

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// CanPrompt reports whether an interactive prompt is possible.
// True if stdin is a TTY, or /dev/tty is readable+writable.
func CanPrompt() bool {
	if isTerminal(os.Stdin) {
		return true
	}
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// ReadLine reads a single line from stdin when stdin is a TTY, otherwise
// from /dev/tty. Trailing newline stripped. Returns "" if reading fails.
// Matches bootstrap_read_line.
func ReadLine() string {
	var r *bufio.Reader
	if isTerminal(os.Stdin) {
		r = bufio.NewReader(os.Stdin)
	} else {
		f, err := os.Open("/dev/tty")
		if err != nil {
			return ""
		}
		defer f.Close()
		r = bufio.NewReader(f)
	}
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimRight(line, "\r\n")
}

// Confirm prompts `msg (yes/no): ` to stderr and returns true iff the
// answer starts with y or Y. The ANSI yellow "[?]" prefix tags the
// line as an interactive prompt.
func Confirm(msg string) bool {
	fmt.Fprintf(os.Stderr, "\033[1;33m[?]\033[0m %s (yes/no): ", msg)
	resp := ReadLine()
	return len(resp) > 0 && (resp[0] == 'y' || resp[0] == 'Y')
}

var (
	digitsOnly      = regexp.MustCompile(`^[0-9]+$`)
	dashRangeRE     = regexp.MustCompile(`^([0-9]+)-([0-9]+)$`)
	leadingDigitsRE = regexp.MustCompile(`^[^0-9]*([0-9]+)`)
)

// NormalizeNumericMenuChoice parses a user-typed numeric menu choice.
// Accepts:
//   - a bare positive integer,
//   - a "N-M" range (e.g. the menu line "1-1" pasted back), keeping N,
//   - otherwise the first run of digits in the input.
//
// When `max > 0`, values outside [1, max] are rejected. Returns "" on
// any rejection path.
func NormalizeNumericMenuChoice(raw string, max int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var num int
	switch {
	case digitsOnly.MatchString(raw):
		num, _ = strconv.Atoi(raw)
	case dashRangeRE.MatchString(raw):
		m := dashRangeRE.FindStringSubmatch(raw)
		num, _ = strconv.Atoi(m[1])
	case leadingDigitsRE.MatchString(raw):
		m := leadingDigitsRE.FindStringSubmatch(raw)
		num, _ = strconv.Atoi(m[1])
	default:
		return ""
	}
	if max > 0 && (num < 1 || num > max) {
		return ""
	}
	return strconv.Itoa(num)
}

// isTerminal reports whether f is attached to a TTY. Using a minimal
// POSIX-style ioctl would add platform-specific code; we get by with the
// device-mode check (ModeCharDevice) which is what the stdlib uses in
// golang.org/x/term's IsTerminal for this path.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}