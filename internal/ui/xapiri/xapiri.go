// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package xapiri implements yage's interactive configuration TUI,
// invoked via `yage --xapiri`.
//
// xapiri are sacred spirits in the Yanomami people's cosmology.
// yage runs xapiri to get help from the spirits to create a
// visionary deployment — a full-screen dashboard that surfaces
// every config knob, validates choices against the active provider,
// and persists the result to a Secret in the local kind cluster
// (yage-system namespace) before any state is changed on the target
// cloud. yage stores its config and provider credentials in kind
// Secrets; local disk is used only for encrypted kind cluster
// backup/restore archives.
//
// The dashboard is shaped per docs/abstraction-plan.md §22:
// budget-first / product-shape-first, with on-prem-vs-cloud mode
// selection, live cost comparison, and a review+deploy confirmation
// step. The resolved config is persisted via kindsync so a subsequent
// non-interactive `yage` run picks it up without re-prompting.
//
// Tone: calm, walkthrough-shaped, never an interrogation.
package xapiri

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"

	"github.com/lpasquali/yage/internal/cluster/kind"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/obs"
)

// multiHandler is a slog.Handler that fans out to two handlers: the
// primary (existing) handler and a secondary ring-buffer handler.
type multiHandler struct {
	primary slog.Handler
	ring    *logRing
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.primary.Enabled(ctx, level)
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	// Single-line structured format: [15:04:05] LEVL message  key=val …
	lvl := r.Level.String()
	if len(lvl) > 4 {
		lvl = lvl[:4]
	}
	line := fmt.Sprintf("[%s] %-4s %s", r.Time.Format("15:04:05"), lvl, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		line += "  " + a.Key + "=" + fmt.Sprintf("%v", a.Value.Any())
		return true
	})
	_, _ = h.ring.Write([]byte(line + "\n"))
	// Delegate to the primary handler.
	return h.primary.Handle(ctx, r)
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &multiHandler{primary: h.primary.WithAttrs(attrs), ring: h.ring}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	return &multiHandler{primary: h.primary.WithGroup(name), ring: h.ring}
}

// globalLogRing is the process-wide log ring buffer installed before the
// dashboard starts.  It is initialised by installLogTee and read by the
// [logs] tab.
var globalLogRing = &logRing{}

// installLogTee wraps the global obs.Logger with a multiHandler that writes
// plain-text lines to globalLogRing (visible in [logs] tab). stdout/stderr
// are discarded: the TUI is running in alt-screen and any direct write to
// those file descriptors would corrupt the rendered output.
func installLogTee() {
	primary := obs.NewPrettyHandlerWithWriters(io.Discard, io.Discard, true)
	teeHandler := &multiHandler{primary: primary, ring: globalLogRing}
	obs.SetGlobal(obs.NewLoggerFromHandler(teeHandler))
}

// Run starts the interactive dashboard. Returns the exit code
// main should propagate: 0 on a clean exit (whether persisted to
// kind, persisted to local fallback, or user-cancelled at the
// review step); non-zero only on hard I/O failures we can't
// recover from.
//
// The kind management cluster is brought up before the dashboard
// opens so the dashboard can persist into the kind Secret without
// silently falling back to local disk. The kind cluster is created
// without any CAPI infrastructure provider — those land later, in
// the orchestrator's normal phases, once the user has picked one.
// Skipped when YAGE_XAPIRI_SKIP_KIND=1 (offline review mode).
//
// Caller contract: cmd/yage's `--xapiri` branch passes os.Stdout +
// the resolved cfg.
func Run(w io.Writer, cfg *config.Config) int {
	if cfg == nil {
		fmt.Fprintln(w, "xapiri: nil config (internal error)")
		return 2
	}
	s := newState(w, cfg)
	return runHuhBranch(w, cfg, s)
}

// runHuhBranch is the dashboard entry. The kind prelude brings
// the management cluster up; the dashboard handles config selection and all
// subsequent steps interactively.
func runHuhBranch(w io.Writer, cfg *config.Config, s *state) int {
	if cfg.KindClusterName == "" {
		cfg.KindClusterName = "yage-mgmt"
	}
	if !skipKindPrelude() {
		if err := kind.EnsureClusterUp(cfg, w); err != nil {
			fmt.Fprintf(w, "xapiri: kind management cluster could not be brought up: %v\n", err)
			fmt.Fprintln(w, "  set YAGE_XAPIRI_SKIP_KIND=1 to run the wizard offline (config will not be persisted into kind).")
			return 1
		}
	}
	installLogTee()
	res := runDashboard(w, cfg, s)
	if !res.saved {
		return 0
	}
	if !res.deployRequested {
		return 0
	}
	if strings.TrimSpace(cfg.InfraProvider) == "" {
		fmt.Fprintln(w, "xapiri: no infrastructure provider selected for deployment; choose a provider in the config tab before deploying")
		return 1
	}
	s.cfg.XapiriDeployNow = true
	return 0
}

// skipKindPrelude reports whether YAGE_XAPIRI_SKIP_KIND opts out of
// the kind-up prelude. Useful for offline review of the wizard
// (tests, demos) where bringing up Docker isn't possible.
func skipKindPrelude() bool {
	v := strings.TrimSpace(os.Getenv("YAGE_XAPIRI_SKIP_KIND"))
	return v == "1" || strings.EqualFold(v, "true")
}

// providerSubStruct resolves cfg.Providers.<ProperCase(name)> via
// reflection. Returns the Value + a bool reporting whether the
// field was found — providers registered by name but missing a
// sub-struct (the "minstub" path used in tests) silently skip the
// section. Used by applyGeoRegionDefaults in georegions.go.
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
