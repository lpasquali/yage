// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package orchestrator

import "time"

// timeNow is a var so tests can override. We don't need freezing at the
// moment but keeping the indirection is cheap.
var timeNow = time.Now