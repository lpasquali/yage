// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// LocalRunner implements Runner by executing `tofu` as a local subprocess
// using shell.RunWithEnv. It is used in dev/test and for orchestrator
// phases that run before a management cluster exists.
//
// State directory: ~/.yage/tofu-<module>/ (generalised from the legacy
// proxmox-identity-terraform path; see StateDir for the legacy variant).
type LocalRunner struct {
	// extraEnv holds additional environment variables injected into every
	// tofu invocation (e.g. provider-specific credentials).
	extraEnv []string
}

// NewLocalRunner returns a LocalRunner with no extra environment variables.
// Callers that need to pass credentials call WithEnv before Apply/Destroy.
func NewLocalRunner(extraEnv []string) *LocalRunner {
	if extraEnv == nil {
		extraEnv = []string{}
	}
	return &LocalRunner{extraEnv: extraEnv}
}

// moduleDirFor returns ~/.yage/tofu-<module>/
func moduleDirFor(module string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".yage", "tofu-"+module)
}

// Apply implements Runner. It creates the module state directory, then runs
// `tofu init -upgrade && tofu apply -auto-approve` with vars passed as
// `-var key=value` flags.
func (r *LocalRunner) Apply(ctx context.Context, module string, vars map[string]string) error {
	dir := moduleDirFor(module)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("local runner: mkdir %s: %w", dir, err)
	}
	logx.Log("LocalRunner: tofu init -upgrade for module %s ...", module)
	if err := shell.RunWithEnv(r.extraEnv, "tofu", "-chdir="+dir, "init", "-upgrade"); err != nil {
		return fmt.Errorf("local runner: tofu init (%s): %w", module, err)
	}
	args := []string{"tofu", "-chdir=" + dir, "apply", "-auto-approve"}
	for k, v := range vars {
		args = append(args, "-var", k+"="+v)
	}
	logx.Log("LocalRunner: tofu apply -auto-approve for module %s ...", module)
	if err := shell.RunWithEnv(r.extraEnv, args...); err != nil {
		return fmt.Errorf("local runner: tofu apply (%s): %w", module, err)
	}
	return nil
}

// Destroy implements Runner. Runs `tofu destroy -auto-approve` in the
// module state directory. No-op when the state directory does not exist.
func (r *LocalRunner) Destroy(ctx context.Context, module string) error {
	dir := moduleDirFor(module)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		logx.Warn("LocalRunner: state dir %s not found; skipping destroy.", dir)
		return nil
	}
	logx.Log("LocalRunner: tofu destroy -auto-approve for module %s ...", module)
	if err := shell.RunWithEnv(r.extraEnv, "tofu", "-chdir="+dir, "destroy", "-auto-approve"); err != nil {
		return fmt.Errorf("local runner: tofu destroy (%s): %w", module, err)
	}
	return nil
}

// Output implements Runner. Runs `tofu output -json` and returns the decoded
// map. Keys map to JSON-typed values (string, float64, bool, map, slice, etc.)
// as produced by encoding/json.Unmarshal.
func (r *LocalRunner) Output(_ context.Context, module string) (map[string]any, error) {
	dir := moduleDirFor(module)
	raw, _, err := shell.Capture("tofu", "-chdir="+dir, "output", "-json")
	if err != nil {
		return nil, fmt.Errorf("local runner: tofu output (%s): %w", module, err)
	}
	raw = strings.TrimSpace(raw)
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("local runner: parse tofu output json (%s): %w", module, err)
	}
	return result, nil
}
