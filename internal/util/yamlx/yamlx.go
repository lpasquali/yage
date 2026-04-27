// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package yamlx provides tiny flat-YAML helpers used to read/write
// ${PROXMOX_BOOTSTRAP_CONFIG_FILE} and other non-nested config files.
// The contract is intentionally narrow: only top-level scalar
// key:value pairs are recognised.
package yamlx

import (
	"os"
	"regexp"
	"strings"
)

// keyValueRE matches a flat top-level "key: value" line.
var keyValueRE = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$`)

// GetValue reads a top-level scalar key:value pair from a flat YAML
// file at path. Returns "" for:
//   - empty path or key,
//   - nonexistent file,
//   - read errors,
//   - no matching key in the file.
//
// The first matching top-level key wins. Matched values have surrounding
// single- or double-quotes stripped, but nothing else about them is
// interpreted (no ${} expansion, no escape sequences).
func GetValue(path, key string) string {
	if path == "" || key == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		// strip "# ..." comments, rtrim
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimRight(line, " \t\r\n")
		if line == "" || !strings.Contains(line, ":") {
			continue
		}
		m := keyValueRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] != key {
			continue
		}
		val := strings.TrimSpace(m[2])
		if len(val) >= 2 {
			first, last := val[0], val[len(val)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		return val
	}
	return ""
}