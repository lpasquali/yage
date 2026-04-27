// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"bytes"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lpasquali/yage/internal/platform/airgap"
)

// TestNilTransportHTTPClientUsesDefaultTransportAfterAirgapApply documents
// that fetchers using &http.Client{Timeout: …} with a nil Transport inherit
// http.DefaultTransport — the same transport airgap.Apply patches for the
// internal CA bundle (§17 / §21.4).
func TestNilTransportHTTPClientUsesDefaultTransportAfterAirgapApply(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cert := ts.Certificate()
	if cert == nil {
		t.Fatal("httptest server certificate is nil")
	}
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caPath := filepath.Join(dir, "test-ca.pem")
	if err := os.WriteFile(caPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := http.DefaultTransport
	oldSSL := os.Getenv("SSL_CERT_FILE")
	t.Cleanup(func() {
		http.DefaultTransport = orig
		_ = airgap.Apply("", "", "")
		if oldSSL == "" {
			_ = os.Unsetenv("SSL_CERT_FILE")
		} else {
			_ = os.Setenv("SSL_CERT_FILE", oldSSL)
		}
	})

	if err := airgap.Apply(caPath, "", ""); err != nil {
		t.Fatal(err)
	}

	// Same shape as internal/pricing fetchers: nil Transport → DefaultTransport.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("TLS GET after airgap.Apply: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d want 200", resp.StatusCode)
	}
}
