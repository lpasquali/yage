// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package config holds every tunable variable yage exposes, with
// env-var overrides and defaults. One struct is shared by every
// other package: subsystems read from *Config; they never reach
// into os.Getenv directly (the one exception is boot-time Load,
// below).
//
// Naming convention: each Go field is UpperCamelCase, with
// _EXPLICIT suffixed flags kept as <Name>Explicit.
//
// Default-value helpers: getenv(..., "default") for the
// "missing-or-empty → default" semantics; getenvKeep(..., "default")
// to preserve empty-string overrides.
package config

import (
	"os"
	"strconv"
	"strings"

	syskeyring "github.com/lpasquali/yage/internal/platform/keyring"
)

// WorkloadShape is the user-stated product description consumed by the
// §23 feasibility gate: how many apps of which template, database
// size, egress budget, resilience, environment. The TUI (§22) and any
// future YAML-loaded shape both populate this struct; the feasibility
// gate then projects it into a minimum-viable cluster footprint and
// compares against per-provider live pricing or on-prem inventory.
//
// All fields are optional today: a zero-value WorkloadShape causes
// feasibility.Check to return ErrNotApplicable so existing
// command-line flows that don't yet describe the product (the
// Phase-A/B/C plumbing still drives off the explicit machine counts /
// sizing fields) pass through unchanged.
type WorkloadShape struct {
	// Apps is the list of app groups (count × template). e.g.
	// `[{6, "medium"}, {2, "heavy"}]` reads as "6 medium apps + 2
	// heavy apps". Order is preserved for display purposes only —
	// the feasibility math sums into a single `total cores / total
	// memory` figure regardless of order.
	Apps []AppGroup
	// DatabaseGB is the total persistent volume size the workload's
	// database needs. Drives DB compute heuristics (db_cores =
	// max(2, db_GB/50); db_mem = max(2 GiB, db_GB × 100 MiB)) and
	// IOPS-floor checks.
	DatabaseGB int
	// EgressGBMonth is monthly outbound internet traffic (DB-to-user
	// patterns, image serving, etc.). 0 = "user did not state an
	// estimate"; feasibility surfaces a warning instead of silently
	// assuming zero (the §23.6 sandbag-defense path). The xapiri TUI
	// makes this field required (default suggestion = DatabaseGB × 2).
	EgressGBMonth int
	// Resilience picks the redundancy tier:
	//   - "single"  → 1 control-plane node, no anti-affinity
	//   - "ha"      → 3 control-plane nodes, anti-affinity within zone
	//   - "ha-mr"   → 3+ CP nodes spread across regions
	// Empty string treated as "single" (default).
	Resilience string
	// Environment is the sibling axis to Resilience:
	//   - "dev"     → minimal addons, no Argo, no monitoring
	//   - "staging" → Argo CD + light monitoring
	//   - "prod"    → Argo CD HA + full monitoring + backups
	// Empty string treated as "dev" (default).
	Environment string
	// HasQueue / HasObjStore / HasCache record whether the operator
	// opted into each add-on in the xapiri walkthrough. Used to
	// pre-populate the add-on prompts on subsequent runs (so the
	// [y/N] default reflects the saved choice).
	HasQueue    bool
	HasObjStore bool
	HasCache    bool
}

// AppGroup is one entry in WorkloadShape.Apps. The combination
// (count, template) describes a homogeneous group of pods; the
// feasibility gate sums every group's Count × template's
// (cores, memMiB) into the workload's total compute requirement.
type AppGroup struct {
	// Count is the number of pod replicas in this group.
	Count int
	// Template names the sizing preset used for this group. Valid
	// values: "light" (100m / 128 MiB), "medium" (200m / 256 MiB),
	// "heavy" (500m / 1024 MiB), or "custom" (use CustomCores +
	// CustomMemMiB). Empty string treated as "medium".
	Template string
	// CustomCores / CustomMemMiB override the named template's CPU
	// + memory request when Template == "custom". CustomCores is
	// in millicores (e.g. 750 = 0.75 cores). Ignored otherwise.
	CustomCores  int
	CustomMemMiB int64
}

// MgmtConfig holds management-cluster shape that every provider needs:
// names, K8s version, replica counts, control-plane endpoint, Cilium add-on
// toggles, and the rendered CAPI manifest. Provider-specific bits (Proxmox
// VM sizing, template IDs, pool, CSI) live in ProxmoxMgmtConfig under
// cfg.Providers.Proxmox.Mgmt.
type MgmtConfig struct {
	ClusterName               string
	ClusterNameExplicit       bool
	ClusterNamespace          string
	ClusterNamespaceExplicit  bool
	KubernetesVersion         string
	KubernetesVersionExplicit bool
	CiliumClusterID           string
	ControlPlaneMachineCount  string // "1" by default (single-node mgmt)
	WorkerMachineCount        string // "0" by default (CP-only)
	ControlPlaneEndpointIP    string // 1 VIP — user-provided
	ControlPlaneEndpointPort  string
	NodeIPRanges              string // 2-IP range — user-provided
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
	WorkerNumSockets             string // "1"
	WorkerNumCores               string // "2"
	WorkerMemoryMiB              string // "2048"
	WorkerBootVolumeDevice       string
	WorkerBootVolumeSize         string // "30"
	WorkerTemplateID             string
	// Pool is the Proxmox VE pool name the management cluster's VMs are
	// tagged with. See ProxmoxConfig.Pool for the workload counterpart.
	Pool         string
	PoolExplicit bool
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
	Azure        AzureConfig
	GCP          GCPConfig
	Hetzner      HetznerConfig
	OpenStack    OpenStackConfig
	Vsphere      VsphereConfig
	DigitalOcean DigitalOceanConfig
	Linode       LinodeConfig
	OCI          OCIConfig
	IBMCloud     IBMCloudConfig
}

// OpenStackConfig is the per-provider OpenStack (CAPO) configuration.
// OpenStack is the second flat-quota cloud after Proxmox (§13's
// OpenStack validation), so it's a candidate for a future real
// Inventory implementation. Today's surface covers the fields CAPO
// reads via clouds.yaml + the ones xapiri prompts for.
type OpenStackConfig struct {
	// Cloud is the named cloud entry in clouds.yaml (e.g. "devstack").
	Cloud string
	// ProjectName is the Keystone project / tenant.
	ProjectName string
	// Region scopes resource queries (e.g. "RegionOne").
	Region string
	// FailureDomain hints CAPO where to place control-plane VMs
	// (typically an availability zone).
	FailureDomain string
	// ImageName names the Glance image used for VM boot.
	ImageName string
	// ControlPlaneFlavor / WorkerFlavor are Nova flavor names
	// (e.g. "m1.large").
	ControlPlaneFlavor string
	WorkerFlavor       string
	// DNSNameservers is a comma-separated list pushed to subnet
	// resources.
	DNSNameservers string
	// SSHKeyName is the Nova keypair name CAPO injects via cloud-init.
	SSHKeyName string
}

// VsphereConfig is the per-provider vSphere (CAPV) configuration.
// Sketch from §13's vSphere validation report — these are the
// fields CAPV's manifest expects.
type VsphereConfig struct {
	Server        string // vCenter URL/host
	Datacenter    string
	Folder        string // VM folder under the datacenter
	ResourcePool  string // soft-quota tree
	Datastore     string
	Network       string // vSphere network name
	Template      string // VM template (governor) used for VM clones
	TLSThumbprint string // vCenter cert thumbprint when self-signed
	// Username / Password — operator-supplied via env (VSPHERE_USERNAME /
	// VSPHERE_PASSWORD); kept on cfg so xapiri can prompt and kindsync
	// can round-trip.
	Username string
	Password string

	// ---- Per-role VSphereMachineTemplate sizing ----
	//
	// These map onto the inline sizing fields in VSphereMachineTemplate
	// spec.template.spec: numCPUs, numCoresPerSocket, memoryMiB, diskGiB.
	// PatchManifest rewrites them post-render so the operator can override
	// defaults without editing the manifest by hand.
	// Env vars follow the VSPHERE_* namespace to stay consistent with the
	// rest of VsphereConfig.
	ControlPlaneNumCPUs           string // VSPHERE_CONTROL_PLANE_NUM_CPUS (e.g. "2")
	ControlPlaneNumCoresPerSocket string // VSPHERE_CONTROL_PLANE_NUM_CORES_PER_SOCKET (e.g. "1")
	ControlPlaneMemoryMiB         string // VSPHERE_CONTROL_PLANE_MEMORY_MIB (e.g. "4096")
	ControlPlaneDiskGiB           string // VSPHERE_CONTROL_PLANE_DISK_GIB (e.g. "25")
	WorkerNumCPUs                 string // VSPHERE_WORKER_NUM_CPUS (e.g. "2")
	WorkerNumCoresPerSocket       string // VSPHERE_WORKER_NUM_CORES_PER_SOCKET (e.g. "1")
	WorkerMemoryMiB               string // VSPHERE_WORKER_MEMORY_MIB (e.g. "4096")
	WorkerDiskGiB                 string // VSPHERE_WORKER_DISK_GIB (e.g. "25")
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
	// SubscriptionID / TenantID / ResourceGroup / VNetName / SubnetName /
	// ClientID — Azure-side state the workload manifest needs. CAPZ
	// reads these as env vars; yage surfaces them on cfg so xapiri /
	// kindsync can round-trip them. ClientID is part of the Service-
	// Principal / Managed-Identity identity model (see IdentityModel).
	SubscriptionID string
	TenantID       string
	ResourceGroup  string
	VNetName       string
	SubnetName     string
	ClientID       string
	// IdentityModel selects which Azure identity flavor yage / CAPZ uses
	// (§13.4 #4). One of:
	//   - "service-principal" (default; AZURE_CLIENT_ID + secret)
	//   - "managed-identity"  (User-Assigned MI on the bootstrap host)
	//   - "workload-identity" (AAD Workload Identity federation)
	// EnsureIdentity branches on this. Today's no-op
	// EnsureIdentity ignores it; lands when CAPZ identity bootstrap
	// is wired.
	IdentityModel string
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
	// Network names the VPC the workload cluster's nodes live in.
	// Empty = "default" (CAPG's auto-mode VPC). Surfaced for
	// TemplateVars (GCP_NETWORK_NAME).
	Network string
	// ImageFamily picks the GCE image family for node OS (debian-12,
	// ubuntu-2204-lts, etc.). Empty = CAPG default.
	ImageFamily string
	// IdentityModel selects which GCP identity flavor (§13.4 #4):
	//   - "service-account" (default; GOOGLE_APPLICATION_CREDENTIALS JSON)
	//   - "adc"             (Application Default Credentials)
	//   - "workload-identity" (GKE Workload Identity federation)
	// Same shape as Azure.IdentityModel; lands when CAPG identity
	// bootstrap is wired.
	IdentityModel string
}

// HetznerConfig is the per-provider Hetzner (CAPHV) configuration.
type HetznerConfig struct {
	// Token is the Hetzner Cloud project API token (env: HCLOUD_TOKEN).
	// CAPHV reads it directly from env; we surface it here for cross-
	// fill with cfg.Cost.Credentials.HetznerToken (same secret, two
	// consumers). See §16.
	Token string
	// ControlPlaneMachineType / NodeMachineType drive the Hetzner Cloud
	// server types CAPHV provisions when --infrastructure-provider
	// hetzner. Defaults to cx23 — cost-optimized successor to cx22
	// (Hetzner Cloud API; cx22 removed from catalog Jan 2026).
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
// API token comes from env DIGITALOCEAN_TOKEN — not from cfg (same as Hetzner
// reading HCLOUD_TOKEN directly from the CAPI controller pod env).
type DigitalOceanConfig struct {
	Region           string
	ControlPlaneSize string // s-2vcpu-4gb, s-4vcpu-8gb, ...
	NodeSize         string
	// VPCUUID optionally pins all droplets to a specific VPC. Empty = use
	// the region default VPC (CAPDO's behaviour when the field is absent).
	VPCUUID      string
	OverheadTier string // "dev" | "prod" | "enterprise" — mirrors AWS/Azure
}

// LinodeConfig is the per-provider Linode/Akamai (CAPL) configuration.
// Catalog is auth-free; provisioning needs LINODE_TOKEN from env.
type LinodeConfig struct {
	Region           string
	ControlPlaneType string // g6-standard-2, g6-standard-4, ...
	NodeType         string
	OverheadTier     string // "dev" | "prod" | "enterprise"
}

// OCIConfig is the per-provider Oracle Cloud Infrastructure (CAPOCI)
// configuration. Cost estimator JSON is auth-free; provisioning needs
// OCI API key credentials from env.
type OCIConfig struct {
	Region            string
	ControlPlaneShape string // VM.Standard.E4.Flex, ...
	NodeShape         string
	// Provisioning-time fields — not secrets. OCI private key stays on-disk;
	// only its path is tracked here (never the key material itself).
	TenancyOCID     string
	UserOCID        string
	Fingerprint     string
	CompartmentOCID string
	ImageID         string
	PrivateKeyPath  string
	OverheadTier    string // "dev" | "prod" | "enterprise"
}

// IBMCloudConfig is the per-provider IBM Cloud (CAPIBM) configuration.
// Both the Global Catalog (pricing) and provisioning need IBMCLOUD_API_KEY.
type IBMCloudConfig struct {
	Region              string
	ControlPlaneProfile string // bx2-2x8, cx2-4x8, ...
	NodeProfile         string
	ResourceGroup       string // IBM Cloud resource group name
	VPCName             string // existing VPC name; empty = CAPIBM creates one
	Zone                string // availability zone, e.g. us-south-1
	ImageID             string // VPC Gen2 image ID for worker nodes
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
	OverheadTier       string
	NATGatewayCount    string // overrides the tier default
	ALBCount           string
	NLBCount           string
	DataTransferGB     string // monthly egress estimate
	CloudWatchLogsGB   string
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
	CAPIToken      string
	CAPISecret     string
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
	Pool         string
	PoolExplicit bool

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
	CSIStorageClassName     string
	CSIMgmtStorageClassName string // PROXMOX_CSI_MGMT_STORAGE_CLASS, default ""
	CSIStorage              string
	CSIReclaimPolicy        string
	CSIFsType               string
	CSIDefaultClass         string

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

// CostConfig groups the cross-cutting cost-estimation configuration:
// API credentials for vendor pricing endpoints + currency / FX
// preferences. Per docs/abstraction-plan.md §16, these are
// orchestrator-owned (NOT per-provider) because --cost-compare runs
// every vendor regardless of which provider is the active
// INFRA_PROVIDER. The struct is the single in-process home so
// pricing/* fetchers stop calling os.Getenv directly and the
// xapiri TUI / kindsync Secret have one schema to populate.
type CostConfig struct {
	Credentials CostCredentials
	Currency    CostCurrency
}

// CostCredentials are the per-vendor pricing API tokens / keys.
// Azure Retail Prices, Linode catalog, OCI catalog are anonymous — no entry
// here. Values persist to yage-system/cost-compare-config Secret (see
// kindsync.WriteCostCompareSecret); env vars are a first-run fallback before
// the kind cluster exists.
// NOTE: AWS credentials intentionally have no env-var fallback — using
// AWS_ACCESS_KEY_ID / AWS_PROFILE from the operator's shell would silently
// price against the wrong account.
type CostCredentials struct {
	// AWS Pricing API. No ambient-credential fallback — must be set
	// explicitly via --cost-compare-config or the cost-compare-config Secret.
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	// GCP Cloud Billing Catalog (env: YAGE_GCP_API_KEY / GOOGLE_BILLING_API_KEY).
	GCPAPIKey string
	// Hetzner Cloud (env: YAGE_HCLOUD_TOKEN / HCLOUD_TOKEN). Same token also
	// serves cfg.Providers.Hetzner.Token; Load() cross-fills if either is empty.
	HetznerToken string
	// DigitalOcean (env: YAGE_DO_TOKEN / DIGITALOCEAN_TOKEN).
	DigitalOceanToken string
	// Linode/Akamai (env: YAGE_LINODE_TOKEN / LINODE_TOKEN).
	// Catalog calls are auth-free; token is only needed for provisioning.
	LinodeToken string
	// IBM Cloud (env: YAGE_IBMCLOUD_API_KEY / IBMCLOUD_API_KEY).
	IBMCloudAPIKey string
}

// CostCurrency holds locale / FX preferences for cost output. These
// aren't secrets but they belong with the rest of the cost-display
// configuration.
type CostCurrency struct {
	// DisplayCurrency forces output in a specific ISO currency code
	// (env: YAGE_TALLER_CURRENCY). Empty = auto-detect from
	// DataCenterLocation, then geo-IP, then USD fallback.
	DisplayCurrency string
	// DataCenterLocation is an ISO-3166 alpha-2 country code (e.g.
	// "IT", "DE", "US") set via --data-center-location or env
	// YAGE_DATA_CENTER_LOCATION. When set, it drives BOTH:
	//   - empty Region / Location fields on every provider (filled
	//     with the nearest centroid to the country's capital, same
	//     mechanism xapiri's geo path uses)
	//   - the active taller display currency (country → ISO
	//     currency via pricing.CountryCurrency)
	// Has higher priority than geo-IP detection and lower priority
	// than an explicit DisplayCurrency override.
	DataCenterLocation string
}

// CSIConfig holds the multi-driver CSI add-on selection (§20). The
// driver registry lives in internal/csi/; cfg.CSI is the operator-
// facing selection knob. Empty means "use the per-provider default
// from internal/csi.DefaultsFor(cfg.InfraProvider)".
//
// AWS-EBS, Azure-Disk, and GCP-PD drivers register on import. The
// same shape supports the rest of the §20.1 matrix as new drivers
// register themselves.
type CSIConfig struct {
	// Drivers is the ordered list of CSI driver names to install on
	// the workload cluster. Empty → use the provider's default set
	// (internal/csi.DefaultsFor(cfg.InfraProvider)). Names that
	// aren't registered are silently dropped with a logx warning so
	// a partial driver matrix doesn't break dry-run plans.
	//
	// Env: YAGE_CSI_DRIVERS (comma-separated). CLI: --csi-driver
	// <name> (repeatable; appends each occurrence).
	Drivers []string

	// DefaultClass picks which driver provides the cluster default
	// StorageClass when multiple drivers install. Empty → first
	// driver in Drivers (after registry-filtering) wins.
	//
	// Env: YAGE_CSI_DEFAULT_CLASS. CLI: --csi-default-class <name>.
	DefaultClass string
}

// Config holds every runtime tunable. Zero value is not meaningful — always
// call Load().
type Config struct {
	// Providers groups per-cloud configuration.
	Providers Providers
	// Cost groups cross-cutting cost-estimation configuration: vendor
	// pricing credentials + currency/FX preferences. See §16.
	Cost CostConfig
	// CSI groups multi-driver CSI add-on selection (§20). The
	// driver registry lives in internal/csi/.
	CSI CSIConfig
	// Workload describes the user-stated product shape (apps × template,
	// database GB, egress GB/month, resilience, environment) consumed by
	// the §23 feasibility gate. Today the xapiri TUI populates these
	// (§22 commit); legacy command-line flows leave them zero, in which
	// case feasibility.Check returns ErrNotApplicable and the existing
	// resource-budget check stays the only gate.
	Workload WorkloadShape
	// ArgoCD groups all Argo CD configuration: operator/server versions,
	// install toggles, access modes, GitOps repo coordinates, and
	// workload post-sync hook settings. See ArgoCDConfig for the field
	// roster.
	ArgoCD ArgoCDConfig
	// Capacity groups resource-budget and capacity-planning configuration:
	// host budget fraction, overcommit policy, and the system-apps CPU/
	// memory reserve. See CapacityConfig for the field roster.
	Capacity CapacityConfig
	// Pivot groups pivot-orchestration configuration: whether to pivot,
	// kind teardown policy, verify timeout, dry-run, and the
	// stop-before-workload escape hatch. See PivotConfig for the field
	// roster.
	Pivot PivotConfig

	// ---- Tool versions ----
	KindVersion       string
	KubectlVersion    string
	ClusterctlVersion string
	CiliumCLIVersion  string
	CiliumVersion     string
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

	// Workload GitOps (non-ArgoCD fields that remain at top-level)
	WorkloadGitopsMode        string
	WorkloadRolloutStandalone bool
	WorkloadRolloutMode       string
	WorkloadRolloutNoWait     bool

	// ---- Top-level flags ----
	Force                        bool
	NoDeleteKind                 bool
	BootstrapPersistLocalSecrets bool
	Purge                        bool
	BuildAll                     bool
	// DryRun, when true, makes Run() print a structured plan of what
	// every phase would do (based on the current cfg) and exit without
	// executing any phase. Distinct from PivotDryRun (which actually
	// provisions the mgmt cluster and stops at `clusterctl move`).
	DryRun bool
	// CostCompare, when true, makes the dry-run plan include a
	// cross-cloud comparison: same logical cluster shape evaluated
	// against every registered provider's EstimateMonthlyCostUSD,
	// with a per-cloud "if you spent this on storage" retention
	// column. Independent of cfg.InfraProvider — runs all of them.
	CostCompare bool
	// BudgetUSDMonth, when > 0, drives a retention calculation:
	// budget − compute = leftover; leftover ÷ block-storage $/GB-mo
	// = how much persistent volume capacity remains for
	// observability / DB buckets after the cluster is paid for.
	BudgetUSDMonth float64
	// PrintPricingSetup, when non-empty, makes the program print
	// the IAM/token setup snippet for the named vendor (or "all"
	// for every vendor that needs setup) and exit. Intended for
	// users who dismissed the first-run hint and want to see it
	// again. Empty string means "no special action".
	PrintPricingSetup string
	// Xapiri, when true, launches the interactive configuration TUI
	// (--xapiri) and exits. Mutually exclusive with the orchestrator
	// run; setting it short-circuits main() before orchestrator.Run.
	Xapiri bool
	// XapiriDeployNow is set by the xapiri walkthrough when the user
	// answers "deploy now? y" at step 8. main() reads it to decide
	// whether to fall through to orchestrator.Run after the walkthrough.
	XapiriDeployNow bool
	// PrintCommand, when non-empty, makes the program render the
	// equivalent `yage <flags>` invocation that reproduces the
	// resolved cfg, then exits. Useful for pipelines (capture the
	// canonical CLI form) and for periodic cost reports (re-run the
	// same flags against a fresh catalog). Allowed values:
	//   "env"     — sensitive values emit as $VAR refs (default)
	//   "raw"     — sensitive values inline (full reproducibility)
	//   "masked"  — sensitive values emit as ********
	PrintCommand string
	// ClearKeyring, when true, removes Proxmox credentials from the OS
	// keychain and exits. Set by the --clear-keyring flag.
	ClearKeyring bool
	// SkipProviders is a comma-separated list of registry names to
	// drop from the cost-compare table. Useful when the operator
	// has no interest in some clouds (e.g. SkipProviders="oci,ibmcloud"
	// hides those rows). The provider can still be picked as the
	// active --infra-provider; only the comparison view filters them.
	// Env: YAGE_SKIP_PROVIDERS.
	SkipProviders string
	// CostCompareEnabled, when true, enables live cost-estimation API
	// calls in --xapiri and --cost-compare. Set by the presence of the
	// yage-system/cost-compare-config Secret (loaded at xapiri startup)
	// or by the --cost-compare-config flag (which also triggers the
	// credential-setup step). When false, all live pricing calls are
	// suppressed and the dashboard right panel shows a placeholder.
	CostCompareEnabled bool
	// UseManagedPostgres controls whether the cluster relies on the
	// vendor's SaaS Postgres (RDS / Aurora / Cloud SQL / Azure DB for
	// PG / DO Managed DB / Linode Managed DB / OCI DB for PG / IBM
	// Cloud Databases for PostgreSQL) instead of the in-cluster
	// CloudNativePG operator. Defaults to true: when the active
	// vendor's managed-services matrix entry is true and the
	// workload signals NeedsPostgres, the orchestrator skips the
	// cnpg helm install and the cost line shows the SaaS price.
	// --no-managed-postgres opts back into in-cluster cnpg.
	// Env: YAGE_USE_MANAGED_POSTGRES.
	UseManagedPostgres bool
	// In-cluster substitute footprint overrides — one set per
	// service slot (Postgres / message queue / object storage /
	// in-memory cache). Empty / 0 means "use the default from
	// cost.SubstituteFootprint(svc)". Each pair of envs:
	//   YAGE_<SVC>_CPU_MILLICORES
	//   YAGE_<SVC>_MEMORY_MIB
	//   YAGE_<SVC>_VOLUME_GB
	// drives the forecast worker capacity + persistent volume cost
	// when the active vendor lacks the SaaS equivalent.
	PostgresCPUMillicoresOverride int
	PostgresMemoryMiBOverride     int
	PostgresVolumeGBOverride      int
	MQCPUMillicoresOverride       int
	MQMemoryMiBOverride           int
	MQVolumeGBOverride            int
	ObjStoreCPUMillicoresOverride int
	ObjStoreMemoryMiBOverride     int
	ObjStoreVolumeGBOverride      int
	CacheCPUMillicoresOverride    int
	CacheMemoryMiBOverride        int
	// HardwareCostUSD is the capex of the entire on-prem cluster
	// (sum of every node's purchase price). > 0 enables the TCO
	// path for self-hosted providers (Proxmox, vSphere) — they
	// otherwise return ErrNotApplicable. Amortized monthly capex
	// is HardwareCostUSD / (HardwareUsefulLifeYears × 12).
	HardwareCostUSD float64
	// HardwareUsefulLifeYears is the depreciation horizon over
	// which to amortize the capex. Default 5 — the typical server
	// refresh cadence and the IRS MACRS 5-year property class.
	HardwareUsefulLifeYears float64
	// HardwareWatts is the cluster's continuous draw at typical
	// load (NOT max nameplate). Used to compute electricity opex.
	HardwareWatts float64
	// HardwareKWHRateUSD is the user's electricity rate in USD per
	// kWh (delivered, including transmission/taxes — not just
	// generation). Default 0.15 (rough US average).
	HardwareKWHRateUSD float64
	// HardwareSupportUSDMonth is any flat monthly cost the operator
	// wants to fold into the estimate — vSphere licensing, ESXi
	// support contract, IPMI subscription, colo/rack rental, etc.
	HardwareSupportUSDMonth float64
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
	BootstrapMode string

	// ---- Kind / management cluster ----
	ClusterID                    string
	KindClusterName              string
	ClusterName                  string
	KindConfig                   string
	BootstrapEphemeralKindConfig string
	BootstrapKindConfigEphemeral bool

	// ---- CAPI manifest (workload) ----
	CAPIManifest                           string
	BootstrapCAPIManifestEphemeral         bool
	BootstrapCAPIManifestUserSet           bool
	BootstrapCAPIUseSecret                 bool
	BootstrapRegenerateCAPIManifest        bool
	BootstrapSkipImmutableManifestWarning  bool
	BootstrapClusterctlRegeneratedManifest bool
	CAPIManifestSecretNamespace            string
	CAPIManifestSecretName                 string
	CAPIManifestSecretKey                  string

	// ---- Kind backup/restore ----
	BootstrapKindBackupNamespaces string
	BootstrapKindBackupOut        string
	BootstrapKindBackupEncrypt    string
	BootstrapKindBackupPassphrase string
	BootstrapKindStateOp          string
	BootstrapKindStatePath        string

	// Airgapped, when true, disables every internet-requiring code
	// path: pricing fetchers (live vendor catalogs), geo detection,
	// FX rate lookup, and the entire hyperscale/cloud-provider
	// surface (AWS, Azure, GCP, Hetzner, DigitalOcean, Linode, OCI,
	// IBM Cloud). Only on-prem providers (Proxmox, OpenStack,
	// vSphere, CAPD) remain available.
	//
	// CLI flag: --airgapped. Env: YAGE_AIRGAPPED.
	// See docs/abstraction-plan.md §17.
	Airgapped bool

	// GeoIPEnabled, when true, fetches the operator's outbound IP via
	// GeoJS to determine nearest cloud regions for cost estimation.
	// Off by default; DataCenterLocation provides location hints without
	// any outbound lookup. CLI flag: --geoip.
	GeoIPEnabled bool

	// ImageRegistryMirror, when non-empty, prefixes every CAPI
	// provider image reference passed to `clusterctl init` so the
	// images come from an internal mirror instead of the public
	// registries (registry.k8s.io, ghcr.io, quay.io). Required
	// in airgapped deployments; warning fires when --airgapped is
	// set without this. Format: a host/path prefix without a
	// trailing slash, e.g. "harbor.internal/yage-mirror" — yage
	// rewrites "registry.k8s.io/cluster-api/core" to
	// "harbor.internal/yage-mirror/cluster-api/core".
	//
	// CLI flag: --image-registry-mirror. Env: YAGE_IMAGE_REGISTRY_MIRROR.
	// See docs/abstraction-plan.md §17 follow-up.
	ImageRegistryMirror string

	// InternalCABundle, when non-empty, is a path to a PEM bundle
	// of CA certificates that yage trusts for every outbound HTTPS
	// call (Helm chart pulls, OCI image pulls in clusterctl,
	// kubectl-against-workload, pricing/inventory APIs in
	// non-airgapped mode). The bundle is loaded once at startup,
	// installed on http.DefaultTransport, and exported as
	// SSL_CERT_FILE so child processes (helm, clusterctl, kind)
	// trust it too.
	//
	// CLI flag: --internal-ca-bundle. Env: YAGE_INTERNAL_CA_BUNDLE.
	// See docs/abstraction-plan.md §17 / §21.4.
	InternalCABundle string

	// HelmRepoMirror, when non-empty, is the base URL of an
	// internal Helm chart repository (Harbor, ChartMuseum, …) that
	// yage rewrites every outgoing chart-repo URL onto. Format: a
	// single base URL with no trailing slash, e.g.
	// "https://harbor.internal/chartrepo/yage" — yage rewrites
	// "https://charts.jetstack.io/cert-manager" to
	// "https://harbor.internal/chartrepo/yage/cert-manager".
	// `oci://…` repo URLs are rewritten the same way.
	//
	// CLI flag: --helm-repo-mirror. Env: YAGE_HELM_REPO_MIRROR.
	// See docs/abstraction-plan.md §17 / §21.4.
	HelmRepoMirror string

	// NodeImage, when non-empty, overrides the kind worker base
	// image (kindest/node:vX.Y.Z) that the management kind cluster
	// boots from. In airgapped envs the operator pulls that image
	// into their internal registry under a different name; this
	// flag lets them point yage at the internal copy. Cross-
	// provider knob (per-provider templates / AMIs / Glance images
	// stay on their own per-provider config fields).
	//
	// CLI flag: --node-image. Env: YAGE_NODE_IMAGE.
	// See docs/abstraction-plan.md §17 / §21.4.
	NodeImage string

	// InfraProviderDefaulted is true when the user neither set the
	// INFRA_PROVIDER env var nor passed --infra-provider. There is
	// no silent default: main() turns this into a hard error and
	// directs the user to `yage --xapiri` (TUI fork picks
	// on-prem/cloud and sets InfraProvider) or to pass the flag
	// explicitly. See §18.
	InfraProviderDefaulted bool

	// ConfigFile is the path to the optional YAML config file loaded via
	// --config <path> or YAGE_CONFIG_FILE. Used for display / debug only
	// after the file has already been applied via config.ApplyYAMLFile.
	ConfigFile string

	// ---- CAPI providers ----
	InfraProvider         string
	IPAMProvider          string
	CAPMOXRepo            string
	CAPMOXImageRepo       string
	CAPMOXBuildDir        string
	CAPMOXVersion         string
	CAPICoreImage         string
	CAPICoreRepo          string
	CAPIBootstrapImage    string
	CAPIControlplaneImage string
	IPAMImage             string
	IPAMRepo              string

	// ---- Clusterctl experimental / topology ----
	ExpClusterResourceSet             string
	ClusterTopology                   string
	ExpKubeadmBootstrapFormatIgnition string

	// ---- metrics-server ----
	EnableMetricsServer              bool
	EnableWorkloadMetricsServer      bool
	WorkloadMetricsServerInsecureTLS string
	MetricsServerManifestURL         string
	MetricsServerGitChartTag         string

	WorkloadPostsyncNamespace string

	// ---- Kyverno ----
	KyvernoEnabled              bool
	KyvernoChartVersion         string
	KyvernoChartRepoURL         string
	KyvernoNamespace            string
	KyvernoTolerateControlPlane string

	// ---- cert-manager ----
	CertManagerEnabled      bool
	CertManagerChartVersion string
	CertManagerChartRepoURL string
	CertManagerNamespace    string

	// ---- Crossplane ----
	CrossplaneEnabled      bool
	CrossplaneChartVersion string
	CrossplaneChartRepoURL string
	CrossplaneNamespace    string

	// ---- CloudNativePG ----
	CNPGEnabled      bool
	CNPGChartVersion string
	CNPGChartRepoURL string
	CNPGChartName    string
	CNPGNamespace    string

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
	WorkloadClusterName              string
	WorkloadCiliumClusterID          string
	WorkloadClusterNamespace         string
	WorkloadClusterNameExplicit      bool
	WorkloadClusterNamespaceExplicit bool
	// ConfigName is the per-config discriminator used to name the
	// yage-system bootstrap-config Secret (<ConfigName>-bootstrap-config).
	// Defaults to WorkloadClusterName so case-1 (N workload clusters on one
	// mgmt) needs no extra flag. Set --config-name explicitly to create
	// named profiles (case 2) or draft scenarios (case 3).
	ConfigName                        string
	ConfigNameExplicit                bool
	WorkloadKubernetesVersion         string
	WorkloadKubernetesVersionExplicit bool
	ControlPlaneMachineCount          string
	WorkerMachineCount                string

	// ---- Observability ----
	// TraceEndpoint is the gRPC endpoint for OTEL span export (e.g.
	// "localhost:4317"). When empty the global tracer is the zero-overhead
	// NoopTracer. Set via --trace-endpoint or YAGE_TRACE_ENDPOINT.
	TraceEndpoint string

	// --- On-prem platform services (Phase H, ADR 0009) ---

	// RegistryNode is the Proxmox node on which to provision the bootstrap
	// registry VM. Empty means registry provisioning is skipped.
	// Env: YAGE_REGISTRY_NODE.
	RegistryNode string

	// RegistryVMFlavor is the Proxmox VM flavor (template) to use for the
	// registry VM. Empty means use the provider default.
	// Env: YAGE_REGISTRY_VM_FLAVOR.
	RegistryVMFlavor string

	// RegistryNetwork is the Proxmox network bridge for the registry VM.
	// Env: YAGE_REGISTRY_NETWORK.
	RegistryNetwork string

	// RegistryStorage is the Proxmox storage pool for the registry VM's
	// volumes. Env: YAGE_REGISTRY_STORAGE.
	RegistryStorage string

	// RegistryFlavor is the registry software to deploy (e.g. "harbor").
	// Defaults to "harbor". Env: YAGE_REGISTRY_FLAVOR.
	RegistryFlavor string

	// IssuingCARootCert is the PEM-encoded root CA certificate for signing
	// the intermediate issuing CA. Not persisted to kind Secrets. Empty
	// means issuing CA provisioning is skipped.
	// Env: YAGE_ISSUING_CA_ROOT_CERT.
	IssuingCARootCert string

	// IssuingCARootKey is the PEM-encoded root CA private key. Not persisted
	// to kind Secrets.
	// Env: YAGE_ISSUING_CA_ROOT_KEY.
	IssuingCARootKey string

	// TofuRepo is the Git URL of the yage-tofu repository.
	// Defaults to "https://github.com/lpasquali/yage-tofu". Env: YAGE_TOFU_REPO.
	TofuRepo string

	// TofuRef is the Git ref of the lpasquali/yage-tofu repo to use.
	// Defaults to main. Env: YAGE_TOFU_REF.
	TofuRef string

	// ManifestsRepo is the Git URL of the yage-manifests repository.
	// Defaults to "https://github.com/lpasquali/yage-manifests". Env: YAGE_MANIFESTS_REPO.
	ManifestsRepo string

	// ManifestsRef is the git tag or branch of the lpasquali/yage-manifests
	// repository that manifests.Fetcher clones/checks-out. Defaults to "v0.1.0"
	// (ADR 0008 §5). Env: YAGE_MANIFESTS_REF.
	ManifestsRef string

	// ReposPVCSize is the size of the yage-repos PersistentVolumeClaim used
	// by the yage-repo-sync Job to store cloned repositories. Defaults to "500Mi".
	// Env: YAGE_REPOS_PVC_SIZE.
	ReposPVCSize string
}

// Load reads environment variables and applies defaults to produce a
// fresh *Config. CLI flag parsing runs *after* Load() and can overwrite
// any field in place.
//
// Defaults for non-obvious fields are referenced inline; trivial
// defaults are applied with the getenv helper.
func Load() *Config {
	c := &Config{}

	// --- versions (lines 337-341, 416, 446-451, 501, 508) ---
	c.KindVersion = getenv("KIND_VERSION", "v0.31.0")
	c.KubectlVersion = getenv("KUBECTL_VERSION", "v1.35.4")
	c.ClusterctlVersion = getenv("CLUSTERCTL_VERSION", "v1.11.8")
	c.CiliumCLIVersion = getenv("CILIUM_CLI_VERSION", "v0.19.2")
	c.CiliumVersion = getenv("CILIUM_VERSION", "1.19.3")
	c.ArgoCD.Version = getenv("ARGOCD_VERSION", "v3.3.8")
	c.ArgoCD.OperatorVersion = getenv("ARGOCD_OPERATOR_VERSION", "v0.16.0")
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
	c.ArgoCD.DisableOperatorManagedIngress = getenv("ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS", "false")
	c.ArgoCD.Enabled = envBool("ARGOCD_ENABLED", true)
	c.ArgoCD.ServerInsecure = getenv("ARGOCD_SERVER_INSECURE", "false")
	c.ArgoCD.PrometheusEnabled = getenv("ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED", "false")
	c.ArgoCD.MonitoringEnabled = getenv("ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED", "false")
	c.ArgoCD.PrintAccessTarget = getenv("ARGOCD_PRINT_ACCESS_TARGET", "workload")
	c.ArgoCD.PrintAccessStandalone = envBool("ARGOCD_PRINT_ACCESS_STANDALONE", false)
	c.ArgoCD.PortForwardStandalone = envBool("ARGOCD_PORT_FORWARD_STANDALONE", false)
	c.ArgoCD.PortForwardTarget = getenv("ARGOCD_PORT_FORWARD_TARGET", "workload")
	c.ArgoCD.PortForwardPort = getenv("ARGOCD_PORT_FORWARD_PORT", getenv("ARGOCD_PORT_FORWARD_WORKLOAD_PORT", "8443"))

	// Workload ArgoCD/GitOps (lines 430-479)
	c.ArgoCD.WorkloadEnabled = envBool("WORKLOAD_ARGOCD_ENABLED", true)
	c.ArgoCD.WorkloadNamespace = getenv("WORKLOAD_ARGOCD_NAMESPACE", "argocd")
	c.WorkloadGitopsMode = getenv("WORKLOAD_GITOPS_MODE", "caaph")
	c.ArgoCD.AppOfAppsGitURL = getenv("WORKLOAD_APP_OF_APPS_GIT_URL", "https://github.com/lpasquali/workload-app-of-apps.git")
	c.ArgoCD.AppOfAppsGitPath = getenv("WORKLOAD_APP_OF_APPS_GIT_PATH", "examples/default")
	c.ArgoCD.AppOfAppsGitRef = getenv("WORKLOAD_APP_OF_APPS_GIT_REF", "main")
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
	c.Capacity.AllowOvercommit = envBool("ALLOW_RESOURCE_OVERCOMMIT", false)
	c.Capacity.ResourceBudgetFraction = envFloat("RESOURCE_BUDGET_FRACTION", 2.0/3.0)
	c.Capacity.OvercommitTolerancePct = envFloat("OVERCOMMIT_TOLERANCE_PCT", 15.0)
	c.HardwareCostUSD = envFloat("HARDWARE_COST_USD", 0)
	c.HardwareUsefulLifeYears = envFloat("HARDWARE_USEFUL_LIFE_YEARS", 5)
	c.HardwareWatts = envFloat("HARDWARE_WATTS", 0)
	c.HardwareKWHRateUSD = envFloat("HARDWARE_KWH_RATE_USD", 0.15)
	c.HardwareSupportUSDMonth = envFloat("HARDWARE_SUPPORT_USD_MONTH", 0)
	c.Airgapped = envBool("YAGE_AIRGAPPED", false)
	c.ImageRegistryMirror = strings.TrimRight(getenv("YAGE_IMAGE_REGISTRY_MIRROR", ""), "/")
	c.InternalCABundle = strings.TrimSpace(getenv("YAGE_INTERNAL_CA_BUNDLE", ""))
	c.HelmRepoMirror = strings.TrimRight(strings.TrimSpace(getenv("YAGE_HELM_REPO_MIRROR", "")), "/")
	c.NodeImage = strings.TrimSpace(getenv("YAGE_NODE_IMAGE", ""))
	c.TraceEndpoint = getenv("YAGE_TRACE_ENDPOINT", "")
	c.TofuRepo = getenv("YAGE_TOFU_REPO", "https://github.com/lpasquali/yage-tofu")
	c.TofuRef = getenv("YAGE_TOFU_REF", "main")
	c.ManifestsRepo = getenv("YAGE_MANIFESTS_REPO", "https://github.com/lpasquali/yage-manifests")
	c.ManifestsRef = getenv("YAGE_MANIFESTS_REF", "v0.1.0")
	c.ReposPVCSize = getenv("YAGE_REPOS_PVC_SIZE", "500Mi")

	// --- On-prem platform services (Phase H, ADR 0009) ---
	c.RegistryNode = getenv("YAGE_REGISTRY_NODE", "")
	c.RegistryVMFlavor = getenv("YAGE_REGISTRY_VM_FLAVOR", "")
	c.RegistryNetwork = getenv("YAGE_REGISTRY_NETWORK", "")
	c.RegistryStorage = getenv("YAGE_REGISTRY_STORAGE", "")
	c.RegistryFlavor = getenv("YAGE_REGISTRY_FLAVOR", "harbor")
	// Root CA material is read from env but never persisted to kind Secrets
	// (omitted from Snapshot). See IssuingCARootCert / IssuingCARootKey docs.
	c.IssuingCARootCert = getenv("YAGE_ISSUING_CA_ROOT_CERT", "")
	c.IssuingCARootKey = getenv("YAGE_ISSUING_CA_ROOT_KEY", "")

	// CSI add-on selection (§20 / Phase F). YAGE_CSI_DRIVERS is a
	// comma-separated list of driver names — empty values get
	// dropped so "a,,b" reads as ["a","b"] rather than three
	// elements. Empty env → cfg.CSI.Drivers stays nil, which means
	// "use the per-provider default" at Selector() time.
	if v := os.Getenv("YAGE_CSI_DRIVERS"); v != "" {
		for _, n := range strings.Split(v, ",") {
			n = strings.TrimSpace(n)
			if n != "" {
				c.CSI.Drivers = append(c.CSI.Drivers, n)
			}
		}
	}
	c.CSI.DefaultClass = os.Getenv("YAGE_CSI_DEFAULT_CLASS")

	// Cost-estimation credentials and currency preferences (§16).
	// Each YAGE_X spelling wins over the vendor-native fallback.
	c.Cost.Credentials.GCPAPIKey = firstNonEmpty(
		os.Getenv("YAGE_GCP_API_KEY"),
		os.Getenv("GOOGLE_BILLING_API_KEY"),
	)
	c.Cost.Credentials.HetznerToken = firstNonEmpty(
		os.Getenv("YAGE_HCLOUD_TOKEN"),
		os.Getenv("HCLOUD_TOKEN"),
	)
	c.Cost.Credentials.DigitalOceanToken = firstNonEmpty(
		os.Getenv("YAGE_DO_TOKEN"),
		os.Getenv("DIGITALOCEAN_TOKEN"),
	)
	c.Cost.Credentials.LinodeToken = firstNonEmpty(
		os.Getenv("YAGE_LINODE_TOKEN"),
		os.Getenv("LINODE_TOKEN"),
	)
	c.Cost.Credentials.IBMCloudAPIKey = firstNonEmpty(
		os.Getenv("YAGE_IBMCLOUD_API_KEY"),
		os.Getenv("IBMCLOUD_API_KEY"),
	)
	c.Cost.Currency.DisplayCurrency = os.Getenv("YAGE_TALLER_CURRENCY")
	c.Cost.Currency.DataCenterLocation = strings.ToUpper(strings.TrimSpace(
		os.Getenv("YAGE_DATA_CENTER_LOCATION")))

	// Cross-fill the Hetzner token between the cost-credentials view
	// and the provider's own view: same secret, two consumers.
	if c.Cost.Credentials.HetznerToken == "" && c.Providers.Hetzner.Token != "" {
		c.Cost.Credentials.HetznerToken = c.Providers.Hetzner.Token
	}
	if c.Providers.Hetzner.Token == "" && c.Cost.Credentials.HetznerToken != "" {
		c.Providers.Hetzner.Token = c.Cost.Credentials.HetznerToken
	}

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
	// Azure identity / state — surfaces the env vars CAPZ already
	// reads (AZURE_SUBSCRIPTION_ID, AZURE_TENANT_ID, ...) on cfg so
	// xapiri / kindsync can round-trip them. IdentityModel defaults
	// to "service-principal" — the historical CAPZ default.
	c.Providers.Azure.SubscriptionID = getenv("AZURE_SUBSCRIPTION_ID", "")
	c.Providers.Azure.TenantID = getenv("AZURE_TENANT_ID", "")
	c.Providers.Azure.ResourceGroup = getenv("AZURE_RESOURCE_GROUP", "")
	c.Providers.Azure.VNetName = getenv("AZURE_VNET_NAME", "")
	c.Providers.Azure.SubnetName = getenv("AZURE_SUBNET_NAME", "")
	c.Providers.Azure.ClientID = getenv("AZURE_CLIENT_ID", "")
	c.Providers.Azure.IdentityModel = getenv("AZURE_IDENTITY_MODEL", "service-principal")
	c.Providers.GCP.ControlPlaneMachineType = getenv("GCP_CONTROL_PLANE_MACHINE_TYPE", "n2-standard-2")
	c.Providers.GCP.NodeMachineType = getenv("GCP_NODE_MACHINE_TYPE", "n2-standard-2")
	c.Providers.GCP.Region = getenv("GCP_REGION", "us-central1")
	c.Providers.GCP.Project = getenv("GCP_PROJECT", "")
	c.Providers.GCP.Mode = getenv("GCP_MODE", "unmanaged")
	c.Providers.GCP.OverheadTier = getenv("GCP_OVERHEAD_TIER", "prod")
	// GCP network / image / identity — Network surfaces as
	// GCP_NETWORK_NAME (the spelling CAPG uses for TemplateVars);
	// IdentityModel defaults to "service-account" (the historical
	// GOOGLE_APPLICATION_CREDENTIALS path).
	c.Providers.GCP.Network = getenv("GCP_NETWORK_NAME", "")
	c.Providers.GCP.ImageFamily = getenv("GCP_IMAGE_FAMILY", "")
	c.Providers.GCP.IdentityModel = getenv("GCP_IDENTITY_MODEL", "service-account")
	// OpenStack — canonical spelling is OPENSTACK_*. Empty defaults
	// across the board: the CAPO manifest needs them set explicitly anyway.
	c.Providers.OpenStack.Cloud = getenv("OPENSTACK_CLOUD", "")
	c.Providers.OpenStack.ProjectName = getenv("OPENSTACK_PROJECT_NAME", "")
	c.Providers.OpenStack.Region = getenv("OPENSTACK_REGION", "")
	c.Providers.OpenStack.FailureDomain = getenv("OPENSTACK_FAILURE_DOMAIN", "")
	c.Providers.OpenStack.ImageName = getenv("OPENSTACK_IMAGE_NAME", "")
	c.Providers.OpenStack.ControlPlaneFlavor = getenv("OPENSTACK_CONTROL_PLANE_FLAVOR", "")
	c.Providers.OpenStack.WorkerFlavor = getenv("OPENSTACK_WORKER_FLAVOR", "")
	c.Providers.OpenStack.DNSNameservers = getenv("OPENSTACK_DNS_NAMESERVERS", "")
	c.Providers.OpenStack.SSHKeyName = getenv("OPENSTACK_SSH_KEY_NAME", "")
	// vSphere — the env-var roster CAPV's manifest expects, plus
	// operator-supplied credentials (VSPHERE_USERNAME / VSPHERE_PASSWORD)
	// surfaced on cfg so xapiri can prompt and kindsync can round-trip.
	c.Providers.Vsphere.Server = getenv("VSPHERE_SERVER", "")
	c.Providers.Vsphere.Datacenter = getenv("VSPHERE_DATACENTER", "")
	c.Providers.Vsphere.Folder = getenv("VSPHERE_FOLDER", "")
	c.Providers.Vsphere.ResourcePool = getenv("VSPHERE_RESOURCE_POOL", "")
	c.Providers.Vsphere.Datastore = getenv("VSPHERE_DATASTORE", "")
	c.Providers.Vsphere.Network = getenv("VSPHERE_NETWORK", "")
	c.Providers.Vsphere.Template = getenv("VSPHERE_TEMPLATE", "")
	c.Providers.Vsphere.TLSThumbprint = getenv("VSPHERE_TLS_THUMBPRINT", "")
	c.Providers.Vsphere.Username = getenv("VSPHERE_USERNAME", "")
	c.Providers.Vsphere.Password = getenv("VSPHERE_PASSWORD", "")
	// VSphereMachineTemplate sizing — PatchManifest writes these into
	// numCPUs / numCoresPerSocket / memoryMiB / diskGiB post-render.
	// Defaults mirror the literals in the K3s template (25 GiB disk,
	// 1 core-per-socket; CPU and memory left empty so the template
	// placeholder substitution takes effect when the operator has not
	// set the VSPHERE_* override).
	c.Providers.Vsphere.ControlPlaneNumCPUs = getenv("VSPHERE_CONTROL_PLANE_NUM_CPUS", "")
	c.Providers.Vsphere.ControlPlaneNumCoresPerSocket = getenv("VSPHERE_CONTROL_PLANE_NUM_CORES_PER_SOCKET", "")
	c.Providers.Vsphere.ControlPlaneMemoryMiB = getenv("VSPHERE_CONTROL_PLANE_MEMORY_MIB", "")
	c.Providers.Vsphere.ControlPlaneDiskGiB = getenv("VSPHERE_CONTROL_PLANE_DISK_GIB", "")
	c.Providers.Vsphere.WorkerNumCPUs = getenv("VSPHERE_WORKER_NUM_CPUS", "")
	c.Providers.Vsphere.WorkerNumCoresPerSocket = getenv("VSPHERE_WORKER_NUM_CORES_PER_SOCKET", "")
	c.Providers.Vsphere.WorkerMemoryMiB = getenv("VSPHERE_WORKER_MEMORY_MIB", "")
	c.Providers.Vsphere.WorkerDiskGiB = getenv("VSPHERE_WORKER_DISK_GIB", "")
	c.Providers.Hetzner.ControlPlaneMachineType = getenv("HCLOUD_CONTROL_PLANE_MACHINE_TYPE", "cx23")
	c.Providers.Hetzner.NodeMachineType = getenv("HCLOUD_NODE_MACHINE_TYPE", "cx23")
	c.Providers.Hetzner.Location = getenv("HCLOUD_REGION", "fsn1")
	c.Providers.Hetzner.OverheadTier = getenv("HETZNER_OVERHEAD_TIER", "prod")
	c.Providers.DigitalOcean.Region = getenv("DIGITALOCEAN_REGION", "nyc3")
	c.Providers.DigitalOcean.ControlPlaneSize = getenv("DIGITALOCEAN_CONTROL_PLANE_SIZE", "s-2vcpu-4gb")
	c.Providers.DigitalOcean.NodeSize = getenv("DIGITALOCEAN_NODE_SIZE", "s-2vcpu-4gb")
	c.Providers.DigitalOcean.VPCUUID = getenv("DIGITALOCEAN_VPC_UUID", "")
	c.Providers.DigitalOcean.OverheadTier = getenv("DIGITALOCEAN_OVERHEAD_TIER", "prod")
	c.Providers.Linode.Region = getenv("LINODE_REGION", "us-east")
	c.Providers.Linode.ControlPlaneType = getenv("LINODE_CONTROL_PLANE_TYPE", "g6-standard-2")
	c.Providers.Linode.NodeType = getenv("LINODE_NODE_TYPE", "g6-standard-2")
	c.Providers.Linode.OverheadTier = getenv("LINODE_OVERHEAD_TIER", "prod")
	c.Providers.OCI.Region = getenv("OCI_REGION", "us-ashburn-1")
	c.Providers.OCI.ControlPlaneShape = getenv("OCI_CONTROL_PLANE_SHAPE", "VM.Standard.E4.Flex")
	c.Providers.OCI.NodeShape = getenv("OCI_NODE_SHAPE", "VM.Standard.E4.Flex")
	c.Providers.OCI.TenancyOCID = getenv("OCI_TENANCY_OCID", "")
	c.Providers.OCI.UserOCID = getenv("OCI_USER_OCID", "")
	c.Providers.OCI.Fingerprint = getenv("OCI_FINGERPRINT", "")
	c.Providers.OCI.CompartmentOCID = getenv("OCI_COMPARTMENT_OCID", "")
	c.Providers.OCI.ImageID = getenv("OCI_IMAGE_ID", "")
	c.Providers.OCI.PrivateKeyPath = getenv("OCI_PRIVATE_KEY_PATH", "")
	c.Providers.OCI.OverheadTier = getenv("OCI_OVERHEAD_TIER", "prod")
	c.Providers.IBMCloud.Region = getenv("IBMCLOUD_REGION", "us-south")
	c.Providers.IBMCloud.ControlPlaneProfile = getenv("IBMCLOUD_CONTROL_PLANE_PROFILE", "bx2-2x8")
	c.Providers.IBMCloud.NodeProfile = getenv("IBMCLOUD_NODE_PROFILE", "bx2-2x8")
	c.Providers.IBMCloud.ResourceGroup = getenv("IBMCLOUD_RESOURCE_GROUP", "")
	c.Providers.IBMCloud.VPCName = getenv("IBMCLOUD_VPC_NAME", "")
	c.Providers.IBMCloud.Zone = getenv("IBMCLOUD_ZONE", "")
	c.Providers.IBMCloud.ImageID = getenv("IBMCLOUD_VPC_IMAGE_ID", "")
	c.Capacity.SystemAppsCPUMillicores = int(envFloat("SYSTEM_APPS_CPU_MILLICORES", 2000))
	c.Capacity.SystemAppsMemoryMiB = int64(envFloat("SYSTEM_APPS_MEMORY_MIB", 4096))

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
	c.CAPIManifestSecretNamespace = getenv("CAPI_MANIFEST_SECRET_NAMESPACE", "yage-system")
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
	// No silent default. If neither INFRA_PROVIDER nor
	// --infra-provider is set, main() prints a hard error and
	// directs the user to `yage --xapiri` (the TUI sets
	// InfraProvider via the on-prem/cloud fork) or to pass
	// --infra-provider explicitly. InfraProviderDefaulted is kept
	// only so callers that consume an already-loaded Config can
	// distinguish "user picked nothing" from "user passed empty".
	if _, set := os.LookupEnv("INFRA_PROVIDER"); !set {
		c.InfraProviderDefaulted = true
	}
	c.InfraProvider = getenv("INFRA_PROVIDER", "")
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
	c.ArgoCD.PostsyncHooksEnabled = envBool("ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED", true)
	// bash uses "${VAR-default}" (keep-empty) — we preserve that: only fall
	// back when the env var is truly unset.
	c.ArgoCD.PostsyncHooksGitURL = getenvKeep("ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL", "https://github.com/lpasquali/workload-smoketests.git")
	c.ArgoCD.PostsyncHooksGitPath = getenvKeep("ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH", "")
	c.ArgoCD.PostsyncHooksGitRef = getenvKeep("ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF", "")
	c.ArgoCD.PostsyncHooksKubectlImg = getenv("ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE", "")
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
	c.Providers.Proxmox.BootstrapSecretNamespace = getenv("PROXMOX_BOOTSTRAP_SECRET_NAMESPACE", "yage-system")
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
	c.Providers.Proxmox.CAPIToken = getenv("PROXMOX_CAPI_TOKEN", getenv("PROXMOX_TOKEN", ""))
	c.Providers.Proxmox.CAPISecret = getenv("PROXMOX_CAPI_SECRET", getenv("PROXMOX_SECRET", ""))
	c.Providers.Proxmox.AdminUsername = getenv("PROXMOX_ADMIN_USERNAME", "root@pam!capi-bootstrap")
	c.Providers.Proxmox.AdminToken = getenv("PROXMOX_ADMIN_TOKEN", "")
	c.Providers.Proxmox.Region = getenv("PROXMOX_REGION", "")
	c.Providers.Proxmox.Node = getenv("PROXMOX_NODE", "")
	c.Providers.Proxmox.SourceNode = getenv("PROXMOX_SOURCENODE", "")
	c.Providers.Proxmox.TopologyRegion = getenv("PROXMOX_TOPOLOGY_REGION", "")
	c.Providers.Proxmox.TopologyZone = getenv("PROXMOX_TOPOLOGY_ZONE", "")
	c.Providers.Proxmox.TemplateID = getenv("PROXMOX_TEMPLATE_ID", getenv("TEMPLATE_VMID", "104"))
	c.Providers.Proxmox.Bridge = getenv("PROXMOX_BRIDGE", "vmbr0")

	// Keyring fallback: if env vars were empty, try the OS keychain.
	// Silently skips when no keyring backend is available (headless Linux,
	// CI). The kind-Secret merge in main() runs later and may further fill
	// empty fields; this fallback sits between env and kind-Secret.
	if c.Providers.Proxmox.CAPIToken == "" {
		if val, err := syskeyring.Get(syskeyring.KeyProxmoxCAPIToken); err == nil {
			c.Providers.Proxmox.CAPIToken = val
		}
	}
	if c.Providers.Proxmox.CAPISecret == "" {
		if val, err := syskeyring.Get(syskeyring.KeyProxmoxCAPISecret); err == nil {
			c.Providers.Proxmox.CAPISecret = val
		}
	}
	if c.Providers.Proxmox.AdminToken == "" {
		if val, err := syskeyring.Get(syskeyring.KeyProxmoxAdminToken); err == nil {
			c.Providers.Proxmox.AdminToken = val
		}
	}

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
	c.Providers.Proxmox.CSIMgmtStorageClassName = getenv("PROXMOX_CSI_MGMT_STORAGE_CLASS", "")
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
	c.WorkloadClusterNameExplicit = envBoolLoose("WORKLOAD_CLUSTER_NAME_EXPLICIT", false) || os.Getenv("WORKLOAD_CLUSTER_NAME") != ""
	c.WorkloadClusterNamespaceExplicit = envBoolLoose("WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT", false) || os.Getenv("WORKLOAD_CLUSTER_NAMESPACE") != ""
	c.ConfigName = getenv("YAGE_CONFIG_NAME", "")
	c.ConfigNameExplicit = envBoolLoose("YAGE_CONFIG_NAME_EXPLICIT", false) || os.Getenv("YAGE_CONFIG_NAME") != ""
	if c.ConfigName == "" {
		c.ConfigName = c.WorkloadClusterName
	}
	c.WorkloadKubernetesVersion = getenv("WORKLOAD_KUBERNETES_VERSION", "v1.35.0")
	c.WorkloadKubernetesVersionExplicit = os.Getenv("WORKLOAD_KUBERNETES_VERSION") != ""
	c.ControlPlaneMachineCount = getenv("CONTROL_PLANE_MACHINE_COUNT", "1")
	c.WorkerMachineCount = getenv("WORKER_MACHINE_COUNT", "2")

	// --- Pivot orchestration toggles ---
	// PivotEnabled defaults to true: kind is a launcher, not a long-
	// lived control plane. The orchestrator silently skips the pivot
	// path when the active provider hasn't implemented PivotTarget
	// yet (logs once, then proceeds with kind as mgmt). Operators
	// who want kind as the permanent management plane pass --no-pivot.
	c.Pivot.Enabled = envBool("PIVOT_ENABLED", true)
	c.Pivot.KeepKind = envBool("PIVOT_KEEP_KIND", false)
	c.Pivot.DryRun = envBool("PIVOT_DRY_RUN", false)
	c.Pivot.VerifyTimeout = getenv("PIVOT_VERIFY_TIMEOUT", "10m")
	c.Pivot.StopBeforeWorkload = envBool("YAGE_STOP_BEFORE_WORKLOAD", false)
	c.SkipProviders = getenv("YAGE_SKIP_PROVIDERS", "")
	c.CostCompareEnabled = envBool("YAGE_COST_COMPARE_CONFIG", false)
	c.UseManagedPostgres = envBool("YAGE_USE_MANAGED_POSTGRES", true)
	c.PostgresCPUMillicoresOverride = int(envFloat("YAGE_POSTGRES_CPU_MILLICORES", 0))
	c.PostgresMemoryMiBOverride = int(envFloat("YAGE_POSTGRES_MEMORY_MIB", 0))
	c.PostgresVolumeGBOverride = int(envFloat("YAGE_POSTGRES_VOLUME_GB", 0))
	c.MQCPUMillicoresOverride = int(envFloat("YAGE_MQ_CPU_MILLICORES", 0))
	c.MQMemoryMiBOverride = int(envFloat("YAGE_MQ_MEMORY_MIB", 0))
	c.MQVolumeGBOverride = int(envFloat("YAGE_MQ_VOLUME_GB", 0))
	c.ObjStoreCPUMillicoresOverride = int(envFloat("YAGE_OBJSTORE_CPU_MILLICORES", 0))
	c.ObjStoreMemoryMiBOverride = int(envFloat("YAGE_OBJSTORE_MEMORY_MIB", 0))
	c.ObjStoreVolumeGBOverride = int(envFloat("YAGE_OBJSTORE_VOLUME_GB", 0))
	c.CacheCPUMillicoresOverride = int(envFloat("YAGE_CACHE_CPU_MILLICORES", 0))
	c.CacheMemoryMiBOverride = int(envFloat("YAGE_CACHE_MEMORY_MIB", 0))

	// --- Management cluster shape (universal) ---
	c.Mgmt.ClusterName = getenv("MGMT_CLUSTER_NAME", "capi-management")
	c.Mgmt.ClusterNameExplicit = envBoolLoose("MGMT_CLUSTER_NAME_EXPLICIT", false) || os.Getenv("MGMT_CLUSTER_NAME") != ""
	c.Mgmt.ClusterNamespace = getenv("MGMT_CLUSTER_NAMESPACE", "default")
	c.Mgmt.ClusterNamespaceExplicit = envBoolLoose("MGMT_CLUSTER_NAMESPACE_EXPLICIT", false) || os.Getenv("MGMT_CLUSTER_NAMESPACE") != ""
	c.Mgmt.KubernetesVersion = getenv("MGMT_KUBERNETES_VERSION", c.WorkloadKubernetesVersion)
	c.Mgmt.KubernetesVersionExplicit = os.Getenv("MGMT_KUBERNETES_VERSION") != ""
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
	c.Providers.Proxmox.Mgmt.WorkerNumSockets = getenv("MGMT_WORKER_NUM_SOCKETS", "1")
	c.Providers.Proxmox.Mgmt.WorkerNumCores = getenv("MGMT_WORKER_NUM_CORES", "2")
	c.Providers.Proxmox.Mgmt.WorkerMemoryMiB = getenv("MGMT_WORKER_MEMORY_MIB", "2048")
	c.Providers.Proxmox.Mgmt.WorkerBootVolumeDevice = getenv("MGMT_WORKER_BOOT_VOLUME_DEVICE", c.Providers.Proxmox.WorkerBootVolumeDevice)
	c.Providers.Proxmox.Mgmt.WorkerBootVolumeSize = getenv("MGMT_WORKER_BOOT_VOLUME_SIZE", "30")
	// Proxmox CSI on the management cluster: off by default (stateless).
	c.Providers.Proxmox.Mgmt.CSIEnabled = envBool("MGMT_PROXMOX_CSI_ENABLED", false)

	// Pool defaults to the matching cluster name so each cluster
	// gets its own organizational bucket. User can override or set empty.
	c.Providers.Proxmox.Pool = getenv("PROXMOX_POOL", c.WorkloadClusterName)
	c.Providers.Proxmox.PoolExplicit = envBoolLoose("PROXMOX_POOL_EXPLICIT", false) || os.Getenv("PROXMOX_POOL") != ""
	c.Providers.Proxmox.Mgmt.Pool = getenv("MGMT_PROXMOX_POOL", c.Mgmt.ClusterName)
	c.Providers.Proxmox.Mgmt.PoolExplicit = envBoolLoose("MGMT_PROXMOX_POOL_EXPLICIT", false) || os.Getenv("MGMT_PROXMOX_POOL") != ""

	return c
}

// --- helpers ---

// firstNonEmpty returns the first non-empty argument, or "" if all
// are empty. Used to express dual-spelling env-var fallback chains
// (YAGE_X / VENDOR_X) as a single readable expression.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

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

// envBoolLoose parses *_EXPLICIT-style flags that may be expressed as
// "1"/"0" counters or boolean strings.
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
