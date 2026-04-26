// Package aws is the yage Provider implementation for the
// Cluster API AWS infrastructure provider (CAPA —
// github.com/kubernetes-sigs/cluster-api-provider-aws).
//
// Status: stub-shape. The K3s template is wired and the package
// registers cleanly so `provider.For(cfg)` returns it when the user
// sets --infrastructure-provider aws (or INFRA_PROVIDER=aws). The
// identity / capacity / group / CSI phases all return
// provider.ErrNotApplicable today; AWS has analogues for each but
// they're not in the same shape as the Proxmox helpers and would
// each be a follow-up of their own:
//
//   - Identity: CAPA expects an IAM role + access key pair, supplied
//     to clusterctl via AWS_REGION + AWS_ACCESS_KEY_ID +
//     AWS_SECRET_ACCESS_KEY (or an EC2 instance profile when running
//     on an AWS-hosted bootstrap host). The IAM role itself is
//     created by `clusterawsadm bootstrap iam create-cloudformation-stack`,
//     which the user runs once per AWS account. yage
//     doesn't manage that stack — out of scope.
//   - Inventory: per-family AWS Service Quotas (t3 vs m5 etc.)
//     don't compose with flat Total/Used/Available, so AWS returns
//     ErrNotApplicable per §13.4 #1; capacity preflight is skipped
//     and EstimateMonthlyCostUSD + DescribeWorkload cover the
//     pre-deploy gates.
//   - Group: AWS has IAM groups, but the cluster-level grouping CAPA
//     uses is VPC + tags (managed by CAPA itself, not us). Returning
//     ErrNotApplicable is the honest answer.
//   - CSI: aws-ebs-csi-driver ships via Helm + an IRSA-bound IAM
//     role. Different shape from the Proxmox CSI Secret apply path;
//     out of scope for the stub.
//
// PatchManifest is a no-op: AWSMachineTemplate sizes via
// spec.template.spec.instanceType (e.g. "t3.medium"), so a future
// patch could map cfg.Providers.Proxmox.ControlPlaneNumCores / Memory to the right
// instance family. Not implemented yet.
package aws

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

func init() {
	provider.Register("aws", func() provider.Provider { return &Provider{} })
}

// Provider implements provider.Provider for CAPA.
type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "aws" }
func (p *Provider) InfraProviderName() string { return "aws" }

// EnsureIdentity is a no-op for AWS: the IAM role + access key the
// user supplies via env vars (or an EC2 instance profile) is the
// identity layer, and the CloudFormation stack that creates the IAM
// role is bootstrapped out-of-band by `clusterawsadm bootstrap iam
// create-cloudformation-stack`.
func (p *Provider) EnsureIdentity(cfg *config.Config) error { return provider.ErrNotApplicable }

// Inventory is unimplemented for AWS: per-family quotas (t3 vs m5)
// don't compose with the flat Total/Used/Available shape, and AWS
// preflight is already covered by EstimateMonthlyCostUSD +
// DescribeWorkload (per §13.4 #1). The orchestrator skips capacity
// preflight when this returns ErrNotApplicable.
func (p *Provider) Inventory(cfg *config.Config) (*provider.Inventory, error) {
	return nil, provider.ErrNotApplicable
}

// EnsureGroup is a no-op for AWS: cluster-level grouping is via VPC
// + tags, both of which CAPA manages itself.
func (p *Provider) EnsureGroup(cfg *config.Config, name string) error {
	return provider.ErrNotApplicable
}

// ClusterctlInitArgs returns "--infrastructure aws". Bootstrap
// (kubeadm vs k3s) is added by the orchestrator from cfg.BootstrapMode.
func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "aws"}
}

// k3sTemplate is the AWS-flavored K3s manifest. Same overall shape
// as the Docker / Proxmox templates; the differences are the
// AWSCluster / AWSMachineTemplate kinds and the AWS-specific
// placeholders (region, SSH key, instance types, AMI). Cloud
// provider is set to external on the kubelet so the AWS cloud
// controller manager (deployed separately) handles node
// initialization.
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
    kind: AWSCluster
    name: ${CLUSTER_NAME}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: AWSCluster
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  region: ${AWS_REGION}
  sshKeyName: ${AWS_SSH_KEY_NAME}
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
      kind: AWSMachineTemplate
      name: ${CLUSTER_NAME}-control-plane
  kthreesConfigSpec:
    serverConfig:
      disableComponents: [servicelb, traefik]
    agentConfig:
      airGapped: false
      kubeletArgs:
        - "--cloud-provider=external"
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: AWSMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-control-plane
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      instanceType: ${AWS_CONTROL_PLANE_MACHINE_TYPE}
      iamInstanceProfile: control-plane.cluster-api-provider-aws.sigs.k8s.io
      sshKeyName: ${AWS_SSH_KEY_NAME}
      ami:
        id: ${AWS_AMI_ID}
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
        kind: AWSMachineTemplate
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
apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
kind: AWSMachineTemplate
metadata:
  name: ${CLUSTER_NAME}-md-0
  namespace: ${NAMESPACE}
spec:
  template:
    spec:
      instanceType: ${AWS_NODE_MACHINE_TYPE}
      iamInstanceProfile: nodes.cluster-api-provider-aws.sigs.k8s.io
      sshKeyName: ${AWS_SSH_KEY_NAME}
      ami:
        id: ${AWS_AMI_ID}
`

func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return k3sTemplate, nil
}

// PatchManifest is a no-op for AWS today. Future: map
// cfg.Providers.Proxmox.ControlPlaneNumCores / Memory to an instance family
// (t3.medium, m5.large, …) and patch
// AWSMachineTemplate.spec.template.spec.instanceType.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}

// EnsureCSISecret is unimplemented for AWS: aws-ebs-csi-driver ships
// via Helm + an IRSA-bound IAM role rather than a credentials Secret.
func (p *Provider) EnsureCSISecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return provider.ErrNotApplicable
}
