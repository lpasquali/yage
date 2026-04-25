// Package linode is the bootstrap-capi Provider implementation for
// the Cluster API Linode/Akamai infrastructure provider (CAPL —
// linode/cluster-api-provider-linode).
//
// Status: cost-only stub (live pricing fetcher + clusterctl init).
// Catalog calls are auth-free; provisioning needs LINODE_TOKEN.
package linode

import (
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
)

func init() {
	provider.Register("linode", func() provider.Provider { return &Provider{} })
}

type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "linode" }
func (p *Provider) InfraProviderName() string { return "linode" }

func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "linode"}
}
