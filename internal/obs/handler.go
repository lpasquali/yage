// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// PrettyHandler is a slog.Handler that emits emoji-prefixed, human-readable
// log lines to the console.
//
// Output format (no attrs):
//
//	Info:  "✅ 🎉 <msg>\n"   → os.Stdout
//	Warn:  "⚠️ 🙈 <msg>\n"   → os.Stderr
//	Error: "❌ 💩 <msg>\n"   → os.Stderr
//
// When key=value attributes are present they are appended after the message:
//
//	"✅ 🎉 <msg>  key=value …\n"
//
// Color ANSI codes are emitted only when the output is a TTY and the NO_COLOR
// environment variable is not set.
type PrettyHandler struct {
	mu      sync.Mutex
	out     io.Writer // primary writer (stdout for Info)
	errOut  io.Writer // writer for Warn/Error
	noColor bool
	attrs   []slog.Attr
	group   string
}

// NewPrettyHandler creates a PrettyHandler that writes Info to stdout and
// Warn/Error to stderr.  Color is auto-detected via TTY check and NO_COLOR.
func NewPrettyHandler() *PrettyHandler {
	return NewPrettyHandlerWithWriters(os.Stdout, os.Stderr, !isTTY(os.Stdout) || os.Getenv("NO_COLOR") != "")
}

// NewPrettyHandlerWithWriters creates a PrettyHandler with explicit writers and
// color setting.  out receives Info-level records; errOut receives Warn and
// Error records.  Set noColor=true to suppress ANSI escape codes.
//
// This constructor is intended for testing; production code should use
// NewPrettyHandler.
func NewPrettyHandlerWithWriters(out, errOut io.Writer, noColor bool) *PrettyHandler {
	return &PrettyHandler{
		out:     out,
		errOut:  errOut,
		noColor: noColor,
	}
}

// isTTY reports whether w is a character device (a real terminal).
func isTTY(w *os.File) bool {
	fi, err := w.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Enabled reports whether the handler handles records at the given level.
// PrettyHandler handles Info and above.
func (h *PrettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

// Handle formats and writes the log record.
func (h *PrettyHandler) Handle(_ context.Context, r slog.Record) error {
	var prefix, color, reset string

	switch {
	case r.Level >= slog.LevelError:
		prefix = "❌ 💩"
		if !h.noColor {
			color = "\x1b[31m" // red
			reset = "\x1b[0m"
		}
	case r.Level >= slog.LevelWarn:
		prefix = "⚠️ 🙈"
		if !h.noColor {
			color = "\x1b[33m" // yellow
			reset = "\x1b[0m"
		}
	default:
		prefix = "✅ 🎉"
		if !h.noColor {
			color = "\x1b[32m" // green
			reset = "\x1b[0m"
		}
	}

	// Collect all attrs (handler-level + record-level).
	allAttrs := make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs())
	allAttrs = append(allAttrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		allAttrs = append(allAttrs, a)
		return true
	})

	var buf bytes.Buffer
	buf.WriteString(color)
	buf.WriteString(prefix)
	buf.WriteByte(' ')
	buf.WriteString(r.Message)

	if len(allAttrs) > 0 {
		for _, a := range allAttrs {
			buf.WriteString("  ")
			if h.group != "" {
				buf.WriteString(h.group)
				buf.WriteByte('.')
			}
			buf.WriteString(a.Key)
			buf.WriteByte('=')
			buf.WriteString(fmt.Sprintf("%v", a.Value.Any()))
		}
	}

	buf.WriteString(reset)
	buf.WriteByte('\n')

	// Choose the right writer based on level.
	var w io.Writer
	if r.Level >= slog.LevelWarn {
		w = h.errOut
	} else {
		w = h.out
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := w.Write(buf.Bytes())
	return err
}

// WithAttrs returns a new handler whose output includes the given attributes.
func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	merged := make([]slog.Attr, len(h.attrs)+len(attrs))
	copy(merged, h.attrs)
	copy(merged[len(h.attrs):], attrs)
	return &PrettyHandler{
		out:     h.out,
		errOut:  h.errOut,
		noColor: h.noColor,
		attrs:   merged,
		group:   h.group,
	}
}

// WithGroup returns a new handler that qualifies key names with the given group.
func (h *PrettyHandler) WithGroup(name string) slog.Handler {
	h.mu.Lock()
	defer h.mu.Unlock()
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &PrettyHandler{
		out:     h.out,
		errOut:  h.errOut,
		noColor: h.noColor,
		attrs:   h.attrs,
		group:   g,
	}
}

// Ensure PrettyHandler implements slog.Handler at compile time.
var _ slog.Handler = (*PrettyHandler)(nil)
