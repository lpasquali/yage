package cli

import (
	_ "embed"
	"fmt"
	"io"
	"os"
)

// Bash usage() { sed -n '2,300p' "${BASH_SOURCE[0]}"; } prints the header
// comment block from the script itself. We embed the equivalent text at
// build time so the Go port prints the same usage message.
//
//go:embed usage.txt
var usageText string

// PrintUsage writes the embedded usage block to the given writer.
func PrintUsage(w io.Writer) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprint(w, usageText)
}
