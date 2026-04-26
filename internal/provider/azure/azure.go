// Package azure is the yage Provider implementation for the
// Cluster API Azure infrastructure provider (CAPZ —
// github.com/kubernetes-sigs/cluster-api-provider-azure).
//
// Status: stub-shape, mirrors the AWS provider. The K3s template is
// wired and the package registers cleanly so `provider.For(cfg)`
// returns it when the user sets --infrastructure-provider azure (or
// INFRA_PROVIDER=azure). The identity / capacity / group / CSI
// phases all return provider.ErrNotApplicable today; CAPZ has
// analogues for each but they're each their own follow-up:
//
//   - Identity: CAPZ expects either a Service Principal or a
//     User-Assigned Managed Identity that the user provides via
//     environment variables (AZURE_SUBSCRIPTION_ID,
//     AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET) or via
//     `az login` on the bootstrap host. yage doesn't
//     create the SP / Managed Identity — that's an out-of-band
//     `az ad sp create-for-rbac` step the user runs once per
//     subscription.
//   - Capacity: would query the Azure Resource Manager Compute
//     Quota API for the subscription's vCPU / family limits per
//     region. Future work; the orchestrator falls back to "skip
//     preflight, trust the user" when Capacity returns
//     ErrNotApplicable.
//   - Group: Azure has Resource Groups, but CAPZ creates and
//     manages those itself given AZURE_RESOURCE_GROUP. Returning
//     ErrNotApplicable is the honest answer (we shouldn't race
//     CAPZ for ownership).
//   - CSI: azuredisk-csi-driver / azurefile-csi-driver ship via
//     Helm + a Workload Identity (or SP) binding. Different shape
//     from the Proxmox CSI Secret apply path; out of scope for the
//     stub.
//
// PatchManifest is a no-op: AzureMachineTemplate sizes via
// spec.template.spec.vmSize (e.g. "Standard_D2s_v3"), so a future
// patch could map cfg.ControlPlaneNumCores / Memory to the right VM
// family. Not implemented yet.
package azure

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

func init() {
	provider.Register("azure", func() provider.Provider { return &Provider{} })
}

// Provider implements provider.Provider for CAPZ.
type Provider struct{}

func (p *Provider) Name() string              { return "azure" }
func (p *Provider) InfraProviderName() string { return "azure" }

// EnsureIdentity is a no-op for Azure: the Service Principal /
// Managed Identity the user supplies via env vars (or `az login` on
// the bootstrap host) is the identity layer, and the SP itself is
// bootstrapped out-of-band with `az ad sp create-for-rbac`.
func (p *Provider) EnsureIdentity(cfg *config.Config) error { return provider.ErrNotApplicable }

// Capacity is unimplemented for Azure today. Future: query the
// Resource Manager Compute Quota API for the subscription's vCPU /
// VM-family limits per region.
func (p *Provider) Capacity(cfg *config.Config) (*provider.HostCapacity, error) {
	return nil, provider.ErrNotApplicable
}

// EnsureGroup is a no-op for Azure: Resource Groups are the closest
// equivalent, but CAPZ creates and manages them itself given
// AZURE_RESOURCE_GROUP. We don't want to race CAPZ for ownership.
func (p *Provider) EnsureGroup(cfg *config.Config, name string) error {
	return provider.ErrNotApplicable
}

// ClusterctlInitArgs returns "--infrastructure azure". Bootstrap
// (kubeadm vs k3s) is added by the orchestrator from
// cfg.BootstrapMode.
func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "azure"}
}

// k3sTemplate is the Azure-flavored K3s manifest. Same overall shape
// as the AWS / Proxmox templates; the differences are the
// AzureCluster / AzureMachineTemplate kinds (still on
// infrastructure.cluster.x-k8s.io/v1beta1 — CAPZ is one minor
// version behind the v1beta2 the rest of CAPI is on) and the
// Azure-specific placeholders (subscription, location, resource
// group, vnet, subnet, VM sizes, SSH key). Cloud provider is set to
// external on the kubelet so the Azure cloud controller manager
// (deployed separately) handles node initialization.
const k3sTemplate = `apiVersion: cluster.x-k8s.io/v1beta2
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
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: AzureCluster
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: AzureCluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  location: ${AZURE_LOCATION}
  subscriptionID: ${AZURE_SUBSCRIPTION_ID}
  resourceGroup: ${AZURE_RESOURCE_GROUP}
  networkSpec:
    vnet:
      name: ${AZURE_VNET_NAME}
    subnets:
    - name: ${AZURE_SUBNET_NAME}
      role: node
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: AzureMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      vmSize: ${AZURE_CONTROL_PLANE_MACHINE_TYPE}
      osDisk:
        diskSizeGB: 128
        managedDisk:
          storageAccountType: Premium_LRS
        osType: Linux
      sshPublicKey: ${AZURE_SSH_PUBLIC_KEY_B64}
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
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
      kind: AzureMachineTemplate
      name: ${CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
      kubeletArgs:
        - "--cloud-provider=external"
---
apiVersion: cluster.x-k8s.io/v1beta2
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
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: AzureMachineTemplate
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
        airGapped: false
        kubeletArgs:
          - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: AzureMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      vmSize: ${AZURE_NODE_MACHINE_TYPE}
      osDisk:
        diskSizeGB: 128
        managedDisk:
          storageAccountType: Premium_LRS
        osType: Linux
      sshPublicKey: ${AZURE_SSH_PUBLIC_KEY_B64}
`

func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for Azure today. Future: map
// cfg.ControlPlaneNumCores / Memory to a VM family
// (Standard_B2s, Standard_D2s_v3, …) and patch
// AzureMachineTemplate.spec.template.spec.vmSize.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}

// EnsureCSISecret is unimplemented for Azure: azuredisk-csi-driver
// ships via Helm + a Workload Identity (or Service Principal)
// binding rather than a credentials Secret in the yage
// shape.
func (p *Provider) EnsureCSISecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return provider.ErrNotApplicable
}
