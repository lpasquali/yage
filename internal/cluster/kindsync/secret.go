// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package kindsync pushes Proxmox bootstrap state into kind
// Secrets (so a re-run finds the same state in the management
// cluster when local env is thin) and pulls it back on demand.
package kindsync

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// kindSecret models a kind Secret merge: take the existing in-cluster
// data (restricted to AllowedKeys), overlay new non-empty values for the
// same keys, server-side-apply a fresh Secret object.
//
// The label block is preserved from the existing object when present.
// Missing AllowedKeys are not erased on the kind side (data is merged,
// not replaced) — matches the bash Python behaviour.
type kindSecret struct {
	Context     string
	Namespace   string
	Name        string
	AllowedKeys []string
	// LookupValue returns the environment value (or current config) for
	// key. Empty string means "skip / don't overlay".
	LookupValue func(key string) string
}

// apply mirrors the `printf '%s' "$existing_json" | python3 -c '...' |
// kubectl apply -f -` pipeline in bash. Returns nil on success; warns
// and returns an error on a failed apply so callers can log and move on
// (the bash uses `|| warn` then continues).
func (s kindSecret) apply() error {
	cli, err := k8sclient.ForContext(s.Context)
	if err != nil {
		logx.Warn("Failed to update %s/%s.", s.Namespace, s.Name)
		return err
	}
	bg := context.Background()

	allowed := make(map[string]struct{}, len(s.AllowedKeys))
	for _, k := range s.AllowedKeys {
		allowed[k] = struct{}{}
	}

	out := map[string][]byte{}
	var labels map[string]string

	// Preserve existing data for the allowed key set.
	if cur, getErr := cli.Typed.CoreV1().Secrets(s.Namespace).Get(bg, s.Name, metav1.GetOptions{}); getErr == nil && cur != nil {
		for k, v := range cur.Data {
			if _, ok := allowed[k]; ok && len(v) > 0 {
				out[k] = v
			}
		}
		labels = cur.Labels
	}
	// Overlay with current env/config values (raw bytes — the typed
	// Secret object owns the b64 wire encoding).
	for _, k := range s.AllowedKeys {
		v := s.LookupValue(k)
		if v == "" {
			continue
		}
		out[k] = []byte(v)
	}

	if err := applySecret(bg, cli, s.Namespace, s.Name, out, labels); err != nil {
		logx.Warn("Failed to update %s/%s.", s.Namespace, s.Name)
		return err
	}
	return nil
}

// ensureNamespace mirrors the idempotent
// `kubectl create ns ... --dry-run=client -o yaml | kubectl apply -f -`.
func ensureNamespace(kctx, ns string) error {
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return err
	}
	return cli.EnsureNamespace(context.Background(), ns)
}