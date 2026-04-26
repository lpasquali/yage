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
	"regexp"
	"strconv"
	"strings"
	"unicode"
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

// promptSecret is the sensitive variant. We don't actually disable
// terminal echo — that requires platform-specific code (golang.org/x/term)
// and the spec explicitly asks only that we mark them sensitive *for
// later display*. The prompt label gets a "(sensitive)" hint so the
// user knows to be careful; the review step masks the value.
func (r *reader) promptSecret(label, cur string) string {
	display := ""
	if cur != "" {
		display = "set"
	} else {
		display = "unset"
	}
	fmt.Fprintf(r.out, "  %s (sensitive) [%s]: ", label, display)
	v := r.readLine()
	if v == "" {
		return cur
	}
	return v
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

// maskValue returns a fixed-width placeholder for sensitive values
// when echoing the resolved config back to the user. Empty stays empty
// so the review knows the field was never set.
func maskValue(v string) string {
	if v == "" {
		return "(unset)"
	}
	return "********"
}
