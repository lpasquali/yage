// Package equinix is the bootstrap-capi Provider implementation for
// the Cluster API Equinix Metal provider (CAPP / CAPEM —
// equinix-labs/cluster-api-provider-packet). The infrastructure
// provider name in clusterctl is still "packet" (legacy name from
// before Equinix acquired Packet).
//
// Status: cost-only stub. Catalog + provisioning use METAL_AUTH_TOKEN.
package equinix

import (
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
)

func init() {
	provider.Register("equinix", func() provider.Provider { return &Provider{} })
}

type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "equinix" }
func (p *Provider) InfraProviderName() string { return "packet" }

func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "packet"}
}
