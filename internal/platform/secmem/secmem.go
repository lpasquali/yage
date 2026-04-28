// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package secmem

import (
	"runtime"
	"unsafe"

	"github.com/awnumar/memguard"
)

// Zero wipes b in place; runtime.KeepAlive prevents compiler elision.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}

// ZeroString zeroes the backing bytes of s in place using unsafe.
// Best-effort: Go may intern short strings, sharing the backing array
// with the read-only data segment; on those the write will fault.
func ZeroString(s string) {
	if len(s) == 0 {
		return
	}
	b := unsafe.Slice(unsafe.StringData(s), len(s))
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(s)
}

// LockedString holds a value in mlock'd, non-swappable,
// core-dump-excluded memory. Create with NewLockedString; always call
// Destroy() when done.
type LockedString struct {
	lb *memguard.LockedBuffer
}

// NewLockedString stores val in locked memory.
// The empty string is handled without allocating a LockedBuffer.
func NewLockedString(val string) *LockedString {
	if val == "" {
		return &LockedString{}
	}
	src := []byte(val)
	// NewBufferFromBytes moves src into locked memory and wipes src.
	return &LockedString{lb: memguard.NewBufferFromBytes(src)}
}

// NewLockedStringFromBytes stores b in locked memory.
// NewBufferFromBytes moves b into locked memory and wipes the source slice,
// so callers must not use b after this call.
func NewLockedStringFromBytes(b []byte) *LockedString {
	if len(b) == 0 {
		return &LockedString{}
	}
	return &LockedString{lb: memguard.NewBufferFromBytes(b)}
}

// Value returns a plain Go string copy of the locked content.
// The caller should zero the backing bytes when done; use ZeroString.
func (ls *LockedString) Value() string {
	if ls.lb == nil {
		return ""
	}
	return string(ls.lb.Bytes())
}

// Bytes returns a direct slice into locked memory, valid until Destroy.
func (ls *LockedString) Bytes() []byte {
	if ls.lb == nil {
		return nil
	}
	return ls.lb.Bytes()
}

// IsEmpty reports whether the locked string holds no content.
func (ls *LockedString) IsEmpty() bool {
	if ls.lb == nil {
		return true
	}
	return ls.lb.Size() == 0
}

// Destroy wipes and munlocks the backing memory.
// Safe to call multiple times; subsequent calls are no-ops.
func (ls *LockedString) Destroy() {
	if ls.lb == nil {
		return
	}
	ls.lb.Destroy()
}
