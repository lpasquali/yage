// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// logring.go — thread-safe ring buffer for capturing log output.
// Implements io.Writer; the dashboard reads it via Lines() for the
// [logs] tab and Subscribe() for change notifications.

import (
	"sync"
)

const logRingCap = 500

// logRing is a thread-safe ring buffer that holds up to logRingCap lines of
// plain-text log output.  It implements io.Writer so it can be plugged into
// any slog handler or io.MultiWriter.
type logRing struct {
	mu   sync.Mutex
	buf  [logRingCap]string
	head int // index of the next slot to write
	size int // number of valid entries (≤ logRingCap)

	subsMu sync.Mutex
	subs   []chan struct{}
}

// Write appends the bytes as one or more lines (split on '\n') to the ring.
// Empty lines produced by a trailing '\n' are discarded.
// Implements io.Writer; always returns len(p), nil.
func (r *logRing) Write(p []byte) (int, error) {
	// Split on newlines.
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '\n' {
			if i > start {
				r.appendLine(string(p[start:i]))
			}
			start = i + 1
		}
	}
	return len(p), nil
}

// appendLine adds a single line to the ring and notifies subscribers.
func (r *logRing) appendLine(line string) {
	r.mu.Lock()
	r.buf[r.head] = line
	r.head = (r.head + 1) % logRingCap
	if r.size < logRingCap {
		r.size++
	}
	r.mu.Unlock()

	r.notify()
}

// Lines returns a snapshot of all buffered lines in insertion order
// (oldest first).
func (r *logRing) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size == 0 {
		return nil
	}
	out := make([]string, r.size)
	start := (r.head - r.size + logRingCap) % logRingCap
	for i := 0; i < r.size; i++ {
		out[i] = r.buf[(start+i)%logRingCap]
	}
	return out
}

// Subscribe returns a channel that receives an empty struct whenever new
// lines are appended.  The channel is buffered (capacity 1) so a slow
// reader does not block writers.  Call Unsubscribe when done.
func (r *logRing) Subscribe() <-chan struct{} {
	ch := make(chan struct{}, 1)
	r.subsMu.Lock()
	r.subs = append(r.subs, ch)
	r.subsMu.Unlock()
	return ch
}

// Unsubscribe removes a channel previously returned by Subscribe.
func (r *logRing) Unsubscribe(ch <-chan struct{}) {
	r.subsMu.Lock()
	defer r.subsMu.Unlock()
	for i, s := range r.subs {
		if s == ch {
			r.subs = append(r.subs[:i], r.subs[i+1:]...)
			return
		}
	}
}

// notify sends a non-blocking signal to all subscribers.
func (r *logRing) notify() {
	r.subsMu.Lock()
	defer r.subsMu.Unlock()
	for _, ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
