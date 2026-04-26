// Package xapiri implements yage's interactive configuration TUI,
// invoked via `yage --xapiri`.
//
// xapiri are sacred spirits in the Yanomami people's cosmology.
// yage runs xapiri to get help from the spirits to create a
// visionary deployment — an interactive walkthrough that surfaces
// every config knob, validates choices against the active provider,
// and writes the result to disk before any state is changed on the
// target cloud.
//
// This package is currently a stub: Run prints a notice and exits.
// The full TUI is planned but not yet wired.
package xapiri

import (
	"fmt"
	"io"

	"github.com/lpasquali/yage/internal/config"
)

// Run starts the interactive configuration walkthrough. Today it
// prints a placeholder notice; the eventual implementation will
// drive a multi-step prompt and write the resulting config to a
// file the user can review before invoking yage without --xapiri.
func Run(w io.Writer, cfg *config.Config) int {
	fmt.Fprintln(w, "🌿 xapiri — interactive configuration TUI")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "    Not yet implemented. The xapiri are still arriving.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "    For now, configure yage via env vars or CLI flags.")
	fmt.Fprintln(w, "    See `yage --help` for the full surface.")
	return 0
}
