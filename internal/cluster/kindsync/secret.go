// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package kindsync pushes bootstrap state into kind Secrets (so a
// re-run finds the same state in the management cluster when local
// env is thin) and pulls it back on demand.
package kindsync