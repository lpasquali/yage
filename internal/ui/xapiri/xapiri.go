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
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"

	"github.com/lpasquali/yage/internal/cluster/kind"
	"github.com/lpasquali/yage/internal/cluster/kindsync"
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
	// Write plain text (no ANSI) to the ring buffer.
	line := r.Message
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

// installLogTee wraps the global obs.Logger with a multiHandler that also
// writes plain-text lines to globalLogRing.
func installLogTee() {
	existing := obs.Global()
	// Retrieve the underlying slog.Handler via a slog.Logger adapter.
	// obs.Logger wraps *slog.Logger; we create a new one from the current
	// logger's handler (accessed by promoting through obs.NewLoggerFromHandler).
	primary := obs.NewPrettyHandler()
	teeHandler := &multiHandler{primary: primary, ring: globalLogRing}
	obs.SetGlobal(obs.NewLoggerFromHandler(teeHandler))
	_ = existing // existing logger is replaced; its handler is superseded
}

// Run starts the interactive walkthrough. Returns the exit code
// main should propagate: 0 on a clean exit (whether persisted to
// kind, persisted to local fallback, or user-cancelled at the
// review step); non-zero only on hard I/O failures we can't
// recover from.
//
// First question is the kind management cluster name — every later
// step (cost-compare, persist) needs it, and it doubles as the
// kubectl context. Then the prelude brings that cluster up so step
// 8 can persist into the kind Secret without silently falling back
// to local disk. The kind cluster is created without any CAPI
// infrastructure provider — those land later, in the orchestrator's
// normal phases, once the user has picked one.
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
	if useHuhTUI() {
		return runHuhBranch(w, cfg, s)
	}
	s.greet()
	if err := s.stepKindClusterName(); err != nil {
		return s.exit(err)
	}
	// Load all previously saved state now that we know the kind cluster
	// name. Two stores exist:
	//   1. yage-system/proxmox-bootstrap-config — the orchestrator's
	//      store, written after a real deployment. Contains network
	//      fields, provider URL/tokens, workload cluster name.
	//   2. yage-system/bootstrap-config — xapiri's own store, written
	//      at the end of a successful walkthrough. Contains a full
	//      snapshot including everything from store 1.
	// Call 1 first so store 2 (which is fresher when it exists) wins
	// on overlap. Both are best-effort no-ops when the cluster or Secret
	// is not yet reachable (first-run case).
	kindsync.MergeBootstrapSecretsFromKind(cfg)
	_ = kindsync.MergeBootstrapConfigFromKind(cfg)
	_ = kindsync.ReadCostCompareSecret(cfg) // sets CostCompareEnabled + loads credentials when secret exists
	disableProvidersMissingCredentials(cfg)
	s.initFromConfig(cfg) // re-seed walkthrough state now that kind merges have run
	if cfg.CostCompareEnabled {
		if err := s.stepCostCompareSetup(); err != nil {
			return s.exit(err)
		}
	}
	if err := s.stepKubernetesVersion(); err != nil {
		return s.exit(err)
	}
	if !skipKindPrelude() {
		if err := kind.EnsureClusterUp(cfg, w); err != nil {
			fmt.Fprintf(w, "xapiri: kind management cluster could not be brought up: %v\n", err)
			fmt.Fprintln(w, "  set YAGE_XAPIRI_SKIP_KIND=1 to run the wizard offline (config will not be persisted into kind).")
			return 1
		}
	}
	if err := s.step0_modePick(); err != nil {
		return s.exit(err)
	}
	if s.fork == forkOnPrem {
		return s.runOnPremFork()
	}
	return s.runCloudFork()
}

// useHuhTUI reports whether YAGE_XAPIRI_TUI=huh asks for the spike
// flow. Any other value (or unset) keeps the legacy bufio-driven
// walkthrough.
func useHuhTUI() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("YAGE_XAPIRI_TUI")), "huh")
}

// runHuhBranch is the YAGE_XAPIRI_TUI=huh entry. The kind prelude
// still runs (every later step needs the cluster), then the huh
// form covers steps 1..4 of the cloud fork; cost-compare and the
// shared tail run unchanged afterwards.
func runHuhBranch(w io.Writer, cfg *config.Config, s *state) int {
	// Speculative merge using the default cluster name so the huh form
	// sees saved values as its initial field values.
	if cfg.KindClusterName == "" {
		cfg.KindClusterName = "yage-mgmt"
	}
	kindsync.MergeBootstrapSecretsFromKind(cfg)
	_ = kindsync.MergeBootstrapConfigFromKind(cfg)
	_ = kindsync.ReadCostCompareSecret(cfg) // sets CostCompareEnabled + loads credentials when secret exists
	disableProvidersMissingCredentials(cfg)
	s.initFromConfig(cfg) // re-seed walkthrough state now that kind merges have run
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
	if s.fork == forkOnPrem {
		return s.runOnPremFork()
	}
	if res.deployRequested {
		// User pressed Start Deploy — proceed directly to the orchestrator.
		if err := s.runSharedTail(); err != nil {
			return s.exit(err)
		}
		return 0
	}
	for {
		err := s.step5_cloud_costCompare()
		if err == nil {
			break
		}
		if err == ErrUserExit {
			return 0
		}
		fmt.Fprintf(w, "xapiri: cost-compare blocked: %v\n", err)
		return 1
	}
	if err := s.runSharedTail(); err != nil {
		return s.exit(err)
	}
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

// isOverheadField spots provider-config fields whose values were
// already priced into the cost-compare row using defaults derived
// from resilience tier + workload shape. Re-prompting for them in
// step6 would let the operator type a number that silently disagrees
// with the headline figure they just picked from. They remain
// settable via the corresponding --<provider>-<flag>; the wizard
// just doesn't ask. Match by suffix on overhead-shaped names.
func isOverheadField(name string) bool {
	for _, suf := range []string{
		"GatewayCount", "ALBCount", "NLBCount",
		"DataTransferGB", "CloudWatchLogsGB", "Route53HostedZones",
		"LogAnalyticsGB", "PublicIPCount", "DNSZones",
	} {
		if hasSuffix(name, suf) {
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