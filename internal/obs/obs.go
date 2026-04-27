// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package obs provides structured observability primitives for yage: a
// Logger, a Tracer, Phase helpers, and context propagation utilities.
//
// By default the global Logger uses a PrettyHandler whose output is visually
// identical to the legacy logx helpers:
//
//	Info  → "✅ 🎉 <msg>\n"  on stdout
//	Warn  → "⚠️ 🙈 <msg>\n"  on stderr
//	Error → "❌ 💩 <msg>\n"  on stderr
//
// Call SetGlobal or SetTracer to swap in a different backend (e.g. an OTEL
// exporter) without touching call sites.
package obs

import (
	"log/slog"
	"sync"
)

var (
	globalMu     sync.RWMutex
	globalLogger Logger
	globalTracer Tracer = NoopTracer{}
)

func init() {
	globalLogger = newLogger(slog.New(NewPrettyHandler()))
}

// Global returns the process-wide Logger.
func Global() Logger {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalLogger
}

// SetGlobal replaces the process-wide Logger.
func SetGlobal(l Logger) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalLogger = l
}

// SetTracer replaces the process-wide Tracer used by StartPhase.
func SetTracer(t Tracer) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalTracer = t
}

// globalTracerVal reads the current tracer under the read lock.
func globalTracerVal() Tracer {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalTracer
}
