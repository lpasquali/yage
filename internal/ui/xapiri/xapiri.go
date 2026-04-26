// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package xapiri implements yage's interactive configuration TUI,
// invoked via `yage --xapiri`.
//
// xapiri are sacred spirits in the Yanomami people's cosmology.
// yage runs xapiri to get help from the spirits to create a
// visionary deployment — an interactive walkthrough that surfaces
// every config knob, validates choices against the active provider,
// and persists the result to a Secret in the local kind cluster
// (yage-system namespace) before any state is changed on the target
// cloud. yage stores its config and provider credentials in kind
// Secrets; local disk is used only for encrypted kind cluster
// backup/restore archives.
//
// The walkthrough is shaped per docs/abstraction-plan.md §22:
// budget-first / product-shape-first, with an on-prem-vs-cloud
// fork at step 0. Steps 1, 2, 3 share their shape across forks
// (with fork-tweaked options); step 4 diverges (provider-pick on
// on-prem; budget on cloud); step 5 diverges (capacity check on
// on-prem; cost-compare + feasibility-merge on cloud); steps 6
// (provider details), 7 (review + cost line), and 8 (persist +
// decide) are identical in code on both forks.
//
// Tone: calm, walkthrough-shaped, never an interrogation. The
// resolved config is echoed back via plan.NewTextWriter at step 7
// so the review style matches `--dry-run`.
package xapiri

import (
	"fmt"
	"io"
	"reflect"

	"github.com/lpasquali/yage/internal/config"
)

// Run starts the interactive walkthrough. Returns the exit code
// main should propagate: 0 on a clean exit (whether persisted to
// kind, persisted to local fallback, or user-cancelled at the
// review step); non-zero only on hard I/O failures we can't
// recover from.
//
// Caller contract is unchanged from the prior stub: cmd/yage's
// `--xapiri` branch passes os.Stdout + the resolved cfg.
func Run(w io.Writer, cfg *config.Config) int {
	if cfg == nil {
		fmt.Fprintln(w, "xapiri: nil config (internal error)")
		return 2
	}
	s := newState(w, cfg)
	s.greet()
	if err := s.step0_modePick(); err != nil {
		return s.exit(err)
	}
	if s.fork == forkOnPrem {
		return s.runOnPremFork()
	}
	return s.runCloudFork()
}

// providerSubStruct resolves cfg.Providers.<ProperCase(name)> via
// reflection. Returns the Value + a bool reporting whether the
// field was found — providers registered by name but missing a
// sub-struct (the "minstub" path used in tests) silently skip the
// section. Used by step6_providerDetails in shared.go.
func providerSubStruct(cfg *config.Config, name string) (reflect.Value, bool) {
	pv := reflect.ValueOf(&cfg.Providers).Elem()
	sub := pv.FieldByName(properCase(name))
	if !sub.IsValid() || sub.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	return sub, true
}

// properCase maps a provider's registry id ("aws", "ibmcloud",
// "digitalocean", "proxmox", …) to the matching field name on the
// Providers struct. The mapping is small enough that a switch is
// clearer (and faster) than a generic title-case helper, and the
// switch surfaces "we don't know about this provider yet" loudly
// when a new one is registered.
func properCase(name string) string {
	switch name {
	case "aws":
		return "AWS"
	case "azure":
		return "Azure"
	case "gcp":
		return "GCP"
	case "hetzner":
		return "Hetzner"
	case "digitalocean":
		return "DigitalOcean"
	case "linode":
		return "Linode"
	case "oci":
		return "OCI"
	case "ibmcloud":
		return "IBMCloud"
	case "proxmox":
		return "Proxmox"
	case "openstack":
		return "OpenStack"
	case "vsphere":
		return "Vsphere"
	default:
		// Last-ditch: capitalize the first letter. Better to surface
		// a blank section than to silently skip a registered provider
		// just because the mapping is missing.
		if name == "" {
			return ""
		}
		return string(name[0]-32) + name[1:]
	}
}

// isSensitiveFieldName recognises field names that should be masked
// in the review pass and prompted via promptSecret. We match by
// suffix on common conventions; missing a field here only loses the
// echo-mask, never functionality.
func isSensitiveFieldName(name string) bool {
	for _, suf := range []string{"Token", "Secret", "Password", "APIKey", "Passphrase"} {
		if hasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// isInternalBookkeeping spots provider-config fields that aren't
// meant to be hand-tuned during the walkthrough — e.g. cached
// kindsync-side flags and bootstrap-Secret name placeholders. They'd
// just clutter the prompt list with bookkeeping the user shouldn't
// be touching.
func isInternalBookkeeping(name string) bool {
	for _, pre := range []string{"Bootstrap", "KindCAPMOX", "Identity"} {
		if hasPrefix(name, pre) {
			return true
		}
	}
	return false
}

// hasPrefix / hasSuffix — tiny helpers so we don't pull in the
// strings package twice for two trivial calls. Equivalent to
// strings.HasPrefix / strings.HasSuffix but kept inline so the
// file is self-describing.
func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
func hasSuffix(s, p string) bool { return len(s) >= len(p) && s[len(s)-len(p):] == p }