// Package kindsync ports the bash helpers that push Proxmox bootstrap
// state into kind Secrets (so a re-run finds the same state in the
// management cluster when local env is thin).
//
// Bash source map (bootstrap-capi.sh):
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
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// kindSecret models a kind Secret merge: take the existing in-cluster
// data (restricted to AllowedKeys), overlay new non-empty values for the
// same keys, emit a fresh Secret JSON, then `kubectl apply -f -` it.
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
	raw, _, _ := shell.Capture(
		"kubectl", "--context", s.Context,
		"get", "secret", s.Name,
		"-n", s.Namespace, "-o", "json",
	)
	var cur struct {
		Metadata struct {
			Labels map[string]string `json:"labels,omitempty"`
		} `json:"metadata,omitempty"`
		Data map[string]string `json:"data,omitempty"`
	}
	_ = json.Unmarshal([]byte(raw), &cur)

	allowed := make(map[string]struct{}, len(s.AllowedKeys))
	for _, k := range s.AllowedKeys {
		allowed[k] = struct{}{}
	}

	out := map[string]string{}
	// Preserve existing data for the allowed key set.
	for k, v := range cur.Data {
		if _, ok := allowed[k]; ok && v != "" {
			out[k] = v
		}
	}
	// Overlay with current env/config values, b64-encoded.
	for _, k := range s.AllowedKeys {
		v := s.LookupValue(k)
		if v == "" {
			continue
		}
		out[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}

	meta := map[string]any{
		"name":      s.Name,
		"namespace": s.Namespace,
	}
	if len(cur.Metadata.Labels) > 0 {
		meta["labels"] = cur.Metadata.Labels
	}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   meta,
		"type":       "Opaque",
		"data":       out,
	}
	doc, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	if err := shell.Pipe(string(doc), "kubectl", "--context", s.Context, "apply", "-f", "-"); err != nil {
		logx.Warn("Failed to update %s/%s.", s.Namespace, s.Name)
		return err
	}
	return nil
}

// ensureNamespace mirrors the idempotent
// `kubectl create ns ... --dry-run=client -o yaml | kubectl apply -f -`.
func ensureNamespace(ctx, ns string) error {
	doc := fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q}}`, ns)
	return shell.Pipe(doc, "kubectl", "--context", ctx, "apply", "-f", "-")
}
