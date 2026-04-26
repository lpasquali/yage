// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// csi.go — Selector resolves cfg into a concrete Driver list.
//
// Two inputs feed into the resolution:
//
//   1. cfg.CSI.Drivers — explicit operator override. When non-
//      empty, names not in this list are NOT installed (and names
//      that aren't registered are silently dropped with a logx
//      warning).
//
//   2. DefaultsFor(cfg.InfraProvider) — the per-provider default
//      list when the operator hasn't picked. Same drop-unknown-
//      with-warning behavior, since DefaultsFor() may name drivers
//      that haven't shipped yet (Phase F is scoped to AWS/Azure/GCP).
//
// Selector returns a fresh slice on every call; callers may sort or
// trim freely without affecting the registry.
package csi

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// Selector resolves cfg into the ordered list of Driver instances
// that should install on the workload cluster. Order matters: when
// multiple drivers install and cfg.CSI.DefaultClass is empty, the
// first in the returned slice supplies the cluster default
// StorageClass.
//
// Unknown driver names produce a logx.Warn line and are skipped —
// the orchestrator continues with whatever drivers DID register.
// That keeps a partial Phase F (today: AWS/Azure/GCP only) from
// breaking dry-run plans for providers whose default driver hasn't
// landed yet.
func Selector(cfg *config.Config) []Driver {
	names := cfg.CSI.Drivers
	if len(names) == 0 {
		names = DefaultsFor(cfg.InfraProvider)
	}
	if len(names) == 0 {
		return nil
	}
	out := make([]Driver, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		d, err := Get(n)
		if err != nil {
			logx.Warn("csi: skipping unknown driver %q (%v)", n, err)
			continue
		}
		out = append(out, d)
	}
	return out
}