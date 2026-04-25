package kindsync

// handoff.go — bootstrap-state Secret hand-off from kind → Proxmox-hosted
// management cluster. Lives alongside the existing kind-write helpers
// (config.go, kindsync.go, secret.go) and reuses their primitives:
//
//   - getSecretJSON: typed Get + json.Marshal on the source (kind) side.
//   - applySecret:   server-side-apply on the destination (mgmt) side.
//   - k8sclient.FieldManager / k8sclient.ForContext / ForKubeconfigFile.
//
// The set of Secrets handled mirrors what
// SyncBootstrapConfigToKind + SyncProxmoxBootstrapLiteralCredentialsToKind
// write into kind, plus the live capmox-system/capmox-manager-credentials
// copy that bash carries across as part of the same hand-off.
//
// CAPI inventory (Cluster, Machines, KubeadmConfig, etc.) is *not* this
// package's concern — clusterctl move handles that. We own the
// proxmox-bootstrap-system Secrets and the live capmox copy only.

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/k8sclient"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
)

// capmoxLiveNamespace / capmoxLiveSecretName are the live capmox-controller
// credentials that bash also carries across in the same hand-off step.
// These are not bootstrap-state per se, but a deleted in-cluster copy is
// restored on next sync only after this Secret reappears, so we keep it
// in lockstep with the rest of the move.
const (
	capmoxLiveNamespace  = "capmox-system"
	capmoxLiveSecretName = "capmox-manager-credentials"
)

// handoffTarget describes one Secret to copy from kind → mgmt. Namespace
// is resolved at call-time so an empty config field (e.g. legacy
// ProxmoxBootstrapSecretName) is treated as "no such Secret to copy".
type handoffTarget struct {
	Namespace string
	Name      string
	// Description is the short label used in the per-Secret log line.
	Description string
}

// expectedHandoffTargets returns the list of Secrets we attempt to hand off,
// in deterministic order. Empty Name entries are filtered by the caller —
// e.g. the legacy single-Secret name is empty in the default split layout.
func expectedHandoffTargets(cfg *config.Config) []handoffTarget {
	ns := cfg.ProxmoxBootstrapSecretNamespace
	return []handoffTarget{
		{Namespace: ns, Name: cfg.ProxmoxBootstrapConfigSecretName, Description: "bootstrap config snapshot"},
		{Namespace: ns, Name: cfg.ProxmoxBootstrapCAPMOXSecretName, Description: "CAPI / clusterctl credentials"},
		{Namespace: ns, Name: cfg.ProxmoxBootstrapCSISecretName, Description: "CSI credentials"},
		{Namespace: ns, Name: cfg.ProxmoxBootstrapAdminSecretName, Description: "admin token YAML"},
		{Namespace: ns, Name: cfg.ProxmoxBootstrapSecretName, Description: "legacy single-Secret credentials"},
		{Namespace: capmoxLiveNamespace, Name: capmoxLiveSecretName, Description: "live capmox-controller credentials"},
	}
}

// HandOffBootstrapSecretsToManagement copies the proxmox-bootstrap-system
// namespace and its Secrets from the kind management context to the
// freshly-provisioned Proxmox management cluster.
//
// Secrets copied (when present on kind):
//   - <ProxmoxBootstrapConfigSecretName>          (config.yaml: snapshot of cfg)
//   - <ProxmoxBootstrapCAPMOXSecretName>          (CAPI clusterctl creds)
//   - <ProxmoxBootstrapCSISecretName>             (CSI creds)
//   - <ProxmoxBootstrapAdminSecretName>           (admin token YAML)
//   - <ProxmoxBootstrapSecretName>                (legacy single-Secret form)
//   - capmox-system/capmox-manager-credentials    (the live capmox copy)
//
// Both kindCtx and mgmtKubeconfig identify the source / destination. The
// destination namespace is created when missing. Returns the count of
// Secrets copied + any error encountered (we keep going on per-Secret
// failures to maximise data-arrival; the caller chains a VerifyParity
// step after).
func HandOffBootstrapSecretsToManagement(cfg *config.Config, kindCtx, mgmtKubeconfig string) (copied int, err error) {
	if cfg == nil {
		return 0, fmt.Errorf("handoff: nil config")
	}
	srcCli, srcErr := k8sclient.ForContext(kindCtx)
	if srcErr != nil {
		return 0, fmt.Errorf("handoff: load kind context %q: %w", kindCtx, srcErr)
	}
	dstCli, dstErr := k8sclient.ForKubeconfigFile(mgmtKubeconfig)
	if dstErr != nil {
		return 0, fmt.Errorf("handoff: load management kubeconfig %q: %w", mgmtKubeconfig, dstErr)
	}
	bg := context.Background()

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
		// provisioned, so neither proxmox-bootstrap-system nor
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

		copied++
		logx.Log("Hand-off: copied %s/%s (%s) from %s to management cluster.",
			tgt.Namespace, tgt.Name, tgt.Description, kindCtx)
	}

	logx.Log("Hand-off summary: %d Secret(s) copied from kind context %s to management cluster.", copied, kindCtx)
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
