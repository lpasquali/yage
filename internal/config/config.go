// Package config holds every tunable variable the bash script's globals
// expose, with the same env-var overrides and defaults. One struct is shared
// by every other package: subsystems read from *Config, they never reach
// into os.Getenv directly (the one exception is boot-time Load, below).
//
// Naming convention: the Go field is the UpperCamelCase spelling of the
// bash var, with _EXPLICIT suffixed flags kept as <Name>Explicit.
//
// Defaults are taken verbatim from the original bash port (lines ~337-673). When
// bash uses ${FOO:-default}, we use getenv(..., "default"); when bash uses
// ${FOO-default} (empty-string preserved), we use getenvKeep(..., "default").
package config

import (
	"os"
	"strconv"
	"strings"
)

// MgmtConfig holds management-cluster shape that every provider needs:
// names, K8s version, replica counts, control-plane endpoint, Cilium add-on
// toggles, and the rendered CAPI manifest. Provider-specific bits (Proxmox
// VM sizing, template IDs, pool, CSI) live in ProxmoxMgmtConfig under
// cfg.Providers.Proxmox.Mgmt.
type MgmtConfig struct {
	ClusterName              string
	ClusterNamespace         string
	KubernetesVersion        string
	CiliumClusterID          string
	ControlPlaneMachineCount string // "1" by default (single-node mgmt)
	WorkerMachineCount       string // "0" by default (CP-only)
	ControlPlaneEndpointIP   string // 1 VIP — user-provided
	ControlPlaneEndpointPort string
	NodeIPRanges             string // 2-IP range — user-provided
	// CiliumHubble / CiliumLBIPAM tune the mgmt-side Cilium
	// HelmChartProxy. Hubble defaults to true (observability is cheap on
	// a single-node cluster); LB-IPAM defaults to false (no L2/BGP
	// announcements needed for management add-ons).
	CiliumHubble string
	CiliumLBIPAM string
	// CAPIManifest is the rendered management-cluster CAPI manifest.
	// Lives next to cfg.CAPIManifest as a Secret on the kind cluster
	// during bootstrap; cleaned up after pivot.
	CAPIManifest string
}

// ProxmoxMgmtConfig holds Proxmox-only sizing / pool / CSI knobs for the
// management cluster. Lives at cfg.Providers.Proxmox.Mgmt because none of
// it makes sense for AWS/Azure/GCP/Hetzner/...
type ProxmoxMgmtConfig struct {
	ControlPlaneNumSockets       string // "1"
	ControlPlaneNumCores         string // "2"
	ControlPlaneMemoryMiB        string // "4096"
	ControlPlaneBootVolumeDevice string
	ControlPlaneBootVolumeSize   string
	ControlPlaneTemplateID       string
	WorkerTemplateID             string
	// Pool is the Proxmox VE pool name the management cluster's VMs are
	// tagged with. See ProxmoxConfig.Pool for the workload counterpart.
	Pool string
	// CSIEnabled — when true, install Proxmox CSI on the management
	// cluster too. Default false: management is stateless (CAPI
	// controllers + bootstrap state Secrets only — no PVCs).
	CSIEnabled bool
}

// Providers groups all per-provider configuration sub-structs. Each cloud
// (and Proxmox, treated as a "cloud" of one) has its own struct under
// cfg.Providers.<Name>; only fields meaningful when that provider is the
// active --infra-provider live there. Universal cluster-shape fields stay at
// the top level of Config.
type Providers struct {
	// Proxmox holds every Proxmox-only knob: API/CSI credentials, identity
	// suffix bookkeeping, VM template / pool / network bridge, the
	// per-cluster VM sizing fields, and the Proxmox-specific bits of the
	// management cluster.
	Proxmox ProxmoxConfig
	// AWS holds AWS-only knobs: region/SKU/AMI defaults, CAPA flavor mode
	// (unmanaged/EKS/EKS+Fargate), Fargate sizing inputs, the
	// "everything else" overhead tier, and per-component overhead
	// overrides for the cost estimator. Credentials come from the AWS
	// SDK chain (env, ~/.aws/config, IAM role) — not from cfg.
	AWS AWSConfig
	// Azure / GCP / Hetzner / DigitalOcean / Linode / OCI / IBMCloud:
	// minimal sub-configs covering the bits the cost estimator and CAPI
	// plumbing read for each provider. See each Config struct's docstring
	// for the field-by-field rationale.
	Azure         AzureConfig
	GCP           GCPConfig
	Hetzner       HetznerConfig
	DigitalOcean  DigitalOceanConfig
	Linode        LinodeConfig
	OCI           OCIConfig
	IBMCloud      IBMCloudConfig
}

// AzureConfig is the per-provider Azure (CAPZ) configuration.
type AzureConfig struct {
	// ControlPlaneMachineType / NodeMachineType drive the Azure VM SKUs
	// CAPZ provisions for the workload cluster when --infrastructure-
	// provider azure. Defaults match the CAPZ quick-start
	// (Standard_D2s_v3 for both CP and worker). The Azure provider
	// also uses these to estimate monthly cost in dry-run.
	ControlPlaneMachineType string
	NodeMachineType         string
	Location                string
	// Mode picks the CAPZ flavor:
	//   - "unmanaged" (default): self-managed Kubernetes on Azure VMs.
	//   - "aks": AKS-managed control plane (AzureManagedControlPlane)
	//     + Node Pool VMs (AzureManagedMachinePool). CP costs flip
	//     from N× VM to a flat hourly fee per cluster.
	Mode string
	// OverheadTier picks the bundled cost estimate (NAT, LB, public IPs,
	// Log Analytics, DNS, egress).
	OverheadTier string
}

// GCPConfig is the per-provider GCP (CAPG) configuration.
type GCPConfig struct {
	// ControlPlaneMachineType / NodeMachineType drive the GCE machine
	// types CAPG provisions for the workload cluster when --infrastructure-
	// provider gcp. Defaults to n2-standard-2 for both. Sustained-use and
	// spot/preemptible discounts are NOT applied in the cost estimate.
	ControlPlaneMachineType string
	NodeMachineType         string
	Region                  string
	Project                 string
	// Mode picks the CAPG flavor (unmanaged / gke). Autopilot is not
	// modeled today.
	Mode string
	// OverheadTier picks the bundled cost estimate (Cloud NAT, LBs,
	// Cloud Logging, Cloud DNS, internet egress).
	OverheadTier string
}

// HetznerConfig is the per-provider Hetzner (CAPHV) configuration.
type HetznerConfig struct {
	// ControlPlaneMachineType / NodeMachineType drive the Hetzner Cloud
	// server types CAPHV provisions when --infrastructure-provider
	// hetzner. Defaults to cx22 — the cheapest type that comfortably
	// runs k3s. Hetzner has no managed-Kubernetes service in CAPHV
	// today, so there's no Mode equivalent to AWS/Azure.
	ControlPlaneMachineType string
	NodeMachineType         string
	Location                string // fsn1, nbg1, hel1, ash, hil, sin
	// OverheadTier picks the bundled cost estimate (LBs, floating IPs,
	// optional volumes, optional backup surcharge).
	OverheadTier string
}

// DigitalOceanConfig is the per-provider DigitalOcean (CAPDO) configuration.
// API token comes from env DIGITALOCEAN_TOKEN — not from cfg.
type DigitalOceanConfig struct {
	Region           string
	ControlPlaneSize string // s-2vcpu-4gb, s-4vcpu-8gb, ...
	NodeSize         string
}

// LinodeConfig is the per-provider Linode/Akamai (CAPL) configuration.
// Catalog is auth-free; provisioning needs LINODE_TOKEN.
type LinodeConfig struct {
	Region           string
	ControlPlaneType string // g6-standard-2, g6-standard-4, ...
	NodeType         string
}

// OCIConfig is the per-provider Oracle Cloud Infrastructure (CAPOCI)
// configuration. Cost estimator JSON is auth-free; provisioning needs
// OCI API key (not in cfg).
type OCIConfig struct {
	Region            string
	ControlPlaneShape string // VM.Standard.E4.Flex, ...
	NodeShape         string
}

// IBMCloudConfig is the per-provider IBM Cloud (CAPIBM) configuration.
// Both the Global Catalog (pricing) and provisioning need IBMCLOUD_API_KEY.
type IBMCloudConfig struct {
	Region              string
	ControlPlaneProfile string // bx2-2x8, cx2-4x8, ...
	NodeProfile         string
}

// AWSConfig is the per-provider AWS configuration. Field names lose the
// AWS prefix because the path qualifies them.
type AWSConfig struct {
	// ControlPlaneMachineType / NodeMachineType drive the EC2 instance
	// types CAPA provisions for the workload cluster when
	// --infrastructure-provider aws. Defaults match the CAPA quick-start
	// (t3.large CP, t3.medium worker). The AWS provider also uses these
	// to estimate monthly cost in dry-run.
	ControlPlaneMachineType string
	NodeMachineType         string
	Region                  string
	SSHKeyName              string
	AMIID                   string
	// Mode picks the CAPA flavor:
	//   - "unmanaged" (default): self-managed Kubernetes on EC2.
	//     CAPA emits AWSCluster + AWSMachineTemplate; you pay for
	//     CP + worker EC2 nodes + EBS.
	//   - "eks": EKS-managed control plane (AWSManagedControlPlane)
	//     + EC2 worker nodes (AWSManagedMachinePool). CP costs flip
	//     from 3× EC2 to a flat hourly fee per cluster (live AWS price).
	//   - "eks-fargate": EKS CP + Fargate workers (AWSFargateProfile).
	//     No worker EC2 fleet; pay per pod-vCPU-hour + GB-hour.
	Mode string
	// FargatePodCount / FargatePodCPU / FargatePodMemoryGiB parameterise
	// the cost estimate when Mode=eks-fargate. Pod count is the
	// application-pod count you expect (rough planning input — Argo
	// later actually deploys), not a VM count. Default 10 pods.
	FargatePodCount     string
	FargatePodCPU       string // vCPU per Fargate task; "0.25" "0.5" "1" "2" "4"
	FargatePodMemoryGiB string // GiB per Fargate task; pairs with CPU per AWS rules
	// OverheadTier picks the bundled "everything else" cost estimate
	// (NAT Gateway, ALB/NLB, CloudWatch, Route53, ECR, VPC endpoints,
	// data transfer):
	//   - "dev"        — single-AZ, no NAT, 1 ALB, minimal CloudWatch
	//   - "prod"       — 1 NAT GW, 1 ALB, CloudWatch, Route53        (default)
	//   - "enterprise" — 3 NAT GWs (multi-AZ HA), 2 ALBs, VPC endpoints
	// Per-component overrides are also available; see NATGatewayCount,
	// ALBCount, NLBCount, DataTransferGB, CloudWatchLogsGB.
	OverheadTier      string
	NATGatewayCount   string // overrides the tier default
	ALBCount          string
	NLBCount          string
	DataTransferGB    string // monthly egress estimate
	CloudWatchLogsGB  string
	Route53HostedZones string
}

// ProxmoxConfig is the per-provider Proxmox configuration. Field names lose
// the redundant `Proxmox` prefix because the path already qualifies them
// (cfg.Providers.Proxmox.URL is unambiguous). CLI flag and env-var spellings
// are preserved verbatim for back-compat — only the in-process struct path
// changes.
type ProxmoxConfig struct {
	// ---- Bootstrap config / secret bookkeeping ----
	BootstrapConfigFile       string
	BootstrapConfigSecretName string
	BootstrapConfigSecretKey  string
	BootstrapAdminSecretKey   string
	BootstrapSecretNamespace  string
	BootstrapSecretName       string
	BootstrapCAPMOXSecretName string
	BootstrapCSISecretName    string
	BootstrapAdminSecretName  string
	BootstrapKindSecretUsed   bool
	KindCAPMOXActive          bool

	// ---- Admin / identity ----
	AdminConfig             string
	AdminInsecure           string
	AdminUsername           string
	AdminToken              string
	IdentityTF              string
	IdentityRecreateScope   string
	IdentityRecreateStateRm bool
	IdentitySuffix          string
	RecreateIdentities      bool

	// ---- Core API / target ----
	URL            string
	Token          string
	Secret         string
	Region         string
	Node           string
	SourceNode     string
	TopologyRegion string
	TopologyZone   string
	TemplateID     string
	Bridge         string
	// Pool is the Proxmox VE pool name VMs created by CAPMOX will be tagged
	// with. Pools group VMs in the Proxmox UI and gate ACLs (delegating
	// start/stop/console permissions); they do NOT enforce CPU/memory
	// quotas — that remains per-VM + per-storage. Empty default means
	// "no pool"; when set, yage pre-creates the pool via the admin API
	// before applying the CAPI manifest, so CAPMOX won't fail on a missing
	// pool reference.
	Pool string

	// ---- CSI ----
	CSIEnabled          bool
	CSISmokeEnabled     bool
	CSIChartRepoURL     string
	CSIChartName        string
	CSIChartVersion     string
	CSINamespace        string
	CSIConfigProvider   string
	CSITopologyLabels   string
	CSIConfig           string
	CSIURL              string
	CSITokenID          string
	CSITokenSecret      string
	CSIUserID           string
	CSITokenPrefix      string
	CSIInsecure         string
	CSIStorageClassName string
	CSIStorage          string
	CSIReclaimPolicy    string
	CSIFsType           string
	CSIDefaultClass     string

	// ---- CAPMOX identity / cloud-init / memory ----
	CAPIUserID                 string
	CAPITokenPrefix            string
	CAPIMachineTemplateSpecRev bool
	CloudinitStorage           string
	MemoryAdjustment           string

	// ---- Per-cluster VM sizing (Proxmox VM concepts) ----
	//
	// NumSockets / NumCores / MemoryMiB / BootVolume{Device,Size} are
	// Proxmox VM knobs — AWS encodes the same idea as a single instance
	// type string (AWSNodeMachineType), Hetzner as a server type, etc.
	// They live here, not at the top level, because only the Proxmox
	// orchestrator path reads them.
	ControlPlaneBootVolumeDevice string
	ControlPlaneBootVolumeSize   string
	ControlPlaneNumSockets       string
	ControlPlaneNumCores         string
	ControlPlaneMemoryMiB        string
	WorkerBootVolumeDevice       string
	WorkerBootVolumeSize         string
	WorkerNumSockets             string
	WorkerNumCores               string
	WorkerMemoryMiB              string

	// ---- Management cluster (Proxmox VM specifics) ----
	//
	// Mirrors the workload sizing block above but for the kind→mgmt-cluster
	// pivot target. See ProxmoxMgmtConfig for the field-by-field rationale.
	Mgmt ProxmoxMgmtConfig
}

// Config holds every runtime tunable. Zero value is not meaningful — always
// call Load().
type Config struct {
	// Providers groups per-cloud configuration. Today only Proxmox lives
	// here; the AWS/Azure/… buckets land in subsequent commits of Phase C.
	Providers Providers


	// ---- Tool versions ----
	KindVersion       string
	KubectlVersion    string
	ClusterctlVersion string
	CiliumCLIVersion  string
	CiliumVersion     string
	// ArgoCDVersion drives both the argocd CLI release tag and the
	// ArgoCD CR spec.version; the two are kept in lockstep upstream.
	ArgoCDVersion         string
	ArgoCDOperatorVersion string
	KyvernoCLIVersion string
	CmctlVersion      string
	OpenTofuVersion   string

	// ---- Cilium ----
	CiliumWaitDuration                string
	CiliumIngress                     string
	CiliumKubeProxyReplacement        string
	CiliumLBIPAM                      string
	CiliumLBIPAMPoolCIDR              string
	CiliumLBIPAMPoolStart             string
	CiliumLBIPAMPoolStop              string
	CiliumLBIPAMPoolName              string
	CiliumHubble                      string
	CiliumHubbleUI                    string
	CiliumIPAMClusterPoolIPv4         string
	CiliumIPAMClusterPoolIPv4MaskSize string
	CiliumGatewayAPIEnabled           string

	// ---- ArgoCD ----
	ArgoCDEnabled                         bool
	ArgoCDServerInsecure                  string
	ArgoCDDisableOperatorManagedIngress   string
	ArgoCDOperatorArgoCDPrometheusEnabled string
	ArgoCDOperatorArgoCDMonitoringEnabled string
	ArgoCDPrintAccessStandalone           bool
	ArgoCDPrintAccessTarget               string
	ArgoCDPortForwardStandalone           bool
	ArgoCDPortForwardTarget               string
	ArgoCDPortForwardPort                 string

	// Workload ArgoCD / GitOps
	WorkloadArgoCDEnabled      bool
	WorkloadArgoCDNamespace    string
	WorkloadGitopsMode         string
	WorkloadAppOfAppsGitURL    string
	WorkloadAppOfAppsGitPath   string
	WorkloadAppOfAppsGitRef    string
	WorkloadRolloutStandalone  bool
	WorkloadRolloutMode        string
	WorkloadRolloutNoWait      bool

	// ---- Top-level flags ----
	Force                       bool
	NoDeleteKind                bool
	BootstrapPersistLocalSecrets bool
	Purge                       bool
	BuildAll                    bool
	// DryRun, when true, makes Run() print a structured plan of what
	// every phase would do (based on the current cfg) and exit without
	// executing any phase. Distinct from PivotDryRun (which actually
	// provisions the mgmt cluster and stops at `clusterctl move`).
	DryRun                      bool
	// CostCompare, when true, makes the dry-run plan include a
	// cross-cloud comparison: same logical cluster shape evaluated
	// against every registered provider's EstimateMonthlyCostUSD,
	// with a per-cloud "if you spent this on storage" retention
	// column. Independent of cfg.InfraProvider — runs all of them.
	CostCompare                 bool
	// BudgetUSDMonth, when > 0, drives a retention calculation:
	// budget − compute = leftover; leftover ÷ block-storage $/GB-mo
	// = how much persistent volume capacity remains for
	// observability / DB buckets after the cluster is paid for.
	BudgetUSDMonth              float64
	// PrintPricingSetup, when non-empty, makes the program print
	// the IAM/token setup snippet for the named vendor (or "all"
	// for every vendor that needs setup) and exit. Intended for
	// users who dismissed the first-run hint and want to see it
	// again. Empty string means "no special action".
	PrintPricingSetup           string
	// Xapiri, when true, launches the interactive configuration TUI
	// (--xapiri) and exits. Mutually exclusive with the orchestrator
	// run; setting it short-circuits main() before orchestrator.Run.
	Xapiri                      bool
	// ResourceBudgetFraction caps the share of available Proxmox host
	// CPU/memory/storage that the configured clusters may consume.
	// 0.75 by default — the remaining 25 % is reserved for the host
	// OS, hypervisor overhead, and slack for VM rollouts.
	ResourceBudgetFraction      float64
	// AllowResourceOvercommit, when true, downgrades the
	// over-the-budget capacity check to a warning instead of failing
	// the run.
	AllowResourceOvercommit     bool
	// OvercommitTolerancePct caps how far above 100% of host capacity
	// the combined (existing-VM + planned) demand may go before the
	// orchestrator refuses to continue. 15 = "(existing + planned)
	// must be ≤ host × 1.15" — Proxmox supports memory overcommit
	// via ballooning + swap, but >15% drift starts to OOM under
	// load. Below the soft threshold (ResourceBudgetFraction) is
	// fine; between threshold and 1+tolerance is "tight, warn-and-
	// continue"; above 1+tolerance is "abort unless --allow-resource-
	// overcommit". Default 15. See capacity.CheckCombined.
	OvercommitTolerancePct      float64
	// HardwareCostUSD is the capex of the entire on-prem cluster
	// (sum of every node's purchase price). > 0 enables the TCO
	// path for self-hosted providers (Proxmox, vSphere) — they
	// otherwise return ErrNotApplicable. Amortized monthly capex
	// is HardwareCostUSD / (HardwareUsefulLifeYears × 12).
	HardwareCostUSD             float64
	// HardwareUsefulLifeYears is the depreciation horizon over
	// which to amortize the capex. Default 5 — the typical server
	// refresh cadence and the IRS MACRS 5-year property class.
	HardwareUsefulLifeYears     float64
	// HardwareWatts is the cluster's continuous draw at typical
	// load (NOT max nameplate). Used to compute electricity opex.
	HardwareWatts               float64
	// HardwareKWHRateUSD is the user's electricity rate in USD per
	// kWh (delivered, including transmission/taxes — not just
	// generation). Default 0.15 (rough US average).
	HardwareKWHRateUSD          float64
	// HardwareSupportUSDMonth is any flat monthly cost the operator
	// wants to fold into the estimate — vSphere licensing, ESXi
	// support contract, IPMI subscription, colo/rack rental, etc.
	HardwareSupportUSDMonth     float64
	// SystemAppsCPUMillicores / SystemAppsMemoryMiB define the cluster-
	// wide reserve for the system add-ons yage installs:
	// kyverno, cert-manager, proxmox-csi (controller), argocd (operator
	// + server + repo + redis), keycloak (SSO), external-secrets, and
	// infisical. The remainder of the workload cluster's worker capacity
	// is split into three equal buckets (db / observability / product).
	SystemAppsCPUMillicores     int    // default 2000 = 2 cores
	SystemAppsMemoryMiB         int64  // default 4096 = 4 GiB
	// AWS-only fields live in cfg.Providers.AWS.* (see AWSConfig).
	// Azure / GCP / Hetzner / DigitalOcean / Linode / OCI / IBMCloud
	// per-provider fields live under cfg.Providers.<Name>.* — see the
	// matching <Name>Config struct above for the field roster.
	// BootstrapMode selects the Kubernetes flavor:
	//   - "kubeadm" (default): standard upstream Kubernetes via kubeadm,
	//     control-plane runs etcd + apiserver + controller-manager +
	//     scheduler. Realistic minimum 2 vCPU + 4 GiB per node.
	//   - "k3s": single-binary Kubernetes optimised for low-resource
	//     environments (Raspberry Pi, edge, dev VMs). Fits in ~1 vCPU
	//     + 1 GiB. Requires the CAPI K3s providers (KCP-K3s + CABK3s).
	BootstrapMode               string

	// ---- Kind / management cluster ----
	ClusterID                    string
	KindClusterName              string
	ClusterName                  string
	KindConfig                   string
	BootstrapEphemeralKindConfig string
	BootstrapKindConfigEphemeral bool

	// ---- CAPI manifest (workload) ----
	CAPIManifest                             string
	BootstrapCAPIManifestEphemeral           bool
	BootstrapCAPIManifestUserSet             bool
	BootstrapCAPIUseSecret                   bool
	BootstrapRegenerateCAPIManifest          bool
	BootstrapSkipImmutableManifestWarning    bool
	BootstrapClusterctlRegeneratedManifest   bool
	CAPIManifestSecretNamespace              string
	CAPIManifestSecretName                   string
	CAPIManifestSecretKey                    string

	// ---- Kind backup/restore ----
	BootstrapKindBackupNamespaces string
	BootstrapKindBackupOut        string
	BootstrapKindBackupEncrypt    string
	BootstrapKindBackupPassphrase string
	BootstrapKindStateOp          string
	BootstrapKindStatePath        string

	// ---- CAPI providers ----
	InfraProvider      string
	IPAMProvider       string
	CAPMOXRepo         string
	CAPMOXImageRepo    string
	CAPMOXBuildDir     string
	CAPMOXVersion      string
	CAPICoreImage      string
	CAPICoreRepo       string
	CAPIBootstrapImage string
	CAPIControlplaneImage string
	IPAMImage          string
	IPAMRepo           string

	// ---- Clusterctl experimental / topology ----
	ExpClusterResourceSet           string
	ClusterTopology                 string
	ExpKubeadmBootstrapFormatIgnition string

	// ---- metrics-server ----
	EnableMetricsServer               bool
	EnableWorkloadMetricsServer       bool
	WorkloadMetricsServerInsecureTLS  string
	MetricsServerManifestURL          string
	MetricsServerGitChartTag          string

	// Argo workload post-sync hooks
	ArgoWorkloadPostsyncHooksEnabled    bool
	ArgoWorkloadPostsyncHooksGitURL     string
	ArgoWorkloadPostsyncHooksGitPath    string
	ArgoWorkloadPostsyncHooksGitRef     string
	ArgoWorkloadPostsyncHooksKubectlImg string
	WorkloadPostsyncNamespace           string

	// ---- Kyverno ----
	KyvernoEnabled                bool
	KyvernoChartVersion           string
	KyvernoChartRepoURL           string
	KyvernoNamespace              string
	KyvernoTolerateControlPlane   string

	// ---- cert-manager ----
	CertManagerEnabled       bool
	CertManagerChartVersion  string
	CertManagerChartRepoURL  string
	CertManagerNamespace     string

	// ---- Crossplane ----
	CrossplaneEnabled       bool
	CrossplaneChartVersion  string
	CrossplaneChartRepoURL  string
	CrossplaneNamespace     string

	// ---- CloudNativePG ----
	CNPGEnabled       bool
	CNPGChartVersion  string
	CNPGChartRepoURL  string
	CNPGChartName     string
	CNPGNamespace     string

	// ---- External-Secrets ----
	ExternalSecretsEnabled      bool
	ExternalSecretsChartRepoURL string
	ExternalSecretsChartVersion string
	ExternalSecretsNamespace    string

	// ---- Infisical ----
	InfisicalOperatorEnabled bool
	InfisicalChartRepoURL    string
	InfisicalChartName       string
	InfisicalChartVersion    string
	InfisicalNamespace       string

	// ---- SPIRE ----
	SPIREEnabled               bool
	SPIREChartRepoURL          string
	SPIREChartName             string
	SPIREChartVersion          string
	SPIRECRDsChartName         string
	SPIRECRDsChartVersion      string
	SPIREHelmEnableGlobalHooks string
	SPIRENamespace             string
	SPIREOIDCInsecureHTTP      string
	SPIREOIDCBundleSource      string
	SPIRETolerateControlPlane  string

	// ---- OpenTelemetry ----
	OTELEnabled         bool
	OTELChartRepoURL    string
	OTELChartName       string
	OTELChartVersion    string
	OTELImageRepository string
	OTELCollectorMode   string
	OTELNamespace       string

	// ---- Grafana ----
	GrafanaEnabled      bool
	GrafanaChartRepoURL string
	GrafanaChartVersion string
	GrafanaNamespace    string

	// ---- VictoriaMetrics ----
	VictoriaMetricsEnabled      bool
	VictoriaMetricsChartRepoURL string
	VictoriaMetricsChartName    string
	VictoriaMetricsChartVersion string
	VictoriaMetricsNamespace    string

	// ---- Backstage ----
	BackstageEnabled      bool
	BackstageChartRepoURL string
	BackstageChartName    string
	BackstageChartVersion string
	BackstageNamespace    string

	// ---- Keycloak ----
	KeycloakEnabled          bool
	KeycloakChartRepoURL     string
	KeycloakChartName        string
	KeycloakChartVersion     string
	KeycloakNamespace        string
	KeycloakKcHostnameStrict string
	KeycloakKcHostname       string
	KeycloakKcDB             string
	KeycloakOperatorEnabled  bool
	KeycloakOperatorGitURL   string
	KeycloakOperatorGitPath  string
	KeycloakOperatorGitRef   string
	KeycloakOperatorNS       string

	// ---- Cluster / clusterctl plumbing (universal) ----
	ClusterctlCfg string
	ClusterSetID  string

	// Mgmt holds universal management-cluster shape (names, K8s version,
	// replica counts, control-plane endpoint, Cilium toggles, manifest).
	// Provider-specific bits — Proxmox VM sizing, template IDs, pool, CSI —
	// live under cfg.Providers.Proxmox.Mgmt.
	Mgmt MgmtConfig

	// MgmtKubeconfigPath is set by the orchestrator after
	// EnsureManagementCluster() returns; read by Provider.PivotTarget
	// (Phase E) to know which kubeconfig file `clusterctl move` should
	// pivot into. Empty until that phase runs. Lives at the top level —
	// not under cfg.Mgmt — because it's runtime-discovered, not
	// configuration the user provides.
	MgmtKubeconfigPath string

	// ---- Network / IP ----
	ControlPlaneEndpointIP   string
	ControlPlaneEndpointPort string
	NodeIPRanges             string
	Gateway                  string
	IPPrefix                 string
	DNSServers               string
	DNSServersExplicit       bool
	GatewayExplicit          bool
	IPPrefixExplicit         bool
	NodeIPRangesExplicit     bool
	AllowedNodesExplicit     bool
	AllowedNodes             string
	VMSSHKeys                string

	// ---- Per-machine-type Proxmox VM template overrides (workload) ----
	//
	// Each cluster (workload or management) and role (control-plane or
	// worker) can specify its own Proxmox template VM ID. Empty values
	// fall through to Providers.Proxmox.TemplateID — the catch-all default
	// that clusterctl substitutes during manifest generation. The overrides
	// are applied as a post-generation patch on the corresponding
	// ProxmoxMachineTemplate (matched by metadata.name containing
	// "control-plane" or "worker"). Mgmt-side equivalents live in
	// cfg.Providers.Proxmox.Mgmt.{ControlPlaneTemplateID,WorkerTemplateID}.
	WorkloadControlPlaneTemplateID string
	WorkloadWorkerTemplateID       string

	// ---- Workload cluster ----
	WorkloadClusterName             string
	WorkloadCiliumClusterID         string
	WorkloadClusterNamespace        string
	WorkloadClusterNameExplicit     bool
	WorkloadClusterNamespaceExplicit bool
	WorkloadKubernetesVersion       string
	ControlPlaneMachineCount        string
	WorkerMachineCount              string

	// ---- Pivot orchestration toggles ----
	//
	// When PivotEnabled is true, the bootstrap follows the standard CAPI
	// "bootstrap-and-pivot" pattern: kind provisions a management cluster
	// on Proxmox, clusterctl-moves CAPI state into it, the
	// proxmox-bootstrap-system Secrets are mirrored, the workload cluster
	// is created from the management cluster, and the kind cluster is
	// torn down once the management cluster is verified to carry the
	// same state.
	//
	// Default sizing is intentionally smaller than the workload defaults:
	// the management cluster runs only CAPI controllers, CAAPH, capmox,
	// in-cluster IPAM, and the bootstrap-state Secrets — no application
	// workload. 1 socket / 2 cores / 4 GiB is enough; one CP endpoint
	// VIP and a 2-IP node range (so a rollout can land a replacement VM
	// before draining the original).
	//
	// CNI: Cilium with Hubble enabled but LB-IPAM disabled (the
	// management cluster has no Services that need LoadBalancer IPs).
	// CSI: disabled by default (the management cluster is stateless
	// unless explicitly opted-in via MGMT_PROXMOX_CSI_ENABLED=true).
	//
	// The cluster shape itself lives in cfg.Mgmt; Proxmox-only sizing /
	// pool / CSI knobs live in cfg.Providers.Proxmox.Mgmt.
	PivotEnabled bool
	// PivotKeepKind, when true, skips the final `kind delete cluster`
	// after a successful pivot — useful for debugging.
	PivotKeepKind bool
	// PivotVerifyTimeout caps how long we wait for the management
	// cluster to look "identical" to kind before declaring success.
	PivotVerifyTimeout string
	// PivotDryRun stops after provisioning + clusterctl-init on the
	// management cluster, runs `clusterctl move --dry-run` so the user
	// can inspect the planned hand-off without executing it, and
	// returns. The workload cluster stays managed by kind. Useful for
	// validating mgmt connectivity / sizing before committing to the
	// move.
	PivotDryRun bool
}

// Load reads environment variables and applies the same defaults the bash
// script did on source. CLI flag parsing runs *after* Load() and can
// overwrite any field in place.
//
// Bash defaults are referenced inline for the non-obvious ones; trivial
// defaults are applied with the getenv helper.
func Load() *Config {
	c := &Config{}

	// --- versions (lines 337-341, 416, 446-451, 501, 508) ---
	c.KindVersion = getenv("KIND_VERSION", "v0.31.0")
	c.KubectlVersion = getenv("KUBECTL_VERSION", "v1.35.4")
	c.ClusterctlVersion = getenv("CLUSTERCTL_VERSION", "v1.11.8")
	c.CiliumCLIVersion = getenv("CILIUM_CLI_VERSION", "v0.19.2")
	c.CiliumVersion = getenv("CILIUM_VERSION", "1.19.3")
	c.ArgoCDVersion = getenv("ARGOCD_VERSION", "v3.3.8")
	c.ArgoCDOperatorVersion = getenv("ARGOCD_OPERATOR_VERSION", "v0.16.0")
	c.KyvernoCLIVersion = getenv("KYVERNO_CLI_VERSION", "v1.17.1")
	c.CmctlVersion = getenv("CMCTL_VERSION", "v2.4.1")
	c.OpenTofuVersion = getenv("OPENTOFU_VERSION", "1.8.5")

	// --- Cilium (lines 342-356) ---
	c.CiliumWaitDuration = getenv("CILIUM_WAIT_DURATION", "10m0s")
	c.CiliumIngress = getenv("CILIUM_INGRESS", "true")
	c.CiliumKubeProxyReplacement = getenv("CILIUM_KUBE_PROXY_REPLACEMENT", "true")
	c.CiliumLBIPAM = getenv("CILIUM_LB_IPAM", "true")
	c.CiliumLBIPAMPoolCIDR = getenv("CILIUM_LB_IPAM_POOL_CIDR", "")
	c.CiliumLBIPAMPoolStart = getenv("CILIUM_LB_IPAM_POOL_START", "")
	c.CiliumLBIPAMPoolStop = getenv("CILIUM_LB_IPAM_POOL_STOP", "")
	c.CiliumLBIPAMPoolName = getenv("CILIUM_LB_IPAM_POOL_NAME", "")
	c.CiliumHubble = getenv("CILIUM_HUBBLE", "true")
	c.CiliumHubbleUI = getenv("CILIUM_HUBBLE_UI", "true")
	c.CiliumIPAMClusterPoolIPv4 = getenv("CILIUM_IPAM_CLUSTER_POOL_IPV4", "10.244.0.0/16")
	c.CiliumIPAMClusterPoolIPv4MaskSize = getenv("CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE", "24")
	c.CiliumGatewayAPIEnabled = getenv("CILIUM_GATEWAY_API_ENABLED", "false")

	// --- ArgoCD (lines 358, 428-472) ---
	c.ArgoCDDisableOperatorManagedIngress = getenv("ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS", "false")
	c.ArgoCDEnabled = envBool("ARGOCD_ENABLED", true)
	c.ArgoCDServerInsecure = getenv("ARGOCD_SERVER_INSECURE", "false")
	c.ArgoCDOperatorArgoCDPrometheusEnabled = getenv("ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED", "false")
	c.ArgoCDOperatorArgoCDMonitoringEnabled = getenv("ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED", "false")
	c.ArgoCDPrintAccessTarget = getenv("ARGOCD_PRINT_ACCESS_TARGET", "workload")
	c.ArgoCDPrintAccessStandalone = envBool("ARGOCD_PRINT_ACCESS_STANDALONE", false)
	c.ArgoCDPortForwardStandalone = envBool("ARGOCD_PORT_FORWARD_STANDALONE", false)
	c.ArgoCDPortForwardTarget = getenv("ARGOCD_PORT_FORWARD_TARGET", "workload")
	c.ArgoCDPortForwardPort = getenv("ARGOCD_PORT_FORWARD_PORT", getenv("ARGOCD_PORT_FORWARD_WORKLOAD_PORT", "8443"))

	// Workload ArgoCD/GitOps (lines 430-479)
	c.WorkloadArgoCDEnabled = envBool("WORKLOAD_ARGOCD_ENABLED", true)
	c.WorkloadArgoCDNamespace = getenv("WORKLOAD_ARGOCD_NAMESPACE", "argocd")
	c.WorkloadGitopsMode = getenv("WORKLOAD_GITOPS_MODE", "caaph")
	c.WorkloadAppOfAppsGitURL = getenv("WORKLOAD_APP_OF_APPS_GIT_URL", "https://github.com/lpasquali/workload-app-of-apps.git")
	c.WorkloadAppOfAppsGitPath = getenv("WORKLOAD_APP_OF_APPS_GIT_PATH", "examples/default")
	c.WorkloadAppOfAppsGitRef = getenv("WORKLOAD_APP_OF_APPS_GIT_REF", "main")
	c.WorkloadRolloutStandalone = envBool("WORKLOAD_ROLLOUT_STANDALONE", false)
	c.WorkloadRolloutMode = getenv("WORKLOAD_ROLLOUT_MODE", "argocd")
	c.WorkloadRolloutNoWait = envBool("WORKLOAD_ROLLOUT_NO_WAIT", false)

	// --- Top-level flags (lines 360-364) ---
	c.Force = envBool("FORCE", false)
	c.NoDeleteKind = envBool("NO_DELETE_KIND", false)
	c.BootstrapPersistLocalSecrets = envBool("BOOTSTRAP_PERSIST_LOCAL_SECRETS", false)
	c.Purge = envBool("PURGE", false)
	c.BuildAll = envBool("BUILD_ALL", false)
	c.DryRun = envBool("DRY_RUN", false)
	c.AllowResourceOvercommit = envBool("ALLOW_RESOURCE_OVERCOMMIT", false)
	c.ResourceBudgetFraction = envFloat("RESOURCE_BUDGET_FRACTION", 2.0/3.0)
	c.OvercommitTolerancePct = envFloat("OVERCOMMIT_TOLERANCE_PCT", 15.0)
	c.HardwareCostUSD = envFloat("HARDWARE_COST_USD", 0)
	c.HardwareUsefulLifeYears = envFloat("HARDWARE_USEFUL_LIFE_YEARS", 5)
	c.HardwareWatts = envFloat("HARDWARE_WATTS", 0)
	c.HardwareKWHRateUSD = envFloat("HARDWARE_KWH_RATE_USD", 0.15)
	c.HardwareSupportUSDMonth = envFloat("HARDWARE_SUPPORT_USD_MONTH", 0)
	c.BootstrapMode = getenv("BOOTSTRAP_MODE", "kubeadm")
	c.Providers.AWS.ControlPlaneMachineType = getenv("AWS_CONTROL_PLANE_MACHINE_TYPE", "t3.large")
	c.Providers.AWS.NodeMachineType = getenv("AWS_NODE_MACHINE_TYPE", "t3.medium")
	c.Providers.AWS.Region = getenv("AWS_REGION", "us-east-1")
	c.Providers.AWS.SSHKeyName = getenv("AWS_SSH_KEY_NAME", "")
	c.Providers.AWS.AMIID = getenv("AWS_AMI_ID", "")
	c.Providers.AWS.Mode = getenv("AWS_MODE", "unmanaged")
	c.Providers.AWS.FargatePodCount = getenv("AWS_FARGATE_POD_COUNT", "10")
	c.Providers.AWS.FargatePodCPU = getenv("AWS_FARGATE_POD_CPU", "0.5")
	c.Providers.AWS.FargatePodMemoryGiB = getenv("AWS_FARGATE_POD_MEMORY_GIB", "1")
	c.Providers.AWS.OverheadTier = getenv("AWS_OVERHEAD_TIER", "prod")
	// Per-component overrides default to empty so the tier defaults
	// apply; setting any of these to a non-empty value pins that
	// component regardless of tier.
	c.Providers.AWS.NATGatewayCount = getenv("AWS_NAT_GATEWAY_COUNT", "")
	c.Providers.AWS.ALBCount = getenv("AWS_ALB_COUNT", "")
	c.Providers.AWS.NLBCount = getenv("AWS_NLB_COUNT", "")
	c.Providers.AWS.DataTransferGB = getenv("AWS_DATA_TRANSFER_GB", "")
	c.Providers.AWS.CloudWatchLogsGB = getenv("AWS_CLOUDWATCH_LOGS_GB", "")
	c.Providers.AWS.Route53HostedZones = getenv("AWS_ROUTE53_HOSTED_ZONES", "")
	c.Providers.Azure.ControlPlaneMachineType = getenv("AZURE_CONTROL_PLANE_MACHINE_TYPE", "Standard_D2s_v3")
	c.Providers.Azure.NodeMachineType = getenv("AZURE_NODE_MACHINE_TYPE", "Standard_D2s_v3")
	c.Providers.Azure.Location = getenv("AZURE_LOCATION", "eastus")
	c.Providers.Azure.Mode = getenv("AZURE_MODE", "unmanaged")
	c.Providers.Azure.OverheadTier = getenv("AZURE_OVERHEAD_TIER", "prod")
	c.Providers.GCP.ControlPlaneMachineType = getenv("GCP_CONTROL_PLANE_MACHINE_TYPE", "n2-standard-2")
	c.Providers.GCP.NodeMachineType = getenv("GCP_NODE_MACHINE_TYPE", "n2-standard-2")
	c.Providers.GCP.Region = getenv("GCP_REGION", "us-central1")
	c.Providers.GCP.Project = getenv("GCP_PROJECT", "")
	c.Providers.GCP.Mode = getenv("GCP_MODE", "unmanaged")
	c.Providers.GCP.OverheadTier = getenv("GCP_OVERHEAD_TIER", "prod")
	c.Providers.Hetzner.ControlPlaneMachineType = getenv("HCLOUD_CONTROL_PLANE_MACHINE_TYPE", "cx22")
	c.Providers.Hetzner.NodeMachineType = getenv("HCLOUD_NODE_MACHINE_TYPE", "cx22")
	c.Providers.Hetzner.Location = getenv("HCLOUD_REGION", "fsn1")
	c.Providers.Hetzner.OverheadTier = getenv("HETZNER_OVERHEAD_TIER", "prod")
	c.Providers.DigitalOcean.Region = getenv("DIGITALOCEAN_REGION", "nyc3")
	c.Providers.DigitalOcean.ControlPlaneSize = getenv("DIGITALOCEAN_CONTROL_PLANE_SIZE", "s-2vcpu-4gb")
	c.Providers.DigitalOcean.NodeSize = getenv("DIGITALOCEAN_NODE_SIZE", "s-2vcpu-4gb")
	c.Providers.Linode.Region = getenv("LINODE_REGION", "us-east")
	c.Providers.Linode.ControlPlaneType = getenv("LINODE_CONTROL_PLANE_TYPE", "g6-standard-2")
	c.Providers.Linode.NodeType = getenv("LINODE_NODE_TYPE", "g6-standard-2")
	c.Providers.OCI.Region = getenv("OCI_REGION", "us-ashburn-1")
	c.Providers.OCI.ControlPlaneShape = getenv("OCI_CONTROL_PLANE_SHAPE", "VM.Standard.E4.Flex")
	c.Providers.OCI.NodeShape = getenv("OCI_NODE_SHAPE", "VM.Standard.E4.Flex")
	c.Providers.IBMCloud.Region = getenv("IBMCLOUD_REGION", "us-south")
	c.Providers.IBMCloud.ControlPlaneProfile = getenv("IBMCLOUD_CONTROL_PLANE_PROFILE", "bx2-2x8")
	c.Providers.IBMCloud.NodeProfile = getenv("IBMCLOUD_NODE_PROFILE", "bx2-2x8")
	c.SystemAppsCPUMillicores = int(envFloat("SYSTEM_APPS_CPU_MILLICORES", 2000))
	c.SystemAppsMemoryMiB = int64(envFloat("SYSTEM_APPS_MEMORY_MIB", 4096))

	// --- Kind / management ----
	c.ClusterID = getenv("CLUSTER_ID", "1")
	c.KindClusterName = getenv("KIND_CLUSTER_NAME", "")
	c.ClusterName = getenv("CLUSTER_NAME", "")
	c.KindConfig = getenv("KIND_CONFIG", "")

	// --- CAPI manifest ---
	c.CAPIManifest = getenv("CAPI_MANIFEST", "")
	c.BootstrapRegenerateCAPIManifest = envBool("BOOTSTRAP_REGENERATE_CAPI_MANIFEST", false)
	c.BootstrapSkipImmutableManifestWarning = envBool("BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING", false)
	c.BootstrapClusterctlRegeneratedManifest = envBool("BOOTSTRAP_CLUSTERCTL_REGENERATED_MANIFEST", false)
	c.Providers.Proxmox.CAPIMachineTemplateSpecRev = envBool("CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV", true)
	c.CAPIManifestSecretNamespace = getenv("CAPI_MANIFEST_SECRET_NAMESPACE", "proxmox-bootstrap-system")
	c.CAPIManifestSecretName = getenv("CAPI_MANIFEST_SECRET_NAME", "proxmox-yage-manifest")
	c.CAPIManifestSecretKey = getenv("CAPI_MANIFEST_SECRET_KEY", "workload.yaml")
	c.Providers.Proxmox.BootstrapConfigFile = getenv("PROXMOX_BOOTSTRAP_CONFIG_FILE", "")
	c.Providers.Proxmox.BootstrapConfigSecretName = getenv("PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME", "proxmox-bootstrap-config")
	c.Providers.Proxmox.BootstrapConfigSecretKey = getenv("PROXMOX_BOOTSTRAP_CONFIG_SECRET_KEY", "config.yaml")
	c.Providers.Proxmox.BootstrapAdminSecretKey = getenv("PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY", "proxmox-admin.yaml")

	// --- Kind backup/restore (lines 391-397, 476-478) ---
	c.BootstrapKindBackupNamespaces = getenv("BOOTSTRAP_KIND_BACKUP_NAMESPACES", "")
	c.BootstrapKindBackupOut = getenv("BOOTSTRAP_KIND_BACKUP_OUT", "")
	c.BootstrapKindBackupEncrypt = getenv("BOOTSTRAP_KIND_BACKUP_ENCRYPT", "auto")
	c.BootstrapKindBackupPassphrase = getenv("BOOTSTRAP_KIND_BACKUP_PASSPHRASE", "")
	c.BootstrapKindStateOp = getenv("BOOTSTRAP_KIND_STATE_OP", "")
	c.BootstrapKindStatePath = getenv("BOOTSTRAP_KIND_STATE_PATH", "")

	// --- CAPI providers (lines 399-415) ---
	c.InfraProvider = getenv("INFRA_PROVIDER", "proxmox")
	c.IPAMProvider = getenv("IPAM_PROVIDER", "in-cluster")
	c.CAPMOXRepo = getenv("CAPMOX_REPO", "https://github.com/ionos-cloud/cluster-api-provider-proxmox.git")
	c.CAPMOXImageRepo = getenv("CAPMOX_IMAGE_REPO", "ghcr.io/ionos-cloud/cluster-api-provider-proxmox")
	c.CAPMOXBuildDir = getenv("CAPMOX_BUILD_DIR", "./cluster-api-provider-proxmox")
	c.CAPMOXVersion = getenv("CAPMOX_VERSION", "v0.8.1")
	c.CAPICoreRepo = getenv("CAPI_CORE_REPO", "https://github.com/kubernetes-sigs/cluster-api.git")
	c.CAPICoreImage = getenv("CAPI_CORE_IMAGE", "registry.k8s.io/cluster-api/cluster-api-controller:"+c.ClusterctlVersion)
	c.CAPIBootstrapImage = getenv("CAPI_BOOTSTRAP_IMAGE", "registry.k8s.io/cluster-api/kubeadm-bootstrap-controller:"+c.ClusterctlVersion)
	c.CAPIControlplaneImage = getenv("CAPI_CONTROLPLANE_IMAGE", "registry.k8s.io/cluster-api/kubeadm-control-plane-controller:"+c.ClusterctlVersion)
	c.IPAMImage = getenv("IPAM_IMAGE", "registry.k8s.io/capi-ipam-ic/cluster-api-ipam-in-cluster-controller:v1.0.3")
	c.IPAMRepo = getenv("IPAM_REPO", "https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster.git")

	// --- Clusterctl experimental (lines 671-673) ---
	c.ExpClusterResourceSet = getenv("EXP_CLUSTER_RESOURCE_SET", "false")
	c.ClusterTopology = getenv("CLUSTER_TOPOLOGY", "true")
	c.ExpKubeadmBootstrapFormatIgnition = getenv("EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION", "true")

	// --- metrics-server (lines 433-442) ---
	c.EnableMetricsServer = envBool("ENABLE_METRICS_SERVER", true)
	c.EnableWorkloadMetricsServer = envBool("ENABLE_WORKLOAD_METRICS_SERVER", true)
	c.WorkloadMetricsServerInsecureTLS = getenv("WORKLOAD_METRICS_SERVER_INSECURE_TLS", "true")
	c.MetricsServerManifestURL = getenv("METRICS_SERVER_MANIFEST_URL", "https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml")
	c.MetricsServerGitChartTag = getenv("METRICS_SERVER_GIT_CHART_TAG", "v0.7.2")

	// --- Proxmox CSI (lines 481-496, 617) ---
	c.Providers.Proxmox.CSIEnabled = envBool("PROXMOX_CSI_ENABLED", true)
	c.Providers.Proxmox.CSISmokeEnabled = envBool("PROXMOX_CSI_SMOKE_ENABLED", true)
	c.Providers.Proxmox.CSIChartRepoURL = getenv("PROXMOX_CSI_CHART_REPO_URL", "oci://ghcr.io/sergelogvinov/charts")
	c.Providers.Proxmox.CSIChartName = getenv("PROXMOX_CSI_CHART_NAME", "proxmox-csi-plugin")
	c.Providers.Proxmox.CSIChartVersion = getenv("PROXMOX_CSI_CHART_VERSION", "0.5.7")
	c.Providers.Proxmox.CSINamespace = getenv("PROXMOX_CSI_NAMESPACE", "csi-proxmox")
	c.Providers.Proxmox.CSIConfigProvider = getenv("PROXMOX_CSI_CONFIG_PROVIDER", "proxmox")
	c.Providers.Proxmox.CSITopologyLabels = getenv("PROXMOX_CSI_TOPOLOGY_LABELS", "true")

	// --- Argo workload postsync ---
	c.ArgoWorkloadPostsyncHooksEnabled = envBool("ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED", true)
	// bash uses "${VAR-default}" (keep-empty) — we preserve that: only fall
	// back when the env var is truly unset.
	c.ArgoWorkloadPostsyncHooksGitURL = getenvKeep("ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL", "https://github.com/lpasquali/workload-smoketests.git")
	c.ArgoWorkloadPostsyncHooksGitPath = getenvKeep("ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH", "")
	c.ArgoWorkloadPostsyncHooksGitRef = getenvKeep("ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF", "")
	c.ArgoWorkloadPostsyncHooksKubectlImg = getenv("ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE", "")
	c.WorkloadPostsyncNamespace = getenv("WORKLOAD_POSTSYNC_NAMESPACE", "workload-smoke")

	// --- Kyverno (lines 497-503) ---
	c.KyvernoEnabled = envBool("KYVERNO_ENABLED", true)
	c.KyvernoChartVersion = getenv("KYVERNO_CHART_VERSION", "3.7.1")
	c.KyvernoChartRepoURL = getenv("KYVERNO_CHART_REPO_URL", "https://kyverno.github.io/kyverno/")
	c.KyvernoNamespace = getenv("KYVERNO_NAMESPACE", "kyverno")
	c.KyvernoTolerateControlPlane = getenv("KYVERNO_TOLERATE_CONTROL_PLANE", "true")

	// --- cert-manager (lines 504-508) ---
	c.CertManagerEnabled = envBool("CERT_MANAGER_ENABLED", true)
	c.CertManagerChartVersion = getenv("CERT_MANAGER_CHART_VERSION", "v1.20.2")
	c.CertManagerChartRepoURL = getenv("CERT_MANAGER_CHART_REPO_URL", "https://charts.jetstack.io")
	c.CertManagerNamespace = getenv("CERT_MANAGER_NAMESPACE", "cert-manager")

	// --- Crossplane ---
	c.CrossplaneEnabled = envBool("CROSSPLANE_ENABLED", true)
	c.CrossplaneChartVersion = getenv("CROSSPLANE_CHART_VERSION", "2.2.1")
	c.CrossplaneChartRepoURL = getenv("CROSSPLANE_CHART_REPO_URL", "https://charts.crossplane.io/stable")
	c.CrossplaneNamespace = getenv("CROSSPLANE_NAMESPACE", "crossplane-system")

	// --- CNPG ---
	c.CNPGEnabled = envBool("CNPG_ENABLED", true)
	c.CNPGChartVersion = getenv("CNPG_CHART_VERSION", "")
	c.CNPGChartRepoURL = getenv("CNPG_CHART_REPO_URL", "https://cloudnative-pg.github.io/charts")
	c.CNPGChartName = getenv("CNPG_CHART_NAME", "cloudnative-pg")
	c.CNPGNamespace = getenv("CNPG_NAMESPACE", "cnpg-system")

	// --- External-Secrets ---
	c.ExternalSecretsEnabled = envBool("EXTERNAL_SECRETS_ENABLED", true)
	c.ExternalSecretsChartRepoURL = getenv("EXTERNAL_SECRETS_CHART_REPO_URL", "https://charts.external-secrets.io")
	c.ExternalSecretsChartVersion = getenv("EXTERNAL_SECRETS_CHART_VERSION", "")
	c.ExternalSecretsNamespace = getenv("EXTERNAL_SECRETS_NAMESPACE", "external-secrets-system")

	// --- Infisical ---
	c.InfisicalOperatorEnabled = envBool("INFISICAL_OPERATOR_ENABLED", true)
	c.InfisicalChartRepoURL = getenv("INFISICAL_CHART_REPO_URL", "https://dl.cloudsmith.io/public/infisical/helm-charts/helm/charts/")
	c.InfisicalChartName = getenv("INFISICAL_CHART_NAME", "secrets-operator")
	c.InfisicalChartVersion = getenv("INFISICAL_CHART_VERSION", "")
	c.InfisicalNamespace = getenv("INFISICAL_NAMESPACE", "infisical-system")

	// --- SPIRE ---
	c.SPIREEnabled = envBool("SPIRE_ENABLED", true)
	c.SPIREChartRepoURL = getenv("SPIRE_CHART_REPO_URL", "https://spiffe.github.io/helm-charts-hardened")
	c.SPIREChartName = getenv("SPIRE_CHART_NAME", "spire")
	c.SPIREChartVersion = getenv("SPIRE_CHART_VERSION", "0.28.4")
	c.SPIRECRDsChartName = getenv("SPIRE_CRDS_CHART_NAME", "spire-crds")
	c.SPIRECRDsChartVersion = getenv("SPIRE_CRDS_CHART_VERSION", "0.5.0")
	c.SPIREHelmEnableGlobalHooks = getenv("SPIRE_HELM_ENABLE_GLOBAL_HOOKS", "false")
	c.SPIRENamespace = getenv("SPIRE_NAMESPACE", "spire")
	c.SPIREOIDCInsecureHTTP = getenv("SPIRE_OIDC_INSECURE_HTTP", "true")
	c.SPIREOIDCBundleSource = getenv("SPIRE_OIDC_BUNDLE_SOURCE", "CSI")
	c.SPIRETolerateControlPlane = getenv("SPIRE_TOLERATE_CONTROL_PLANE", "true")

	// --- OTEL ---
	c.OTELEnabled = envBool("OTEL_ENABLED", true)
	c.OTELChartRepoURL = getenv("OTEL_CHART_REPO_URL", "https://open-telemetry.github.io/opentelemetry-helm-charts")
	c.OTELChartName = getenv("OTEL_CHART_NAME", "opentelemetry-collector")
	c.OTELChartVersion = getenv("OTEL_CHART_VERSION", "0.152.0")
	c.OTELImageRepository = getenv("OTEL_IMAGE_REPOSITORY", "otel/opentelemetry-collector-k8s")
	c.OTELCollectorMode = getenv("OTEL_COLLECTOR_MODE", "deployment")
	c.OTELNamespace = getenv("OTEL_NAMESPACE", "opentelemetry")

	// --- Grafana ---
	c.GrafanaEnabled = envBool("GRAFANA_ENABLED", true)
	c.GrafanaChartRepoURL = getenv("GRAFANA_CHART_REPO_URL", "https://grafana.github.io/helm-charts")
	c.GrafanaChartVersion = getenv("GRAFANA_CHART_VERSION", "")
	c.GrafanaNamespace = getenv("GRAFANA_NAMESPACE", "grafana")

	// --- VictoriaMetrics ---
	c.VictoriaMetricsEnabled = envBool("VICTORIAMETRICS_ENABLED", true)
	c.VictoriaMetricsChartRepoURL = getenv("VICTORIAMETRICS_CHART_REPO_URL", "https://victoriametrics.github.io/helm-charts/")
	c.VictoriaMetricsChartName = getenv("VICTORIAMETRICS_CHART_NAME", "victoria-metrics-single")
	c.VictoriaMetricsChartVersion = getenv("VICTORIAMETRICS_CHART_VERSION", "")
	c.VictoriaMetricsNamespace = getenv("VICTORIAMETRICS_NAMESPACE", "victoria-metrics")

	// --- Backstage ---
	c.BackstageEnabled = envBool("BACKSTAGE_ENABLED", false)
	c.BackstageChartRepoURL = getenv("BACKSTAGE_CHART_REPO_URL", "")
	c.BackstageChartName = getenv("BACKSTAGE_CHART_NAME", "backstage")
	c.BackstageChartVersion = getenv("BACKSTAGE_CHART_VERSION", "")
	c.BackstageNamespace = getenv("BACKSTAGE_NAMESPACE", "backstage")

	// --- Keycloak ---
	c.KeycloakEnabled = envBool("KEYCLOAK_ENABLED", true)
	c.KeycloakChartRepoURL = getenv("KEYCLOAK_CHART_REPO_URL", "https://codecentric.github.io/helm-charts")
	c.KeycloakChartName = getenv("KEYCLOAK_CHART_NAME", "keycloakx")
	c.KeycloakChartVersion = getenv("KEYCLOAK_CHART_VERSION", "")
	c.KeycloakNamespace = getenv("KEYCLOAK_NAMESPACE", "keycloak")
	c.KeycloakKcHostnameStrict = getenv("KEYCLOAK_KC_HOSTNAME_STRICT", "false")
	c.KeycloakKcHostname = getenv("KEYCLOAK_KC_HOSTNAME", "")
	c.KeycloakKcDB = getenv("KEYCLOAK_KC_DB", "")
	c.KeycloakOperatorEnabled = envBool("KEYCLOAK_OPERATOR_ENABLED", false)
	c.KeycloakOperatorGitURL = getenv("KEYCLOAK_OPERATOR_GIT_URL", "")
	c.KeycloakOperatorGitPath = getenv("KEYCLOAK_OPERATOR_GIT_PATH", ".")
	c.KeycloakOperatorGitRef = getenv("KEYCLOAK_OPERATOR_GIT_REF", "main")
	c.KeycloakOperatorNS = getenv("KEYCLOAK_OPERATOR_NAMESPACE", "keycloak-realm-operator")

	// --- Proxmox bootstrap / identities ---
	c.ClusterctlCfg = getenv("CLUSTERCTL_CFG", "")
	c.Providers.Proxmox.AdminConfig = getenv("PROXMOX_ADMIN_CONFIG", "")
	c.Providers.Proxmox.CSIConfig = getenv("PROXMOX_CSI_CONFIG", "")
	c.Providers.Proxmox.BootstrapSecretNamespace = getenv("PROXMOX_BOOTSTRAP_SECRET_NAMESPACE", "proxmox-bootstrap-system")
	c.Providers.Proxmox.BootstrapSecretName = getenv("PROXMOX_BOOTSTRAP_SECRET_NAME", "")
	c.Providers.Proxmox.BootstrapCAPMOXSecretName = getenv("PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME", "proxmox-bootstrap-capmox-credentials")
	c.Providers.Proxmox.BootstrapCSISecretName = getenv("PROXMOX_BOOTSTRAP_CSI_SECRET_NAME", "proxmox-bootstrap-csi-credentials")
	c.Providers.Proxmox.BootstrapAdminSecretName = getenv("PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME", "proxmox-bootstrap-admin-credentials")
	c.Providers.Proxmox.BootstrapKindSecretUsed = envBool("PROXMOX_BOOTSTRAP_KIND_SECRET_USED", false)
	c.Providers.Proxmox.KindCAPMOXActive = envBool("PROXMOX_KIND_CAPMOX_CREDENTIALS_ACTIVE", false)
	c.Providers.Proxmox.IdentityTF = getenv("PROXMOX_IDENTITY_TF", "proxmox-identity.tf")
	c.Providers.Proxmox.AdminInsecure = getenv("PROXMOX_ADMIN_INSECURE", "true")
	c.ClusterSetID = getenv("CLUSTER_SET_ID", "")
	c.Providers.Proxmox.RecreateIdentities = envBool("RECREATE_PROXMOX_IDENTITIES", false)
	c.Providers.Proxmox.IdentityRecreateScope = getenv("PROXMOX_IDENTITY_RECREATE_SCOPE", "both")
	c.Providers.Proxmox.IdentityRecreateStateRm = envBool("PROXMOX_IDENTITY_RECREATE_STATE_RM", false)
	c.Providers.Proxmox.IdentitySuffix = getenv("PROXMOX_IDENTITY_SUFFIX", "")

	// --- Proxmox core ---
	c.Providers.Proxmox.URL = getenv("PROXMOX_URL", "")
	c.Providers.Proxmox.Token = getenv("PROXMOX_TOKEN", "")
	c.Providers.Proxmox.Secret = getenv("PROXMOX_SECRET", "")
	c.Providers.Proxmox.AdminUsername = getenv("PROXMOX_ADMIN_USERNAME", "root@pam!capi-bootstrap")
	c.Providers.Proxmox.AdminToken = getenv("PROXMOX_ADMIN_TOKEN", "")
	c.Providers.Proxmox.Region = getenv("PROXMOX_REGION", "")
	c.Providers.Proxmox.Node = getenv("PROXMOX_NODE", "")
	c.Providers.Proxmox.SourceNode = getenv("PROXMOX_SOURCENODE", "")
	c.Providers.Proxmox.TopologyRegion = getenv("PROXMOX_TOPOLOGY_REGION", "")
	c.Providers.Proxmox.TopologyZone = getenv("PROXMOX_TOPOLOGY_ZONE", "")
	c.Providers.Proxmox.TemplateID = getenv("PROXMOX_TEMPLATE_ID", getenv("TEMPLATE_VMID", "104"))
	c.Providers.Proxmox.Bridge = getenv("PROXMOX_BRIDGE", "vmbr0")

	// --- Network ---
	c.ControlPlaneEndpointIP = getenv("CONTROL_PLANE_ENDPOINT_IP", "192.168.0.20")
	c.ControlPlaneEndpointPort = getenv("CONTROL_PLANE_ENDPOINT_PORT", "6443")
	c.NodeIPRanges = getenv("NODE_IP_RANGES", "192.168.0.21-192.168.0.30")
	c.Gateway = getenv("GATEWAY", "192.168.0.1")
	c.IPPrefix = getenv("IP_PREFIX", "24")
	c.DNSServers = getenv("DNS_SERVERS", "8.8.8.8,8.8.4.4")
	c.DNSServersExplicit = envBool("DNS_SERVERS_EXPLICIT", false)
	c.GatewayExplicit = envBool("GATEWAY_EXPLICIT", false)
	c.IPPrefixExplicit = envBool("IP_PREFIX_EXPLICIT", false)
	c.NodeIPRangesExplicit = envBool("NODE_IP_RANGES_EXPLICIT", false)
	c.AllowedNodesExplicit = envBool("ALLOWED_NODES_EXPLICIT", false)
	c.AllowedNodes = getenv("ALLOWED_NODES", c.Providers.Proxmox.Node)
	c.VMSSHKeys = getenv("VM_SSH_KEYS", "")

	// --- Proxmox CSI credentials/storage ---
	c.Providers.Proxmox.CSIURL = getenv("PROXMOX_CSI_URL", "")
	c.Providers.Proxmox.CSITokenID = getenv("PROXMOX_CSI_TOKEN_ID", "")
	c.Providers.Proxmox.CSITokenSecret = getenv("PROXMOX_CSI_TOKEN_SECRET", "")
	c.Providers.Proxmox.CSIUserID = getenv("PROXMOX_CSI_USER_ID", "")
	c.Providers.Proxmox.CSITokenPrefix = getenv("PROXMOX_CSI_TOKEN_PREFIX", "csi")
	c.Providers.Proxmox.CSIInsecure = getenv("PROXMOX_CSI_INSECURE", c.Providers.Proxmox.AdminInsecure)
	c.Providers.Proxmox.CSIStorageClassName = getenv("PROXMOX_CSI_STORAGE_CLASS_NAME", "proxmox-data-xfs")
	c.Providers.Proxmox.CSIStorage = getenv("PROXMOX_CSI_STORAGE", "local-lvm")
	c.Providers.Proxmox.CloudinitStorage = getenv("PROXMOX_CLOUDINIT_STORAGE", "local")
	c.Providers.Proxmox.MemoryAdjustment = getenv("PROXMOX_MEMORY_ADJUSTMENT", "0")
	c.Providers.Proxmox.CSIReclaimPolicy = getenv("PROXMOX_CSI_RECLAIM_POLICY", "Delete")
	c.Providers.Proxmox.CSIFsType = getenv("PROXMOX_CSI_FSTYPE", "xfs")
	c.Providers.Proxmox.CSIDefaultClass = getenv("PROXMOX_CSI_DEFAULT_CLASS", "true")
	c.Providers.Proxmox.CAPIUserID = getenv("PROXMOX_CAPI_USER_ID", "")
	c.Providers.Proxmox.CAPITokenPrefix = getenv("PROXMOX_CAPI_TOKEN_PREFIX", "capi")

	// --- VM sizing (Proxmox-only — see ProxmoxConfig) ---
	c.Providers.Proxmox.ControlPlaneBootVolumeDevice = getenv("CONTROL_PLANE_BOOT_VOLUME_DEVICE", "scsi0")
	c.Providers.Proxmox.ControlPlaneBootVolumeSize = getenv("CONTROL_PLANE_BOOT_VOLUME_SIZE", "40")
	// Bare-minimum kubeadm CP sizing: etcd + apiserver + controller-mgr +
	// scheduler + Cilium fit comfortably in 2 vCPU / 4 GiB. Larger
	// workloads should bump these; --bootstrap-mode k3s targets even
	// smaller envs (1 vCPU / 1 GiB).
	c.Providers.Proxmox.ControlPlaneNumSockets = getenv("CONTROL_PLANE_NUM_SOCKETS", "1")
	c.Providers.Proxmox.ControlPlaneNumCores = getenv("CONTROL_PLANE_NUM_CORES", "2")
	c.Providers.Proxmox.ControlPlaneMemoryMiB = getenv("CONTROL_PLANE_MEMORY_MIB", "4096")
	c.Providers.Proxmox.WorkerBootVolumeDevice = getenv("WORKER_BOOT_VOLUME_DEVICE", "scsi0")
	c.Providers.Proxmox.WorkerBootVolumeSize = getenv("WORKER_BOOT_VOLUME_SIZE", "40")
	// Bare-minimum kubeadm worker sizing: kubelet + kube-proxy +
	// Cilium agent fit in 2 vCPU / 4 GiB; remaining pods compete for
	// what's left. Bump for larger workloads.
	c.Providers.Proxmox.WorkerNumSockets = getenv("WORKER_NUM_SOCKETS", "1")
	c.Providers.Proxmox.WorkerNumCores = getenv("WORKER_NUM_CORES", "2")
	c.Providers.Proxmox.WorkerMemoryMiB = getenv("WORKER_MEMORY_MIB", "4096")

	// Per-machine-type template overrides; empty → fall back to Providers.Proxmox.TemplateID.
	c.WorkloadControlPlaneTemplateID = getenv("WORKLOAD_CONTROL_PLANE_TEMPLATE_ID", "")
	c.WorkloadWorkerTemplateID = getenv("WORKLOAD_WORKER_TEMPLATE_ID", "")
	c.Providers.Proxmox.Mgmt.ControlPlaneTemplateID = getenv("MGMT_CONTROL_PLANE_TEMPLATE_ID", "")
	c.Providers.Proxmox.Mgmt.WorkerTemplateID = getenv("MGMT_WORKER_TEMPLATE_ID", "")

	// --- Workload cluster ---
	c.WorkloadClusterName = getenv("WORKLOAD_CLUSTER_NAME", "capi-quickstart")
	c.WorkloadCiliumClusterID = getenv("WORKLOAD_CILIUM_CLUSTER_ID", "")
	c.WorkloadClusterNamespace = getenv("WORKLOAD_CLUSTER_NAMESPACE", "default")
	c.WorkloadClusterNameExplicit = envBoolLoose("WORKLOAD_CLUSTER_NAME_EXPLICIT", false)
	c.WorkloadClusterNamespaceExplicit = envBoolLoose("WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT", false)
	c.WorkloadKubernetesVersion = getenv("WORKLOAD_KUBERNETES_VERSION", "v1.35.0")
	c.ControlPlaneMachineCount = getenv("CONTROL_PLANE_MACHINE_COUNT", "1")
	c.WorkerMachineCount = getenv("WORKER_MACHINE_COUNT", "2")

	// --- Pivot orchestration toggles ---
	c.PivotEnabled = envBool("PIVOT_ENABLED", false)
	c.PivotKeepKind = envBool("PIVOT_KEEP_KIND", false)
	c.PivotDryRun = envBool("PIVOT_DRY_RUN", false)
	c.PivotVerifyTimeout = getenv("PIVOT_VERIFY_TIMEOUT", "10m")

	// --- Management cluster shape (universal) ---
	c.Mgmt.ClusterName = getenv("MGMT_CLUSTER_NAME", "capi-management")
	c.Mgmt.ClusterNamespace = getenv("MGMT_CLUSTER_NAMESPACE", "default")
	c.Mgmt.KubernetesVersion = getenv("MGMT_KUBERNETES_VERSION", c.WorkloadKubernetesVersion)
	c.Mgmt.CiliumClusterID = getenv("MGMT_CILIUM_CLUSTER_ID", "")
	c.Mgmt.ControlPlaneMachineCount = getenv("MGMT_CONTROL_PLANE_MACHINE_COUNT", "1")
	c.Mgmt.WorkerMachineCount = getenv("MGMT_WORKER_MACHINE_COUNT", "0")
	c.Mgmt.ControlPlaneEndpointIP = getenv("MGMT_CONTROL_PLANE_ENDPOINT_IP", "")
	c.Mgmt.ControlPlaneEndpointPort = getenv("MGMT_CONTROL_PLANE_ENDPOINT_PORT", c.ControlPlaneEndpointPort)
	c.Mgmt.NodeIPRanges = getenv("MGMT_NODE_IP_RANGES", "")
	// Cilium on the management cluster: Hubble on, LB-IPAM off — no
	// LoadBalancer Services run on a stateless single-node mgmt.
	c.Mgmt.CiliumHubble = getenv("MGMT_CILIUM_HUBBLE", "true")
	c.Mgmt.CiliumLBIPAM = getenv("MGMT_CILIUM_LB_IPAM", "false")
	c.Mgmt.CAPIManifest = getenv("MGMT_CAPI_MANIFEST", "")

	// --- Management cluster (Proxmox-only sizing / pool / CSI) ---
	// Leaner than workload defaults because the mgmt cluster only carries
	// CAPI controllers + bootstrap state.
	c.Providers.Proxmox.Mgmt.ControlPlaneNumSockets = getenv("MGMT_CONTROL_PLANE_NUM_SOCKETS", "1")
	c.Providers.Proxmox.Mgmt.ControlPlaneNumCores = getenv("MGMT_CONTROL_PLANE_NUM_CORES", "2")
	c.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB = getenv("MGMT_CONTROL_PLANE_MEMORY_MIB", "2048")
	c.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeDevice = getenv("MGMT_CONTROL_PLANE_BOOT_VOLUME_DEVICE", c.Providers.Proxmox.ControlPlaneBootVolumeDevice)
	c.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize = getenv("MGMT_CONTROL_PLANE_BOOT_VOLUME_SIZE", "30")
	// Proxmox CSI on the management cluster: off by default (stateless).
	c.Providers.Proxmox.Mgmt.CSIEnabled = envBool("MGMT_PROXMOX_CSI_ENABLED", false)

	// Pool defaults to the matching cluster name so each cluster
	// gets its own organizational bucket. User can override or set empty.
	c.Providers.Proxmox.Pool = getenv("PROXMOX_POOL", c.WorkloadClusterName)
	c.Providers.Proxmox.Mgmt.Pool = getenv("MGMT_PROXMOX_POOL", c.Mgmt.ClusterName)

	return c
}

// --- helpers ---

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// getenvKeep honours ${VAR-default} semantics: fallback only when the var is
// unset. If it's set (even to an empty string) we keep the empty string.
func getenvKeep(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// envFloat parses a float from $key (e.g. "0.75"); returns def on
// missing/empty/parse-error.
func envFloat(key string, def float64) float64 {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return f
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch v {
	case "true", "1", "yes", "y", "on", "TRUE":
		return true
	case "false", "0", "no", "n", "off", "FALSE":
		return false
	}
	return def
}

// envBoolLoose accepts "1"/"0" for *_EXPLICIT flags the bash script uses as
// counters/booleans (lines 666-667).
func envBoolLoose(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	case "0", "false", "FALSE", "no", "off":
		return false
	}
	return def
}
