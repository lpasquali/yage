// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cli

import (
	"embed"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// helpFS holds every per-topic help body. Each file is one topic; the
// filename minus ".txt" is the topic name. index.txt is the default
// landing page when --help is invoked without a topic argument.
//
//go:embed help/*.txt
var helpFS embed.FS

// PrintHelp writes the named topic body to w. When topic is "" or unknown,
// the index page is written instead. An "unknown topic %q" notice is
// prepended (one line) when an unknown topic was requested. stdout is
// used when w is nil.
func PrintHelp(w io.Writer, topic string) {
	if w == nil {
		w = os.Stdout
	}
	topic = strings.ToLower(strings.TrimSpace(topic))
	if topic == "" {
		writeTopic(w, "index")
		return
	}
	if _, err := helpFS.ReadFile("help/" + topic + ".txt"); err != nil {
		fmt.Fprintf(w, "unknown topic %q — showing index\n\n", topic)
		writeTopic(w, "index")
		return
	}
	writeTopic(w, topic)
}

// PrintUsage is a thin wrapper around PrintHelp that prints the index
// page. Kept for source compatibility with older callers.
func PrintUsage(w io.Writer) {
	PrintHelp(w, "")
}

// HelpTopics returns the sorted list of available topic names (filename
// minus the .txt extension). Used by the bash completion script to
// suggest completions for `yage --help <TAB>`.
func HelpTopics() []string {
	entries, err := helpFS.ReadDir("help")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".txt") {
			continue
		}
		out = append(out, strings.TrimSuffix(name, ".txt"))
	}
	sort.Strings(out)
	return out
}

func writeTopic(w io.Writer, topic string) {
	body, err := helpFS.ReadFile("help/" + topic + ".txt")
	if err != nil {
		fmt.Fprintf(w, "help topic %q is missing from the embedded FS\n", topic)
		return
	}
	if _, err := w.Write(body); err != nil {
		return
	}
	if len(body) > 0 && body[len(body)-1] != '\n' {
		fmt.Fprintln(w)
	}
}
