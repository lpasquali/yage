// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package postsync hosts the Argo CD PostSync-hook helpers and
// the proxmox-csi smoke-test renderers.
package postsync

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/shell"
)

// BootstrapDir ports workload_postsync_hooks_bootstrap_dir. In Go this
// is the directory of the running executable (not BASH_SOURCE). Good
// enough because the repo root is the same place for either shape.
func BootstrapDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

// DiscoverURL ports workload_postsync_hooks_discover_url. Prefers the
// env override, falls back to the origin remote of the containing git
// repo (translating git@host:path to https://host/path and appending
// .git when absent).
func DiscoverURL(cfg *config.Config) string {
	if cfg.ArgoWorkloadPostsyncHooksGitURL != "" {
		return cfg.ArgoWorkloadPostsyncHooksGitURL
	}
	root := BootstrapDir()
	out, _, err := shell.CaptureIn(root, "git", "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(out)
	if url == "" {
		return ""
	}
	if strings.HasPrefix(url, "git@") {
		// git@host:path → https://host/path
		rest := strings.TrimPrefix(url, "git@")
		if i := strings.IndexByte(rest, ':'); i >= 0 {
			url = "https://" + rest[:i] + "/" + rest[i+1:]
		}
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return ""
	}
	if !strings.HasSuffix(url, ".git") {
		url += ".git"
	}
	return url
}

// DiscoverRef ports workload_postsync_hooks_discover_ref. Prefers the
// env override, otherwise git branch, otherwise short commit, otherwise
// "main".
func DiscoverRef(cfg *config.Config) string {
	if cfg.ArgoWorkloadPostsyncHooksGitRef != "" {
		return cfg.ArgoWorkloadPostsyncHooksGitRef
	}
	root := BootstrapDir()
	branch, _, err := shell.CaptureIn(root, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		br := strings.TrimSpace(branch)
		if br != "" && br != "HEAD" {
			return br
		}
	}
	sha, _, err := shell.CaptureIn(root, "git", "rev-parse", "--short=12", "HEAD")
	if err == nil {
		if s := strings.TrimSpace(sha); s != "" {
			return s
		}
	}
	return "main"
}

// FullRelpath ports workload_postsync_hooks_full_relpath. Returns
// "<prefix>/argo-postsync-hooks/<short>" or the path without the prefix
// when ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH is unset.
func FullRelpath(cfg *config.Config, short string) string {
	if short == "" {
		return ""
	}
	pfx := strings.TrimSuffix(strings.TrimPrefix(cfg.ArgoWorkloadPostsyncHooksGitPath, "./"), "/")
	if pfx != "" {
		return pfx + "/argo-postsync-hooks/" + short
	}
	return "argo-postsync-hooks/" + short
}

// ResolveKubectlImage ports workload_postsync_hooks_resolve_kubectl_image.
// Prefers the env override, otherwise registry.k8s.io/kubectl:<tag> with
// <tag> derived from the manifest via SmokeKubectlOCITag.
func ResolveKubectlImage(cfg *config.Config) string {
	if cfg.ArgoWorkloadPostsyncHooksKubectlImg != "" {
		return cfg.ArgoWorkloadPostsyncHooksKubectlImg
	}
	return "registry.k8s.io/kubectl:" + SmokeKubectlOCITag(cfg)
}

// KustomizeBlockForJob ports workload_postsync_kustomize_block_for_job.
func KustomizeBlockForJob(cfg *config.Config, job string) string {
	ns := cfg.WorkloadPostsyncNamespace
	if ns == "" {
		ns = "workload-smoke"
	}
	img := ResolveKubectlImage(cfg)
	return fmt.Sprintf(`    kustomize:
      namespace: %s
      patches:
        - target:
            group: batch
            version: v1
            kind: Job
            name: %s
          patch: |
            - op: replace
              path: /spec/template/spec/containers/0/image
              value: '%s'
`, ns, job, img)
}

// SmokeK8sVersionForImage ports proxmox_csi_smoke_k8s_version_for_image.
// Scans CAPI_MANIFEST for a Cluster-topology.version, else
// KubeadmControlPlane.spec.version; falls back to
// cfg.WorkloadKubernetesVersion (default v1.35.0).
func SmokeK8sVersionForImage(cfg *config.Config) string {
	fallback := cfg.WorkloadKubernetesVersion
	if fallback == "" {
		fallback = "v1.35.0"
	}
	if cfg.CAPIManifest == "" {
		return fallback
	}
	raw, err := os.ReadFile(cfg.CAPIManifest)
	if err != nil {
		return fallback
	}
	text := string(raw)
	// Split on `^---` boundaries.
	docRE := regexp.MustCompile(`(?m)^---\s*\n`)
	docs := docRE.Split(text, -1)
	topoRE := regexp.MustCompile(`(?m)^\s+version:\s*(v?[\d.]+)\s*(?:#.*)?$`)
	for _, d := range docs {
		if !strings.Contains(d, "kind: Cluster") || !strings.Contains(d, "topology:") {
			continue
		}
		i := strings.Index(d, "topology:")
		sub := d[i+len("topology:"):]
		headLines := strings.Split(sub, "\n")
		if len(headLines) > 120 {
			headLines = headLines[:120]
		}
		head := strings.Join(headLines, "\n")
		if m := topoRE.FindStringSubmatch(head); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	kcpRE := regexp.MustCompile(`(?m)^  version:\s*(v?[\d.]+)\s*(?:#.*)?$`)
	for _, d := range docs {
		if !strings.Contains(d, "kind: KubeadmControlPlane") {
			continue
		}
		if i := strings.Index(d, "spec:"); i >= 0 {
			d = d[i:]
		}
		if m := kcpRE.FindStringSubmatch(d); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return fallback
}

// SmokeKubectlOCITag ports proxmox_csi_smoke_kubectl_oci_tag: vX.Y.Z,
// extending X.Y to X.Y.0.
func SmokeKubectlOCITag(cfg *config.Config) string {
	v := strings.TrimPrefix(SmokeK8sVersionForImage(cfg), "v")
	if regexp.MustCompile(`^[0-9]+\.[0-9]+$`).MatchString(v) {
		v += ".0"
	}
	return "v" + v
}

// SmokeRenderKustomizeBlock ports proxmox_csi_smoke_render_kustomize_block.
func SmokeRenderKustomizeBlock(cfg *config.Config) string {
	ns := cfg.Providers.Proxmox.CSINamespace
	sc := cfg.Providers.Proxmox.CSIStorageClassName
	img := ResolveKubectlImage(cfg)
	return fmt.Sprintf(`    kustomize:
      namespace: %s
      patches:
        - target:
            group: batch
            version: v1
            kind: Job
            name: proxmox-csi-smoke
          patch: |
            - op: replace
              path: /spec/template/spec/containers/0/image
              value: '%s'
            - op: replace
              path: /spec/template/spec/containers/0/env/0/value
              value: "%s"
            - op: replace
              path: /spec/template/spec/containers/0/env/1/value
              value: "%s"
`, ns, img, ns, sc)
}

// Silence unused-import of exec if we end up not needing it.
var _ = exec.Command