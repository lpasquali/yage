// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

//go:build linux

package secmem

import "golang.org/x/sys/unix"

// DisableDump prevents ptrace, /proc/self/mem reads, and core dumps
// from exposing credentials.
func DisableDump() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}
