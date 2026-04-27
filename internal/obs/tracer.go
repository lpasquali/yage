// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs

import "context"

// Span represents a single unit of work within a trace.
type Span interface {
	// End marks the span as completed successfully.
	End()
	// SetErr records an error on the span.
	SetErr(error)
	// Set attaches additional fields to the span.
	Set(...Field)
}

// Tracer creates and manages Spans.
type Tracer interface {
	// Start opens a new span as a child of ctx. It returns a child context
	// that carries the span plus the span itself.
	Start(ctx context.Context, name string, attrs ...Field) (context.Context, Span)
}

// NoopSpan is a Span implementation that does nothing and allocates nothing.
type NoopSpan struct{}

func (NoopSpan) End()        {}
func (NoopSpan) SetErr(error) {}
func (NoopSpan) Set(...Field) {}

// noopSpanVal is a package-level value used to return NoopSpan as a Span
// interface without an allocation on the heap.
var noopSpanVal Span = NoopSpan{}

// NoopTracer is a Tracer that creates NoopSpans and does not modify the
// context. It is the default tracer until SetTracer is called.
type NoopTracer struct{}

// Start returns the parent context unchanged and a zero-allocation NoopSpan.
func (NoopTracer) Start(ctx context.Context, _ string, _ ...Field) (context.Context, Span) {
	return ctx, noopSpanVal
}
