// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs

import (
	"context"
	"time"
)

// Phase represents a named, timed phase of work within a run.  It captures a
// Logger, a Span, and a start time so that callers can report duration and
// outcome with a single method call.
//
// Note: the constructor is named StartPhase (not Phase) to avoid a name
// collision between the type and the constructor function in the same package.
type Phase struct {
	name  string
	start time.Time
	span  Span
	ctx   context.Context
	log   Logger
}

// StartPhase begins a named phase: it logs an Info entry, opens a span via the
// global Tracer, and returns a child context that carries the span plus the
// *Phase value.
//
// Callers must close the phase with either End or Fail:
//
//	ctx, ph := obs.StartPhase(ctx, "bootstrap", obs.Str("env", env))
//	if err := doWork(ctx); err != nil {
//	    ph.Fail(err)
//	    return err
//	}
//	ph.End()
func StartPhase(ctx context.Context, name string, attrs ...Field) (context.Context, *Phase) {
	log := FromCtx(ctx)
	log.Info(name, attrs...)

	childCtx, span := globalTracerVal().Start(ctx, name, attrs...)

	return childCtx, &Phase{
		name:  name,
		start: time.Now(),
		span:  span,
		ctx:   childCtx,
		log:   log,
	}
}

// End marks the phase as successfully complete.  It logs elapsed time at Info
// and closes the underlying span.
func (p *Phase) End() {
	elapsed := time.Since(p.start)
	p.log.Info(p.name+" done", Dur("elapsed", elapsed))
	p.span.End()
}

// Fail marks the phase as failed.  It logs the error at Error level, records
// the error on the span, and closes the span.
func (p *Phase) Fail(err error) {
	elapsed := time.Since(p.start)
	p.log.Error(p.name+" failed", err, Dur("elapsed", elapsed))
	p.span.SetErr(err)
	p.span.End()
}
