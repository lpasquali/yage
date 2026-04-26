// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cli

import (
	_ "embed"
	"fmt"
	"io"
	"os"
)

// usageText is the binary's --help body. The bash script's own help is
// the comment block at the top of the original bash port extracted via
// `sed -n '2,300p'`; here we embed a Go-CLI-native rewrite that groups
// flags by topic, lists pivot/standalone sections explicitly, and ends
// with concrete examples. The bash script remains the canonical
// reference for default values and corner-case behavior.
//
//go:embed usage.txt
var usageText string

// PrintUsage writes the embedded usage block to the given writer.
// stdout when w is nil. Trailing newline is part of the embedded text.
func PrintUsage(w io.Writer) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprint(w, usageText)
}