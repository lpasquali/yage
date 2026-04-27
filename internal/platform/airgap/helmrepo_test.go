// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package airgap

import "testing"

func TestRewriteHelmRepo_withoutMirror(t *testing.T) {
	t.Cleanup(func() { _ = Apply("", "", "") })
	_ = Apply("", "", "")

	if got := RewriteHelmRepo("https://charts.jetstack.io"); got != "https://charts.jetstack.io" {
		t.Fatalf("without mirror, got %q want identity", got)
	}
	if got := RewriteHelmRepo(""); got != "" {
		t.Fatalf("empty in: got %q want empty", got)
	}
}

func TestRewriteHelmRepo_withMirror(t *testing.T) {
	const mirror = "https://harbor.internal/chartrepo/yage"
	t.Cleanup(func() { _ = Apply("", "", "") })
	if err := Apply("", mirror, ""); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		in   string
		want string
	}{
		{"https://charts.jetstack.io", mirror},
		{"https://charts.jetstack.io/foo/bar", mirror + "/foo/bar"},
		{"oci://ghcr.io/sergelogvinov/charts", mirror + "/sergelogvinov/charts"},
		{mirror + "/cert-manager", mirror + "/cert-manager"},
		{"", ""},
		{"git+ssh://example.com/repo.git", "git+ssh://example.com/repo.git"},
	}
	for _, tc := range tests {
		if got := RewriteHelmRepo(tc.in); got != tc.want {
			t.Errorf("RewriteHelmRepo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
