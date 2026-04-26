// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package ibmcloud is the yage Provider implementation for
// the Cluster API IBM Cloud provider (CAPIBM —
// kubernetes-sigs/cluster-api-provider-ibmcloud).
//
// Status: cost-only stub. IBM Cloud Global Catalog needs an IAM
// API key; provisioning needs the same key + a tenancy.
package ibmcloud

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

func init() {
	provider.Register("ibmcloud", func() provider.Provider { return &Provider{} })
}

type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "ibmcloud" }
func (p *Provider) InfraProviderName() string { return "ibmcloud" }

func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "ibmcloud"}
}