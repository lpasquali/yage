// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package oci is the yage Provider implementation for the
// Cluster API Oracle Cloud Infrastructure provider (CAPOCI —
// oracle/cluster-api-provider-oci).
//
// What's wired:
//
//   - K3sTemplate: OCICluster + OCIMachineTemplate with ${OCI_*} placeholders.
//   - KindSyncFields / AbsorbConfigYAML / TemplateVars: state.go.
//   - EstimateMonthlyCostUSD: live OCI public price-list fetcher (cost.go).
//
// OCI authentication uses a private key file. The CAPOCI controller
// reads the key via a Kubernetes Secret it is bootstrapped with;
// yage only stores the on-disk path reference (never the key itself).
package oci

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
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

// k3sTemplate is the OCI-flavored K3s manifest.
// CRD API group: infrastructure.cluster.x-k8s.io/v1beta2 (CAPOCI).
// Placeholders: OCI_REGION, OCI_CP_SHAPE, OCI_WORKER_SHAPE,
// OCI_TENANCY_OCID, OCI_USER_OCID, OCI_COMPARTMENT_OCID, OCI_IMAGE_ID,
// WORKLOAD_CLUSTER_NAME (universal).
// OCI private key is supplied out-of-band via a Secret bootstrapped
// into the CAPOCI controller namespace — not substituted here.
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
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
    kind: OCICluster
    name: ${WORKLOAD_CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: OCICluster
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  compartmentId: ${OCI_COMPARTMENT_OCID}
  region: ${OCI_REGION}
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
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
      kind: OCIMachineTemplate
      name: ${WORKLOAD_CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
      kubeletArgs:
        - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: OCIMachineTemplate
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      compartmentId: ${OCI_COMPARTMENT_OCID}
      imageId: ${OCI_IMAGE_ID}
      shape: ${OCI_CP_SHAPE}
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
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
        kind: OCIMachineTemplate
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
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: OCIMachineTemplate
metadata:
  name: ${WORKLOAD_CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      compartmentId: ${OCI_COMPARTMENT_OCID}
      imageId: ${OCI_IMAGE_ID}
      shape: ${OCI_WORKER_SHAPE}
`

// K3sTemplate returns the OCI K3s manifest template.
func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for OCI today. Sizing is driven entirely
// by OCI_CP_SHAPE / OCI_WORKER_SHAPE in TemplateVars.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}