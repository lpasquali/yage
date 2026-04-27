// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs

import (
	"context"
	"log/slog"
)

// Logger wraps *slog.Logger to expose a Field-based API that matches the rest
// of the obs package.
type Logger struct {
	l *slog.Logger
}

// newLogger creates a Logger from a slog.Logger.
func newLogger(sl *slog.Logger) Logger { return Logger{l: sl} }

// NewLoggerFromHandler creates a Logger backed by the given slog.Handler.
// This is useful for injecting custom handlers (e.g. in tests).
func NewLoggerFromHandler(h slog.Handler) Logger {
	return newLogger(slog.New(h))
}

// Info logs a message at INFO level.
func (l Logger) Info(msg string, attrs ...slog.Attr) {
	l.l.LogAttrs(context.Background(), slog.LevelInfo, msg, attrs...)
}

// Warn logs a message at WARN level.
func (l Logger) Warn(msg string, attrs ...slog.Attr) {
	l.l.LogAttrs(context.Background(), slog.LevelWarn, msg, attrs...)
}

// Error logs a message at ERROR level.  err may be nil.
func (l Logger) Error(msg string, err error, attrs ...slog.Attr) {
	if err != nil {
		attrs = append([]slog.Attr{Err(err)}, attrs...)
	}
	l.l.LogAttrs(context.Background(), slog.LevelError, msg, attrs...)
}

// With returns a new Logger that always includes the given attributes.
func (l Logger) With(attrs ...slog.Attr) Logger {
	args := make([]any, len(attrs))
	for i, a := range attrs {
		args[i] = a
	}
	return Logger{l: l.l.With(args...)}
}
