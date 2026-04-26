package provider

import (
	"errors"
	"fmt"
)

// ErrAirgapped is returned by For() when cfg.Airgapped is true and
// the requested provider needs internet access. Distinct from
// ErrNotApplicable so callers can tell "the provider doesn't do
// this" apart from "we won't let you use this provider in airgapped
// mode."
var ErrAirgapped = errors.New("provider not available in airgapped mode")

// onPremProviders is the set of registered providers that can
// satisfy the Provider interface without ever reaching out to the
// public internet. Their control plane lives on hardware the
// operator already owns (Proxmox, vSphere) or in a federated /
// private cloud the operator already runs (OpenStack), or it's the
// CAPI Docker reference provider used for tests (CAPD).
//
// Hyperscale + managed-cloud providers (AWS, Azure, GCP, Hetzner,
// DigitalOcean, Linode, OCI, IBM Cloud) are excluded — they all
// need outbound HTTPS to vendor APIs for identity, provisioning,
// and pricing.
var onPremProviders = map[string]struct{}{
	"proxmox":   {},
	"openstack": {},
	"vsphere":   {},
	"capd":      {},
}

// AirgapCompatible reports whether the named provider can run
// without internet access. The orchestrator filters the registry
// through this when cfg.Airgapped is true:
//
//   - provider.For() returns ErrAirgapped for non-compatible
//     providers
//   - cost-compare iterates compatible providers only
//   - the dry-run plan omits cloud-provider sections
//
// Used by:
//   - internal/provider/provider.go For()
//   - internal/cost/compare.go (filter the iteration list)
//   - internal/orchestrator/plan.go (skip cloud sections)
func AirgapCompatible(name string) bool {
	_, ok := onPremProviders[name]
	return ok
}

// AirgapAwareForName returns the provider registered as `name` only
// if it's airgap-compatible OR airgapped is false. Otherwise
// returns ErrAirgapped (wrapping the name for log clarity). Useful
// inside iterations (cost-compare, plan-output) where the caller
// wants a single source of truth instead of duplicating the
// AirgapCompatible check at every site.
func AirgapAwareForName(name string, airgapped bool) (Provider, error) {
	if airgapped && !AirgapCompatible(name) {
		return nil, fmt.Errorf("%w: %s", ErrAirgapped, name)
	}
	return Get(name)
}

// AirgapFilter returns the input provider names with non-airgap-
// compatible ones removed when airgapped is true. When airgapped is
// false the input is returned unchanged. Order is preserved.
func AirgapFilter(names []string, airgapped bool) []string {
	if !airgapped {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if AirgapCompatible(n) {
			out = append(out, n)
		}
	}
	return out
}
