// Package versionx ports _normalize_git_version and _versions_match from
// bootstrap-capi.sh — used to decide whether a locally-installed CLI matches
// the pinned *_VERSION and should be reinstalled.
package versionx

import "strings"

// Normalize strips a leading "v" and anything after the first "-" (pre-release
// suffix or git build metadata), matching _normalize_git_version.
func Normalize(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	return s
}

// Match reports whether two version strings compare equal after Normalize.
// Matches _versions_match: both sides must be non-empty.
func Match(a, b string) bool {
	na := Normalize(a)
	nb := Normalize(b)
	return na != "" && nb != "" && na == nb
}
