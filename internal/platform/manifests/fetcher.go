// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package manifests resolves and renders YAML templates from the in-cluster
// yage-repos PVC populated by opentofux.EnsureRepoSync (ADR 0010 §2).
//
// Templates live in the lpasquali/yage-manifests repository, cloned to
// <MountRoot>/yage-manifests/ on the PVC. The Fetcher reads each .yaml.tmpl
// from that path and executes it with text/template using the per-group
// wrapper structs defined in internal/capi/templates/ (ADR 0012 §3).
//
// Templates are executed with Option("missingkey=error"): an undeclared
// field on the data value is a hard error rather than the stdlib default
// "<no value>" placeholder. This surfaces yage / yage-manifests version
// skew at render time, before half-formed YAML reaches the apiserver
// (ADR 0012 §4).
package manifests

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

// defaultMountRoot is the in-pod path at which the yage-repos PVC is
// mounted by yage-managed Jobs. yage-manifests content lives at
// <defaultMountRoot>/yage-manifests/.
const defaultMountRoot = "/repos"

// manifestsSubdir is the per-repo subdirectory under MountRoot that holds
// the yage-manifests checkout produced by EnsureRepoSync.
const manifestsSubdir = "yage-manifests"

// Fetcher resolves yage-manifests template paths from the in-cluster
// yage-repos PVC and renders them via text/template.
//
// Production callers run inside a Job that mounts the PVC at /repos and
// leave MountRoot zero; tests and dev tools may set MountRoot to a local
// fixture directory that mirrors the PVC layout.
type Fetcher struct {
	// MountRoot is the path at which the yage-repos PVC is mounted in
	// the current pod. Empty means defaultMountRoot ("/repos").
	MountRoot string

	// funcs holds functions registered via RegisterFunc.
	funcs template.FuncMap
}

// NewFetcher constructs a Fetcher with MountRoot left at its default
// (resolved to /repos at render time). Callers that need a non-default
// mount point may set MountRoot directly on the returned value.
func NewFetcher() *Fetcher {
	return &Fetcher{}
}

// RegisterFunc registers fn under name in the Fetcher's internal FuncMap.
// The function is applied on every subsequent Render call.
// RegisterFunc is the only permitted extension point for adding template
// functions (ADR 0012 §4.1). Future additions require an ADR amendment.
func (f *Fetcher) RegisterFunc(name string, fn any) {
	if f.funcs == nil {
		f.funcs = make(template.FuncMap)
	}
	f.funcs[name] = fn
}

// Render reads the .yaml.tmpl at templatePath (relative to the
// yage-manifests repository root on the PVC) and executes it with data
// via text/template using Option("missingkey=error").
//
// Returned errors wrap the underlying cause (file read, parse, execute)
// with the resolved on-disk path so log output points operators at the
// exact PVC location.
func (f *Fetcher) Render(templatePath string, data any) (string, error) {
	fullPath := filepath.Join(f.mountRoot(), manifestsSubdir, templatePath)

	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("manifests: read template %s: %w", fullPath, err)
	}

	t := template.New(filepath.Base(templatePath)).Option("missingkey=error")
	if len(f.funcs) > 0 {
		t = t.Funcs(f.funcs)
	}
	tmpl, err := t.Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("manifests: parse template %s: %w", fullPath, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("manifests: execute template %s: %w", fullPath, err)
	}
	return buf.String(), nil
}

// mountRoot returns the effective PVC mount root, falling back to
// defaultMountRoot when the field is empty.
func (f *Fetcher) mountRoot() string {
	if f.MountRoot == "" {
		return defaultMountRoot
	}
	return f.MountRoot
}
