// Package capd is a reference implementation of provider.Provider
// for the CAPI Docker provider (https://cluster-api.sigs.k8s.io/user/quick-start.html#docker).
//
// CAPD provisions workload clusters as docker containers — handy
// for CI / smoke tests that don't have a real hypervisor handy. The
// implementation is deliberately minimal: it shows the shape every
// new provider takes, and it's the smallest working second
// implementation that proves the plugin pattern.
//
// To activate CAPD, set --infrastructure-provider docker (or
// INFRA_PROVIDER=docker). The orchestrator looks the implementation
// up via provider.For(cfg) and dispatches per-phase.
//
// Identity / inventory are no-ops (Docker has no identity layer and
// no single capacity endpoint we care about — `docker info` is
// fine for "unlimited"). EnsureGroup is a no-op (no Docker concept
// for VM grouping). The K3s template references DockerMachineTemplate
// instead of ProxmoxMachineTemplate, with the simpler set of fields
// CAPD expects.
package capd

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

func init() {
	provider.Register("docker", func() provider.Provider { return &Provider{} })
}

// Provider implements provider.Provider for CAPD.
type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "docker" }
func (p *Provider) InfraProviderName() string { return "docker" }

func (p *Provider) EnsureIdentity(cfg *config.Config) error { return provider.ErrNotApplicable }
func (p *Provider) Inventory(cfg *config.Config) (*provider.Inventory, error) {
	return nil, provider.ErrNotApplicable
}
func (p *Provider) EnsureGroup(cfg *config.Config, name string) error { return provider.ErrNotApplicable }
func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "docker"}
}

// k3sTemplate is a CAPD-flavored K3s manifest. Same shape as the
// Proxmox one; only the MachineTemplate kind + spec field set differs.
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
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
    kind: DockerCluster
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: DockerCluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec: {}
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
      kind: DockerMachineTemplate
      name: ${CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: DockerMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec: {}
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
          apiVersion: orchestrator.cluster.x-k8s.io/v1beta2
          kind: KThreesConfigTemplate
          name: ${CLUSTER_NAME}-md-0
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
        kind: DockerMachineTemplate
        name: ${CLUSTER_NAME}-md-0
---
apiVersion: orchestrator.cluster.x-k8s.io/v1beta2
kind: KThreesConfigTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      agentConfig:
        airGapped: false
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: DockerMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec: {}
`

func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for CAPD: DockerMachineTemplate has no
// per-role sizing fields (containers inherit host resources). Real
// providers (proxmox, vsphere, aws) implement this richly.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}

// EnsureCSISecret — CAPD has no CSI shipped with yage.
func (p *Provider) EnsureCSISecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return provider.ErrNotApplicable
}

// EstimateMonthlyCostUSD — provider doesn't track variable usage
// pricing in the same shape as AWS on-demand instances. Self-hosted
// (Proxmox), private (vSphere), or pricing-too-variable (OpenStack)
// providers return ErrNotApplicable; the orchestrator displays the
// estimate only when it's available.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	return provider.CostEstimate{}, provider.ErrNotApplicable
}
