// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package airgap

import "strings"

// RewriteHelmRepo rewrites a Helm chart-repo URL onto the operator's
// internal mirror when one is configured (--helm-repo-mirror /
// YAGE_HELM_REPO_MIRROR). When no mirror is set, the URL is returned
// unchanged — making this safe to wrap around every chart-repo
// reference at config-load and chart-render time.
//
// The contract:
//
//   - http(s)://<host>/<path>      → <mirror>/<path>
//   - oci://<host>/<path>          → <mirror>/<path>          (mirror keeps its scheme)
//   - "" or non-URL strings        → returned as-is
//   - <mirror> already absent      → URL returned unchanged
//
// The rewrite is host-stripping, not host-preserving: the operator's
// mirror is assumed to publish every chart under one prefix tree.
// That matches Harbor / ChartMuseum semantics and keeps the rewrite
// idempotent across re-invocations.
func RewriteHelmRepo(u string) string {
	mirror := HelmMirror()
	if mirror == "" || u == "" {
		return u
	}
	trimmed := strings.TrimRight(u, "/")
	// If the URL is already pointing at the mirror, leave it alone.
	if strings.HasPrefix(trimmed, mirror) {
		return u
	}

	// Strip scheme + host. Accept http, https, and oci.
	for _, scheme := range []string{"https://", "http://", "oci://"} {
		if strings.HasPrefix(trimmed, scheme) {
			rest := strings.TrimPrefix(trimmed, scheme)
			// rest = host[/path…]; drop the host segment.
			slash := strings.IndexByte(rest, '/')
			if slash < 0 {
				// Just a host with no path — point at the mirror root.
				return mirror
			}
			path := rest[slash:] // includes the leading slash
			return mirror + path
		}
	}
	// Not a recognized scheme — leave the value alone rather than
	// guess. yage's chart-repo defaults are all HTTPS or OCI today.
	return u
}
