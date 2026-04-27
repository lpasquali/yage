// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package idgen provides ID generation utilities used across yage.
package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateUUIDv4 returns an RFC 4122 v4 UUID using crypto/rand.
func GenerateUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("idgen: crypto/rand unavailable: " + err.Error())
	}
	// Set version v4 and variant bits.
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
