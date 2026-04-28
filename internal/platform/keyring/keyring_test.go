// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package keyring

import "testing"

func TestKeyring_SetGetDelete(t *testing.T) {
	if !Available() {
		t.Skip("no keyring backend available")
	}
	const key = "test.credential"
	if err := Set(key, "s3cr3t"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	val, err := Get(key)
	if err != nil || val != "s3cr3t" {
		t.Fatalf("Get: val=%q err=%v", val, err)
	}
	if err := Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
