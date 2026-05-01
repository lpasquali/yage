// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

// EnsureIssuingCA provisions an intermediate/issuing CA for the workload cluster
// (ADR 0009 §2, Phase H gap 2). It generates an intermediate CA certificate
// signed by the operator-supplied root CA (cfg.IssuingCARootCert +
// cfg.IssuingCARootKey), stores the material as a Secret in yage-system on the
// management cluster, and applies a cert-manager ClusterIssuer to the workload
// cluster.
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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
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
	issuingCASecretName      = "yage-issuing-ca"
	issuingCANamespace       = "yage-system"
	issuingCACertManagerNS   = "cert-manager"
	issuingCAClusterIssuer   = "yage-ca-issuer"
	issuingCAValidityDays    = 365
	issuingCAApplyTimeout    = 30 * time.Second
)

// EnsureIssuingCA generates an intermediate CA cert signed by the operator's
// root CA (cfg.IssuingCARootCert + cfg.IssuingCARootKey), stores the
// intermediate cert+key as a Secret in yage-system on the management cluster,
// and applies the cert-manager ClusterIssuer manifest to the workload cluster.
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
	var mgmtCli *k8sclient.Client
	var err error
	if cfg.MgmtKubeconfigPath != "" {
		mgmtCli, err = k8sclient.ForKubeconfigFile(cfg.MgmtKubeconfigPath)
		if err != nil {
			return fmt.Errorf("EnsureIssuingCA: load mgmt kubeconfig %s: %w", cfg.MgmtKubeconfigPath, err)
		}
	} else {
		kindCtx := "kind-" + cfg.KindClusterName
		mgmtCli, err = k8sclient.ForContext(kindCtx)
		if err != nil {
			return fmt.Errorf("EnsureIssuingCA: connect to %s: %w", kindCtx, err)
		}
	}

	// Step 1: Generate intermediate CA material.
	intermediateCertPEM, intermediateKeyPEM, err := generateIntermediateCA(cfg)
	if err != nil {
		return fmt.Errorf("EnsureIssuingCA: generate intermediate CA: %w", err)
	}
	logx.Log("EnsureIssuingCA: intermediate CA generated (CN=yage-issuing-ca-%s)", cfg.WorkloadClusterName)

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

// generateIntermediateCA creates an ECDSA P-256 intermediate CA key, signs a
// cert against the operator-supplied root CA, and returns (certPEM, keyPEM).
func generateIntermediateCA(cfg *config.Config) (certPEM, keyPEM []byte, err error) {
	// Parse root CA cert.
	rootCertBlock, _ := pem.Decode([]byte(cfg.IssuingCARootCert))
	if rootCertBlock == nil {
		return nil, nil, fmt.Errorf("IssuingCARootCert: not a valid PEM block")
	}
	rootCert, err := x509.ParseCertificate(rootCertBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("IssuingCARootCert: parse certificate: %w", err)
	}

	// Parse root CA key. Support both RSA and ECDSA.
	rootKeyBlock, _ := pem.Decode([]byte(cfg.IssuingCARootKey))
	if rootKeyBlock == nil {
		return nil, nil, fmt.Errorf("IssuingCARootKey: not a valid PEM block")
	}
	rootKey, err := parsePrivateKey(rootKeyBlock)
	if err != nil {
		return nil, nil, fmt.Errorf("IssuingCARootKey: %w", err)
	}

	// Generate intermediate private key (ECDSA P-256).
	intermediateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate intermediate key: %w", err)
	}

	// Derive a cluster-name-qualified CN.
	cn := "yage-issuing-ca"
	if cfg.WorkloadClusterName != "" {
		cn = "yage-issuing-ca-" + cfg.WorkloadClusterName
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    now.Add(-5 * time.Minute), // small skew buffer
		NotAfter:     now.Add(issuingCAValidityDays * 24 * time.Hour),
		IsCA:         true,
		// MaxPathLen: 0 requires MaxPathLenZero: true for Go to honour the
		// constraint ("leaf issuer — cannot sign further CAs").
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	// Sign with the root CA key.
	certDER, err := x509.CreateCertificate(rand.Reader, template, rootCert, &intermediateKey.PublicKey, rootKey)
	if err != nil {
		return nil, nil, fmt.Errorf("sign intermediate certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(intermediateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal intermediate key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}

// parsePrivateKey accepts either PKCS#8 or traditional RSA/EC PEM blocks.
func parsePrivateKey(block *pem.Block) (any, error) {
	switch block.Type {
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
		}
		return k, nil
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse RSA private key: %w", err)
		}
		return k, nil
	case "EC PRIVATE KEY":
		k, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse EC private key: %w", err)
		}
		return k, nil
	default:
		return nil, fmt.Errorf("unsupported PEM block type %q (want PRIVATE KEY, RSA PRIVATE KEY, or EC PRIVATE KEY)", block.Type)
	}
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
