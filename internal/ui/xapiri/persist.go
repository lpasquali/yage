// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// persist.go — write the resolved configuration somewhere the next
// non-`--xapiri` invocation can read it.
//
// The plan (see docs/abstraction-plan.md §11 / §16) is for the
// authoritative store to be a kind Secret at
// yage-system/bootstrap-config. The function that lands those bytes
// — kindsync.WriteBootstrapConfigSecret — is owned by Track B and may
// not exist in this branch yet. We discover it dynamically (no import
// dependency) and gracefully degrade to a local YAML file under the
// user's XDG config dir when it isn't reachable. The next non-xapiri
// run picks the file up on first sync.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
)

// persistConfig writes cfg to the kind cluster when possible, else to
// the local fallback path. The returned string describes where the
// data went so the walkthrough can echo it back to the user.
func persistConfig(out io.Writer, cfg *config.Config) (string, error) {
	if err := tryWriteToKind(cfg); err == nil {
		return "Secret yage-system/bootstrap-config (kind)", nil
	} else if !errIsUnimplemented(err) {
		// A real failure (kind reachable but Secret apply blew up): tell
		// the user but still try the local fallback so their answers
		// aren't lost.
		fmt.Fprintf(out, "  kind write failed: %v\n", err)
	}

	path, err := writeLocalFallback(cfg)
	if err != nil {
		return "", fmt.Errorf("local fallback: %w", err)
	}
	fmt.Fprintln(out, "  kind cluster not reachable yet — saved locally; will sync on first non-xapiri run.")
	return path, nil
}

// tryWriteToKind delegates to kindsync.WriteBootstrapConfigSecret when
// it exists. We probe via a tiny helper variable so the call is a
// compile-time noop when Track B hasn't shipped the function — and a
// straight call once it has. Today we return errUnimplemented so the
// caller falls through to the local-disk path.
func tryWriteToKind(cfg *config.Config) error {
	if writeBootstrapConfigSecret == nil {
		return errUnimplemented
	}
	return writeBootstrapConfigSecret(cfg)
}

// writeBootstrapConfigSecret is the indirection for Track B's helper.
// Track B landed in commit 0655951; this binding is the one-line
// promotion the original comment anticipated. yage-system/
// bootstrap-config is now the authoritative TUI persistence target.
var writeBootstrapConfigSecret func(cfg *config.Config) error = kindsync.WriteBootstrapConfigSecret

// errUnimplemented is the sentinel we return while Track B hasn't
// shipped the kind writer yet. We keep it package-private — callers
// don't branch on it; they only ask "is this the not-yet-implemented
// case?" via errIsUnimplemented.
var errUnimplemented = fmt.Errorf("xapiri: kind writer not yet available")

func errIsUnimplemented(err error) bool { return err == errUnimplemented }

// writeLocalFallback persists cfg to ~/.config/yage/bootstrap.yaml
// (XDG_CONFIG_HOME-aware). YAML rather than JSON because the rest of
// yage's on-disk artefacts are YAML and the file is meant to be human-
// inspectable while Track B's kind writer is still in flight.
func writeLocalFallback(cfg *config.Config) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "bootstrap.yaml")
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	// 0600 — config contains credentials when non-airgapped.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// configDir resolves the directory to drop bootstrap.yaml into.
// Honors XDG_CONFIG_HOME; falls back to ~/.config/yage; if neither is
// available, dies with a clear error rather than silently dropping
// the user's answers in /tmp.
func configDir() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "yage"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".config", "yage"), nil
}

// keep the kindsync import live even when writeBootstrapConfigSecret
// is nil — once Track B lands, this package switches to a real call
// and the import becomes load-bearing. Until then we reference a
// trivial value to avoid an "imported and not used" compile error.
var _ = kindsync.SyncBootstrapConfigToKind