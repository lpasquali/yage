// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

//go:build !linux

package secmem

// DisableDump is a no-op on platforms that do not support PR_SET_DUMPABLE.
func DisableDump() error {
	return nil
}
