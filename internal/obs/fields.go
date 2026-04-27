// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package obs

import (
	"log/slog"
	"time"
)

// Field is a structured log attribute.
type Field = slog.Attr

// Str returns a string-valued Field.
func Str(k, v string) Field { return slog.String(k, v) }

// Int returns an int-valued Field.
func Int(k string, v int) Field { return slog.Int(k, v) }

// Bool returns a bool-valued Field.
func Bool(k string, v bool) Field { return slog.Bool(k, v) }

// Err returns a Field keyed "error" with the given error value.
// If err is nil the field value is the string "<nil>".
func Err(err error) Field { return slog.Any("error", err) }

// Dur returns a duration-valued Field.
func Dur(k string, d time.Duration) Field { return slog.Duration(k, d) }
