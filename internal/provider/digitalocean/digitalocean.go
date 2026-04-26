// Package digitalocean is the yage Provider implementation
// for the Cluster API DigitalOcean infrastructure provider (CAPDO —
// kubernetes-sigs/cluster-api-provider-digitalocean).
//
// Status: cost-only stub. clusterctl init args + a working live
// pricing fetcher are wired; K3sTemplate / PatchManifest / Ensure*
// return ErrNotApplicable until the per-CRD k3s flavor is built.
package digitalocean

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

func init() {
	provider.Register("digitalocean", func() provider.Provider { return &Provider{} })
}

type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "digitalocean" }
func (p *Provider) InfraProviderName() string { return "digitalocean" }

func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "digitalocean"}
}
