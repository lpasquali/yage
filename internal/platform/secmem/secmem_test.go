// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package secmem

import "testing"

func TestZero(t *testing.T) {
	b := []byte("secret")
	Zero(b)
	for i, c := range b {
		if c != 0 {
			t.Fatalf("byte %d not zeroed: got %d", i, c)
		}
	}
}

func TestLockedString_roundtrip(t *testing.T) {
	ls := NewLockedString("hello")
	defer ls.Destroy()
	if got := ls.Value(); got != "hello" {
		t.Fatalf("expected %q, got %q", "hello", got)
	}
}

func TestLockedString_destroy(t *testing.T) {
	ls := NewLockedString("hello")
	ls.Destroy()
	ls.Destroy() // must not panic
}
