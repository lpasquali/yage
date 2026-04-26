// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package sysinfo ports small bash helpers around OS/arch detection and
// "is_true"-style bool parsing.
package sysinfo

import (
	"os/exec"
	"runtime"
	"strings"
)

// Arch maps `uname -m` to the Go-release binary convention.
// Mirrors _arch() in the original bash port.
func Arch() string {
	out, err := exec.Command("uname", "-m").Output()
	if err != nil {
		// Fall back to runtime.GOARCH which already uses amd64/arm64.
		return runtime.GOARCH
	}
	m := strings.TrimSpace(string(out))
	switch m {
	case "x86_64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l":
		return "arm"
	case "s390x":
		return "s390x"
	case "ppc64le":
		return "ppc64le"
	default:
		return m
	}
}

// OS returns `uname -s` lowercased ("linux", "darwin", …).
func OS() string {
	out, err := exec.Command("uname", "-s").Output()
	if err != nil {
		return runtime.GOOS
	}
	return strings.ToLower(strings.TrimSpace(string(out)))
}

// IsTrue mirrors the bash is_true() helper: matches the affirmative strings
// case-insensitively and treats everything else as false.
func IsTrue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	}
	return false
}