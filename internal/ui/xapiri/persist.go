// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// persist.go — write the resolved configuration to the authoritative
// store: Secret yage-system/bootstrap-config in the kind management
// cluster. The wizard's prelude (xapiri.Run) brings the cluster up
// before any prompt runs, so by the time we get here the kind write
// is the expected outcome.
//
// Disk fallback is opt-in via YAGE_XAPIRI_DISK_FALLBACK=1 — useful
// when the operator wants to capture answers from a host that can't
// run Docker (review-only sessions, demos). With the env unset, a
// kind-write failure is a hard error: the wizard would otherwise
// silently drop credentials onto disk, which is a footgun.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
)

// persistConfig writes cfg into the kind cluster. When the
// YAGE_XAPIRI_DISK_FALLBACK opt-in env is set, a kind-write failure
// degrades to a local YAML file under the user's XDG config dir.
// The returned string describes where the data went so the
// walkthrough can echo it back to the user.
func persistConfig(out io.Writer, cfg *config.Config) (string, error) {
	if err := tryWriteToKind(cfg); err == nil {
		return "Secret yage-system/bootstrap-config (kind)", nil
	} else {
		if !diskFallbackEnabled() {
			return "", fmt.Errorf("kind write failed and YAGE_XAPIRI_DISK_FALLBACK is not set: %w", err)
		}
		fmt.Fprintf(out, "  kind write failed: %v\n", err)
	}

	path, err := writeLocalFallback(cfg)
	if err != nil {
		return "", fmt.Errorf("local fallback: %w", err)
	}
	fmt.Fprintln(out, "  saved to local disk (YAGE_XAPIRI_DISK_FALLBACK opt-in); will sync on first non-xapiri run.")
	return path, nil
}

// diskFallbackEnabled reports whether the operator opted into the
// local-disk fallback path via YAGE_XAPIRI_DISK_FALLBACK=1.
func diskFallbackEnabled() bool {
	v := strings.TrimSpace(os.Getenv("YAGE_XAPIRI_DISK_FALLBACK"))
	return v == "1" || strings.EqualFold(v, "true")
}

// tryWriteToKind delegates to kindsync.WriteBootstrapConfigSecret.
// The indirection through a package-level var lets tests substitute
// a mock writer.
func tryWriteToKind(cfg *config.Config) error {
	return writeBootstrapConfigSecret(cfg)
}

// writeBootstrapConfigSecret is the kind-side persistence hook.
// yage-system/bootstrap-config is the authoritative TUI persistence
// target.
var writeBootstrapConfigSecret func(cfg *config.Config) error = kindsync.WriteBootstrapConfigSecret

// writeLocalFallback persists cfg to ~/.config/yage/bootstrap.yaml
// (XDG_CONFIG_HOME-aware). YAML rather than JSON because the rest of
// yage's on-disk artefacts are YAML and the file is meant to be
// human-inspectable. Only used when YAGE_XAPIRI_DISK_FALLBACK=1.
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

