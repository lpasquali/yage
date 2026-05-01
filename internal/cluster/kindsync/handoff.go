// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

// handoff.go — bootstrap-state Secret hand-off from kind → Proxmox-hosted
// management cluster. Lives alongside the existing kind-write helpers
// (config.go, kindsync.go, secret.go) and reuses their primitives:
//
//   - getSecretJSON: typed Get + json.Marshal on the source (kind) side.
//   - applySecret:   server-side-apply on the destination (mgmt) side.
//   - k8sclient.FieldManager / k8sclient.ForContext / ForKubeconfigFile.
//
// The set of Secrets handled mirrors what SyncBootstrapConfigToKind
// writes into kind, plus the live capmox-system/capmox-manager-credentials
// copy that ships across as part of the same hand-off.
//
// CAPI inventory (Cluster, Machines, KubeadmConfig, etc.) is *not* this
// package's concern — clusterctl move handles that. We own the
// yage-system Secrets and the live capmox copy only.

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// capmoxLiveNamespace / capmoxLiveSecretName are the live capmox-controller
// credentials carried across in the same hand-off step. These are not
// bootstrap-state per se, but a deleted in-cluster copy is restored on
// next sync only after this Secret reappears, so we keep it in
// lockstep with the rest of the move.
const (
	capmoxLiveNamespace  = "capmox-system"
	capmoxLiveSecretName = "capmox-manager-credentials"
)

// yageManagedBySelector is the label selector used to discover Secrets
// in yage-system that should be carried across to the management cluster
// alongside the named-target list (ADR 0011 §2). Matches the label set
// applied by EnsureYageSystemOnCluster and by the OpenTofu kubernetes
// backend (ADR 0011 §1).
const yageManagedBySelector = "app.kubernetes.io/managed-by=yage"

// HandoffResult is the augmented return value of
// HandOffBootstrapSecretsToManagement. NamedCopied counts Secrets carried
// across from expectedHandoffTargets and the per-config bootstrap-config
// pass; LabelCopied counts Secrets discovered in yage-system via the
// app.kubernetes.io/managed-by=yage label (ADR 0011 §2). FirstErr is the
// first per-Secret error seen — handoff is best-effort, the caller
// chains a VerifyParity step after.
type HandoffResult struct {
	NamedCopied int
	LabelCopied int
	FirstErr    error
}

// Total returns NamedCopied + LabelCopied so callers that only care about
// the gross volume can keep their existing log lines.
func (r HandoffResult) Total() int { return r.NamedCopied + r.LabelCopied }

// handoffTarget describes one Secret to copy from kind → mgmt.
// Namespace is resolved at call-time so an empty config field
// (e.g. an unset Providers.Proxmox.BootstrapSecretName) is
// treated as "no such Secret to copy".
type handoffTarget struct {
	Namespace string
	Name      string
	// Description is the short label used in the per-Secret log line.
	Description string
}

// expectedHandoffTargets returns the list of Secrets to attempt
// to hand off, in deterministic order. Empty Name entries are
// filtered by the caller — for example, the single-Secret name is
// empty in the default split layout.
func expectedHandoffTargets(cfg *config.Config) []handoffTarget {
	ns := cfg.Providers.Proxmox.BootstrapSecretNamespace
	return []handoffTarget{
		{Namespace: ns, Name: cfg.Providers.Proxmox.BootstrapConfigSecretName, Description: "bootstrap config snapshot"},
		{Namespace: ns, Name: cfg.Providers.Proxmox.BootstrapCAPMOXSecretName, Description: "CAPI / clusterctl credentials"},
		{Namespace: ns, Name: cfg.Providers.Proxmox.BootstrapCSISecretName, Description: "CSI credentials"},
		{Namespace: ns, Name: cfg.Providers.Proxmox.BootstrapAdminSecretName, Description: "admin token YAML"},
		{Namespace: ns, Name: cfg.Providers.Proxmox.BootstrapSecretName, Description: "single-Secret credentials"},
		{Namespace: capmoxLiveNamespace, Name: capmoxLiveSecretName, Description: "live capmox-controller credentials"},
	}
}

// HandOffBootstrapSecretsToManagement copies the yage-system
// namespace and its Secrets from the kind management context to the
// freshly-provisioned Proxmox management cluster.
//
// Secrets copied (when present on kind):
//   - <Providers.Proxmox.BootstrapConfigSecretName>          (config.yaml: snapshot of cfg)
//   - <Providers.Proxmox.BootstrapCAPMOXSecretName>          (CAPI clusterctl creds)
//   - <Providers.Proxmox.BootstrapCSISecretName>             (CSI creds)
//   - <Providers.Proxmox.BootstrapAdminSecretName>           (admin token YAML)
//   - <Providers.Proxmox.BootstrapSecretName>                (single-Secret form)
//   - capmox-system/capmox-manager-credentials    (the live capmox copy)
//
// After the named-target pass, every Secret in yage-system carrying
// label app.kubernetes.io/managed-by=yage is discovered and applied to
// the management cluster (ADR 0011 §2 — label-scoped pass; covers
// tofu-state Secrets and any future yage-managed Secrets that opt into
// the label).
//
// Both kindCtx and mgmtKubeconfig identify the source / destination. The
// destination namespace is created when missing. Returns the per-pass
// counts plus the first error encountered — handoff is best-effort, the
// caller chains a VerifyParity step after.
func HandOffBootstrapSecretsToManagement(cfg *config.Config, kindCtx, mgmtKubeconfig string) (HandoffResult, error) {
	if cfg == nil {
		return HandoffResult{}, fmt.Errorf("handoff: nil config")
	}
	srcCli, srcErr := k8sclient.ForContext(kindCtx)
	if srcErr != nil {
		return HandoffResult{}, fmt.Errorf("handoff: load kind context %q: %w", kindCtx, srcErr)
	}
	dstCli, dstErr := k8sclient.ForKubeconfigFile(mgmtKubeconfig)
	if dstErr != nil {
		return HandoffResult{}, fmt.Errorf("handoff: load management kubeconfig %q: %w", mgmtKubeconfig, dstErr)
	}
	bg := context.Background()
	// Tracks (namespace, name) of Secrets already applied via the named
	// pass so the subsequent label pass cannot double-apply them.
	copiedKey := func(ns, name string) string { return ns + "/" + name }
	alreadyCopied := map[string]bool{}
	var (
		namedCopied int
		labelCopied int
	)

	// Track which destination namespaces we have ensured to avoid duplicate
	// EnsureNamespace calls (typically only the bootstrap NS + capmox-system).
	ensuredNS := map[string]bool{}

	var firstErr error
	for _, tgt := range expectedHandoffTargets(cfg) {
		if tgt.Namespace == "" || tgt.Name == "" {
			continue
		}

		// Source-side fetch via the typed client (mirrors getSecretJSON
		// internally, but we want the corev1.Secret object so we can
		// strip server-side metadata cleanly before the apply).
		src, getErr := srcCli.Typed.CoreV1().Secrets(tgt.Namespace).
			Get(bg, tgt.Name, metav1.GetOptions{})
		if getErr != nil {
			if apierrors.IsNotFound(getErr) {
				continue
			}
			logx.Warn("Hand-off: failed to read %s/%s on kind: %v", tgt.Namespace, tgt.Name, getErr)
			if firstErr == nil {
				firstErr = getErr
			}
			continue
		}

		// Ensure the destination namespace before applying any Secret
		// into it — the Proxmox management cluster is freshly
		// provisioned, so neither yage-system nor
		// capmox-system exist there yet.
		if !ensuredNS[tgt.Namespace] {
			if nsErr := dstCli.EnsureNamespace(bg, tgt.Namespace); nsErr != nil {
				logx.Warn("Hand-off: failed to ensure namespace %s on management: %v", tgt.Namespace, nsErr)
				if firstErr == nil {
					firstErr = nsErr
				}
				continue
			}
			ensuredNS[tgt.Namespace] = true
		}

		// Build a clean Secret manifest: keep Name/Namespace/Type/Labels/
		// Annotations/Data/StringData and drop everything server-side
		// fills in (ResourceVersion, UID, CreationTimestamp, SelfLink,
		// OwnerReferences, ManagedFields, Generation, etc.). The two
		// TypeMeta fields are forced — server-side apply requires them
		// in the patch body.
		clean := corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:        src.Name,
				Namespace:   src.Namespace,
				Labels:      src.Labels,
				Annotations: stripServerAnnotations(src.Annotations),
			},
			Type:       src.Type,
			Data:       src.Data,
			StringData: src.StringData,
			Immutable:  src.Immutable,
		}
		if clean.Type == "" {
			clean.Type = corev1.SecretTypeOpaque
		}

		body, marshalErr := json.Marshal(clean)
		if marshalErr != nil {
			logx.Warn("Hand-off: failed to marshal %s/%s: %v", tgt.Namespace, tgt.Name, marshalErr)
			if firstErr == nil {
				firstErr = marshalErr
			}
			continue
		}

		// Server-side apply on the destination cluster. Force=true so we
		// win field-ownership conflicts on a re-run — when nothing has
		// changed this is a no-op.
		_, patchErr := dstCli.Typed.CoreV1().Secrets(tgt.Namespace).Patch(
			bg, tgt.Name, types.ApplyPatchType, body,
			metav1.PatchOptions{
				FieldManager: k8sclient.FieldManager,
				Force:        boolTrue(),
			},
		)
		if patchErr != nil {
			logx.Warn("Hand-off: failed to apply %s/%s on management: %v", tgt.Namespace, tgt.Name, patchErr)
			if firstErr == nil {
				firstErr = patchErr
			}
			continue
		}

		namedCopied++
		alreadyCopied[copiedKey(tgt.Namespace, tgt.Name)] = true
		logx.Log("Hand-off: copied %s/%s (%s) from %s to management cluster.",
			tgt.Namespace, tgt.Name, tgt.Description, kindCtx)
	}

	// Hand off all per-config namespaces (labeled infra.yage-deployment.bucaniere.us=true).
	// Each namespace holds a "bootstrap-config" Secret. Mirror namespace + labels + secret.
	if nsList, nsListErr := srcCli.Typed.CoreV1().Namespaces().List(bg, metav1.ListOptions{
		LabelSelector: "infra.yage-deployment.bucaniere.us=true",
	}); nsListErr == nil {
		for _, srcNS := range nsList.Items {
			nsName := srcNS.Name
			// Ensure the namespace exists on management with the same labels.
			nsLabels := map[string]string{"infra.yage-deployment.bucaniere.us": "true"}
			if prov, ok := srcNS.Labels["infra.capi-provider.bucaniere.us"]; ok && prov != "" {
				nsLabels["infra.capi-provider.bucaniere.us"] = prov
			}
			if !ensuredNS[nsName] {
				if nsErr := dstCli.EnsureNamespaceWithLabels(bg, nsName, nsLabels); nsErr != nil {
					logx.Warn("Hand-off: failed to ensure namespace %s on management: %v", nsName, nsErr)
					if firstErr == nil {
						firstErr = nsErr
					}
					continue
				}
				ensuredNS[nsName] = true
			}
			// Copy the bootstrap-config Secret if present.
			src, secErr := srcCli.Typed.CoreV1().Secrets(nsName).Get(bg, "bootstrap-config", metav1.GetOptions{})
			if secErr != nil {
				continue // namespace exists but secret not yet written — normal during first-run
			}
			clean := corev1.Secret{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
				ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: nsName, Labels: src.Labels, Annotations: stripServerAnnotations(src.Annotations)},
				Type:       src.Type,
				Data:       src.Data,
				StringData: src.StringData,
				Immutable:  src.Immutable,
			}
			if clean.Type == "" {
				clean.Type = corev1.SecretTypeOpaque
			}
			body, merr := json.Marshal(clean)
			if merr != nil {
				logx.Warn("Hand-off: failed to marshal %s/bootstrap-config: %v", nsName, merr)
				if firstErr == nil {
					firstErr = merr
				}
				continue
			}
			_, perr := dstCli.Typed.CoreV1().Secrets(nsName).Patch(
				bg, "bootstrap-config", types.ApplyPatchType, body,
				metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: boolTrue()},
			)
			if perr != nil {
				logx.Warn("Hand-off: failed to apply %s/bootstrap-config on management: %v", nsName, perr)
				if firstErr == nil {
					firstErr = perr
				}
				continue
			}
			namedCopied++
			alreadyCopied[copiedKey(nsName, "bootstrap-config")] = true
			logx.Log("Hand-off: copied %s/bootstrap-config (config %s) from %s to management cluster.",
				nsName, src.Labels["yage.io/config-name"], kindCtx)
		}
	}

	// Label-scoped second pass per ADR 0011 §2: every Secret in
	// yage-system carrying app.kubernetes.io/managed-by=yage that wasn't
	// already applied above is server-side-applied to the destination.
	// This is what carries OpenTofu kubernetes-backend state Secrets
	// (tfstate-default-<module>) and any future yage-managed Secrets that
	// opt into the label across to mgmt without code changes here.
	yageNS := YageSystemNamespace
	if !ensuredNS[yageNS] {
		if nsErr := dstCli.EnsureNamespace(bg, yageNS); nsErr != nil {
			logx.Warn("Hand-off: failed to ensure namespace %s on management: %v", yageNS, nsErr)
			if firstErr == nil {
				firstErr = nsErr
			}
		} else {
			ensuredNS[yageNS] = true
		}
	}
	if ensuredNS[yageNS] {
		labelN, labelErr := copyYageSystemSecrets(bg, srcCli, dstCli, yageNS, alreadyCopied, kindCtx)
		labelCopied = labelN
		if labelErr != nil && firstErr == nil {
			firstErr = labelErr
		}
	}

	logx.Log("Hand-off summary: %d named + %d labeled = %d Secret(s) copied from kind context %s to management cluster.",
		namedCopied, labelCopied, namedCopied+labelCopied, kindCtx)
	return HandoffResult{
		NamedCopied: namedCopied,
		LabelCopied: labelCopied,
		FirstErr:    firstErr,
	}, firstErr
}

// copyYageSystemSecrets lists every Secret in namespace ns on srcCli that
// carries label app.kubernetes.io/managed-by=yage and server-side-applies
// it to dstCli (Force=true). Secrets whose (namespace, name) appears in
// skip are not re-applied; the caller passes the keys handled by the
// named pass to avoid double-counting.
//
// Returns the count of Secrets actually applied and the first per-Secret
// error encountered. Per-Secret failures are warnings — handoff is
// best-effort, the caller chains a VerifyParity step after.
func copyYageSystemSecrets(
	ctx context.Context,
	srcCli, dstCli *k8sclient.Client,
	ns string,
	skip map[string]bool,
	kindCtx string,
) (int, error) {
	list, listErr := srcCli.Typed.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
		LabelSelector: yageManagedBySelector,
	})
	if listErr != nil {
		logx.Warn("Hand-off: failed to list labeled Secrets in %s on kind: %v", ns, listErr)
		return 0, listErr
	}

	var (
		copied   int
		firstErr error
	)
	for i := range list.Items {
		src := &list.Items[i]
		key := ns + "/" + src.Name
		if skip != nil && skip[key] {
			continue
		}

		clean := corev1.Secret{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{
				Name:        src.Name,
				Namespace:   src.Namespace,
				Labels:      src.Labels,
				Annotations: stripServerAnnotations(src.Annotations),
			},
			Type:       src.Type,
			Data:       src.Data,
			StringData: src.StringData,
			Immutable:  src.Immutable,
		}
		if clean.Type == "" {
			clean.Type = corev1.SecretTypeOpaque
		}
		body, marshalErr := json.Marshal(clean)
		if marshalErr != nil {
			logx.Warn("Hand-off: failed to marshal %s/%s: %v", ns, src.Name, marshalErr)
			if firstErr == nil {
				firstErr = marshalErr
			}
			continue
		}
		_, patchErr := dstCli.Typed.CoreV1().Secrets(ns).Patch(
			ctx, src.Name, types.ApplyPatchType, body,
			metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: boolTrue()},
		)
		if patchErr != nil {
			logx.Warn("Hand-off: failed to apply %s/%s on management: %v", ns, src.Name, patchErr)
			if firstErr == nil {
				firstErr = patchErr
			}
			continue
		}
		copied++
		logx.Log("Hand-off (label): copied %s/%s from %s to management cluster.", ns, src.Name, kindCtx)
	}
	return copied, firstErr
}

// ListBootstrapSecretsOnManagement reports which of the expected
// bootstrap Secrets are now present on the management cluster — used by
// pivot.VerifyParity (the pivot agent's package). Returns a map keyed by
// secret name, value = true when present.
//
// Keys in the returned map are the Secret names only (not namespace-
// qualified). Where multiple expected Secrets would share a name across
// different namespaces, the namespace prefix is added on collision so the
// caller never silently overwrites an entry.
func ListBootstrapSecretsOnManagement(cfg *config.Config, mgmtKubeconfig string) (map[string]bool, error) {
	if cfg == nil {
		return nil, fmt.Errorf("list: nil config")
	}
	cli, err := k8sclient.ForKubeconfigFile(mgmtKubeconfig)
	if err != nil {
		return nil, fmt.Errorf("list: load management kubeconfig %q: %w", mgmtKubeconfig, err)
	}
	bg := context.Background()

	out := map[string]bool{}
	for _, tgt := range expectedHandoffTargets(cfg) {
		if tgt.Namespace == "" || tgt.Name == "" {
			continue
		}
		key := tgt.Name
		if _, clash := out[key]; clash {
			key = tgt.Namespace + "/" + tgt.Name
		}

		_, getErr := cli.Typed.CoreV1().Secrets(tgt.Namespace).
			Get(bg, tgt.Name, metav1.GetOptions{})
		switch {
		case getErr == nil:
			out[key] = true
		case apierrors.IsNotFound(getErr):
			out[key] = false
		default:
			// Treat unknown errors as "absent" so VerifyParity drives
			// retries, but surface the first error to the caller so
			// connection issues aren't silently swallowed.
			out[key] = false
			return out, fmt.Errorf("list: get %s/%s: %w", tgt.Namespace, tgt.Name, getErr)
		}
	}
	return out, nil
}

// stripServerAnnotations drops annotations that the API server fills in
// or that pin a manifest to its source cluster — they have no meaning on
// the destination and would just produce SSA noise.
func stripServerAnnotations(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	drop := map[string]struct{}{
		"kubectl.kubernetes.io/last-applied-configuration": {},
		"deployment.kubernetes.io/revision":                {},
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if _, skip := drop[k]; skip {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// boolTrue is the *bool form of true used by metav1.PatchOptions.Force.
// Local to handoff.go so we don't collide with config.go's boolPtr.
func boolTrue() *bool {
	b := true
	return &b
}