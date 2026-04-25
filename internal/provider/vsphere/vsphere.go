// Package vsphere is the bootstrap-capi Provider implementation for
// the Cluster API vSphere infrastructure provider (CAPV —
// github.com/kubernetes-sigs/cluster-api-provider-vsphere).
//
// Shape: closer to Proxmox than to CAPD, but with identity handled
// out-of-band. The vSphere admin pre-creates a service account with
// the CAPV-required role bindings; bootstrap-capi consumes the
// resulting credentials via the standard CAPV env vars
// (VSPHERE_USERNAME / VSPHERE_PASSWORD / VSPHERE_SERVER) which the
// orchestrator passes through to clusterctl. Capacity is reported by
// govmomi inventory queries in a future iteration; this stub returns
// ErrNotApplicable so the orchestrator falls back to "trust the user".
//
// The K3s template targets VSphereCluster + VSphereMachineTemplate
// (apiVersion infrastructure.cluster.x-k8s.io/v1beta1 — CAPV is one
// version behind the v1beta2 the rest of CAPI is on). The
// MachineTemplate spec carries inline sizing fields (numCPUs,
// numCoresPerSocket, memoryMiB, diskGiB) that map cleanly onto
// bootstrap-capi's CONTROL_PLANE_* / WORKER_* config knobs — the
// template wires the placeholders today and a future PatchManifest
// can rewrite them post-render if richer per-role overrides are
// needed.
package vsphere

import (
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/provider"
)

func init() {
	provider.Register("vsphere", func() provider.Provider { return &Provider{} })
}

// Provider implements provider.Provider for CAPV.
type Provider struct{}

func (p *Provider) Name() string              { return "vsphere" }
func (p *Provider) InfraProviderName() string { return "vsphere" }

// EnsureIdentity is a no-op for vSphere: the vSphere admin creates
// the CAPV service account + role bindings out-of-band, and the
// resulting credentials are supplied via VSPHERE_USERNAME /
// VSPHERE_PASSWORD / VSPHERE_SERVER env vars at run time.
func (p *Provider) EnsureIdentity(cfg *config.Config) error {
	return provider.ErrNotApplicable
}

// Capacity is unimplemented for vSphere. A future iteration will
// query govmomi inventory (cluster compute resources + datastore
// summary) and aggregate; until then the orchestrator skips the
// capacity preflight.
func (p *Provider) Capacity(cfg *config.Config) (*provider.HostCapacity, error) {
	return nil, provider.ErrNotApplicable
}

// EnsureGroup is a no-op for vSphere. Folders are the closest
// equivalent to a Proxmox pool / AWS IAM group, but bootstrap-capi
// doesn't manage them today; the operator pre-creates the target
// folder and supplies its path via VSPHERE_FOLDER.
func (p *Provider) EnsureGroup(cfg *config.Config, name string) error {
	return provider.ErrNotApplicable
}

// ClusterctlInitArgs returns "--infrastructure vsphere".
func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "vsphere"}
}

// k3sTemplate is the vSphere-flavored K3s manifest. Mirrors the
// Proxmox K3s flow: KThreesControlPlane references a
// VSphereMachineTemplate; MachineDeployment references a separate
// VSphereMachineTemplate plus a KThreesConfigTemplate. CAPV's CRDs
// are still on infrastructure.cluster.x-k8s.io/v1beta1 (the rest of
// the manifest stays on the v1beta2 cluster + bootstrap +
// controlplane groups).
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
    kind: VSphereCluster
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereCluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  server: ${VSPHERE_SERVER}
  thumbprint: ${VSPHERE_TLS_THUMBPRINT}
  controlPlaneEndpoint:
    host: ${CONTROL_PLANE_ENDPOINT_IP}
    port: ${CONTROL_PLANE_ENDPOINT_PORT}
  identityRef:
    kind: Secret
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      cloneMode: linkedClone
      datacenter: ${VSPHERE_DATACENTER}
      datastore: ${VSPHERE_DATASTORE}
      diskGiB: 25
      folder: ${VSPHERE_FOLDER}
      memoryMiB: ${CONTROL_PLANE_MEMORY_MIB}
      network:
        devices:
        - dhcp4: true
          networkName: ${VSPHERE_NETWORK}
      numCPUs: ${CONTROL_PLANE_NUM_CORES}
      numCoresPerSocket: 1
      os: Linux
      resourcePool: ${VSPHERE_RESOURCE_POOL}
      server: ${VSPHERE_SERVER}
      storagePolicyName: ""
      template: ${VSPHERE_TEMPLATE}
      thumbprint: ${VSPHERE_TLS_THUMBPRINT}
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
      kind: VSphereMachineTemplate
      name: ${CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
      kubeletArgs:
      - "--cloud-provider=external"
    files:
    - path: /var/lib/rancher/k3s/server/manifests/coredns-config.yaml
      owner: root:root
      permissions: "0644"
      content: ""
    preK3sCommands: []
    users:
    - name: capv
      sshAuthorizedKeys:
      - ${VM_SSH_KEYS}
      sudo: ALL=(ALL) NOPASSWD:ALL
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
        kind: VSphereMachineTemplate
        name: ${CLUSTER_NAME}-md-0
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: VSphereMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      cloneMode: linkedClone
      datacenter: ${VSPHERE_DATACENTER}
      datastore: ${VSPHERE_DATASTORE}
      diskGiB: 25
      folder: ${VSPHERE_FOLDER}
      memoryMiB: ${WORKER_MEMORY_MIB}
      network:
        devices:
        - dhcp4: true
          networkName: ${VSPHERE_NETWORK}
      numCPUs: ${WORKER_NUM_CORES}
      numCoresPerSocket: 1
      os: Linux
      resourcePool: ${VSPHERE_RESOURCE_POOL}
      server: ${VSPHERE_SERVER}
      storagePolicyName: ""
      template: ${VSPHERE_TEMPLATE}
      thumbprint: ${VSPHERE_TLS_THUMBPRINT}
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
      users:
      - name: capv
        sshAuthorizedKeys:
        - ${VM_SSH_KEYS}
        sudo: ALL=(ALL) NOPASSWD:ALL
`

func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for vSphere today: the K3s template above
// already wires CONTROL_PLANE_NUM_CORES / CONTROL_PLANE_MEMORY_MIB /
// WORKER_NUM_CORES / WORKER_MEMORY_MIB into the right VSphereMachine-
// Template fields, so the renderer covers per-role sizing on its own.
//
// TODO: VSphereMachineTemplate has its own sizing field set
// (`spec.template.spec.numCPUs`, `numCoresPerSocket`, `memoryMiB`,
// `diskGiB`) that a future patch could honor based on the existing
// CONTROL_PLANE_* / WORKER_* config fields.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}

// EnsureCSISecret is unimplemented. CAPV's CSI story is
// vsphere-cpi + vsphere-csi-driver, both shipped as Helm charts; the
// install path will live alongside CAAPH in a future iteration. Until
// then bootstrap-capi leaves CSI to the operator.
func (p *Provider) EnsureCSISecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return provider.ErrNotApplicable
}

// EstimateMonthlyCostUSD — vSphere is self-hosted; vendor licensing
// is enterprise-quote-only, so there's no public pricing API. The
// operator opts into a TCO estimate by passing --hardware-cost-usd
// (and optionally --hardware-watts / --hardware-kwh-rate-usd /
// --hardware-support-usd-month for vSphere licensing/support).
// Without those, returns ErrNotApplicable.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	return provider.TCOEstimate(cfg, "vsphere")
}
