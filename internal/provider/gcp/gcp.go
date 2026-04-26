// Package gcp is the yage Provider implementation for the
// Cluster API GCP infrastructure provider (CAPG —
// github.com/kubernetes-sigs/cluster-api-provider-gcp).
//
// Status: stub-shape, mirroring the AWS provider. The K3s template is
// wired and the package registers cleanly so `provider.For(cfg)`
// returns it when the user sets --infrastructure-provider gcp (or
// INFRA_PROVIDER=gcp). The identity / capacity / group / CSI phases
// all return provider.ErrNotApplicable today; GCP has analogues for
// each but they don't fit the same shape as the Proxmox helpers and
// each would be its own follow-up:
//
//   - Identity: CAPG expects a GCP service-account-key JSON, supplied
//     via GOOGLE_APPLICATION_CREDENTIALS or the GCP_B64ENCODED_CREDENTIALS
//     env var. The IAM role + key are created by the operator out-of-
//     band (gcloud iam service-accounts create + roles/owner-ish
//     bindings) — yage doesn't manage GCP projects or IAM.
//   - Inventory: GCP Compute quotas are per-family (n2 vs e2 etc.)
//     per-region and don't compose with flat Total/Used/Available,
//     so GCP returns ErrNotApplicable per §13.4 #1; capacity
//     preflight is skipped and EstimateMonthlyCostUSD +
//     DescribeWorkload cover the pre-deploy gates.
//   - Group: GCP has IAM groups via Cloud Identity, but CAPG doesn't
//     use them — cluster-level grouping is by GCP project + label.
//     Returning ErrNotApplicable is the honest answer.
//   - CSI: gcp-pd-csi-driver / filestore-csi-driver ship via Helm or
//     in-cluster manifests; both authenticate via Workload Identity,
//     not a credentials Secret. Different shape from the Proxmox CSI
//     Secret apply path; out of scope for the stub.
//
// PatchManifest is a no-op: GCPMachineTemplate sizes via
// spec.template.spec.instanceType (e.g. "n2-standard-2"), so a future
// patch could map cfg.Providers.Proxmox.ControlPlaneNumCores / Memory to the right
// machine family. Not implemented yet.
package gcp

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

func init() {
	provider.Register("gcp", func() provider.Provider { return &Provider{} })
}

// Provider implements provider.Provider for CAPG.
type Provider struct{}

func (p *Provider) Name() string              { return "gcp" }
func (p *Provider) InfraProviderName() string { return "gcp" }

// EnsureIdentity is a no-op for GCP: the service-account-key JSON the
// user supplies via env vars (GOOGLE_APPLICATION_CREDENTIALS or
// GCP_B64ENCODED_CREDENTIALS) is the identity layer, and the key
// itself is created out-of-band via `gcloud iam service-accounts
// keys create` against an account with the CAPG-required IAM roles.
func (p *Provider) EnsureIdentity(cfg *config.Config) error { return provider.ErrNotApplicable }

// Inventory is unimplemented for GCP: per-family Compute quotas
// don't compose with flat Total/Used/Available, and GCP preflight
// is already covered by EstimateMonthlyCostUSD + DescribeWorkload
// (per §13.4 #1). The orchestrator skips capacity preflight when
// this returns ErrNotApplicable.
func (p *Provider) Inventory(cfg *config.Config) (*provider.Inventory, error) {
	return nil, provider.ErrNotApplicable
}

// EnsureGroup is a no-op for GCP: cluster-level grouping is by
// project + label, both managed outside yage.
func (p *Provider) EnsureGroup(cfg *config.Config, name string) error {
	return provider.ErrNotApplicable
}

// ClusterctlInitArgs returns "--infrastructure gcp". Bootstrap
// (kubeadm vs k3s) is added by the orchestrator from cfg.BootstrapMode.
func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "gcp"}
}

// k3sTemplate is the GCP-flavored K3s manifest. Same overall shape as
// the AWS / vSphere templates; differences are the GCPCluster /
// GCPMachineTemplate kinds (CAPG is on infrastructure.cluster.x-
// k8s.io/v1beta1) and GCP-specific placeholders (project, region,
// network, machine types, image family). Cloud provider is set to
// external on the kubelet so the GCP cloud controller manager
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
    kind: GCPCluster
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: GCPCluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  project: ${GCP_PROJECT}
  region: ${GCP_REGION}
  network:
    name: ${GCP_NETWORK_NAME}
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
      kind: GCPMachineTemplate
      name: ${CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
      kubeletArgs:
        - "--cloud-provider=external"
    users:
    - name: capg
      sshAuthorizedKeys:
      - ${VM_SSH_KEYS}
      sudo: ALL=(ALL) NOPASSWD:ALL
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: GCPMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      instanceType: ${GCP_CONTROL_PLANE_MACHINE_TYPE}
      image: ${GCP_IMAGE_FAMILY}
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
        kind: GCPMachineTemplate
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
      users:
      - name: capg
        sshAuthorizedKeys:
        - ${VM_SSH_KEYS}
        sudo: ALL=(ALL) NOPASSWD:ALL
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: GCPMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      instanceType: ${GCP_NODE_MACHINE_TYPE}
      image: ${GCP_IMAGE_FAMILY}
`

func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for GCP today. Future: map
// cfg.Providers.Proxmox.ControlPlaneNumCores / Memory to a GCP machine family
// (e2-standard-2, n2-standard-2, …) and patch
// GCPMachineTemplate.spec.template.spec.instanceType.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}

// EnsureCSISecret is unimplemented for GCP: the gcp-pd-csi-driver
// authenticates via Workload Identity (or a Workload-Identity-bound
// service account on the node), not a credentials Secret.
func (p *Provider) EnsureCSISecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return provider.ErrNotApplicable
}
