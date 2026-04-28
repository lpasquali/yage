// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package digitalocean is the yage Provider implementation
// for the Cluster API DigitalOcean infrastructure provider (CAPDO —
// kubernetes-sigs/cluster-api-provider-digitalocean).
//
// What's wired:
//
//   - K3sTemplate: DOCluster + DOMachineTemplate with ${DO_*} placeholders.
//   - KindSyncFields / AbsorbConfigYAML / TemplateVars: state.go.
//   - EstimateMonthlyCostUSD: in-binary live /v2/sizes fetcher (cost.go).
//
// DIGITALOCEAN_TOKEN is consumed directly by the CAPDO controller pod
// from its env/secret and is NOT substituted into the manifest — same
// pattern as HCLOUD_TOKEN on Hetzner.
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

// k3sTemplate is the DigitalOcean-flavored K3s manifest.
// CRD API group: infrastructure.cluster.x-k8s.io/v1beta1 (CAPDO v0.x).
// Placeholders: DO_REGION, DO_CP_SIZE, DO_WORKER_SIZE, DO_VPC_UUID,
// WORKLOAD_CLUSTER_NAME (universal) — no token placeholder; the
// CAPDO controller reads DIGITALOCEAN_TOKEN from its own Secret/env.
//
// DO_VPC_UUID may be empty when the user hasn't set DIGITALOCEAN_VPC_UUID;
// CAPDO falls back to the region default VPC in that case. The field is
// included in the template for clusters where it IS set.
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
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: DOCluster
    name: ${WORKLOAD_CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: DOCluster
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  region: ${DO_REGION}
  network:
    vpc:
      uuid: ${DO_VPC_UUID}
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
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
      kind: DOMachineTemplate
      name: ${WORKLOAD_CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
      kubeletArgs:
        - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: DOMachineTemplate
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      size: ${DO_CP_SIZE}
      region: ${DO_REGION}
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
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: DOMachineTemplate
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
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: DOMachineTemplate
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      size: ${DO_WORKER_SIZE}
      region: ${DO_REGION}
`

// K3sTemplate returns the DigitalOcean K3s manifest template.
func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for DigitalOcean today. Sizing is driven
// entirely by DO_CP_SIZE / DO_WORKER_SIZE in TemplateVars.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}