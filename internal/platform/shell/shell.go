// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package shell provides RUN_PRIVILEGED root-elevation and small exec
// helpers (capture stdout, run with tty, etc.). RunPrivileged invokes
// the command directly when the process already runs as root,
// otherwise re-invokes it via sudo.
package shell

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// kindNodeImageOverride / SetKindNodeImage / injectKindImage —
// surface for the §17 / §21.4 --node-image airgap flag. The
// orchestrator's `kind create cluster …` call site lives several
// layers up; rather than thread a config through every call, we
// stash the override here and append `--image <override>` whenever
// shell-level Run/RunWithEnv sees a kind-create-cluster argv. Idempotent
// (won't double-inject if --image is already present).
var (
	kindNodeImageMu       sync.RWMutex
	kindNodeImageOverride string
)

// SetKindNodeImage records the kind worker image override. Set
// once at startup from cmd/yage/main.go (or airgap.Apply); cleared
// by passing "".
func SetKindNodeImage(s string) {
	kindNodeImageMu.Lock()
	defer kindNodeImageMu.Unlock()
	kindNodeImageOverride = s
}

func injectKindImage(argv []string) []string {
	kindNodeImageMu.RLock()
	img := kindNodeImageOverride
	kindNodeImageMu.RUnlock()
	if img == "" || len(argv) < 3 {
		return argv
	}
	if argv[0] != "kind" || argv[1] != "create" || argv[2] != "cluster" {
		return argv
	}
	for _, a := range argv {
		if a == "--image" {
			return argv // operator already pinned a specific image; respect it
		}
	}
	return append(argv, "--image", img)
}

// Privileged prepends "sudo" when the current process is not running as root.
// Returns the argv to hand to exec.Command.
func Privileged(argv ...string) []string {
	if os.Geteuid() == 0 {
		return argv
	}
	return append([]string{"sudo"}, argv...)
}

// Run executes argv, streaming stdout/stderr through to the parent.
// Returns the exit error (nil on success).
func Run(argv ...string) error {
	if len(argv) == 0 {
		return nil
	}
	argv = injectKindImage(argv)
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	return c.Run()
}

// RunPrivileged runs argv, escalating via sudo if the caller is not root.
func RunPrivileged(argv ...string) error {
	return Run(Privileged(argv...)...)
}

// RunWithEnv runs argv with the given environment, inheriting os.Environ()
// and appending extra. Used for commands that need specific env vars set
// (e.g. clusterctl init with EXP_CLUSTER_RESOURCE_SET).
func RunWithEnv(extra []string, argv ...string) error {
	if len(argv) == 0 {
		return nil
	}
	argv = injectKindImage(argv)
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	c.Env = append(os.Environ(), extra...)
	return c.Run()
}

// Capture runs argv, returning (stdout, stderr, err).
// Trailing whitespace is NOT stripped; callers that need it should trim.
func Capture(argv ...string) (string, string, error) {
	if len(argv) == 0 {
		return "", "", nil
	}
	c := exec.Command(argv[0], argv[1:]...)
	var out, errBuf bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errBuf
	err := c.Run()
	return out.String(), errBuf.String(), err
}

// CaptureIn runs argv in dir, returning (stdout, stderr, err). Same
// contract as Capture but with a working directory override — useful
// for git commands that should operate on a specific repo.
func CaptureIn(dir string, argv ...string) (string, string, error) {
	if len(argv) == 0 {
		return "", "", nil
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Dir = dir
	var out, errBuf bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errBuf
	err := c.Run()
	return out.String(), errBuf.String(), err
}

// CaptureOut returns stdout trimmed of surrounding whitespace.
// Errors and stderr are ignored — callers should use Capture() when they
// care about diagnostics.
func CaptureOut(argv ...string) string {
	out, _, _ := Capture(argv...)
	return strings.TrimSpace(out)
}

// Pipe runs argv with `stdin` piped on standard input, and the child's
// stdout/stderr streamed through. Used for `kubectl apply -f -` style calls.
func Pipe(stdin string, argv ...string) error {
	if len(argv) == 0 {
		return nil
	}
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = strings.NewReader(stdin)
	return c.Run()
}

// CommandExists reports whether `name` is resolvable on $PATH.
// Mirrors `command -v NAME >/dev/null 2>&1`.
func CommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// RequireCmd dies with the standard die() format when `name` is not
// on $PATH.
func RequireCmd(name string) {
	if !CommandExists(name) {
		die("Required command not found on PATH: " + name)
	}
}

// RequireFile dies with the standard die() format when `path` is not
// a regular file.
func RequireFile(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		die("Required file not found: " + path)
	}
}

// die is a minimal duplicate of logx.Die used by Require* helpers to avoid
// a circular import (logx -> shell would reintroduce the cycle the Die
// helper was set up to break).
func die(msg string) {
	_, _ = os.Stderr.WriteString("❌ 💩 " + msg + "\n")
	os.Exit(1)
}

// DiscardStderr wraps stderr so a tool's noisy output is silenced while
// still returning an error on non-zero exit.
func DiscardStderr(err error) error {
	// intentionally a pass-through for now; callers use Capture() when
	// they want to swallow stderr selectively.
	return err
}

// CopyBuffer copies src -> dst until EOF or error. Re-exported to avoid
// callers importing io directly for one-liners.
func CopyBuffer(dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, src)
}