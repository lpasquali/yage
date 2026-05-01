// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/lpasquali/yage/internal/config"
)

// selfSignedRootCA generates a minimal self-signed root CA for testing.
// Returns (certPEM, keyPEM).
func selfSignedRootCA(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate root key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "test-root-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		MaxPathLen:            1,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("sign root cert: %v", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal root key: %v", err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

// TestGenerateIntermediateCA verifies that the intermediate cert is correctly
// signed by the root CA (x509.Certificate.CheckSignatureFrom) and that the
// MaxPathLen constraint is set to 0 (leaf-issuer-only).
func TestGenerateIntermediateCA(t *testing.T) {
	rootCertPEM, rootKeyPEM := selfSignedRootCA(t)

	cfg := &config.Config{
		IssuingCARootCert:   rootCertPEM,
		IssuingCARootKey:    rootKeyPEM,
		WorkloadClusterName: "test-cluster",
	}

	certPEM, keyPEM, err := generateIntermediateCA(cfg)
	if err != nil {
		t.Fatalf("generateIntermediateCA: %v", err)
	}
	if len(certPEM) == 0 {
		t.Fatal("certPEM is empty")
	}
	if len(keyPEM) == 0 {
		t.Fatal("keyPEM is empty")
	}

	// Parse the generated intermediate cert.
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("cannot decode intermediate certPEM")
	}
	intermediateCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse intermediate cert: %v", err)
	}

	// Parse the root CA cert.
	rootBlock, _ := pem.Decode([]byte(rootCertPEM))
	if rootBlock == nil {
		t.Fatal("cannot decode rootCertPEM")
	}
	rootCert, err := x509.ParseCertificate(rootBlock.Bytes)
	if err != nil {
		t.Fatalf("parse root cert: %v", err)
	}

	// The intermediate cert must be signed by the root CA.
	if err := intermediateCert.CheckSignatureFrom(rootCert); err != nil {
		t.Errorf("intermediate cert not signed by root CA: %v", err)
	}

	// Subject CN must include the cluster name.
	wantCN := "yage-issuing-ca-test-cluster"
	if intermediateCert.Subject.CommonName != wantCN {
		t.Errorf("CN = %q, want %q", intermediateCert.Subject.CommonName, wantCN)
	}

	// IsCA must be true.
	if !intermediateCert.IsCA {
		t.Error("intermediate cert IsCA is false")
	}

	// MaxPathLen constraint must be 0 (leaf-issuer only; cannot sign further CAs).
	if intermediateCert.MaxPathLen != 0 {
		t.Errorf("MaxPathLen = %d, want 0", intermediateCert.MaxPathLen)
	}
	if !intermediateCert.MaxPathLenZero {
		t.Error("MaxPathLenZero is false — Go will not enforce MaxPathLen:0 without it")
	}

	// Key usages must include CertSign and CRLSign.
	const wantUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	if intermediateCert.KeyUsage&wantUsage != wantUsage {
		t.Errorf("KeyUsage = %v, missing CertSign|CRLSign", intermediateCert.KeyUsage)
	}

	// Validity must be approximately 1 year.
	validity := intermediateCert.NotAfter.Sub(intermediateCert.NotBefore)
	wantMin := time.Duration(issuingCAValidityDays-1) * 24 * time.Hour
	wantMax := time.Duration(issuingCAValidityDays+1) * 24 * time.Hour
	if validity < wantMin || validity > wantMax {
		t.Errorf("certificate validity = %v, want ~%d days", validity, issuingCAValidityDays)
	}

	// The intermediate private key must be parseable.
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("cannot decode keyPEM")
	}
	if _, err := x509.ParseECPrivateKey(keyBlock.Bytes); err != nil {
		t.Errorf("parse intermediate key: %v", err)
	}
}

// TestEnsureIssuingCA_NotApplicable verifies that EnsureIssuingCA returns
// ErrNotApplicable when the root CA material is absent.
func TestEnsureIssuingCA_NotApplicable(t *testing.T) {
	tests := []struct {
		name string
		cert string
		key  string
	}{
		{"both empty", "", ""},
		{"cert empty", "", "some-key"},
		{"key empty", "some-cert", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				IssuingCARootCert: tc.cert,
				IssuingCARootKey:  tc.key,
			}
			err := EnsureIssuingCA(nil, "", cfg)
			if err != ErrNotApplicable {
				t.Errorf("want ErrNotApplicable, got %v", err)
			}
		})
	}
}

// TestGenerateIntermediateCA_InvalidPEM verifies graceful error handling for
// malformed PEM inputs.
func TestGenerateIntermediateCA_InvalidPEM(t *testing.T) {
	validCertPEM, validKeyPEM := selfSignedRootCA(t)

	t.Run("bad cert PEM", func(t *testing.T) {
		cfg := &config.Config{
			IssuingCARootCert: "not-a-pem",
			IssuingCARootKey:  validKeyPEM,
		}
		_, _, err := generateIntermediateCA(cfg)
		if err == nil {
			t.Error("expected error for invalid cert PEM, got nil")
		}
	})

	t.Run("bad key PEM", func(t *testing.T) {
		cfg := &config.Config{
			IssuingCARootCert: validCertPEM,
			IssuingCARootKey:  "not-a-pem",
		}
		_, _, err := generateIntermediateCA(cfg)
		if err == nil {
			t.Error("expected error for invalid key PEM, got nil")
		}
	})
}
