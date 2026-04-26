// Package wlargocd ports the workload Argo CD Application YAML
// renderers. Each renderer emits a complete `---`-prefixed Application
// document the orchestrator will concatenate into one kubectl-apply
// stream.
//
// Bash source map (yage.sh):
//   - _wl_argocd_render_helm_git           ~L6229-6318
//   - _kyverno_argocd_values_toleration_fragment ~L6321-6333
//   - _wl_argocd_render_kyverno            ~L6344-6468
//   - _wl_argocd_render_helm               ~L6471-6556
//   - _wl_argocd_render_helm_oci           ~L6559-6646
//   - _wl_argocd_render_kustomize_git      ~L6649-6736
package wlargocd

import (
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/logx"
	"github.com/lpasquali/yage/internal/postsync"
)

// PostSyncBlock holds the optional second-source fields for a
// PostSync-hook git repo + kustomize patch. Zero value means "no
// PostSync hook attached".
type PostSyncBlock struct {
	URL   string
	Path  string
	Ref   string
	Kz    string // kustomize block (already indented for the `sources:` array)
}

// Derive PostSyncBlock from the config for a given hook short name.
// Returns a zero block when hooks are disabled, no short name, or the
// git URL cannot be discovered.
func derivePostSync(cfg *config.Config, hookShort string) PostSyncBlock {
	if !cfg.ArgoWorkloadPostsyncHooksEnabled || hookShort == "" {
		return PostSyncBlock{}
	}
	url := postsync.DiscoverURL(cfg)
	if url == "" {
		logx.Warn("ARGO_WORKLOAD_POSTSYNC_HOOKS: no git URL; skipping PostSync hook for %s (set ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL).", hookShort)
		return PostSyncBlock{}
	}
	return PostSyncBlock{
		URL:  url,
		Path: postsync.FullRelpath(cfg, hookShort),
		Ref:  shellQuoteEscape(postsync.DiscoverRef(cfg)),
		Kz:   postsync.KustomizeBlockForJob(cfg, hookShort+"-smoketest"),
	}
}

// HelmGit ports _wl_argocd_render_helm_git.
// releaseName and hookShort are optional — pass "" to skip them.
func HelmGit(cfg *config.Config, name, destNS, repoURL, relPath, ref, syncWave, valuesYAML, releaseName, hookShort string) string {
	safeRef := shellQuoteEscape(ref)
	indented, indentedMS := indentValuesBoth(valuesYAML)
	hook := derivePostSync(cfg, hookShort)
	var sb strings.Builder
	if hook.URL != "" {
		sb.WriteString(headerAnnot(name, cfg.WorkloadArgoCDNamespace, syncWave, ""))
		fmt.Fprintf(&sb, "spec:\n  project: default\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: %s\n  sources:\n", destNS)
		fmt.Fprintf(&sb, "    - repoURL: %s\n", repoURL)
		fmt.Fprintf(&sb, "      path: %s\n", relPath)
		fmt.Fprintf(&sb, "      targetRevision: '%s'\n", safeRef)
		fmt.Fprintln(&sb, "      helm:")
		if releaseName != "" {
			fmt.Fprintf(&sb, "        releaseName: %s\n", releaseName)
		}
		fmt.Fprintln(&sb, "        valuesObject:")
		if indentedMS != "" {
			sb.WriteString(indentedMS)
		} else {
			fmt.Fprintln(&sb, "          {}")
		}
		fmt.Fprintf(&sb, "    - repoURL: %s\n", hook.URL)
		fmt.Fprintf(&sb, "      path: %s\n", hook.Path)
		fmt.Fprintf(&sb, "      targetRevision: '%s'\n", hook.Ref)
		sb.WriteString(indent(hook.Kz, "  "))
		sb.WriteString(syncPolicyTail())
	} else {
		sb.WriteString(headerAnnot(name, cfg.WorkloadArgoCDNamespace, syncWave, ""))
		fmt.Fprintf(&sb, "spec:\n  project: default\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: %s\n  source:\n", destNS)
		fmt.Fprintf(&sb, "    repoURL: %s\n", repoURL)
		fmt.Fprintf(&sb, "    path: %s\n", relPath)
		fmt.Fprintf(&sb, "    targetRevision: '%s'\n", safeRef)
		fmt.Fprintln(&sb, "    helm:")
		if releaseName != "" {
			fmt.Fprintf(&sb, "      releaseName: %s\n", releaseName)
		}
		fmt.Fprintln(&sb, "      valuesObject:")
		if indented != "" {
			sb.WriteString(indented)
		} else {
			fmt.Fprintln(&sb, "        {}")
		}
		sb.WriteString(syncPolicyTail())
	}
	return sb.String()
}

// kyvernoTolerationFragment ports _kyverno_argocd_values_toleration_fragment.
// Pre-indented with 8 spaces (matches bash sed 's/^/        /').
func kyvernoTolerationFragment(cfg *config.Config) string {
	if !isTrue(cfg.KyvernoTolerateControlPlane) {
		return ""
	}
	return `        global:
          tolerations:
            - key: "node-role.kubernetes.io/control-plane"
              operator: Exists
              effect: NoSchedule
            - key: "node-role.kubernetes.io/master"
              operator: Exists
              effect: NoSchedule
`
}

// Kyverno ports _wl_argocd_render_kyverno.
func Kyverno(cfg *config.Config, name, ns, repoURL, chart, version, syncWave, hookShort string) string {
	target := version
	if target == "" {
		target = "*"
	}
	hook := derivePostSync(cfg, hookShort)
	tolFragment := kyvernoTolerationFragment(cfg)

	var sb strings.Builder
	sb.WriteString(headerAnnot(name, cfg.WorkloadArgoCDNamespace, syncWave,
		`argocd.argoproj.io/compare-options: ServerSideDiff=true,IncludeMutationWebhook=true`))
	fmt.Fprintf(&sb, "spec:\n  project: default\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: %s\n", ns)
	if hook.URL != "" {
		sb.WriteString("  sources:\n")
		fmt.Fprintf(&sb, "    - repoURL: %s\n", repoURL)
		fmt.Fprintf(&sb, "      chart: %s\n", chart)
		fmt.Fprintf(&sb, "      targetRevision: %q\n", target)
		sb.WriteString("      helm:\n")
		sb.WriteString("        valuesObject:\n")
		sb.WriteString("          config:\n")
		sb.WriteString("            preserve: false\n")
		sb.WriteString("            webhookLabels:\n")
		sb.WriteString("              app.kubernetes.io/managed-by: argocd\n")
		sb.WriteString(indent(tolFragment, "  ")) // bump 8→10 spaces via extra 2
		sb.WriteString(kyvernoReplicasBlock(false))
		fmt.Fprintf(&sb, "    - repoURL: %s\n", hook.URL)
		fmt.Fprintf(&sb, "      path: %s\n", hook.Path)
		fmt.Fprintf(&sb, "      targetRevision: '%s'\n", hook.Ref)
		sb.WriteString(indent(hook.Kz, "  "))
	} else {
		sb.WriteString("  source:\n")
		fmt.Fprintf(&sb, "    repoURL: %s\n", repoURL)
		fmt.Fprintf(&sb, "    chart: %s\n", chart)
		fmt.Fprintf(&sb, "    targetRevision: %q\n", target)
		sb.WriteString("    helm:\n")
		sb.WriteString("      valuesObject:\n")
		sb.WriteString("        config:\n")
		sb.WriteString("          preserve: false\n")
		sb.WriteString("          webhookLabels:\n")
		sb.WriteString("            app.kubernetes.io/managed-by: argocd\n")
		sb.WriteString(tolFragment)
		sb.WriteString(kyvernoReplicasBlock(true))
	}
	sb.WriteString(kyvernoIgnoreDiffs())
	sb.WriteString(syncPolicyTail())
	return sb.String()
}

// kyvernoReplicasBlock: ms=true returns the deeper-indented variant
// (inside `sources:` array).
func kyvernoReplicasBlock(singleSource bool) string {
	var i string
	if singleSource {
		i = "        "
	} else {
		i = "          "
	}
	return fmt.Sprintf(`%sadmissionController:
%s  replicas: 1
%sbackgroundController:
%s  replicas: 1
%scleanupController:
%s  replicas: 1
%sreportsController:
%s  replicas: 1
`, i, i, i, i, i, i, i, i)
}

func kyvernoIgnoreDiffs() string {
	return `  ignoreDifferences:
    - group: admissionregistration.k8s.io
      kind: MutatingWebhookConfiguration
      jqPathExpressions:
        - .webhooks[]?.clientConfig.caBundle
    - group: admissionregistration.k8s.io
      kind: ValidatingWebhookConfiguration
      jqPathExpressions:
        - .webhooks[]?.clientConfig.caBundle
`
}

// Helm ports _wl_argocd_render_helm (HTTP repo).
func Helm(cfg *config.Config, name, ns, repoURL, chart, version, syncWave, valuesYAML, hookShort string) string {
	target := version
	if target == "" {
		target = "*"
	}
	repoURL = strings.TrimSuffix(repoURL, "/")
	indented, indentedMS := indentValuesBoth(valuesYAML)
	hook := derivePostSync(cfg, hookShort)

	var sb strings.Builder
	sb.WriteString(headerAnnot(name, cfg.WorkloadArgoCDNamespace, syncWave, ""))
	if hook.URL != "" {
		fmt.Fprintf(&sb, "spec:\n  project: default\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: %s\n  sources:\n", ns)
		fmt.Fprintf(&sb, "    - repoURL: %s\n", repoURL)
		fmt.Fprintf(&sb, "      chart: %s\n", chart)
		fmt.Fprintf(&sb, "      targetRevision: %q\n", target)
		fmt.Fprintln(&sb, "      helm:")
		fmt.Fprintln(&sb, "        valuesObject:")
		if indentedMS != "" {
			sb.WriteString(indentedMS)
		} else {
			fmt.Fprintln(&sb, "          {}")
		}
		fmt.Fprintf(&sb, "    - repoURL: %s\n", hook.URL)
		fmt.Fprintf(&sb, "      path: %s\n", hook.Path)
		fmt.Fprintf(&sb, "      targetRevision: '%s'\n", hook.Ref)
		sb.WriteString(indent(hook.Kz, "  "))
	} else {
		fmt.Fprintf(&sb, "spec:\n  project: default\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: %s\n  source:\n", ns)
		fmt.Fprintf(&sb, "    repoURL: %s\n", repoURL)
		fmt.Fprintf(&sb, "    chart: %s\n", chart)
		fmt.Fprintf(&sb, "    targetRevision: %q\n", target)
		fmt.Fprintln(&sb, "    helm:")
		fmt.Fprintln(&sb, "      valuesObject:")
		if indented != "" {
			sb.WriteString(indented)
		} else {
			fmt.Fprintln(&sb, "        {}")
		}
	}
	sb.WriteString(syncPolicyTail())
	return sb.String()
}

// HelmOCI ports _wl_argocd_render_helm_oci. Optional PostSync hooks: both
// kustomize blocks must be non-empty for the sources/ multi-source to
// activate.
func HelmOCI(cfg *config.Config, name, ns, ociURL, version, syncWave, valuesYAML, hook1Path, hook1Kz, hook2Path, hook2Kz string) string {
	target := version
	if target == "" {
		target = "*"
	}
	indented, indentedMS := indentValuesBoth(valuesYAML)

	useMulti := cfg.ArgoWorkloadPostsyncHooksEnabled && cfg.ProxmoxCSISmokeEnabled &&
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

	var sb strings.Builder
	sb.WriteString(headerAnnot(name, cfg.WorkloadArgoCDNamespace, syncWave, ""))
	if useMulti {
		fmt.Fprintf(&sb, "spec:\n  project: default\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: %s\n  sources:\n", ns)
		fmt.Fprintf(&sb, "    - repoURL: %s\n", ociURL)
		fmt.Fprintln(&sb, `      path: "."`)
		fmt.Fprintf(&sb, "      targetRevision: %q\n", target)
		fmt.Fprintln(&sb, "      helm:")
		fmt.Fprintln(&sb, "        valuesObject:")
		if indentedMS != "" {
			sb.WriteString(indentedMS)
		} else {
			fmt.Fprintln(&sb, "          {}")
		}
		fmt.Fprintf(&sb, "    - repoURL: %s\n", hURL)
		fmt.Fprintf(&sb, "      path: %s\n", hook1Path)
		fmt.Fprintf(&sb, "      targetRevision: '%s'\n", sref)
		sb.WriteString(indent(hook1Kz, "  "))
		fmt.Fprintf(&sb, "    - repoURL: %s\n", hURL)
		fmt.Fprintf(&sb, "      path: %s\n", hook2Path)
		fmt.Fprintf(&sb, "      targetRevision: '%s'\n", sref)
		sb.WriteString(indent(hook2Kz, "  "))
	} else {
		fmt.Fprintf(&sb, "spec:\n  project: default\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: %s\n  source:\n", ns)
		fmt.Fprintf(&sb, "    repoURL: %s\n", ociURL)
		fmt.Fprintln(&sb, `    path: "."`)
		fmt.Fprintf(&sb, "    targetRevision: %q\n", target)
		fmt.Fprintln(&sb, "    helm:")
		fmt.Fprintln(&sb, "      valuesObject:")
		if indented != "" {
			sb.WriteString(indented)
		} else {
			fmt.Fprintln(&sb, "        {}")
		}
	}
	sb.WriteString(syncPolicyTail())
	return sb.String()
}

// KustomizeGit ports _wl_argocd_render_kustomize_git.
// kustomizeBlock is expected pre-indented for a `source:` child
// (4-space indent) — the multi-source branch re-indents it by 2.
func KustomizeGit(cfg *config.Config, name, destNS, repoURL, relPath, ref, syncWave, kustomizeBlock, hookShort string) string {
	safeRef := shellQuoteEscape(ref)
	hook := derivePostSync(cfg, hookShort)
	var sb strings.Builder
	sb.WriteString(headerAnnot(name, cfg.WorkloadArgoCDNamespace, syncWave, ""))
	fmt.Fprintf(&sb, "spec:\n  project: default\n  destination:\n    server: https://kubernetes.default.svc\n    namespace: %s\n", destNS)
	if hook.URL != "" {
		sb.WriteString("  sources:\n")
		fmt.Fprintf(&sb, "    - repoURL: %s\n", repoURL)
		fmt.Fprintf(&sb, "      path: %s\n", relPath)
		fmt.Fprintf(&sb, "      targetRevision: '%s'\n", safeRef)
		sb.WriteString(indent(kustomizeBlock, "  "))
		fmt.Fprintf(&sb, "    - repoURL: %s\n", hook.URL)
		fmt.Fprintf(&sb, "      path: %s\n", hook.Path)
		fmt.Fprintf(&sb, "      targetRevision: '%s'\n", hook.Ref)
		sb.WriteString(indent(hook.Kz, "  "))
	} else {
		sb.WriteString("  source:\n")
		fmt.Fprintf(&sb, "    repoURL: %s\n", repoURL)
		fmt.Fprintf(&sb, "    path: %s\n", relPath)
		fmt.Fprintf(&sb, "    targetRevision: '%s'\n", safeRef)
		sb.WriteString(kustomizeBlock)
	}
	sb.WriteString(syncPolicyTail())
	return sb.String()
}

// --- helpers ---

// headerAnnot emits the Application header, optionally with an extra
// annotation line (whole line including the key).
func headerAnnot(name, ns, wave, extraAnnot string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("apiVersion: argoproj.io/v1alpha1\n")
	sb.WriteString("kind: Application\n")
	sb.WriteString("metadata:\n")
	fmt.Fprintf(&sb, "  name: %s\n", name)
	fmt.Fprintf(&sb, "  namespace: %s\n", ns)
	sb.WriteString("  annotations:\n")
	fmt.Fprintf(&sb, "    argocd.argoproj.io/sync-wave: %q\n", wave)
	if extraAnnot != "" {
		fmt.Fprintf(&sb, "    %s\n", extraAnnot)
	}
	return sb.String()
}

func syncPolicyTail() string {
	return `  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
`
}

// indentValuesBoth returns (8-space-indent, 10-space-indent) copies of
// valuesYAML or empty strings when valuesYAML is empty. Matches bash
// indented_values and indented_values_ms.
func indentValuesBoth(values string) (string, string) {
	if values == "" {
		return "", ""
	}
	return indent(values, "        "), indent(values, "          ")
}

// indent prefixes every non-empty line of s with `prefix`, preserving a
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
	// bash: sed "s/'/'\"'\"'/g" — replace ' with '"'"'
	return strings.ReplaceAll(s, `'`, `'"'"'`)
}

func isTrue(s string) bool {
	switch s {
	case "true", "1", "yes", "y", "on", "TRUE":
		return true
	}
	return false
}
