// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package versionx normalises git-style version strings and compares
// them — used to decide whether a locally-installed CLI matches the
// pinned *_VERSION and should be reinstalled.
package versionx

import "strings"

// Normalize strips a leading "v" and anything after the first "-"
// (pre-release suffix or git build metadata).
func Normalize(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	return s
}

// Match reports whether two version strings compare equal after Normalize.
// Both sides must be non-empty.
func Match(a, b string) bool {
	na := Normalize(a)
	nb := Normalize(b)
	return na != "" && nb != "" && na == nb
}