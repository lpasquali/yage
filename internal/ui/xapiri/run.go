// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// run.go — small driver glue for the eight-step xapiri state
// machine. The Run() entry point itself lives in xapiri.go (next
// to the package doc + cultural framing); this file holds the
// exit helper that step methods funnel through and a couple of
// trivial re-exits used across forks.

import (
	"errors"
	"fmt"
)

// exit translates a step-method error into the process exit code
// Run() should return:
//
//   - nil         → 0 (the happy path; runFork already returned)
//   - ErrUserExit → 0 (user bailed cleanly; "the spirits rest")
//   - anything else → 1 (hard failure; the step has already
//                         printed a user-visible diagnostic, we
//                         only render the "nothing written" coda)
//
// We render the message at the call site — runFork callers want a
// coherent exit code more than a re-printed error string.
func (s *state) exit(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, ErrUserExit) {
		s.r.info("nothing written. the spirits rest.")
		return 0
	}
	fmt.Fprintf(s.w, "xapiri: %v\n", err)
	return 1
}