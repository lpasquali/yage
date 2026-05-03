// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package wlargocd renders workload Argo CD Application YAML.
//
// Each renderer emits a complete `---`-prefixed Application document
// the orchestrator concatenates into one kubectl-apply stream.
// Bodies are produced by yage-manifests templates resolved through
// internal/platform/manifests.Fetcher (ADR 0008, ADR 0012). The
// indent / shell-quote / postsync-derivation helpers stay Go-side
// per ADR 0012 §wlargocd: they prepare the data passed into the
// wrapper struct, they are not template-shaped.
package wlargocd

import (
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/capi/postsync"
	"github.com/lpasquali/yage/internal/capi/templates"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// derivePostSync builds the single-hook PostSyncBlock for hookShort,
// or returns a zero block when hooks are disabled, the short name is
// empty, or the git URL cannot be discovered. The kustomize partial
// is pre-indented by 2 (depth 6 inside `sources:`) so the template
// can splice it without further indentation.
func derivePostSync(f *manifests.Fetcher, cfg *config.Config, hookShort string) (templates.PostSyncBlock, error) {
	if !cfg.ArgoCD.PostsyncHooksEnabled || hookShort == "" {
		return templates.PostSyncBlock{}, nil
	}
	url := postsync.DiscoverURL(cfg)
	if url == "" {
		logx.Warn("ARGO_WORKLOAD_POSTSYNC_HOOKS: no git URL; skipping PostSync hook for %s (set ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL).", hookShort)
		return templates.PostSyncBlock{}, nil
	}
	kz, err := postsync.KustomizeBlockForJobTemplate(f, cfg, hookShort+"-smoketest")
	if err != nil {
		return templates.PostSyncBlock{}, fmt.Errorf("postsync kustomize block for %s: %w", hookShort, err)
	}
	return templates.PostSyncBlock{
		URL:              url,
		Path:             postsync.FullRelpath(cfg, hookShort),
		Ref:              shellQuoteEscape(postsync.DiscoverRef(cfg)),
		KustomizePartial: indent(kz, "  "),
	}, nil
}

// HelmGit renders an Argo CD Application that pulls a Helm chart from
// a Git source. addonDir is the addons/<addonDir>/ subdirectory that
// holds application.yaml.tmpl. releaseName and hookShort are optional
// — pass "" to skip them.
func HelmGit(f *manifests.Fetcher, cfg *config.Config, addonDir, name, destNS, repoURL, relPath, ref, syncWave, valuesYAML, releaseName, hookShort string) (string, error) {
	hook, err := derivePostSync(f, cfg, hookShort)
	if err != nil {
		return "", err
	}
	indented, indentedMS := indentValuesBoth(valuesYAML)
	values := indented
	var hooks []templates.PostSyncBlock
	if hook.URL != "" {
		values = indentedMS
		hooks = []templates.PostSyncBlock{hook}
	}
	data := templates.ArgoApplicationData{
		Cfg:         cfg,
		Name:        name,
		DestNS:      destNS,
		RepoURL:     repoURL,
		Path:        relPath,
		Ref:         shellQuoteEscape(ref),
		SyncWave:    syncWave,
		ReleaseName: releaseName,
		ValuesYAML:  values,
		PostSyncs:   hooks,
	}
	body, err := f.Render("addons/"+addonDir+"/application.yaml.tmpl", data)
	if err != nil {
		return "", err
	}
	return "---\n" + body, nil
}

// Helm renders an Argo CD Application that pulls a chart from an HTTP
// Helm repository.
func Helm(f *manifests.Fetcher, cfg *config.Config, addonDir, name, ns, repoURL, chart, version, syncWave, valuesYAML, hookShort string) (string, error) {
	target := version
	if target == "" {
		target = "*"
	}
	repoURL = strings.TrimSuffix(repoURL, "/")
	hook, err := derivePostSync(f, cfg, hookShort)
	if err != nil {
		return "", err
	}
	indented, indentedMS := indentValuesBoth(valuesYAML)
	values := indented
	var hooks []templates.PostSyncBlock
	if hook.URL != "" {
		values = indentedMS
		hooks = []templates.PostSyncBlock{hook}
	}
	data := templates.ArgoApplicationData{
		Cfg:        cfg,
		Name:       name,
		DestNS:     ns,
		RepoURL:    repoURL,
		Chart:      chart,
		Ref:        target,
		SyncWave:   syncWave,
		ValuesYAML: values,
		PostSyncs:  hooks,
	}
	body, err := f.Render("addons/"+addonDir+"/application.yaml.tmpl", data)
	if err != nil {
		return "", err
	}
	return "---\n" + body, nil
}

// HelmOCI renders an Argo CD Application that pulls an OCI Helm chart.
// Optional PostSync hooks: both kustomize blocks must be non-empty for
// the multi-source variant to activate, and both hooks are emitted
// together (slice of length 2).
func HelmOCI(f *manifests.Fetcher, cfg *config.Config, addonDir, name, ns, ociURL, version, syncWave, valuesYAML, hook1Path, hook1Kz, hook2Path, hook2Kz string) (string, error) {
	target := version
	if target == "" {
		target = "*"
	}
	indented, indentedMS := indentValuesBoth(valuesYAML)

	useMulti := cfg.ArgoCD.PostsyncHooksEnabled && cfg.Providers.Proxmox.CSISmokeEnabled &&
		hook1Path != "" && hook1Kz != "" && hook2Path != "" && hook2Kz != ""
	var hURL, sref string
	if useMulti {
		hURL = postsync.DiscoverURL(cfg)
		if hURL == "" {
			logx.Warn("ARGO_WORKLOAD_POSTSYNC_HOOKS: no git URL; proxmox-csi will sync without PostSync hook Jobs (set ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL).")
			useMulti = false
		} else {
			sref = shellQuoteEscape(postsync.DiscoverRef(cfg))
		}
	}

	values := indented
	var hooks []templates.PostSyncBlock
	if useMulti {
		values = indentedMS
		hooks = []templates.PostSyncBlock{
			{URL: hURL, Path: hook1Path, Ref: sref, KustomizePartial: indent(hook1Kz, "  ")},
			{URL: hURL, Path: hook2Path, Ref: sref, KustomizePartial: indent(hook2Kz, "  ")},
		}
	}

	data := templates.ArgoApplicationData{
		Cfg:        cfg,
		Name:       name,
		DestNS:     ns,
		RepoURL:    ociURL,
		Path:       `"."`,
		Ref:        target,
		SyncWave:   syncWave,
		ValuesYAML: values,
		PostSyncs:  hooks,
	}
	body, err := f.Render("addons/"+addonDir+"/application.yaml.tmpl", data)
	if err != nil {
		return "", err
	}
	return "---\n" + body, nil
}

// Kyverno renders the Kyverno workload Argo CD Application YAML.
// Kyverno-specific body shape (extra annotation, valuesObject layout,
// replicas, ignoreDifferences) lives in addons/kyverno/application.yaml.tmpl.
// The KyvernoTolerateControlPlane string field is read template-side
// via the isTrue FuncMap; callers must register isTrue on f before
// the first Kyverno call (helmvalues.RegisterIsTrue).
func Kyverno(f *manifests.Fetcher, cfg *config.Config, addonDir, name, ns, repoURL, chart, version, syncWave, hookShort string) (string, error) {
	target := version
	if target == "" {
		target = "*"
	}
	hook, err := derivePostSync(f, cfg, hookShort)
	if err != nil {
		return "", err
	}
	var hooks []templates.PostSyncBlock
	if hook.URL != "" {
		hooks = []templates.PostSyncBlock{hook}
	}
	data := templates.ArgoApplicationData{
		Cfg:       cfg,
		Name:      name,
		DestNS:    ns,
		RepoURL:   repoURL,
		Chart:     chart,
		Ref:       target,
		SyncWave:  syncWave,
		PostSyncs: hooks,
	}
	body, err := f.Render("addons/"+addonDir+"/application.yaml.tmpl", data)
	if err != nil {
		return "", err
	}
	return "---\n" + body, nil
}

// KustomizeGit renders an Argo CD Application that pulls a kustomize
// tree from a Git source. kustomizeBlock is expected pre-indented for
// a `source:` child (4-space indent); the multi-source branch
// re-indents it by 2 here before passing it to the template via
// ArgoApplicationData.ValuesYAML.
func KustomizeGit(f *manifests.Fetcher, cfg *config.Config, addonDir, name, destNS, repoURL, relPath, ref, syncWave, kustomizeBlock, hookShort string) (string, error) {
	hook, err := derivePostSync(f, cfg, hookShort)
	if err != nil {
		return "", err
	}
	var hooks []templates.PostSyncBlock
	primaryKz := kustomizeBlock
	if hook.URL != "" {
		primaryKz = indent(kustomizeBlock, "  ")
		hooks = []templates.PostSyncBlock{hook}
	}
	data := templates.ArgoApplicationData{
		Cfg:        cfg,
		Name:       name,
		DestNS:     destNS,
		RepoURL:    repoURL,
		Path:       relPath,
		Ref:        shellQuoteEscape(ref),
		SyncWave:   syncWave,
		ValuesYAML: primaryKz,
		PostSyncs:  hooks,
	}
	body, err := f.Render("addons/"+addonDir+"/application.yaml.tmpl", data)
	if err != nil {
		return "", err
	}
	return "---\n" + body, nil
}

// indentValuesBoth returns (8-space-indent, 10-space-indent) copies of
// valuesYAML or empty strings when valuesYAML is empty.
func indentValuesBoth(values string) (string, string) {
	if values == "" {
		return "", ""
	}
	return indent(values, "        "), indent(values, "          ")
}

// indent prefixes every non-empty line of s with prefix, preserving a
// trailing newline.
func indent(s, prefix string) string {
	if s == "" {
		return ""
	}
	var sb strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if line == "" {
			continue
		}
		if line == "\n" {
			sb.WriteString(line)
			continue
		}
		sb.WriteString(prefix)
		sb.WriteString(line)
	}
	return sb.String()
}

func shellQuoteEscape(s string) string {
	return strings.ReplaceAll(s, `'`, `'"'"'`)
}
