// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kind

// ensure.go — bring up the kind management cluster idempotently
// from any caller (xapiri prelude, the orchestrator's main path,
// future ad-hoc commands). Stateless and dependency-free of the
// orchestrator package so xapiri can call it before any provider is
// chosen — we install kind controllers that don't need an
// infrastructure provider yet (CAPI core, CAAPH, etc. land later in
// the orchestrator's normal phases).

import (
	"fmt"
	"io"
	"os"

	kindlog "sigs.k8s.io/kind/pkg/log"
	kindcluster "sigs.k8s.io/kind/pkg/cluster"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/shell"
)

// minimalKindConfig is the YAML written when cfg.KindConfig is unset
// at the time EnsureClusterUp runs. Mirrors the orchestrator's
// kindconfig.go default; duplicated here to avoid an import cycle.
const minimalKindConfig = `kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
`

// EnsureClusterUp makes the kind cluster named cfg.KindClusterName
// reachable: writes a minimal kind config when cfg.KindConfig is
// empty, creates the cluster if it doesn't exist, and exports the
// kubeconfig so client-go (and kubectl) pick up the new context.
//
// Idempotent: returns nil immediately when the cluster already
// exists. Progress lines go to out (pass nil to stay quiet).
func EnsureClusterUp(cfg *config.Config, out io.Writer) error {
	if cfg == nil {
		return fmt.Errorf("kind: nil config")
	}
	if cfg.KindClusterName == "" {
		return fmt.Errorf("kind: cfg.KindClusterName is empty")
	}

	if err := ensureKindConfigFile(cfg); err != nil {
		return err
	}

	provider := kindcluster.NewProvider(kindcluster.ProviderWithLogger(quietKindLogger{}))
	names, err := provider.List()
	if err != nil {
		return fmt.Errorf("kind: list clusters: %w", err)
	}
	exists := false
	for _, n := range names {
		if n == cfg.KindClusterName {
			exists = true
			break
		}
	}

	if !exists {
		emit(out, "creating kind management cluster %q (control plane only; CAPI infrastructure provider lands later)…", cfg.KindClusterName)
		raw, err := os.ReadFile(cfg.KindConfig)
		if err != nil {
			return fmt.Errorf("kind: read kind config %s: %w", cfg.KindConfig, err)
		}
		if err := provider.Create(
			cfg.KindClusterName,
			kindcluster.CreateWithRawConfig(raw),
			kindcluster.CreateWithDisplayUsage(false),
			kindcluster.CreateWithDisplaySalutation(false),
		); err != nil {
			return fmt.Errorf("kind: create cluster %q: %w", cfg.KindClusterName, err)
		}
	} else {
		emit(out, "kind management cluster %q already exists — reusing.", cfg.KindClusterName)
	}

	// Export kubeconfig so kubectl / client-go pick up the context.
	// We use the kind binary here (matches the orchestrator's
	// existing behaviour); when the binary isn't in PATH we fall
	// back to the library's KubeConfig fetch + writing a temp file
	// is a future improvement.
	if shell.CommandExists("kind") {
		if err := shell.Run("kind", "export", "kubeconfig", "--name", cfg.KindClusterName); err != nil {
			return fmt.Errorf("kind: export kubeconfig for %q: %w", cfg.KindClusterName, err)
		}
	} else {
		emit(out, "  (kind CLI not in PATH; skipping kubeconfig export — client-go will discover the context if it's already merged)")
	}
	return nil
}

// ensureKindConfigFile writes the minimal kind config to a temp
// file when cfg.KindConfig is empty, mirroring the orchestrator's
// EnsureKindConfig but without the orchestrator-side exit traps —
// xapiri owns its own cleanup.
func ensureKindConfigFile(cfg *config.Config) error {
	if cfg.KindConfig != "" {
		if _, err := os.Stat(cfg.KindConfig); err != nil {
			return fmt.Errorf("kind: KindConfig %s: %w", cfg.KindConfig, err)
		}
		return nil
	}
	f, err := os.CreateTemp("", "yage-kind.*.yaml")
	if err != nil {
		return fmt.Errorf("kind: create temp config: %w", err)
	}
	if _, err := f.WriteString(minimalKindConfig); err != nil {
		f.Close()
		return fmt.Errorf("kind: write temp config: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	cfg.KindConfig = f.Name()
	cfg.BootstrapEphemeralKindConfig = f.Name()
	cfg.BootstrapKindConfigEphemeral = true
	return nil
}

// emit writes a single progress line to out when out is non-nil.
func emit(out io.Writer, format string, args ...any) {
	if out == nil {
		return
	}
	fmt.Fprintf(out, "  %s\n", fmt.Sprintf(format, args...))
}

// quietKindLogger silences the kind library's chatty default logger.
// Errors and warnings still flow through our caller's error path.
type quietKindLogger struct{}

func (quietKindLogger) Warn(string)                       {}
func (quietKindLogger) Warnf(string, ...any)              {}
func (quietKindLogger) Error(string)                      {}
func (quietKindLogger) Errorf(string, ...any)             {}
func (quietKindLogger) V(kindlog.Level) kindlog.InfoLogger { return quietInfoLogger{} }

type quietInfoLogger struct{}

func (quietInfoLogger) Info(string)            {}
func (quietInfoLogger) Infof(string, ...any)   {}
func (quietInfoLogger) Enabled() bool          { return false }
