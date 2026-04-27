// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package airgap centralizes the §17 / §21.4 airgapped-deploy knobs
// (`--internal-ca-bundle`, `--helm-repo-mirror`, `--node-image`) so
// every outbound HTTP/HTTPS call yage makes — directly via Go's
// net/http or indirectly via child processes (helm, clusterctl,
// kind, kubectl) — honors the operator's internal CA bundle and
// chart mirror.
//
// Apply is called once from cmd/yage/main.go after config.Load +
// cli.Parse. It:
//
//   - reads the CA bundle path, installs the PEM on
//     http.DefaultTransport (so Go code using http.DefaultClient or
//     &http.Client{Timeout: …} with a nil Transport trusts it), and
//     sets SSL_CERT_FILE so child processes (helm, clusterctl, kubectl)
//     inherit the same anchors;
//
//   - when the CA path is later cleared with Apply("", …), clears
//     SSL_CERT_FILE only if it still matches the path yage had set
//     (avoids clobbering an unrelated pre-existing env value);
//
//   - stashes cfg.HelmRepoMirror and cfg.NodeImage in package-level
//     globals for the helm-repo rewriter; main.go also calls
//     shell.SetKindNodeImage for kind argv injection;
//
// Read-only consumption: helmrepo.Rewrite and HTTPClient read the
// mirror / pool globals set by Apply. Calling Apply more than once
// replaces the globals — relevant mostly for tests.
package airgap

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Apply wires the three airgap knobs onto the process: CA bundle
// (Go HTTP + child-process env), Helm-repo rewrite global, and kind
// node-image override global. Returns a non-nil error only when the
// CA bundle is set but unreadable / unparseable — every other failure
// is logged and tolerated (best-effort plumbing).
func Apply(caBundlePath, helmRepoMirror, nodeImage string) error {
	helmMirrorMu.Lock()
	helmMirror = strings.TrimRight(strings.TrimSpace(helmRepoMirror), "/")
	helmMirrorMu.Unlock()

	nodeImageMu.Lock()
	nodeImageOverride = strings.TrimSpace(nodeImage)
	nodeImageMu.Unlock()

	if caBundlePath == "" {
		// No CA bundle: clear the cached pool but leave http.DefaultTransport
		// untouched — Go's default cert-pool already loads SSL_CERT_FILE +
		// /etc/ssl/certs.
		caPoolMu.Lock()
		prevPath := caBundlePathSet
		caPool = nil
		caBundlePathSet = ""
		caPoolMu.Unlock()
		// If SSL_CERT_FILE points at our bundle, drop it so a later
		// run without --internal-ca-bundle does not inherit a stale
		// path.
		if prevPath != "" && os.Getenv("SSL_CERT_FILE") == prevPath {
			_ = os.Unsetenv("SSL_CERT_FILE")
		}
		return nil
	}

	pem, err := os.ReadFile(caBundlePath)
	if err != nil {
		return fmt.Errorf("airgap: read --internal-ca-bundle %q: %w", caBundlePath, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return fmt.Errorf("airgap: --internal-ca-bundle %q contains no valid PEM certificates", caBundlePath)
	}

	caPoolMu.Lock()
	caPool = pool
	caBundlePathSet = caBundlePath
	caPoolMu.Unlock()

	// Install on http.DefaultTransport so any code that reaches for
	// http.DefaultClient / http.Get / http.NewRequest picks the bundle
	// up. This is the safety net for the per-pricing-fetcher clients
	// that never go through a custom transport.
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := dt.Clone()
		if clone.TLSClientConfig == nil {
			clone.TLSClientConfig = &tls.Config{}
		}
		clone.TLSClientConfig.RootCAs = pool
		http.DefaultTransport = clone
	}

	// SSL_CERT_FILE is honored by helm, kubectl, clusterctl, and any
	// crypto/x509-based Go binary kind shells out to. Every shell.Run
	// child process inherits os.Environ(), so this propagates.
	_ = os.Setenv("SSL_CERT_FILE", caBundlePath)
	return nil
}

// CAPool returns the parsed CA pool from the most recent Apply call,
// or nil when no bundle is configured.
func CAPool() *x509.CertPool {
	caPoolMu.RLock()
	defer caPoolMu.RUnlock()
	return caPool
}

// CABundlePath returns the path of the CA bundle from the most recent
// Apply call, or "" when no bundle is configured.
func CABundlePath() string {
	caPoolMu.RLock()
	defer caPoolMu.RUnlock()
	return caBundlePathSet
}

// HTTPClient returns an *http.Client whose TLS config trusts the
// configured CA bundle (when set) on top of the system roots, with
// the supplied request timeout. Used by pricing fetchers, inventory
// callers, and any other yage HTTP code that doesn't already have a
// custom client.
func HTTPClient(timeout time.Duration) *http.Client {
	pool := CAPool()
	if pool == nil {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}
}

// HelmMirror returns the configured Helm repo mirror, or "" when
// unset. Trailing slashes are stripped at Apply time.
func HelmMirror() string {
	helmMirrorMu.RLock()
	defer helmMirrorMu.RUnlock()
	return helmMirror
}

// NodeImage returns the configured kind node-image override, or ""
// when unset.
func NodeImage() string {
	nodeImageMu.RLock()
	defer nodeImageMu.RUnlock()
	return nodeImageOverride
}

// --- package globals ---

var (
	caPoolMu        sync.RWMutex
	caPool          *x509.CertPool
	caBundlePathSet string

	helmMirrorMu sync.RWMutex
	helmMirror   string

	nodeImageMu       sync.RWMutex
	nodeImageOverride string
)
