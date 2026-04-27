// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs

import (
	"crypto/rand"
	"fmt"
)

// NewRunID returns a new random UUID v4 string formatted as
// "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".
func NewRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is extremely unlikely; return a deterministic
		// placeholder so callers always get a non-empty string.
		return "00000000-0000-4000-8000-000000000000"
	}
	// Set version (4) and variant bits (RFC 4122).
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant bits

	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:],
	)
}
