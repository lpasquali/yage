// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package templates_test

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/capi/templates"
	"github.com/lpasquali/yage/internal/platform/manifests"
)

// TestPostSyncBlock_AllFieldsRendered exercises PostSyncBlock as a
// standalone payload. The struct is also reachable via
// ArgoApplicationData.PostSync; the dedicated test pins the field
// surface so renames break here even when no Application template is
// wired up to use it yet.
func TestPostSyncBlock_AllFieldsRendered(t *testing.T) {
	f := &manifests.Fetcher{MountRoot: "testdata"}

	data := templates.PostSyncBlock{
		URL:              "https://github.com/lpasquali/workload-smoketests",
		Path:             "kustomize/proxmox-csi",
		Ref:              "main",
		KustomizePartial: "  patches:\n    - path: image-override.yaml",
	}

	got, err := f.Render("postsyncblock.yaml.tmpl", data)
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	wants := []string{
		"url: https://github.com/lpasquali/workload-smoketests",
		"path: kustomize/proxmox-csi",
		"ref: main",
		"kustomizePartial:",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output missing %q\nfull output:\n%s", want, got)
		}
	}
}
