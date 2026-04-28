// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package keyring wraps github.com/zalando/go-keyring to provide a
// thin, error-tolerant credential store for yage. All operations degrade
// gracefully: callers must treat every failure as non-fatal.
package keyring

import "github.com/zalando/go-keyring"

const service = "yage"

// Key names for the Proxmox credential slots.
const (
	KeyProxmoxCAPIToken  = "proxmox.capi_token"
	KeyProxmoxCAPISecret = "proxmox.capi_secret"
	KeyProxmoxAdminToken = "proxmox.admin_token"
)

// Get retrieves a credential. Returns ("", ErrNotFound) when not stored.
// Returns ("", err) on keyring access failure (e.g. no keyring daemon on
// headless Linux).
func Get(key string) (string, error) {
	return keyring.Get(service, key)
}

// Set stores a credential. Returns nil on success, err on failure.
func Set(key, value string) error {
	return keyring.Set(service, key, value)
}

// Delete removes a stored credential. No-ops if not present.
func Delete(key string) error {
	err := keyring.Delete(service, key)
	if err == keyring.ErrNotFound {
		return nil
	}
	return err
}

// Available returns true if the keyring backend is accessible.
// On headless Linux without libsecret it returns false — callers fall back
// gracefully.
func Available() bool {
	_, err := keyring.Get(service, "__probe__")
	// ErrNotFound means the backend responded — key just doesn't exist yet.
	// Any other error (dbus unavailable, no secret service) means unusable.
	return err == nil || err == keyring.ErrNotFound
}
