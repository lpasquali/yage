// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package templates_test

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/capi/templates"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
)

// TestHelmValuesData_AllFieldsRendered exercises every ADR 0012 §3 field
// on HelmValuesData against an in-package fixture template that
// references each field. The Fetcher's missingkey=error policy turns
// any field omission or rename into a hard render error, pinning the
// struct shape against ADR 0012.
func TestHelmValuesData_AllFieldsRendered(t *testing.T) {
	f := &manifests.Fetcher{MountRoot: "testdata"}

	data := templates.HelmValuesData{
		Cfg: &config.Config{WorkloadClusterName: "wl-prod"},
	}

	got, err := f.Render("helmvalues.yaml.tmpl", data)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	for _, want := range []string{"cluster: wl-prod"} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\nfull output:\n%s", want, got)
		}
	}
}
