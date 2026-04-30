// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// dns1123label is the validation regexp for Kubernetes-style names
// (cluster names, namespaces). Lowercase alphanumeric + hyphens; must
// start and end with an alphanumeric. Length is capped at 63 chars.
var dns1123label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validateDNSLabel checks that v is a non-empty DNS-1123 label
// (lowercase alphanumeric + hyphens, max 63 chars, must start and
// end with alphanumeric). Mirrors the huh spike's huhValidateDNSLabel.
func validateDNSLabel(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("kind cluster name is required")
	}
	if len(v) > 63 {
		return fmt.Errorf("too long: %d chars (max 63)", len(v))
	}
	if !dns1123label.MatchString(v) {
		return fmt.Errorf("not a DNS-1123 label (lowercase alphanumeric + hyphens)")
	}
	return nil
}

// validateNonNegativeInt rejects empty, non-numeric, and negative inputs.
func validateNonNegativeInt(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("value is required")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("not an integer: %q", v)
	}
	if n < 0 {
		return fmt.Errorf("must be zero or positive")
	}
	return nil
}

// validateNonNegativeIntOptional accepts empty or a non-negative integer.
func validateNonNegativeIntOptional(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("not an integer: %q", v)
	}
	if n < 0 {
		return fmt.Errorf("must be zero or positive")
	}
	return nil
}

// validatePositiveFloat enforces > 0 on numeric budget inputs.
func validatePositiveFloat(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil // blank = no budget set
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("not a number: %q", v)
	}
	if f <= 0 {
		return fmt.Errorf("must be greater than zero")
	}
	return nil
}

// intToStrOrEmpty returns strconv.Itoa(cur) when cur > 0, otherwise
// strconv.Itoa(fallback). Used to pre-fill add-on resource fields.
func intToStrOrEmpty(cur, fallback int) string {
	if cur > 0 {
		return strconv.Itoa(cur)
	}
	return strconv.Itoa(fallback)
}

// parseIntOrKeep parses s as a non-negative integer. Returns the parsed
// value on success, or cur when s is empty or unparseable.
func parseIntOrKeep(s string, cur int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return cur
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 0 {
		return n
	}
	return cur
}

// validateAppBuckets rejects strings that parse to zero usable buckets.
func validateAppBuckets(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("at least one bucket required (e.g. '4 medium')")
	}
	if len(parseAppBuckets(v)) == 0 {
		return fmt.Errorf("couldn't parse — expected pairs like '6 medium 2 heavy'")
	}
	return nil
}
