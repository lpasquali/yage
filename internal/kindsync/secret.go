// Package kindsync ports the bash helpers that push Proxmox bootstrap
// state into kind Secrets (so a re-run finds the same state in the
// management cluster when local env is thin).
//
// Bash source map (the original bash port):
//   - sync_bootstrap_config_to_kind                        ~L840-L850
//   - sync_proxmox_bootstrap_literal_credentials_to_kind   ~L857-L1043
//   - _apply_proxmox_bootstrap_admin_yaml_to_kind          ~L1046-L1103
//   - update_capmox_manager_secret_on_kind                 ~L3154-L3169
//   - rollout_restart_capmox_controller                    ~L3171-L3177
//   - rollout_restart_proxmox_csi_on_workload              ~L3179-L3196
//
// Stubs (port deferred):
//   - _get_all_bootstrap_variables_as_yaml                 ~L3620-L3692
//   - apply_bootstrap_config_to_management_cluster         ~L3692-L3810
//   - merge_proxmox_bootstrap_secrets_from_kind            ~L3815-L3870
package kindsync

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/k8sclient"
	"github.com/lpasquali/yage/internal/logx"
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
