// Package hetzner is the yage Provider implementation for
// the Cluster API Hetzner Cloud infrastructure provider (CAPHV —
// github.com/syself/cluster-api-provider-hetzner).
//
// Status: stub-shape with a working K3s template + a real cost
// estimator. Hetzner has no managed-Kubernetes service we model
// today (CAPHV is unmanaged-only — the user's Hetzner Robot or a
// managed-K8s partner is out of scope), so unlike AWS there's no
// Providers.AWS.Mode-equivalent switch.
//
// What's wired:
//
//   - K3sTemplate: HetznerCluster + HCloudMachineTemplate, with
//     ${HCLOUD_*} placeholders for region, machine types, image,
//     network, SSH keys.
//   - EstimateMonthlyCostUSD: in-binary price table mirroring
//     Hetzner's hourly+monthly-capped pricing model. Hetzner bills
//     hourly but caps at a monthly maximum; for an always-on K8s
//     cluster the monthly cap is what the user actually pays.
//
// What's not wired (returns ErrNotApplicable):
//
//   - EnsureIdentity: Hetzner Cloud authenticates via a single
//     HCLOUD_TOKEN that the user supplies via env var. No identity-
//     bootstrap step the way AWS IAM or Proxmox BPG users have.
//   - Inventory: Hetzner's quota model is count-based (default 10
//     servers per project) rather than aggregate vCPU/memory, so it
//     doesn't compose with the flat Total/Used/Available shape and
//     returns ErrNotApplicable per §13.4 #1; capacity preflight is
//     skipped and EstimateMonthlyCostUSD + DescribeWorkload cover
//     the pre-deploy gates.
//   - EnsureGroup: Hetzner has Projects (billing buckets) but those
//     pre-exist when the user creates the API token. No CAPI-side
//     group concept beyond that.
//   - EnsureCSISecret: hcloud-cloud-controller-manager and
//     hcloud-csi-driver ship via Helm with the same HCLOUD_TOKEN as
//     the CAPI provider; no separate Secret apply path here.
//
// Pricing currency note: Hetzner publishes prices in EUR (their
// home market). The cost.go price table converts to USD at a fixed
// 1.08 USD/EUR rate that we document in the Note string of the
// estimate. Real billing happens in EUR; the USD figure is a
// planning approximation that drifts with FX.
package hetzner

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

func init() {
	provider.Register("hetzner", func() provider.Provider { return &Provider{} })
}

// Provider implements provider.Provider for CAPHV.
type Provider struct{}

func (p *Provider) Name() string              { return "hetzner" }
func (p *Provider) InfraProviderName() string { return "hetzner" }

// EnsureIdentity is a no-op for Hetzner: the HCLOUD_TOKEN env var the
// user supplies is the entire identity layer. No CloudFormation /
// IAM-stack equivalent.
func (p *Provider) EnsureIdentity(cfg *config.Config) error { return provider.ErrNotApplicable }

// Inventory is unimplemented for Hetzner: the quota model is
// count-based (servers per project) rather than aggregate
// vCPU/memory, so it doesn't compose with flat
// Total/Used/Available. Returning ErrNotApplicable matches §13.4
// #1; the orchestrator skips capacity preflight and relies on
// EstimateMonthlyCostUSD + DescribeWorkload.
func (p *Provider) Inventory(cfg *config.Config) (*provider.Inventory, error) {
	return nil, provider.ErrNotApplicable
}

// EnsureGroup is a no-op for Hetzner: Projects pre-exist when the
// user creates the HCLOUD_TOKEN, and CAPHV doesn't have a per-
// cluster grouping concept beyond resource labels (which CAPHV
// manages itself).
func (p *Provider) EnsureGroup(cfg *config.Config, name string) error {
	return provider.ErrNotApplicable
}

// ClusterctlInitArgs returns "--infrastructure hetzner". Bootstrap
// (kubeadm vs k3s) is added by the orchestrator from cfg.BootstrapMode.
func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "hetzner"}
}

// k3sTemplate is the Hetzner-flavored K3s manifest. Same overall
// shape as the AWS / Docker / Proxmox templates; the differences
// are HetznerCluster / HCloudMachineTemplate kinds (note the
// CAPHV CRD names — HetznerCluster but HCloudMachineTemplate, an
// asymmetry baked into the upstream CRDs) and the Hetzner-specific
// placeholders (region, image, network, SSH keys, machine types).
//
// Cloud provider is set to external on the kubelet so the
// hcloud-cloud-controller-manager (deployed separately via Helm)
// handles node initialization. servicelb + traefik are disabled in
// favor of installing Hetzner Cloud Load Balancers via CCM-managed
// Service objects + a real ingress controller.
//
// HCloudMachineTemplate is the v1beta1 API group (CAPHV is on
// v1beta1 today, not v1beta2 like CAPA/CAPV). Adjust if upstream
// graduates.
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
    kind: HetznerCluster
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: HetznerCluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  controlPlaneRegions:
    - ${HCLOUD_REGION}
  hcloudNetwork:
    enabled: true
  sshKeys:
    hcloud: ${VM_SSH_KEYS}
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
      kind: HCloudMachineTemplate
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
kind: HCloudMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      type: ${HCLOUD_CONTROL_PLANE_MACHINE_TYPE}
      imageName: ${HCLOUD_IMAGE}
      placementGroupName: ${CLUSTER_NAME}-control-plane
      sshKeys:
        - name: ${VM_SSH_KEYS}
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
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: HCloudMachineTemplate
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
kind: HCloudMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      type: ${HCLOUD_NODE_MACHINE_TYPE}
      imageName: ${HCLOUD_IMAGE}
      placementGroupName: ${CLUSTER_NAME}-md-0
      sshKeys:
        - name: ${VM_SSH_KEYS}
`

func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for Hetzner today. Future: map
// cfg.Providers.Proxmox.ControlPlaneNumCores / Memory to a Hetzner instance type
// (cx22 / cx32 / ccx33 …) and patch
// HCloudMachineTemplate.spec.template.spec.type.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}

// EnsureCSISecret is unimplemented for Hetzner: hcloud-csi-driver +
// hcloud-cloud-controller-manager ship via Helm with the same
// HCLOUD_TOKEN secret CAPHV uses. No yage-managed Secret
// apply path.
func (p *Provider) EnsureCSISecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return provider.ErrNotApplicable
}
