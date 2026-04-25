// Package oci is the bootstrap-capi Provider implementation for the
// Cluster API Oracle Cloud Infrastructure provider (CAPOCI —
// oracle/cluster-api-provider-oci).
//
// Status: cost-only stub. OCI publishes an auth-free public price
// list JSON; provisioning needs an OCI API key + tenancy/user OCID.
package oci

import (
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
)

func init() {
	provider.Register("oci", func() provider.Provider { return &Provider{} })
}

type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "oci" }
func (p *Provider) InfraProviderName() string { return "oci" }

func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "oci"}
}
