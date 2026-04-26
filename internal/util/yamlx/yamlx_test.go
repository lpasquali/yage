// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package yamlx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	// Mixed cases: comment, quoted, bare, whitespace, non-matching line,
	// later duplicate (should be ignored — first match wins).
	body := `
# leading comment
FOO: bar
BAZ: "quoted value" # trailing comment
QUX: 'single quotes'
WITH_SPACES:    spaced
EMPTY:
: no-key
notakey = still no
FOO: second-wins-would-be-wrong
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		key, want string
	}{
		{"FOO", "bar"},
		{"BAZ", "quoted value"},
		{"QUX", "single quotes"},
		{"WITH_SPACES", "spaced"},
		{"EMPTY", ""},
		{"MISSING", ""},
		{"", ""},
	}
	for _, c := range cases {
		got := GetValue(path, c.key)
		if got != c.want {
			t.Errorf("key=%q: got %q want %q", c.key, got, c.want)
		}
	}
	if got := GetValue("/does/not/exist", "FOO"); got != "" {
		t.Errorf("nonexistent file: got %q", got)
	}
}