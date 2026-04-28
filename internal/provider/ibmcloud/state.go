// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package ibmcloud

// state.go — KindSyncFields, AbsorbConfigYAML, TemplateVars and the
// K3sTemplate manifest for CAPIBM (VPC Gen2 mode).

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists the IBM Cloud VPC configuration the next
// yage run needs. IBMCLOUD_API_KEY is not stored here — operators
// supply it via the cost-compare-config secret or environment.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("region", cfg.Providers.IBMCloud.Region)
	add("control_plane_profile", cfg.Providers.IBMCloud.ControlPlaneProfile)
	add("node_profile", cfg.Providers.IBMCloud.NodeProfile)
	add("resource_group", cfg.Providers.IBMCloud.ResourceGroup)
	add("vpc_name", cfg.Providers.IBMCloud.VPCName)
	add("zone", cfg.Providers.IBMCloud.Zone)
	return out
}

// AbsorbConfigYAML is the reverse of KindSyncFields.
func (p *Provider) AbsorbConfigYAML(cfg *config.Config, kv map[string]string) bool {
	assigned := false
	assign := func(cur *string, v string) {
		if *cur == "" && v != "" {
			*cur = v
			assigned = true
		}
	}
	for k, v := range kv {
		switch k {
		case "region":
			assign(&cfg.Providers.IBMCloud.Region, v)
		case "control_plane_profile":
			assign(&cfg.Providers.IBMCloud.ControlPlaneProfile, v)
		case "node_profile":
			assign(&cfg.Providers.IBMCloud.NodeProfile, v)
		case "resource_group":
			assign(&cfg.Providers.IBMCloud.ResourceGroup, v)
		case "vpc_name":
			assign(&cfg.Providers.IBMCloud.VPCName, v)
		case "zone":
			assign(&cfg.Providers.IBMCloud.Zone, v)
		}
	}
	return assigned
}

// TemplateVars returns the CAPIBM VPC manifest substitution map.
// IBMCLOUD_API_KEY must be set in the operator's environment.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	region := orDefault(cfg.Providers.IBMCloud.Region, "us-south")
	zone := orDefault(cfg.Providers.IBMCloud.Zone, region+"-1")
	return map[string]string{
		"IBM_VPC_REGION":          region,
		"IBM_VPC_ZONE":            zone,
		"IBM_RESOURCE_GROUP":      orDefault(cfg.Providers.IBMCloud.ResourceGroup, "default"),
		"IBM_VPC_NAME":            cfg.Providers.IBMCloud.VPCName,
		"IBM_VPC_IMAGE_ID":        cfg.Providers.IBMCloud.ImageID,
		"IBM_CP_INSTANCE_PROFILE": orDefault(cfg.Providers.IBMCloud.ControlPlaneProfile, "bx2-4x16"),
		"IBM_WK_INSTANCE_PROFILE": orDefault(cfg.Providers.IBMCloud.NodeProfile, "bx2-2x8"),
	}
}

// k3sTemplate is the CAPIBM VPC Gen2 K3s cluster manifest.
// Requires the cluster-api-provider-ibmcloud infrastructure provider.
const k3sTemplate = `apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: ${CLUSTER_NAME}
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
    name: ${CLUSTER_NAME}-control-plane
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
    kind: IBMVPCCluster
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: IBMVPCCluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  region: ${IBM_VPC_REGION}
  resourceGroup: ${IBM_RESOURCE_GROUP}
  vpc: ${IBM_VPC_NAME}
  zone: ${IBM_VPC_ZONE}
---
apiVersion: controlplane.cluster.x-k8s.io/v1beta2
kind: KThreesControlPlane
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  replicas: ${CONTROL_PLANE_MACHINE_COUNT}
  version: ${KUBERNETES_VERSION}+k3s1
  machineTemplate:
    infrastructureRef:
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
      kind: IBMVPCMachineTemplate
      name: ${CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      kubeletArgs:
        - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: IBMVPCMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      region: ${IBM_VPC_REGION}
      zone: ${IBM_VPC_ZONE}
      profile: ${IBM_CP_INSTANCE_PROFILE}
      image:
        id: ${IBM_VPC_IMAGE_ID}
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  clusterName: ${CLUSTER_NAME}
  replicas: ${WORKER_MACHINE_COUNT}
  selector: { matchLabels: {} }
  template:
    spec:
      clusterName: ${CLUSTER_NAME}
      version: ${KUBERNETES_VERSION}+k3s1
      bootstrap:
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta2
          kind: KThreesConfigTemplate
          name: ${CLUSTER_NAME}-md-0
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
        kind: IBMVPCMachineTemplate
        name: ${CLUSTER_NAME}-md-0
---
apiVersion: bootstrap.cluster.x-k8s.io/v1beta2
kind: KThreesConfigTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      agentConfig:
        kubeletArgs:
          - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: IBMVPCMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      region: ${IBM_VPC_REGION}
      zone: ${IBM_VPC_ZONE}
      profile: ${IBM_WK_INSTANCE_PROFILE}
      image:
        id: ${IBM_VPC_IMAGE_ID}
`

// K3sTemplate returns the CAPIBM VPC K3s cluster manifest.
func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for IBM Cloud.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}

