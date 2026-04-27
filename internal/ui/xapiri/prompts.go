// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// prompts.go — small bufio-backed helpers used by the xapiri walkthrough.
// The TUI is intentionally minimal: a quiet, line-oriented walkthrough
// that fits the calm, ceremonial tone of the package doc.
//
// We deliberately avoid pulling in a TUI library — bufio.Scanner +
// fmt.Fprintf is enough to drive every prompt the spec asks for and
// keeps yage's dependency footprint small.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/term"
)

// dns1123label is the validation regexp for Kubernetes-style names
// (cluster names, namespaces). Lowercase alphanumeric + hyphens; must
// start and end with an alphanumeric. Length is capped at 63 chars.
var dns1123label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// reader bundles the io.Reader + io.Writer the walkthrough drives. We
// keep the writer for prompts (so the spirits speak on stdout) and a
// scanner for line-by-line input. Bufio is fine — interactive use is
// human-paced.
type reader struct {
	in  *bufio.Scanner
	out io.Writer
}

func newReader(in io.Reader, out io.Writer) *reader {
	s := bufio.NewScanner(in)
	// Allow long lines (kubeconfig fragments, JSON tokens) without
	// silently truncating.
	s.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return &reader{in: s, out: out}
}

// readLine reads a single line of input. Empty input (EOF or blank
// enter) returns "" — callers decide whether to keep the existing
// default in that case.
func (r *reader) readLine() string {
	if !r.in.Scan() {
		return ""
	}
	return strings.TrimRight(r.in.Text(), "\r\n")
}

// promptString asks for a value, optionally showing the current
// default. Empty input keeps cur. The trailing colon + space mirrors
// the existing promptx style.
func (r *reader) promptString(label, cur string) string {
	if cur != "" {
		fmt.Fprintf(r.out, "  %s [%s]: ", label, cur)
	} else {
		fmt.Fprintf(r.out, "  %s: ", label)
	}
	v := r.readLine()
	if v == "" {
		return cur
	}
	return v
}

// promptSecret is the sensitive variant used by the reflection-based
// step6 (cloud providers / generic path). The prompt label gets a
// "(sensitive)" hint so the user knows to be careful; the review step
// masks the value.  Echo is NOT disabled here — promptSecretHidden
// does that for the Proxmox-specific flow.
func (r *reader) promptSecret(label, cur string) string {
	display := "unset"
	if cur != "" {
		display = "set"
	}
	fmt.Fprintf(r.out, "  %s (sensitive) [%s]: ", label, display)
	v := r.readLine()
	if v == "" {
		return cur
	}
	return v
}

// promptSecretHidden disables terminal echo while the operator types a
// sensitive value (token, password, API secret).  When stdin is not a
// TTY (pipes, tests, CI) it falls back to the plain visible read so
// automation can still drive the wizard.  Returns the entered value
// (or cur on empty input) and any I/O error that isn't EOF.
func (r *reader) promptSecretHidden(label, cur string) (string, error) {
	display := "unset"
	if cur != "" {
		display = "set"
	}
	fmt.Fprintf(r.out, "  %s (hidden) [%s]: ", label, display)

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(r.out) // move past the prompt line after hidden input
		if err != nil {
			return cur, err
		}
		v := strings.TrimRight(string(raw), "\r\n")
		if v == "" {
			return cur, nil
		}
		return v, nil
	}
	// Non-TTY fallback: visible read (CI, piped tests).
	v := r.readLine()
	if v == "" {
		return cur, nil
	}
	return v, nil
}

// promptInt asks for a non-negative integer. Empty input keeps cur;
// invalid input re-prompts.
func (r *reader) promptInt(label, cur string) string {
	for {
		v := r.promptString(label, cur)
		if v == "" {
			return cur
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			fmt.Fprintf(r.out, "    not a non-negative integer; try again.\n")
			continue
		}
		return strconv.Itoa(n)
	}
}

// promptDNSLabel validates against DNS-1123 label rules and re-prompts
// on rejection. Empty input keeps cur.
func (r *reader) promptDNSLabel(label, cur string) string {
	for {
		v := r.promptString(label, cur)
		if v == "" {
			return cur
		}
		if len(v) > 63 || !dns1123label.MatchString(v) {
			fmt.Fprintf(r.out, "    not a DNS-1123 label (lowercase alphanumeric + hyphens, max 63); try again.\n")
			continue
		}
		return v
	}
}

// promptYesNo returns true/false. Empty input returns def; anything
// starting with y/Y is true, n/N is false. Re-prompts on garbage.
func (r *reader) promptYesNo(label string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(r.out, "  %s [%s]: ", label, hint)
		v := strings.TrimSpace(r.readLine())
		if v == "" {
			return def
		}
		switch unicode.ToLower(rune(v[0])) {
		case 'y':
			return true
		case 'n':
			return false
		}
		fmt.Fprintf(r.out, "    please answer yes or no.\n")
	}
}

// promptChoice prints a numbered menu and returns the chosen value.
// Empty input keeps cur (when cur is in the list); otherwise re-prompts.
func (r *reader) promptChoice(label string, choices []string, cur string) string {
	if len(choices) == 0 {
		return cur
	}
	fmt.Fprintf(r.out, "  %s\n", label)
	for i, c := range choices {
		marker := " "
		if c == cur {
			marker = "*"
		}
		fmt.Fprintf(r.out, "    %s %d) %s\n", marker, i+1, c)
	}
	for {
		hint := ""
		if cur != "" {
			hint = fmt.Sprintf(" [%s]", cur)
		}
		fmt.Fprintf(r.out, "  pick 1-%d%s: ", len(choices), hint)
		v := strings.TrimSpace(r.readLine())
		if v == "" && cur != "" {
			return cur
		}
		// Accept the literal value too (e.g. "aws") not just the index.
		for _, c := range choices {
			if c == v {
				return c
			}
		}
		n, err := strconv.Atoi(v)
		if err == nil && n >= 1 && n <= len(choices) {
			return choices[n-1]
		}
		fmt.Fprintf(r.out, "    not a valid choice; try again.\n")
	}
}

// section prints a small banner ahead of each walkthrough step. Quiet,
// not corporate — matches the package doc tone.
func (r *reader) section(title string) {
	fmt.Fprintf(r.out, "\n— %s —\n", title)
}

// info prints a single informational line.
func (r *reader) info(format string, args ...any) {
	fmt.Fprintf(r.out, "  %s\n", fmt.Sprintf(format, args...))
}

// promptSSHKeys collects one or more SSH public keys, one per line,
// until the user enters an empty line.  Returns the keys joined with
// newlines (the format cloud-init and authorized_keys both expect).
// Pressing enter immediately keeps the existing value (cur) unchanged.
func (r *reader) promptSSHKeys(cur string) string {
	fmt.Fprintf(r.out, "\n  SSH public key(s) for VM cloud-init\n")
	fmt.Fprintf(r.out, "  enter one key per line; empty line to finish")
	if cur != "" {
		// Show the count of already-configured keys as a hint.
		n := len(strings.Split(strings.TrimSpace(cur), "\n"))
		fmt.Fprintf(r.out, " [%d key(s) already set — empty line keeps them]", n)
	}
	fmt.Fprintln(r.out)

	var keys []string
	for {
		fmt.Fprintf(r.out, "  key %d: ", len(keys)+1)
		line := strings.TrimRight(r.readLine(), "\r\n ")
		if line == "" {
			break
		}
		keys = append(keys, line)
	}
	if len(keys) == 0 {
		return cur // keep existing
	}
	return strings.Join(keys, "\n")
}

// maskValue returns a fixed-width placeholder for sensitive values
// when echoing the resolved config back to the user. Empty stays empty
// so the review knows the field was never set.
func maskValue(v string) string {
	if v == "" {
		return "(unset)"
	}
	return "********"
}