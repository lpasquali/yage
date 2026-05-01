// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package manifests

import (
	"path/filepath"
	"strings"
	"testing"
)

type sampleData struct {
	Name     string
	Replicas int
}

func TestFetcher_Render_HappyPath(t *testing.T) {
	f := &Fetcher{MountRoot: "testdata"}

	got, err := f.Render("addons/sample/values.yaml.tmpl", sampleData{
		Name:     "metrics-server",
		Replicas: 2,
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	want := "# yage-manifests/addons/sample/values.yaml.tmpl\nname: metrics-server\nreplicas: 2\n"
	if got != want {
		t.Errorf("Render output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestFetcher_Render_MissingTemplateFile(t *testing.T) {
	f := &Fetcher{MountRoot: "testdata"}

	_, err := f.Render("addons/sample/does-not-exist.yaml.tmpl", sampleData{})
	if err == nil {
		t.Fatal("expected error for missing template file, got nil")
	}
	if !strings.Contains(err.Error(), "read template") {
		t.Errorf("error should mention read template stage; got: %v", err)
	}
	wantPath := filepath.Join("testdata", manifestsSubdir, "addons/sample/does-not-exist.yaml.tmpl")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("error should include resolved path %q; got: %v", wantPath, err)
	}
}

func TestFetcher_Render_MissingKeyInData(t *testing.T) {
	f := &Fetcher{MountRoot: "testdata"}

	// map data missing the "Replicas" key referenced by the template.
	// Per ADR 0012 §4, missingkey=error must hard-error rather than
	// emit the stdlib default "<no value>" placeholder.
	_, err := f.Render("addons/sample/values.yaml.tmpl", map[string]any{
		"Name": "metrics-server",
	})
	if err == nil {
		t.Fatal("expected error for missing key, got nil")
	}
	if !strings.Contains(err.Error(), "execute template") {
		t.Errorf("error should mention execute template stage; got: %v", err)
	}
	if !strings.Contains(err.Error(), "Replicas") {
		t.Errorf("error should mention the missing key Replicas; got: %v", err)
	}
}

func TestFetcher_Render_MalformedTemplate(t *testing.T) {
	f := &Fetcher{MountRoot: "testdata"}

	_, err := f.Render("addons/sample/malformed.yaml.tmpl", sampleData{})
	if err == nil {
		t.Fatal("expected parse error for malformed template, got nil")
	}
	if !strings.Contains(err.Error(), "parse template") {
		t.Errorf("error should mention parse template stage; got: %v", err)
	}
}

func TestFetcher_MountRoot_Default(t *testing.T) {
	// Empty MountRoot must fall back to defaultMountRoot ("/repos").
	// /repos does not exist in CI, so verify via the resolved path in
	// the resulting "read template" error rather than executing a real
	// template against the host filesystem.
	f := &Fetcher{}

	_, err := f.Render("addons/sample/values.yaml.tmpl", sampleData{})
	if err == nil {
		t.Fatal("expected error reading from default /repos mount, got nil")
	}
	wantPath := filepath.Join(defaultMountRoot, manifestsSubdir, "addons/sample/values.yaml.tmpl")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("default MountRoot should resolve to %q; got: %v", wantPath, err)
	}
}

func TestNewFetcher_DefaultsMountRootEmpty(t *testing.T) {
	f := NewFetcher()
	if f.MountRoot != "" {
		t.Errorf("NewFetcher MountRoot = %q; want empty string (resolved at render time)", f.MountRoot)
	}
}
