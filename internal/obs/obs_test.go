// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/obs"
)

// --- TestPrettyHandlerFormat ---

// TestPrettyHandlerFormat verifies that PrettyHandler emits the correct emoji
// prefix for Info, Warn, and Error records, and routes Info to out and
// Warn/Error to errOut.
func TestPrettyHandlerFormat(t *testing.T) {
	var outBuf, errBuf bytes.Buffer

	// noColor=true so assertions don't have to strip ANSI codes.
	h := obs.NewPrettyHandlerWithWriters(&outBuf, &errBuf, true)
	obs.SetGlobal(obs.NewLoggerFromHandler(h))

	obs.Global().Info("hello world")
	obs.Global().Warn("something off")
	obs.Global().Error("boom", nil)

	if got := outBuf.String(); !strings.HasPrefix(got, "✅ 🎉") {
		t.Errorf("Info prefix mismatch: got %q", got)
	}
	if !strings.Contains(outBuf.String(), "hello world") {
		t.Errorf("Info message missing from stdout: %q", outBuf.String())
	}

	// Warn and Error must go to errOut (stderr), not outBuf (stdout).
	if outBuf.String() != "✅ 🎉 hello world\n" {
		t.Errorf("stdout contains unexpected content: %q", outBuf.String())
	}

	errStr := errBuf.String()
	lines := strings.Split(strings.TrimRight(errStr, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines on stderr, got %d: %q", len(lines), errStr)
	}
	if !strings.HasPrefix(lines[0], "⚠️ 🙈") {
		t.Errorf("Warn prefix mismatch: got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "❌ 💩") {
		t.Errorf("Error prefix mismatch: got %q", lines[1])
	}
}

// --- TestNoopTracerAllocsZero ---

// TestNoopTracerAllocsZero verifies that NoopTracer.Start causes no heap
// allocations beyond the baseline.
func TestNoopTracerAllocsZero(t *testing.T) {
	tr := obs.NoopTracer{}
	ctx := context.Background()

	allocs := testing.AllocsPerRun(100, func() {
		_, sp := tr.Start(ctx, "test")
		sp.End()
	})
	if allocs > 0 {
		t.Errorf("NoopTracer.Start allocated %v times, want 0", allocs)
	}
}

// --- TestPhaseEndsClean ---

// TestPhaseEndsClean verifies that StartPhase + End completes without panic.
func TestPhaseEndsClean(t *testing.T) {
	ctx := context.Background()
	ctx, ph := obs.StartPhase(ctx, "test-phase", obs.Str("key", "val"))
	_ = ctx
	ph.End() // must not panic
}

// TestPhaseFailClean verifies that StartPhase + Fail completes without panic.
func TestPhaseFailClean(t *testing.T) {
	ctx := context.Background()
	_, ph := obs.StartPhase(ctx, "failing-phase")
	ph.Fail(io.ErrUnexpectedEOF) // must not panic
}

// --- TestRunIDPropagation ---

// TestRunIDPropagation verifies that WithRunID and RunIDFromCtx round-trip.
func TestRunIDPropagation(t *testing.T) {
	ctx := context.Background()

	// No run ID set yet.
	if got := obs.RunIDFromCtx(ctx); got != "" {
		t.Errorf("expected empty run ID, got %q", got)
	}

	id := obs.NewRunID()
	if id == "" {
		t.Fatal("NewRunID returned empty string")
	}

	ctx = obs.WithRunID(ctx, id)
	if got := obs.RunIDFromCtx(ctx); got != id {
		t.Errorf("RunIDFromCtx = %q, want %q", got, id)
	}
}

// TestNewRunIDFormat verifies the UUID v4 format.
func TestNewRunIDFormat(t *testing.T) {
	id := obs.NewRunID()
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("expected 5 hyphen-separated parts, got %d in %q", len(parts), id)
	}
	lengths := []int{8, 4, 4, 4, 12}
	for i, p := range parts {
		if len(p) != lengths[i] {
			t.Errorf("part[%d] len = %d, want %d (id=%q)", i, len(p), lengths[i], id)
		}
	}
	// Version nibble must be '4'.
	if id[14] != '4' {
		t.Errorf("version nibble = %c, want '4' (id=%q)", id[14], id)
	}
}
