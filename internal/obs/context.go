// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs

import "context"

// loggerKey is the unexported context key for a Logger.
type loggerKey struct{}

// runIDKey is the unexported context key for a run ID string.
type runIDKey struct{}

// WithLogger stores l in ctx and returns the child context.
func WithLogger(ctx context.Context, l Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}

// FromCtx returns the Logger stored in ctx, or Global() if none is present.
func FromCtx(ctx context.Context) Logger {
	if l, ok := ctx.Value(loggerKey{}).(Logger); ok {
		return l
	}
	return Global()
}

// WithRunID stores id in ctx and returns the child context.
func WithRunID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, runIDKey{}, id)
}

// RunIDFromCtx returns the run ID stored in ctx, or "" if none is present.
func RunIDFromCtx(ctx context.Context) string {
	if id, ok := ctx.Value(runIDKey{}).(string); ok {
		return id
	}
	return ""
}
