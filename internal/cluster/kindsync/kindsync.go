// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/platform/kubectl"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/provider/proxmox/pveapi"
	"github.com/lpasquali/yage/internal/platform/shell"
)

// SyncBootstrapConfigToKind ports sync_bootstrap_config_to_kind.
// Requires kubectl + kind on PATH and the resolved kind context to match
// an existing kind cluster. Delegates the real work to the (still
// unported) _get_all_bootstrap_variables_as_yaml /
// apply_bootstrap_config_to_management_cluster pair — emits a warn for
// now so callers see the gap.
func SyncBootstrapConfigToKind(cfg *config.Config) error {
	if !shell.CommandExists("kind") {
		return nil
	}
	ctx, ok := kubectl.ResolveBootstrapContext(cfg)
	if !ok {
		return nil
	}
	name := strings.TrimPrefix(ctx, "kind-")
	if !kindClusterExists(name) {
		return nil
	}
	return applyBootstrapConfigToManagementCluster(cfg, ctx)
}

// SyncProxmoxBootstrapLiteralCredentialsToKind ports
// sync_proxmox_bootstrap_literal_credentials_to_kind.
//
// Two shapes:
//   - Legacy single Secret (PROXMOX_BOOTSTRAP_SECRET_NAME set): CAPI+CSI
//     keys live in one Secret; admin YAML lives alongside (same Secret if
//     admin-Secret-name matches, else a split Secret).
//   - Default split (PROXMOX_BOOTSTRAP_SECRET_NAME empty):
//     ${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME} gets the CAPI/clusterctl
//     keys; ${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} gets the CSI config
//     surface; ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME} gets the admin YAML.
//
// Also calls update_capmox_manager_secret_on_kind so capmox-system's live
// copy is restored on the next sync.
func SyncProxmoxBootstrapLiteralCredentialsToKind(cfg *config.Config) error {
	if !shell.CommandExists("kind") {
		return nil
	}
	ctx, ok := kubectl.ResolveBootstrapContext(cfg)
	if !ok {
		return nil
	}
	name := strings.TrimPrefix(ctx, "kind-")
	if !kindClusterExists(name) {
		return nil
	}
	ns := cfg.Providers.Proxmox.BootstrapSecretNamespace
	if err := ensureNamespace(ctx, ns); err != nil {
		return err
	}

	env := proxmoxEnvMap(cfg)
	lookup := func(k string) string { return env[k] }

	// --- Legacy single-Secret branch ---
	if cfg.Providers.Proxmox.BootstrapSecretName != "" {
		legacyKeys := []string{
			"PROXMOX_URL", "PROXMOX_TOKEN", "PROXMOX_SECRET",
			"PROXMOX_CSI_URL", "PROXMOX_CSI_TOKEN_ID", "PROXMOX_CSI_TOKEN_SECRET",
			"PROXMOX_REGION", "PROXMOX_NODE",
		}
		_ = (kindSecret{
			Context:     ctx,
			Namespace:   ns,
			Name:        cfg.Providers.Proxmox.BootstrapSecretName,
			AllowedKeys: legacyKeys,
			LookupValue: lookup,
		}).apply()
		logx.Log("Updated %s/%s (legacy CAPI/CSI from current environment).", ns, cfg.Providers.Proxmox.BootstrapSecretName)

		adminTarget := cfg.Providers.Proxmox.BootstrapSecretName
		if cfg.Providers.Proxmox.BootstrapAdminSecretName != "" &&
			cfg.Providers.Proxmox.BootstrapAdminSecretName != cfg.Providers.Proxmox.BootstrapSecretName {
			adminTarget = cfg.Providers.Proxmox.BootstrapAdminSecretName
		}
		_ = applyAdminYAMLToKind(cfg, ctx, adminTarget)
		_ = UpdateCapmoxManagerSecretOnKind(cfg)
		return nil
	}

	// --- Default split: CAPMOX (clusterctl) Secret ---
	capmoxKeys := []string{
		"PROXMOX_URL", "PROXMOX_TOKEN", "PROXMOX_SECRET",
		"PROXMOX_REGION", "PROXMOX_NODE",
	}
	_ = (kindSecret{
		Context:     ctx,
		Namespace:   ns,
		Name:        cfg.Providers.Proxmox.BootstrapCAPMOXSecretName,
		AllowedKeys: capmoxKeys,
		LookupValue: lookup,
	}).apply()
	logx.Log("Updated %s/%s (CAPI / clusterctl keys from current environment).", ns, cfg.Providers.Proxmox.BootstrapCAPMOXSecretName)

	// Derive PROXMOX_CSI_URL from PROXMOX_URL when not set — bash L979-L982.
	if cfg.Providers.Proxmox.URL != "" && cfg.Providers.Proxmox.CSIURL == "" {
		cfg.Providers.Proxmox.CSIURL = pveapi.APIJSONURL(cfg)
		env["PROXMOX_CSI_URL"] = cfg.Providers.Proxmox.CSIURL
	}

	// --- Default split: CSI Secret ---
	csiKeys := []string{
		"PROXMOX_URL", "PROXMOX_REGION", "PROXMOX_NODE",
		"PROXMOX_CSI_URL", "PROXMOX_CSI_TOKEN_ID", "PROXMOX_CSI_TOKEN_SECRET",
		"PROXMOX_CSI_USER_ID", "PROXMOX_CSI_TOKEN_PREFIX", "PROXMOX_CSI_INSECURE",
		"PROXMOX_CSI_STORAGE_CLASS_NAME", "PROXMOX_CSI_STORAGE",
		"PROXMOX_CSI_RECLAIM_POLICY", "PROXMOX_CSI_FSTYPE", "PROXMOX_CSI_DEFAULT_CLASS",
		"PROXMOX_CSI_TOPOLOGY_LABELS", "PROXMOX_TOPOLOGY_REGION", "PROXMOX_TOPOLOGY_ZONE",
		"PROXMOX_CSI_CHART_REPO_URL", "PROXMOX_CSI_CHART_NAME", "PROXMOX_CSI_CHART_VERSION",
		"PROXMOX_CSI_NAMESPACE", "PROXMOX_CSI_CONFIG_PROVIDER",
		"PROXMOX_CSI_SMOKE_ENABLED",
		"ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL", "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH",
		"ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF", "ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE",
	}
	_ = (kindSecret{
		Context:     ctx,
		Namespace:   ns,
		Name:        cfg.Providers.Proxmox.BootstrapCSISecretName,
		AllowedKeys: csiKeys,
		LookupValue: lookup,
	}).apply()
	logx.Log("Updated %s/%s (CSI: API URL, token id/secret, user id, chart/storage toggles, … from current environment).",
		ns, cfg.Providers.Proxmox.BootstrapCSISecretName)

	if cfg.Providers.Proxmox.CSITokenID == "" || cfg.Providers.Proxmox.CSITokenSecret == "" {
		logx.Warn("PROXMOX_CSI_TOKEN_ID/PROXMOX_CSI_TOKEN_SECRET are unset; %s will not contain CSI tokens until Terraform identity outputs or the environment set them (URL/region are still written when known).",
			cfg.Providers.Proxmox.BootstrapCSISecretName)
	}

	_ = applyAdminYAMLToKind(cfg, ctx, cfg.Providers.Proxmox.BootstrapAdminSecretName)
	_ = UpdateCapmoxManagerSecretOnKind(cfg)
	return nil
}

// applyAdminYAMLToKind ports _apply_proxmox_bootstrap_admin_yaml_to_kind.
// Merges existing Secret data with a generated proxmox-admin.yaml blob
// (PROXMOX_URL + PROXMOX_ADMIN_* lines). No-op when all admin env vars
// are empty and there is nothing to write.
func applyAdminYAMLToKind(cfg *config.Config, kctx, targetSecret string) error {
	if targetSecret == "" {
		return nil
	}
	adminKeys := []string{
		"PROXMOX_URL", "PROXMOX_ADMIN_USERNAME", "PROXMOX_ADMIN_TOKEN", "PROXMOX_ADMIN_INSECURE",
	}
	env := proxmoxEnvMap(cfg)
	hasAny := false
	for _, k := range adminKeys {
		if env[k] != "" {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return nil
	}

	// Build the flat YAML body: lines of `KEY: "value"` using JSON quoting
	// for values, matching the bash pyjson.dumps().
	var sb strings.Builder
	for _, k := range adminKeys {
		v := env[k]
		if v == "" {
			continue
		}
		q, _ := json.Marshal(v)
		sb.WriteString(k)
		sb.WriteString(": ")
		sb.Write(q)
		sb.WriteByte('\n')
	}
	text := sb.String()
	if strings.TrimSpace(text) == "" {
		return nil
	}

	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		logx.Warn("Failed to load kubeconfig for %s.", kctx)
		return err
	}
	bg := context.Background()

	// Fetch existing Secret, preserve *all* existing data entries (no
	// key whitelist for admin), overlay the admin YAML under the
	// configured key.
	data := map[string][]byte{}
	var existingLabels map[string]string
	cur, getErr := cli.Typed.CoreV1().Secrets(cfg.Providers.Proxmox.BootstrapSecretNamespace).
		Get(bg, targetSecret, metav1.GetOptions{})
	if getErr == nil && cur != nil {
		for k, v := range cur.Data {
			if len(v) > 0 {
				data[k] = v
			}
		}
		existingLabels = cur.Labels
	}
	ak := cfg.Providers.Proxmox.BootstrapAdminSecretKey
	if ak == "" {
		ak = "proxmox-admin.yaml"
	}
	data[ak] = []byte(text)

	if err := applySecret(bg, cli, cfg.Providers.Proxmox.BootstrapSecretNamespace, targetSecret, data, existingLabels); err != nil {
		logx.Warn("Failed to update %s/%s (proxmox-admin.yaml).", cfg.Providers.Proxmox.BootstrapSecretNamespace, targetSecret)
		return err
	}
	logx.Log("Updated %s/%s (merged %s from current environment).",
		cfg.Providers.Proxmox.BootstrapSecretNamespace, targetSecret, ak)
	return nil
}

// UpdateCapmoxManagerSecretOnKind ports update_capmox_manager_secret_on_kind.
// Writes url/token/secret into capmox-system/capmox-manager-credentials —
// required so a deleted in-cluster capmox credential is restored on the
// next sync without waiting for the full Argo / CAAPH loop.
func UpdateCapmoxManagerSecretOnKind(cfg *config.Config) error {
	if cfg.Providers.Proxmox.URL == "" || cfg.Providers.Proxmox.Token == "" || cfg.Providers.Proxmox.Secret == "" {
		return nil
	}
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		logx.Warn("Failed to load kubeconfig for %s — skip capmox-manager-credentials update.", kctx)
		return nil
	}
	bg := context.Background()
	if _, err := cli.Typed.CoreV1().Namespaces().Get(bg, "capmox-system", metav1.GetOptions{}); err != nil {
		logx.Warn("namespace capmox-system not on %s — skip capmox-manager-credentials update.", kctx)
		return nil
	}
	data := map[string][]byte{
		"url":    []byte(cfg.Providers.Proxmox.URL),
		"token":  []byte(cfg.Providers.Proxmox.Token),
		"secret": []byte(cfg.Providers.Proxmox.Secret),
	}
	if err := applySecret(bg, cli, "capmox-system", "capmox-manager-credentials", data, nil); err != nil {
		logx.Die("Failed to update capmox-system/capmox-manager-credentials on %s: %v", kctx, err)
	}
	logx.Log("Updated capmox-system/capmox-manager-credentials on %s.", kctx)
	return nil
}

// RolloutRestartCapmoxController ports rollout_restart_capmox_controller.
// Best-effort: if the deployment is not ready, warn and continue — bash
// uses `|| warn` so never fails the script.
func RolloutRestartCapmoxController(cfg *config.Config) {
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		logx.Warn("capmox-controller-manager restart skipped or not ready (check capmox-system).")
		return
	}
	if err := rolloutRestartDeployment(cli, "capmox-system", "capmox-controller-manager"); err != nil {
		logx.Warn("capmox-controller-manager restart skipped or not ready (check capmox-system).")
		return
	}
	if err := waitDeploymentReady(cli, "capmox-system", "capmox-controller-manager", 180*time.Second); err != nil {
		logx.Warn("capmox-controller-manager restart skipped or not ready (check capmox-system).")
	}
}

// RolloutRestartProxmoxCSIOnWorkload ports rollout_restart_proxmox_csi_on_workload.
// Fetches the workload cluster's kubeconfig from the capi Secret on kind,
// builds an in-process client against it, and restarts
// proxmox-csi-plugin-controller in the CSI namespace on the workload.
// No-op when the kubeconfig secret or the target deployment are missing.
func RolloutRestartProxmoxCSIOnWorkload(cfg *config.Config) {
	kctx := "kind-" + cfg.KindClusterName
	kcfg, err := writeWorkloadKubeconfig(cfg, kctx)
	if err != nil {
		logx.Warn("No workload kubeconfig — skip Proxmox CSI controller restart on workload.")
		return
	}
	defer removeFile(kcfg)

	cli, err := k8sclient.ForKubeconfigFile(kcfg)
	if err != nil {
		logx.Warn("Failed to load workload kubeconfig — skip Proxmox CSI controller restart.")
		return
	}
	ns := cfg.Providers.Proxmox.CSINamespace
	bg := context.Background()
	if _, err := cli.Typed.AppsV1().Deployments(ns).Get(bg, "proxmox-csi-plugin-controller", metav1.GetOptions{}); err != nil {
		logx.Warn("proxmox-csi controller deployment not found in %s — skip restart.", ns)
		return
	}
	_ = rolloutRestartDeployment(cli, ns, "proxmox-csi-plugin-controller")
	_ = waitDeploymentReady(cli, ns, "proxmox-csi-plugin-controller", 300*time.Second)
	logx.Log("Restarted Proxmox CSI controller on workload %s.", cfg.WorkloadClusterName)
}

// rolloutRestartDeployment mirrors `kubectl rollout restart deploy/X` —
// patches the spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]
// with the current RFC3339 timestamp; the deployment controller picks
// that up as a pod-template change and rolls.
func rolloutRestartDeployment(cli *k8sclient.Client, ns, name string) error {
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().Format(time.RFC3339),
	)
	_, err := cli.Typed.AppsV1().Deployments(ns).Patch(
		context.Background(), name, types.StrategicMergePatchType,
		[]byte(patch), metav1.PatchOptions{},
	)
	return err
}

// waitDeploymentReady mirrors `kubectl rollout status deploy/X --timeout=...`.
// Polls the Deployment status until updated/ready replicas match the
// spec or timeout elapses.
func waitDeploymentReady(cli *k8sclient.Client, ns, name string, timeout time.Duration) error {
	return k8sclient.PollUntil(context.Background(), 2*time.Second, timeout,
		func(ctx context.Context) (bool, error) {
			d, err := cli.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			if d.Generation > d.Status.ObservedGeneration {
				return false, nil
			}
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			if d.Status.UpdatedReplicas < desired {
				return false, nil
			}
			if d.Status.AvailableReplicas < desired {
				return false, nil
			}
			return true, nil
		})
}