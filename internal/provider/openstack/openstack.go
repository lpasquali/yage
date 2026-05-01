// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package openstack is the yage Provider implementation
// for the Cluster API OpenStack infrastructure provider (CAPO —
// https://github.com/kubernetes-sigs/cluster-api-provider-openstack).
//
// Scope: minimum-viable, in the same shape as internal/provider/capd
// (inline K3s template, ErrNotApplicable for the phases that don't
// have a clean OpenStack analogue yet). The plugin interface lets us
// fill the "future" branches in follow-up PRs without disturbing the
// rest of the orchestrator.
//
// Activation: `--infrastructure-provider openstack` (or
// `INFRA_PROVIDER=openstack`). The orchestrator looks the
// implementation up via provider.For(cfg) and dispatches per-phase.
//
// Identity model: CAPO is driven by a clouds.yaml + the standard
// `OS_*` environment (OS_AUTH_URL / OS_USERNAME / OS_PASSWORD /
// OS_PROJECT_NAME / OS_DOMAIN_NAME) — typically an application
// credential. The user provides those directly to clusterctl /
// CAPO; yage has nothing to mint, so EnsureIdentity is
// ErrNotApplicable. (Future: we could template a clouds.yaml from
// cfg, but that's out of scope here.)
//
// Inventory / grouping: OpenStack does have `nova quota-show` and
// projects (tenants). Per §13.4 #1 the per-project quota model
// fits flat Total/Used/Available cleanly (Proxmox-shaped).
// Inventory is implemented via gophercloud Nova limits + Cinder
// quota APIs (see inventory.go). PatchManifest resolves the best-fit
// Nova flavor for each role from cfg sizing fields.
// Projects are pre-existing resources rather than
// bootstrap-creatable ones, so EnsureGroup returns
// ErrNotApplicable; the orchestrator skips silently.
//
// CSI: cinder-csi-plugin is the canonical OpenStack CSI; lands as
// a Driver under internal/csi/openstackcinder/ in a follow-up. The
// provider seam intentionally carries no CSI hook — CSI flows
// through internal/csi instead.
package openstack

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

func init() {
	provider.Register("openstack", func() provider.Provider { return &Provider{} })
}

// Provider implements provider.Provider for CAPO.
type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "openstack" }
func (p *Provider) InfraProviderName() string { return "openstack" }

// EnsureIdentity builds and applies the clouds.yaml Secret that CAPO needs
// before it can provision OpenStack machines. The Secret is named
// "<WorkloadClusterName>-cloud-config" and lives in WorkloadClusterNamespace
// on the management (kind) cluster. Implementation is in identity.go.

// Inventory — implemented in inventory.go via gophercloud Nova limits +
// Cinder quota APIs. See inventory.go for details.

// EnsureGroup — OpenStack uses projects (tenants) for grouping, but
// those are pre-existing resources rather than bootstrap-creatable;
// CAPO targets one via OS_PROJECT_NAME. Nothing to ensure here.
func (p *Provider) EnsureGroup(cfg *config.Config, name string) error {
	return provider.ErrNotApplicable
}

// ClusterctlInitArgs returns "--infrastructure openstack". Bootstrap
// (kubeadm vs k3s) is added by the orchestrator from cfg.BootstrapMode.
func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "openstack"}
}

// k3sTemplate is a CAPO-flavored K3s manifest. Same shape as the
// CAPD / Proxmox ones; OpenStackCluster + OpenStackMachineTemplate
// replace the provider-specific MachineTemplate kinds. The CAPO
// CRDs live under infrastructure.cluster.x-k8s.io/v1beta1 (the
// stable storage version at the time of writing).
//
// Sizing comes from the OpenStack flavor name
// (${OPENSTACK_NODE_MACHINE_FLAVOR} /
// ${OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR}); a future PatchManifest
// could resolve cfg.Providers.Proxmox.WorkerNumCores / Providers.Proxmox.WorkerMemoryMiB to the closest
// matching flavor via gophercloud.
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
    kind: OpenStackCluster
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: OpenStackCluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  identityRef:
    name: ${CLUSTER_NAME}-cloud-config
    cloudName: ${OPENSTACK_CLOUD}
  apiServerLoadBalancer:
    enabled: true
  controlPlaneEndpoint:
    host: ${CONTROL_PLANE_ENDPOINT_IP}
    port: 6443
  dnsNameservers:
    - ${OPENSTACK_DNS_NAMESERVERS}
  managedSecurityGroups: true
  nodeCidr: 10.6.0.0/24
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
      kind: OpenStackMachineTemplate
      name: ${CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
      kubeletArgs:
        - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: OpenStackMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      flavor: ${OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR}
      image:
        filter:
          name: ${OPENSTACK_IMAGE_NAME}
      sshKeyName: ${OPENSTACK_SSH_KEY_NAME}
      identityRef:
        name: ${CLUSTER_NAME}-cloud-config
        cloudName: ${OPENSTACK_CLOUD}
      failureDomain: ${OPENSTACK_FAILURE_DOMAIN}
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
      failureDomain: ${OPENSTACK_FAILURE_DOMAIN}
      bootstrap:
        configRef:
          apiVersion: orchestrator.cluster.x-k8s.io/v1beta2
          kind: KThreesConfigTemplate
          name: ${CLUSTER_NAME}-md-0
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: OpenStackMachineTemplate
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
        kubeletArgs:
          - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: OpenStackMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      flavor: ${OPENSTACK_NODE_MACHINE_FLAVOR}
      image:
        filter:
          name: ${OPENSTACK_IMAGE_NAME}
      sshKeyName: ${OPENSTACK_SSH_KEY_NAME}
      identityRef:
        name: ${CLUSTER_NAME}-cloud-config
        cloudName: ${OPENSTACK_CLOUD}
`

// K3sTemplate returns the inline OpenStack-flavored K3s manifest.
// mgmt is currently unused — the management variant matches the
// workload one for CAPO; future divergence (e.g. dedicated
// management network) lands here.
func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest — implemented in inventory.go via gophercloud Nova flavor
// resolution. See inventory.go for details.

// EstimateMonthlyCostUSD — provider doesn't track variable usage
// pricing in the same shape as AWS on-demand instances. Self-hosted
// (Proxmox), private (vSphere), or pricing-too-variable (OpenStack)
// providers return ErrNotApplicable; the orchestrator displays the
// estimate only when it's available.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	return provider.CostEstimate{}, provider.ErrNotApplicable
}