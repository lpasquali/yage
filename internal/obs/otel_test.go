// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs

import "testing"

// TestOTELTracerImplementsInterface is a compile-time assertion that
// OTELTracer and OTELSpan satisfy the obs.Tracer and obs.Span interfaces.
// If either interface is not satisfied this file will not compile.
func TestOTELTracerImplementsInterface(t *testing.T) {
	t.Helper()
	var _ Tracer = OTELTracer{}
	var _ Span = OTELSpan{}
}
