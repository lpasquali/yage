// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package linode is the yage Provider implementation for
// the Cluster API Linode/Akamai infrastructure provider (CAPL —
// linode/cluster-api-provider-linode).
//
// What's wired:
//
//   - K3sTemplate: LinodeCluster + LinodeMachineTemplate with ${LINODE_*} placeholders.
//   - KindSyncFields / AbsorbConfigYAML / TemplateVars: state.go.
//   - EstimateMonthlyCostUSD: live Linode catalog fetcher (cost.go).
//
// LINODE_TOKEN is consumed directly by the CAPL controller pod
// from its env/Secret and is NOT substituted into the manifest — same
// pattern as HCLOUD_TOKEN on Hetzner.
package linode

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
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

// k3sTemplate is the Linode/Akamai-flavored K3s manifest.
// CRD API group: infrastructure.cluster.x-k8s.io/v1alpha2 (CAPL).
// Placeholders: LINODE_REGION, LINODE_CP_TYPE, LINODE_WORKER_TYPE,
// WORKLOAD_CLUSTER_NAME (universal) — no token placeholder; the CAPL
// controller reads LINODE_TOKEN from its own Secret/env.
const k3sTemplate = `apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  clusterNetwork:
    pods:
      cidrBlocks: ["192.168.0.0/16"]
    services:
      cidrBlocks: ["10.96.0.0/12"]
  controlPlaneRef:
    apiVersion: controlplane.cluster.x-k8s.io/v1beta2
    kind: KThreesControlPlane
    name: ${WORKLOAD_CLUSTER_NAME}-control-plane
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1alpha2
    kind: LinodeCluster
    name: ${WORKLOAD_CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha2
kind: LinodeCluster
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  region: ${LINODE_REGION}
---
apiVersion: controlplane.cluster.x-k8s.io/v1beta2
kind: KThreesControlPlane
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  replicas: ${CONTROL_PLANE_MACHINE_COUNT}
  version: ${KUBERNETES_VERSION}+k3s1
  machineTemplate:
    infrastructureRef:
      apiVersion: infrastructure.cluster.x-k8s.io/v1alpha2
      kind: LinodeMachineTemplate
      name: ${WORKLOAD_CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
      kubeletArgs:
        - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha2
kind: LinodeMachineTemplate
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      type: ${LINODE_CP_TYPE}
      region: ${LINODE_REGION}
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  clusterName: ${WORKLOAD_CLUSTER_NAME}
  replicas: ${WORKER_MACHINE_COUNT}
  selector:
    matchLabels: {}
  template:
    spec:
      clusterName: ${WORKLOAD_CLUSTER_NAME}
      version: ${KUBERNETES_VERSION}+k3s1
      bootstrap:
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta2
          kind: KThreesConfigTemplate
          name: ${WORKLOAD_CLUSTER_NAME}-md-0
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1alpha2
        kind: LinodeMachineTemplate
        name: ${WORKLOAD_CLUSTER_NAME}-md-0
---
apiVersion: bootstrap.cluster.x-k8s.io/v1beta2
kind: KThreesConfigTemplate
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      agentConfig:
        airGapped: false
        kubeletArgs:
          - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha2
kind: LinodeMachineTemplate
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      type: ${LINODE_WORKER_TYPE}
      region: ${LINODE_REGION}
`

// K3sTemplate returns the Linode K3s manifest template.
func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for Linode today. Sizing is driven
// entirely by LINODE_CP_TYPE / LINODE_WORKER_TYPE in TemplateVars.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}