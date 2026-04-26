// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package promptx

import "testing"

func TestNormalizeNumericMenuChoice(t *testing.T) {
	cases := []struct {
		raw  string
		max  int
		want string
	}{
		{"3", 5, "3"},
		{"  7  ", 10, "7"},
		// "1-1" menu paste-back — take the first digit group.
		{"1-1", 1, "1"},
		{"2-3", 5, "2"},
		// Leading noise.
		{"item 4 here", 10, "4"},
		// Out of range.
		{"6", 5, ""},
		{"0", 5, ""},
		// No digits at all.
		{"abc", 5, ""},
		// Empty / whitespace.
		{"", 5, ""},
		{"   ", 5, ""},
		// max <= 0 means "no upper bound".
		{"999", 0, "999"},
	}
	for _, c := range cases {
		got := NormalizeNumericMenuChoice(c.raw, c.max)
		if got != c.want {
			t.Errorf("raw=%q max=%d: got %q want %q", c.raw, c.max, got, c.want)
		}
	}
}