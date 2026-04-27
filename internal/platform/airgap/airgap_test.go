// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package airgap

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lpasquali/yage/internal/config"
)

func saveHTTPState() (orig http.RoundTripper, sslCert string) {
	return http.DefaultTransport, os.Getenv("SSL_CERT_FILE")
}

func restoreHTTPState(t *testing.T, orig http.RoundTripper, sslCert string) {
	t.Helper()
	http.DefaultTransport = orig
	_ = Apply("", "", "")
	if sslCert == "" {
		_ = os.Unsetenv("SSL_CERT_FILE")
	} else {
		_ = os.Setenv("SSL_CERT_FILE", sslCert)
	}
}

func writeTestPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "airgap-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestApply_emptyCAClearsPool(t *testing.T) {
	orig, ssl := saveHTTPState()
	t.Cleanup(func() { restoreHTTPState(t, orig, ssl) })

	if err := Apply("", "https://mirror.example", ""); err != nil {
		t.Fatal(err)
	}
	if err := Apply("", "", ""); err != nil {
		t.Fatal(err)
	}
	if CAPool() != nil {
		t.Fatal("expected nil CAPool after empty CA apply")
	}
	if CABundlePath() != "" {
		t.Fatalf("CABundlePath = %q want empty", CABundlePath())
	}
}

func TestApply_bogusCAPath(t *testing.T) {
	orig, ssl := saveHTTPState()
	t.Cleanup(func() { restoreHTTPState(t, orig, ssl) })

	err := Apply("/nonexistent/airgap-test-ca.pem", "", "")
	if err == nil {
		t.Fatal("expected error for missing bundle file")
	}
}

func TestApply_invalidPEM(t *testing.T) {
	orig, ssl := saveHTTPState()
	t.Cleanup(func() { restoreHTTPState(t, orig, ssl) })

	dir := t.TempDir()
	bad := filepath.Join(dir, "not-a-cert.pem")
	if err := os.WriteFile(bad, []byte("not pem certificates\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Apply(bad, "", "")
	if err == nil {
		t.Fatal("expected error for PEM with no certs")
	}
}

func TestApply_validCA(t *testing.T) {
	orig, ssl := saveHTTPState()
	t.Cleanup(func() { restoreHTTPState(t, orig, ssl) })

	p := writeTestPEM(t)
	if err := Apply(p, "", ""); err != nil {
		t.Fatal(err)
	}
	if CAPool() == nil {
		t.Fatal("expected non-nil CAPool")
	}
	if CABundlePath() != p {
		t.Fatalf("CABundlePath = %q want %q", CABundlePath(), p)
	}
	if os.Getenv("SSL_CERT_FILE") != p {
		t.Fatalf("SSL_CERT_FILE = %q want %q", os.Getenv("SSL_CERT_FILE"), p)
	}
}

func TestApply_clearCARemovesSSLCertFileWeSet(t *testing.T) {
	orig, ssl := saveHTTPState()
	t.Cleanup(func() { restoreHTTPState(t, orig, ssl) })

	p := writeTestPEM(t)
	if err := Apply(p, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := Apply("", "", ""); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SSL_CERT_FILE") != "" {
		t.Fatalf("expected SSL_CERT_FILE unset after clearing CA, got %q", os.Getenv("SSL_CERT_FILE"))
	}
}

func TestApply_secondHelmMirrorWins(t *testing.T) {
	t.Cleanup(func() { _ = Apply("", "", "") })
	_ = Apply("", "https://first.example", "")
	if HelmMirror() != "https://first.example" {
		t.Fatalf("first mirror: got %q", HelmMirror())
	}
	_ = Apply("", "https://second.example", "")
	if HelmMirror() != "https://second.example" {
		t.Fatalf("second mirror: got %q", HelmMirror())
	}
}

func TestApply_nodeImageStored(t *testing.T) {
	t.Cleanup(func() { _ = Apply("", "", "") })
	_ = Apply("", "", "kindest/node:v9.9.9")
	if NodeImage() != "kindest/node:v9.9.9" {
		t.Fatalf("NodeImage = %q", NodeImage())
	}
}

func TestHTTPClient_withoutPool(t *testing.T) {
	t.Cleanup(func() { _ = Apply("", "", "") })
	_ = Apply("", "", "")
	c := HTTPClient(time.Second)
	if c.Transport != nil {
		t.Fatal("expected nil Transport when no CA pool")
	}
}

func TestHTTPClient_withPool(t *testing.T) {
	orig, ssl := saveHTTPState()
	t.Cleanup(func() { restoreHTTPState(t, orig, ssl) })

	p := writeTestPEM(t)
	if err := Apply(p, "", ""); err != nil {
		t.Fatal(err)
	}
	c := HTTPClient(time.Second)
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatal("expected *http.Transport with CA pool set")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Fatal("expected RootCAs on TLS config")
	}
}

func TestRewriteConfigChartURLs(t *testing.T) {
	t.Cleanup(func() { _ = Apply("", "", "") })
	if err := Apply("", "https://mirror.example", ""); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.KyvernoChartRepoURL = "https://charts.example/kyverno"
	cfg.Providers.Proxmox.CSIChartRepoURL = "https://charts.example/csi"
	RewriteConfigChartURLs(cfg)
	if want := "https://mirror.example/kyverno"; cfg.KyvernoChartRepoURL != want {
		t.Fatalf("KyvernoChartRepoURL = %q want %q", cfg.KyvernoChartRepoURL, want)
	}
	if want := "https://mirror.example/csi"; cfg.Providers.Proxmox.CSIChartRepoURL != want {
		t.Fatalf("CSIChartRepoURL = %q want %q", cfg.Providers.Proxmox.CSIChartRepoURL, want)
	}
}

func TestRewriteConfigChartURLs_noMirrorNoOp(t *testing.T) {
	t.Cleanup(func() { _ = Apply("", "", "") })
	_ = Apply("", "", "")
	cfg := &config.Config{}
	cfg.KyvernoChartRepoURL = "https://charts.example/kyverno"
	RewriteConfigChartURLs(cfg)
	if cfg.KyvernoChartRepoURL != "https://charts.example/kyverno" {
		t.Fatalf("expected unchanged URL, got %q", cfg.KyvernoChartRepoURL)
	}
}

func TestRewriteConfigChartURLs_nilCfg(t *testing.T) {
	t.Cleanup(func() { _ = Apply("", "", "") })
	if err := Apply("", "https://mirror.example", ""); err != nil {
		t.Fatal(err)
	}
	RewriteConfigChartURLs(nil) // must not panic
}
