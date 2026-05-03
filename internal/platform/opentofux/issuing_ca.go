// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

// EnsureIssuingCA provisions an intermediate/issuing CA for the workload cluster
// (ADR 0009 §3, Phase H gap 2). It applies the yage-tofu/issuing-ca/ module via
// JobRunner against the management cluster, reads back the intermediate cert /
// key from `tofu output -json`, stores the material as a Secret in yage-system
// on the management cluster, and applies a cert-manager ClusterIssuer to the
// workload cluster.
//
// The offline-root boundary (ADR 0009 §4) is preserved: cfg.IssuingCARootCert
// and cfg.IssuingCARootKey are operator-supplied and passed into the module as
// TF_VAR_* values via the JobRunner credentials Secret. The root key never
// leaves the management cluster — the module signs the intermediate inside the
// pod and only the intermediate cert/key are read back as outputs.
//
// State storage uses the kubernetes backend per ADR 0011 §1 (Secret
// tfstate-default-issuing-ca in yage-system, copied across the kind→mgmt
// pivot via the label-based handoff in HandOffBootstrapSecretsToManagement).
//
// ErrNotApplicable is returned when either IssuingCARootCert or
// IssuingCARootKey is empty, so the orchestrator can call this function
// unconditionally and skip gracefully when the operator has not provided CA
// material.
//
// Cert-manager timing note: the ClusterIssuer CRD may not yet exist on the
// workload cluster when this runs (cert-manager is deployed asynchronously via
// Argo CD). When REST mapping for ClusterIssuer fails, a warning is logged and
// the function returns nil — the operator can re-apply via --workload-rollout.

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// ErrNotApplicable is returned by EnsureIssuingCA when the operator has not
// provided the root CA material (IssuingCARootCert or IssuingCARootKey empty).
var ErrNotApplicable = errors.New("issuing CA: not applicable")

const (
	issuingCASecretName    = "yage-issuing-ca"
	issuingCANamespace     = "yage-system"
	issuingCACertManagerNS = "cert-manager"
	issuingCAClusterIssuer = "yage-ca-issuer"
	issuingCAModule        = "issuing-ca"
	issuingCAApplyTimeout  = 30 * time.Second
)

// IssuingCAOutputs holds the structured outputs consumed from the
// yage-tofu/issuing-ca/ module after a successful apply.
type IssuingCAOutputs struct {
	IntermediateCertPEM string
	IntermediateKeyPEM  string
	CAChainPEM          string
}

// EnsureIssuingCA applies the yage-tofu/issuing-ca/ module via a JobRunner on
// the management cluster, reads back the intermediate cert+key, stores them
// as a Secret in yage-system on the management cluster, and applies the
// cert-manager Secret + ClusterIssuer to the workload cluster.
// Returns ErrNotApplicable when IssuingCARootCert or IssuingCARootKey is empty.
//
// The management cluster client is derived from cfg: cfg.MgmtKubeconfigPath when
// set (post-pivot path), otherwise the kind-<KindClusterName> context. This
// avoids the stale-client problem that arises when the caller's mgmtCli was bound
// to the kind API server URL before pivot ran and rebindKindContextToMgmt rewrote
// the on-disk kubeconfig.
func EnsureIssuingCA(ctx context.Context, workloadKubeconfigPath string, cfg *config.Config) error {
	if cfg.IssuingCARootCert == "" || cfg.IssuingCARootKey == "" {
		return ErrNotApplicable
	}

	// Build the management cluster client fresh, honouring post-pivot path.
	mgmtCli, err := mgmtClientForIssuingCA(cfg)
	if err != nil {
		return err
	}

	// Step 1: Apply yage-tofu/issuing-ca/ module via JobRunner and read outputs.
	runner := &JobRunner{cfg: cfg, client: mgmtCli}
	vars := issuingCAVars(cfg)

	logx.Log("EnsureIssuingCA: applying yage-tofu/%s module (cluster_name=%s) ...",
		issuingCAModule, cfg.WorkloadClusterName)
	if err := runner.Apply(ctx, issuingCAModule, vars); err != nil {
		return fmt.Errorf("EnsureIssuingCA: tofu apply: %w", err)
	}

	rawOutputs, err := runner.Output(ctx, issuingCAModule)
	if err != nil {
		return fmt.Errorf("EnsureIssuingCA: tofu output: %w", err)
	}
	outputs, err := parseIssuingCAOutputs(rawOutputs)
	if err != nil {
		return fmt.Errorf("EnsureIssuingCA: parse outputs: %w", err)
	}
	logx.Log("EnsureIssuingCA: intermediate CA generated (CN=yage-issuing-ca-%s)", cfg.WorkloadClusterName)

	intermediateCertPEM := []byte(outputs.IntermediateCertPEM)
	intermediateKeyPEM := []byte(outputs.IntermediateKeyPEM)

	// Step 2: Store in yage-system on the management cluster.
	if err := applyIssuingCASecretToMgmt(ctx, mgmtCli, intermediateCertPEM, intermediateKeyPEM, cfg); err != nil {
		return fmt.Errorf("EnsureIssuingCA: store in management cluster: %w", err)
	}
	logx.Log("EnsureIssuingCA: Secret %s/%s applied to management cluster", issuingCANamespace, issuingCASecretName)

	// Step 3: Apply cert-manager Secret + ClusterIssuer to the workload cluster.
	workloadCli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("EnsureIssuingCA: connect to workload cluster: %w", err)
	}

	if err := applyIssuingCAToWorkload(ctx, workloadCli, intermediateCertPEM, intermediateKeyPEM, cfg); err != nil {
		// Non-fatal: cert-manager CRDs may not yet be installed on the workload.
		// The ClusterIssuer can be re-applied on next --workload-rollout.
		logx.Warn("EnsureIssuingCA: workload cluster cert-manager apply: %v (cert-manager may not yet be installed; re-run --workload-rollout once cert-manager is ready)", err)
	} else {
		logx.Log("EnsureIssuingCA: ClusterIssuer %s applied to workload cluster", issuingCAClusterIssuer)
	}

	return nil
}

// DestroyIssuingCA tears down the issuing-CA module by running tofu destroy
// against yage-tofu/issuing-ca/. It is called by PurgeGeneratedArtifacts
// before the kind cluster is deleted so the kubernetes-backend state Secret
// (tfstate-default-issuing-ca in yage-system) is still reachable.
//
// Returns ErrNotApplicable when IssuingCARootCert or IssuingCARootKey is empty
// (no issuing CA was provisioned, so there is no module state to destroy).
func DestroyIssuingCA(ctx context.Context, cli *k8sclient.Client, cfg *config.Config) error {
	if cfg.IssuingCARootCert == "" || cfg.IssuingCARootKey == "" {
		return ErrNotApplicable
	}
	runner := &JobRunner{cfg: cfg, client: cli}
	logx.Log("DestroyIssuingCA: running tofu destroy on issuing-ca module ...")
	if err := runner.Destroy(ctx, issuingCAModule); err != nil {
		return fmt.Errorf("DestroyIssuingCA: tofu destroy: %w", err)
	}
	logx.Log("DestroyIssuingCA: issuing-ca module destroyed.")
	return nil
}

// mgmtClientForIssuingCA builds a fresh management-cluster client honouring
// post-pivot kubeconfig paths. Extracted for clarity and to keep
// EnsureIssuingCA focused on the orchestration steps.
func mgmtClientForIssuingCA(cfg *config.Config) (*k8sclient.Client, error) {
	if cfg.MgmtKubeconfigPath != "" {
		cli, err := k8sclient.ForKubeconfigFile(cfg.MgmtKubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("EnsureIssuingCA: load mgmt kubeconfig %s: %w", cfg.MgmtKubeconfigPath, err)
		}
		return cli, nil
	}
	kindCtx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kindCtx)
	if err != nil {
		return nil, fmt.Errorf("EnsureIssuingCA: connect to %s: %w", kindCtx, err)
	}
	return cli, nil
}

// issuingCAVars builds the var map passed to the yage-tofu/issuing-ca/ module.
// Sensitive values (root cert + key) flow through TF_VAR_* env vars in the
// JobRunner credentials Secret and are never logged.
func issuingCAVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"cluster_name": cfg.WorkloadClusterName,
		"root_ca_cert": cfg.IssuingCARootCert,
		"root_ca_key":  cfg.IssuingCARootKey,
	}
}

// parseIssuingCAOutputs decodes the structured tofu output map into IssuingCAOutputs.
// Mirrors parseRegistryOutputs: handles both the wrapped {"value": ..., "type": ...}
// form and bare-string form that `tofu output -json` may emit.
func parseIssuingCAOutputs(raw map[string]any) (IssuingCAOutputs, error) {
	get := func(key string) string {
		v, ok := raw[key]
		if !ok {
			return ""
		}
		// Bare string (tofu -json sometimes omits the wrapper for simple types).
		if s, ok := v.(string); ok {
			return s
		}
		// Wrapped: {"value": "...", "type": "string", "sensitive": true}
		if m, ok := v.(map[string]any); ok {
			if s, ok := m["value"].(string); ok {
				return s
			}
		}
		return fmt.Sprintf("%v", v)
	}

	out := IssuingCAOutputs{
		IntermediateCertPEM: get("intermediate_cert_pem"),
		IntermediateKeyPEM:  get("intermediate_key_pem"),
		CAChainPEM:          get("ca_chain_pem"),
	}
	if out.IntermediateCertPEM == "" || out.IntermediateKeyPEM == "" {
		return out, fmt.Errorf("issuing-ca tofu outputs missing required fields (intermediate_cert_pem and/or intermediate_key_pem empty)")
	}
	return out, nil
}

// applyIssuingCASecretToMgmt applies the yage-issuing-ca Secret to yage-system
// on the management cluster. Uses SSA with Force: true.
func applyIssuingCASecretToMgmt(ctx context.Context, mgmtCli *k8sclient.Client, certPEM, keyPEM []byte, cfg *config.Config) error {
	applyCtx, cancel := context.WithTimeout(ctx, issuingCAApplyTimeout)
	defer cancel()

	if err := mgmtCli.EnsureNamespace(applyCtx, issuingCANamespace); err != nil {
		return fmt.Errorf("ensure namespace %s: %w", issuingCANamespace, err)
	}

	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      issuingCASecretName,
			Namespace: issuingCANamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "yage",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
			"ca.crt":  []byte(cfg.IssuingCARootCert),
		},
	}

	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("marshal Secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("yaml→json: %w", err)
	}

	force := true
	_, err = mgmtCli.Typed.CoreV1().Secrets(issuingCANamespace).Patch(
		applyCtx, issuingCASecretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	return err
}

// applyIssuingCAToWorkload applies the TLS Secret and ClusterIssuer to the
// workload cluster. The cert-manager namespace is created first (cert-manager
// may not be installed yet — only the namespace is needed for the Secret). If
// the ClusterIssuer CRD is not yet present, the error is returned so the caller
// can log a warning.
func applyIssuingCAToWorkload(ctx context.Context, workloadCli *k8sclient.Client, certPEM, keyPEM []byte, cfg *config.Config) error {
	applyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Ensure cert-manager namespace exists so the Secret can land even if
	// the cert-manager Helm chart hasn't run yet.
	if err := workloadCli.EnsureNamespace(applyCtx, issuingCACertManagerNS); err != nil {
		return fmt.Errorf("ensure namespace %s: %w", issuingCACertManagerNS, err)
	}

	// Apply the TLS Secret that cert-manager reads.
	caSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      issuingCASecretName,
			Namespace: issuingCACertManagerNS,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "yage",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt": certPEM,
			"tls.key": keyPEM,
		},
	}

	yamlBody, err := yaml.Marshal(caSecret)
	if err != nil {
		return fmt.Errorf("marshal cert-manager Secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("yaml→json cert-manager Secret: %w", err)
	}

	force := true
	_, err = workloadCli.Typed.CoreV1().Secrets(issuingCACertManagerNS).Patch(
		applyCtx, issuingCASecretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("apply cert-manager Secret %s/%s: %w", issuingCACertManagerNS, issuingCASecretName, err)
	}

	// Apply the ClusterIssuer (cluster-scoped CRD). Uses ApplyYAML which
	// drives the dynamic client + REST mapper; returns an error when the
	// ClusterIssuer CRD is not yet registered.
	clusterIssuerYAML := fmt.Sprintf(`apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: %s
  labels:
    app.kubernetes.io/managed-by: yage
spec:
  ca:
    secretName: %s
`, issuingCAClusterIssuer, issuingCASecretName)

	if err := workloadCli.ApplyYAML(applyCtx, []byte(clusterIssuerYAML)); err != nil {
		return fmt.Errorf("apply ClusterIssuer %s: %w", issuingCAClusterIssuer, err)
	}

	return nil
}
