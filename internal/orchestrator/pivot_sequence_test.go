// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package orchestrator

// Post-pivot phase-order regression test for issue #119.
//
// Purpose: assert that runPostPivotSequence executes the ADR 0011 §7 phases
// in the exact canonical order:
//
//	moveCAPIState → handoff → ensureYageSystem → ensureCSI
//	  → ensureRepoSync → verifyParity → rebind
//
// A recorder-closure pattern is used: each fake dep appends its step name to a
// shared slice. The final assertion compares the slice to the canonical order.
// The test is hermetic — no Docker, no kind, no cluster required.

import (
	"context"
	"errors"
	"testing"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/plan"
)

// fakeDeps builds a postPivotDeps where every function appends its step label
// to *calls and returns the error from errs[step] (nil if step not in map).
// csiDrivers returns a single no-op fake driver so the CSI loop records one
// "ensureCSI" entry.
func fakeDeps(calls *[]string, errs map[string]error) postPivotDeps {
	ret := func(step string) error {
		if errs != nil {
			if e, ok := errs[step]; ok {
				return e
			}
		}
		return nil
	}
	return postPivotDeps{
		moveCAPIState: func(_ *config.Config, _ string) error {
			*calls = append(*calls, "moveCAPIState")
			return ret("moveCAPIState")
		},
		handoff: func(_ *config.Config, _, _ string) (kindsync.HandoffResult, error) {
			*calls = append(*calls, "handoff")
			return kindsync.HandoffResult{}, ret("handoff")
		},
		mgmtClient: func(_ string) (*k8sclient.Client, error) {
			// Return nil client; all downstream fakes ignore the cli arg.
			return nil, ret("mgmtClient")
		},
		ensureYageSystem: func(_ context.Context, _ *k8sclient.Client) error {
			*calls = append(*calls, "ensureYageSystem")
			return ret("ensureYageSystem")
		},
		prepareCSI: func(_ *config.Config) {
			*calls = append(*calls, "prepareCSI")
		},
		csiDrivers: func(_ *config.Config) []csi.Driver {
			return []csi.Driver{fakeCSIDriver{
				name: "fake-csi",
				err:  ret("ensureCSI"),
				calls: calls,
			}}
		},
		ensureRepoSync: func(_ context.Context, _ *k8sclient.Client, _ *config.Config) error {
			*calls = append(*calls, "ensureRepoSync")
			return ret("ensureRepoSync")
		},
		verifyParity: func(_ *config.Config, _ string) error {
			*calls = append(*calls, "verifyParity")
			return ret("verifyParity")
		},
		rebind: func(_ *config.Config, _ string) error {
			*calls = append(*calls, "rebind")
			return ret("rebind")
		},
	}
}

// fakeCSIDriver is a minimal csi.Driver implementation whose
// EnsureManagementInstall records the "ensureCSI" step.
type fakeCSIDriver struct {
	name  string
	err   error
	calls *[]string
}

func (f fakeCSIDriver) Name() string { return f.name }
func (f fakeCSIDriver) EnsureManagementInstall(_ *config.Config, _ string) error {
	*f.calls = append(*f.calls, "ensureCSI")
	return f.err
}

// fakeCSIDriver satisfies csi.Driver — remaining methods are stubs.
func (f fakeCSIDriver) K8sCSIDriverName() string                               { return "fake.csi.test" }
func (f fakeCSIDriver) Defaults() []string                                     { return nil }
func (f fakeCSIDriver) HelmChart(_ *config.Config) (string, string, string, error) {
	return "", "", "", csi.ErrNotApplicable
}
func (f fakeCSIDriver) RenderValues(_ *config.Config) (string, error)   { return "", nil }
func (f fakeCSIDriver) EnsureSecret(_ *config.Config, _ string) error   { return csi.ErrNotApplicable }
func (f fakeCSIDriver) DefaultStorageClass() string                     { return "" }
func (f fakeCSIDriver) DescribeInstall(_ plan.Writer, _ *config.Config) {}

// canonicalOrder is the ADR 0011 §7 phase sequence that regression tests
// compare against. If this slice and the sequence in pivot_sequence.go
// diverge, one of the tests below will fail.
var canonicalOrder = []string{
	"moveCAPIState",
	"handoff",
	"ensureYageSystem",
	"prepareCSI",
	"ensureCSI",
	"ensureRepoSync",
	"verifyParity",
	"rebind",
}

func minimalCfg() *config.Config {
	c := &config.Config{}
	c.Pivot.Enabled = true
	c.KindClusterName = "test-kind"
	return c
}

// TestRunPostPivotSequence_HappyPath asserts that, when all deps succeed,
// every phase is called exactly once and in canonical ADR 0011 §7 order.
// If someone re-orders the phases in pivot_sequence.go this test will fail.
func TestRunPostPivotSequence_HappyPath(t *testing.T) {
	t.Parallel()
	var calls []string
	deps := fakeDeps(&calls, nil)
	cfg := minimalCfg()

	err := runPostPivotSequence(context.Background(), cfg, "fake.kubeconfig", deps, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertCallOrder(t, calls, canonicalOrder)
}

// TestRunPostPivotSequence_DryRun asserts that, when dryRun=true, only
// moveCAPIState is called and the function returns nil.
func TestRunPostPivotSequence_DryRun(t *testing.T) {
	t.Parallel()
	var calls []string
	deps := fakeDeps(&calls, nil)
	cfg := minimalCfg()

	err := runPostPivotSequence(context.Background(), cfg, "fake.kubeconfig", deps, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"moveCAPIState"}
	assertCallOrder(t, calls, want)
}

// TestRunPostPivotSequence_MoveCAPIStateFails asserts that when moveCAPIState
// returns an error, none of the downstream phases run and the error is returned.
// This is the acceptance-critical test: re-ordering would break this.
func TestRunPostPivotSequence_MoveCAPIStateFails(t *testing.T) {
	t.Parallel()
	var calls []string
	sentinel := errors.New("clusterctl move: timeout")
	deps := fakeDeps(&calls, map[string]error{"moveCAPIState": sentinel})
	cfg := minimalCfg()

	err := runPostPivotSequence(context.Background(), cfg, "fake.kubeconfig", deps, false)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got: %v", err)
	}
	// Only moveCAPIState must appear; no downstream phase may run.
	if len(calls) != 1 || calls[0] != "moveCAPIState" {
		t.Errorf("downstream phases ran after moveCAPIState failure: %v", calls)
	}
}

// TestRunPostPivotSequence_HandoffFailContinues asserts that a handoff error
// is warn-not-fatal: the remaining phases still execute in order.
func TestRunPostPivotSequence_HandoffFailContinues(t *testing.T) {
	t.Parallel()
	var calls []string
	deps := fakeDeps(&calls, map[string]error{"handoff": errors.New("copy failed: forbidden")})
	cfg := minimalCfg()

	err := runPostPivotSequence(context.Background(), cfg, "fake.kubeconfig", deps, false)
	if err != nil {
		t.Fatalf("handoff error must not abort the sequence; got: %v", err)
	}
	assertCallOrder(t, calls, canonicalOrder)
}

// TestRunPostPivotSequence_EnsureYageSystemFails asserts that when
// ensureYageSystem fails, ensureRepoSync and rebind do not run.
func TestRunPostPivotSequence_EnsureYageSystemFails(t *testing.T) {
	t.Parallel()
	var calls []string
	sentinel := errors.New("yage-system SA create: forbidden")
	deps := fakeDeps(&calls, map[string]error{"ensureYageSystem": sentinel})
	cfg := minimalCfg()

	err := runPostPivotSequence(context.Background(), cfg, "fake.kubeconfig", deps, false)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got: %v", err)
	}
	// prepareCSI and ensureCSI must NOT have run.
	for _, s := range calls {
		if s == "prepareCSI" || s == "ensureCSI" || s == "ensureRepoSync" || s == "verifyParity" || s == "rebind" {
			t.Errorf("phase %q ran after ensureYageSystem failure; full trace: %v", s, calls)
		}
	}
}

// TestRunPostPivotSequence_VerifyParityFails asserts that when verifyParity
// returns a hard error, rebind does not run.
func TestRunPostPivotSequence_VerifyParityFails(t *testing.T) {
	t.Parallel()
	var calls []string
	sentinel := errors.New("parity: yage-repos PVC not bound")
	deps := fakeDeps(&calls, map[string]error{"verifyParity": sentinel})
	cfg := minimalCfg()

	err := runPostPivotSequence(context.Background(), cfg, "fake.kubeconfig", deps, false)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got: %v", err)
	}
	for _, s := range calls {
		if s == "rebind" {
			t.Errorf("rebind ran after verifyParity failure; full trace: %v", calls)
		}
	}
	// All phases up to verifyParity must have run.
	wantPrefix := []string{
		"moveCAPIState", "handoff", "ensureYageSystem",
		"prepareCSI", "ensureCSI", "ensureRepoSync", "verifyParity",
	}
	assertCallOrder(t, calls, wantPrefix)
}

// TestRunPostPivotSequence_VerifyParityWarnDontFail asserts that when
// verifyParity returns nil (ADR 0011 §6 warn-don't-fail behavior from #148),
// rebind still runs. The internal warn is issued inside pivot.VerifyParity; the
// orchestrator only sees nil, so this is the standard success path.
func TestRunPostPivotSequence_VerifyParityWarnDontFail(t *testing.T) {
	t.Parallel()
	var calls []string
	// verifyParity returns nil to simulate the warn-only path.
	deps := fakeDeps(&calls, nil)
	cfg := minimalCfg()

	err := runPostPivotSequence(context.Background(), cfg, "fake.kubeconfig", deps, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// rebind must appear after verifyParity.
	assertCallOrder(t, calls, canonicalOrder)
}

// assertCallOrder verifies that calls matches want exactly (length + contents).
func assertCallOrder(t *testing.T, calls, want []string) {
	t.Helper()
	if len(calls) != len(want) {
		t.Fatalf("call sequence length: got %d (%v), want %d (%v)", len(calls), calls, len(want), want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("step[%d]: got %q, want %q  (full trace: %v)", i, calls[i], want[i], calls)
		}
	}
}
