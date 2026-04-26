// Package plan defines the Writer seam every Provider's plan-body
// hook prints through. It exists so the orchestrator's dry-run plan
// output stays decoupled from any specific provider's text: the
// orchestrator owns the section ordering and the cross-cutting
// blocks (Capacity / Cost / Allocations); each Provider owns the
// bullets inside its own Identity / Workload / Pivot sections via
// PlanDescriber (see internal/provider).
//
// Three concrete renderers ship with the package:
//
//   - TextWriter writes to an io.Writer in the same Unicode marker
//     style the bash-era plan used (▸ section, • bullet, ◦ skip:).
//     This is what the production --dry-run path constructs.
//   - CapturingWriter records every call as a structured Event.
//     Snapshot tests in internal/orchestrator use it to assert plan
//     output without re-parsing the rendered text.
//   - DiscardWriter swallows every call. Handy for paths that need
//     a non-nil Writer but don't want output (e.g. plumbing tests).
//
// Skip semantics (per docs/abstraction-plan.md §8 "Skip semantics:
// two-layer"):
//
//   - When a provider's section title is meaningful but its body is
//     skipped this run, the provider calls w.Skip("reason") AFTER
//     calling w.Section("Title"). Renders as "◦ skip: reason".
//   - When a section doesn't apply at all to this provider, the
//     provider simply doesn't call w.Section() — nothing is emitted.
//
// The Writer methods return nothing: rendering errors (e.g. a
// closed pipe) are not actionable from inside a Describe* hook.
// TextWriter swallows them to keep the call sites uncluttered.
package plan

import (
	"fmt"
	"io"
)

// Writer is the seam between the orchestrator's structured plan
// output and per-provider DescribePlan implementations. Three
// primitives: section header, bullet, skip.
type Writer interface {
	// Section emits a section header ("▸ <title>"). Sections never
	// nest; every Bullet/Skip call afterward belongs to the most
	// recent Section until another Section call replaces it.
	Section(title string)
	// Bullet emits a bullet point under the current section. fmt.Sprintf
	// is applied to (format, args...).
	Bullet(format string, args ...any)
	// Skip emits a "◦ skip: <reason>" note under the current section.
	// Same fmt.Sprintf convention as Bullet.
	Skip(format string, args ...any)
}

// NewTextWriter returns a Writer that renders each call to the
// given io.Writer using yage's plan marker style.
func NewTextWriter(w io.Writer) Writer { return &textWriter{w: w} }

type textWriter struct{ w io.Writer }

func (t *textWriter) Section(title string) {
	fmt.Fprintf(t.w, "\n▸ %s\n", title)
}
func (t *textWriter) Bullet(format string, args ...any) {
	fmt.Fprintf(t.w, "    • "+format+"\n", args...)
}
func (t *textWriter) Skip(format string, args ...any) {
	fmt.Fprintf(t.w, "    ◦ skip: "+format+"\n", args...)
}

// EventKind discriminates the three primitives.
type EventKind int

const (
	EventSection EventKind = iota
	EventBullet
	EventSkip
)

func (k EventKind) String() string {
	switch k {
	case EventSection:
		return "Section"
	case EventBullet:
		return "Bullet"
	case EventSkip:
		return "Skip"
	default:
		return "?"
	}
}

// Event is a single recorded call on a CapturingWriter. Tests
// inspect Events to assert plan output without re-parsing rendered
// text.
type Event struct {
	Kind EventKind
	// Text is the title (Section) or the formatted body (Bullet/Skip).
	Text string
}

// CapturingWriter is an in-memory Writer that records every call.
// Used by snapshot tests in internal/orchestrator.
type CapturingWriter struct {
	Events []Event
}

// NewCapturingWriter returns a fresh CapturingWriter.
func NewCapturingWriter() *CapturingWriter { return &CapturingWriter{} }

func (c *CapturingWriter) Section(title string) {
	c.Events = append(c.Events, Event{Kind: EventSection, Text: title})
}
func (c *CapturingWriter) Bullet(format string, args ...any) {
	c.Events = append(c.Events, Event{Kind: EventBullet, Text: fmt.Sprintf(format, args...)})
}
func (c *CapturingWriter) Skip(format string, args ...any) {
	c.Events = append(c.Events, Event{Kind: EventSkip, Text: fmt.Sprintf(format, args...)})
}

// Sections returns the section titles in order, useful for asserting
// "did the right phases get printed at all" without checking every
// bullet.
func (c *CapturingWriter) Sections() []string {
	out := make([]string, 0, len(c.Events))
	for _, e := range c.Events {
		if e.Kind == EventSection {
			out = append(out, e.Text)
		}
	}
	return out
}

// NewDiscardWriter returns a Writer that swallows every call.
func NewDiscardWriter() Writer { return discardWriter{} }

type discardWriter struct{}

func (discardWriter) Section(string)             {}
func (discardWriter) Bullet(string, ...any)      {}
func (discardWriter) Skip(string, ...any)        {}
