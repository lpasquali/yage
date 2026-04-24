// Package config holds every tunable variable the bash script's globals
// expose, with the same env-var overrides and defaults. One struct is shared
// by every other package: subsystems read from *Config, they never reach
// into os.Getenv directly (the one exception is boot-time Load, below).
//
// Naming convention: the Go field is the UpperCamelCase spelling of the
// bash var, with _EXPLICIT suffixed flags kept as <Name>Explicit.
//
// Defaults are taken verbatim from bootstrap-capi.sh (lines ~337-673). When
// bash uses ${FOO:-default}, we use getenv(..., "default"); when bash uses
// ${FOO-default} (empty-string preserved), we use getenvKeep(..., "default").
package config

import (
	"os"
)

// Config holds every runtime tunable. Zero value is not meaningful — always
// call Load().
type Config struct {
	// ---- Tool versions ----
	KindVersion       string
	KubectlVersion    string
	ClusterctlVersion string
	CiliumCLIVersion  string
	CiliumVersion     string
	ArgoCDCLIVersion  string
	ArgoCDVersion     string // application image tag, ArgoCD CR spec.version
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
	CAPIProxmoxMachineTemplateSpecRev        bool
	CAPIManifestSecretNamespace              string
	CAPIManifestSecretName                   string
	CAPIManifestSecretKey                    string
	ProxmoxBootstrapConfigFile               string
	ProxmoxBootstrapConfigSecretName         string
	ProxmoxBootstrapConfigSecretKey          string
	ProxmoxBootstrapAdminSecretKey           string

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

	// ---- Proxmox CSI ----
	ProxmoxCSIEnabled        bool
	ProxmoxCSISmokeEnabled   bool
	ProxmoxCSIChartRepoURL   string
	ProxmoxCSIChartName      string
	ProxmoxCSIChartVersion   string
	ProxmoxCSINamespace      string
	ProxmoxCSIConfigProvider string
	ProxmoxCSITopologyLabels string

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

	// ---- Proxmox core / admin / CSI / identities ----
	ClusterctlCfg                    string
	ProxmoxAdminConfig               string
	ProxmoxCSIConfig                 string
	ProxmoxBootstrapSecretNamespace  string
	ProxmoxBootstrapSecretName       string
	ProxmoxBootstrapCAPMOXSecretName string
	ProxmoxBootstrapCSISecretName    string
	ProxmoxBootstrapAdminSecretName  string
	ProxmoxBootstrapKindSecretUsed   bool
	ProxmoxKindCAPMOXActive          bool
	ProxmoxIdentityTF                string
	ProxmoxAdminInsecure             string
	ClusterSetID                     string
	RecreateProxmoxIdentities        bool
	ProxmoxIdentityRecreateScope     string
	ProxmoxIdentityRecreateStateRm   bool
	ProxmoxIdentitySuffix            string

	ProxmoxURL           string
	ProxmoxToken         string
	ProxmoxSecret        string
	ProxmoxAdminUsername string
	ProxmoxAdminToken    string
	ProxmoxRegion        string
	ProxmoxNode          string
	ProxmoxSourceNode    string
	ProxmoxTopologyRegion string
	ProxmoxTopologyZone   string
	ProxmoxTemplateID    string
	ProxmoxBridge        string

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

	// ---- Proxmox CSI credentials / storage ----
	ProxmoxCSIURL               string
	ProxmoxCSITokenID           string
	ProxmoxCSITokenSecret       string
	ProxmoxCSIUserID            string
	ProxmoxCSITokenPrefix       string
	ProxmoxCSIInsecure          string
	ProxmoxCSIStorageClassName  string
	ProxmoxCSIStorage           string
	ProxmoxCloudinitStorage     string
	ProxmoxMemoryAdjustment     string
	ProxmoxCSIReclaimPolicy     string
	ProxmoxCSIFsType            string
	ProxmoxCSIDefaultClass      string
	ProxmoxCAPIUserID           string
	ProxmoxCAPITokenPrefix      string

	// ---- VM sizing ----
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

	// ---- Workload cluster ----
	WorkloadClusterName             string
	WorkloadCiliumClusterID         string
	WorkloadClusterNamespace        string
	WorkloadClusterNameExplicit     bool
	WorkloadClusterNamespaceExplicit bool
	WorkloadKubernetesVersion       string
	ControlPlaneMachineCount        string
	WorkerMachineCount              string
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
	c.ArgoCDCLIVersion = getenv("ARGOCD_CLI_VERSION", "v3.3.8")
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
	c.ArgoCDPortForwardPort = getenv("ARGOCD_PORT_FORWARD_PORT", getenv("ARGOCD_PORT_FORWARD_WORKLOAD_PORT", "8080"))

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
	c.CAPIProxmoxMachineTemplateSpecRev = envBool("CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV", true)
	c.CAPIManifestSecretNamespace = getenv("CAPI_MANIFEST_SECRET_NAMESPACE", "proxmox-bootstrap-system")
	c.CAPIManifestSecretName = getenv("CAPI_MANIFEST_SECRET_NAME", "proxmox-bootstrap-capi-manifest")
	c.CAPIManifestSecretKey = getenv("CAPI_MANIFEST_SECRET_KEY", "workload.yaml")
	c.ProxmoxBootstrapConfigFile = getenv("PROXMOX_BOOTSTRAP_CONFIG_FILE", "")
	c.ProxmoxBootstrapConfigSecretName = getenv("PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME", "proxmox-bootstrap-config")
	c.ProxmoxBootstrapConfigSecretKey = getenv("PROXMOX_BOOTSTRAP_CONFIG_SECRET_KEY", "config.yaml")
	c.ProxmoxBootstrapAdminSecretKey = getenv("PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY", "proxmox-admin.yaml")

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
	c.ProxmoxCSIEnabled = envBool("PROXMOX_CSI_ENABLED", true)
	c.ProxmoxCSISmokeEnabled = envBool("PROXMOX_CSI_SMOKE_ENABLED", true)
	c.ProxmoxCSIChartRepoURL = getenv("PROXMOX_CSI_CHART_REPO_URL", "oci://ghcr.io/sergelogvinov/charts")
	c.ProxmoxCSIChartName = getenv("PROXMOX_CSI_CHART_NAME", "proxmox-csi-plugin")
	c.ProxmoxCSIChartVersion = getenv("PROXMOX_CSI_CHART_VERSION", "0.5.7")
	c.ProxmoxCSINamespace = getenv("PROXMOX_CSI_NAMESPACE", "csi-proxmox")
	c.ProxmoxCSIConfigProvider = getenv("PROXMOX_CSI_CONFIG_PROVIDER", "proxmox")
	c.ProxmoxCSITopologyLabels = getenv("PROXMOX_CSI_TOPOLOGY_LABELS", "true")

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
	c.ProxmoxAdminConfig = getenv("PROXMOX_ADMIN_CONFIG", "")
	c.ProxmoxCSIConfig = getenv("PROXMOX_CSI_CONFIG", "")
	c.ProxmoxBootstrapSecretNamespace = getenv("PROXMOX_BOOTSTRAP_SECRET_NAMESPACE", "proxmox-bootstrap-system")
	c.ProxmoxBootstrapSecretName = getenv("PROXMOX_BOOTSTRAP_SECRET_NAME", "")
	c.ProxmoxBootstrapCAPMOXSecretName = getenv("PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME", "proxmox-bootstrap-capmox-credentials")
	c.ProxmoxBootstrapCSISecretName = getenv("PROXMOX_BOOTSTRAP_CSI_SECRET_NAME", "proxmox-bootstrap-csi-credentials")
	c.ProxmoxBootstrapAdminSecretName = getenv("PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME", "proxmox-bootstrap-admin-credentials")
	c.ProxmoxBootstrapKindSecretUsed = envBool("PROXMOX_BOOTSTRAP_KIND_SECRET_USED", false)
	c.ProxmoxKindCAPMOXActive = envBool("PROXMOX_KIND_CAPMOX_CREDENTIALS_ACTIVE", false)
	c.ProxmoxIdentityTF = getenv("PROXMOX_IDENTITY_TF", "proxmox-identity.tf")
	c.ProxmoxAdminInsecure = getenv("PROXMOX_ADMIN_INSECURE", "true")
	c.ClusterSetID = getenv("CLUSTER_SET_ID", "")
	c.RecreateProxmoxIdentities = envBool("RECREATE_PROXMOX_IDENTITIES", false)
	c.ProxmoxIdentityRecreateScope = getenv("PROXMOX_IDENTITY_RECREATE_SCOPE", "both")
	c.ProxmoxIdentityRecreateStateRm = envBool("PROXMOX_IDENTITY_RECREATE_STATE_RM", false)
	c.ProxmoxIdentitySuffix = getenv("PROXMOX_IDENTITY_SUFFIX", "")

	// --- Proxmox core ---
	c.ProxmoxURL = getenv("PROXMOX_URL", "")
	c.ProxmoxToken = getenv("PROXMOX_TOKEN", "")
	c.ProxmoxSecret = getenv("PROXMOX_SECRET", "")
	c.ProxmoxAdminUsername = getenv("PROXMOX_ADMIN_USERNAME", "root@pam!capi-bootstrap")
	c.ProxmoxAdminToken = getenv("PROXMOX_ADMIN_TOKEN", "")
	c.ProxmoxRegion = getenv("PROXMOX_REGION", "")
	c.ProxmoxNode = getenv("PROXMOX_NODE", "")
	c.ProxmoxSourceNode = getenv("PROXMOX_SOURCENODE", "")
	c.ProxmoxTopologyRegion = getenv("PROXMOX_TOPOLOGY_REGION", "")
	c.ProxmoxTopologyZone = getenv("PROXMOX_TOPOLOGY_ZONE", "")
	c.ProxmoxTemplateID = getenv("PROXMOX_TEMPLATE_ID", getenv("TEMPLATE_VMID", "104"))
	c.ProxmoxBridge = getenv("PROXMOX_BRIDGE", "vmbr0")

	// --- Network ---
	c.ControlPlaneEndpointIP = getenv("CONTROL_PLANE_ENDPOINT_IP", "10.27.192.20")
	c.ControlPlaneEndpointPort = getenv("CONTROL_PLANE_ENDPOINT_PORT", "6443")
	c.NodeIPRanges = getenv("NODE_IP_RANGES", "10.27.192.21-10.27.192.30")
	c.Gateway = getenv("GATEWAY", "10.27.192.78")
	c.IPPrefix = getenv("IP_PREFIX", "24")
	c.DNSServers = getenv("DNS_SERVERS", "8.8.8.8,8.8.4.4")
	c.DNSServersExplicit = envBool("DNS_SERVERS_EXPLICIT", false)
	c.GatewayExplicit = envBool("GATEWAY_EXPLICIT", false)
	c.IPPrefixExplicit = envBool("IP_PREFIX_EXPLICIT", false)
	c.NodeIPRangesExplicit = envBool("NODE_IP_RANGES_EXPLICIT", false)
	c.AllowedNodesExplicit = envBool("ALLOWED_NODES_EXPLICIT", false)
	c.AllowedNodes = getenv("ALLOWED_NODES", c.ProxmoxNode)
	c.VMSSHKeys = getenv("VM_SSH_KEYS", "")

	// --- Proxmox CSI credentials/storage ---
	c.ProxmoxCSIURL = getenv("PROXMOX_CSI_URL", "")
	c.ProxmoxCSITokenID = getenv("PROXMOX_CSI_TOKEN_ID", "")
	c.ProxmoxCSITokenSecret = getenv("PROXMOX_CSI_TOKEN_SECRET", "")
	c.ProxmoxCSIUserID = getenv("PROXMOX_CSI_USER_ID", "")
	c.ProxmoxCSITokenPrefix = getenv("PROXMOX_CSI_TOKEN_PREFIX", "csi")
	c.ProxmoxCSIInsecure = getenv("PROXMOX_CSI_INSECURE", c.ProxmoxAdminInsecure)
	c.ProxmoxCSIStorageClassName = getenv("PROXMOX_CSI_STORAGE_CLASS_NAME", "proxmox-data-xfs")
	c.ProxmoxCSIStorage = getenv("PROXMOX_CSI_STORAGE", "local-lvm")
	c.ProxmoxCloudinitStorage = getenv("PROXMOX_CLOUDINIT_STORAGE", "local")
	c.ProxmoxMemoryAdjustment = getenv("PROXMOX_MEMORY_ADJUSTMENT", "0")
	c.ProxmoxCSIReclaimPolicy = getenv("PROXMOX_CSI_RECLAIM_POLICY", "Delete")
	c.ProxmoxCSIFsType = getenv("PROXMOX_CSI_FSTYPE", "xfs")
	c.ProxmoxCSIDefaultClass = getenv("PROXMOX_CSI_DEFAULT_CLASS", "true")
	c.ProxmoxCAPIUserID = getenv("PROXMOX_CAPI_USER_ID", "")
	c.ProxmoxCAPITokenPrefix = getenv("PROXMOX_CAPI_TOKEN_PREFIX", "capi")

	// --- VM sizing ---
	c.ControlPlaneBootVolumeDevice = getenv("CONTROL_PLANE_BOOT_VOLUME_DEVICE", "scsi0")
	c.ControlPlaneBootVolumeSize = getenv("CONTROL_PLANE_BOOT_VOLUME_SIZE", "100")
	c.ControlPlaneNumSockets = getenv("CONTROL_PLANE_NUM_SOCKETS", "2")
	c.ControlPlaneNumCores = getenv("CONTROL_PLANE_NUM_CORES", "1")
	c.ControlPlaneMemoryMiB = getenv("CONTROL_PLANE_MEMORY_MIB", "8192")
	c.WorkerBootVolumeDevice = getenv("WORKER_BOOT_VOLUME_DEVICE", "scsi0")
	c.WorkerBootVolumeSize = getenv("WORKER_BOOT_VOLUME_SIZE", "100")
	c.WorkerNumSockets = getenv("WORKER_NUM_SOCKETS", "2")
	c.WorkerNumCores = getenv("WORKER_NUM_CORES", "4")
	c.WorkerMemoryMiB = getenv("WORKER_MEMORY_MIB", "16384")

	// --- Workload cluster ---
	c.WorkloadClusterName = getenv("WORKLOAD_CLUSTER_NAME", "capi-quickstart")
	c.WorkloadCiliumClusterID = getenv("WORKLOAD_CILIUM_CLUSTER_ID", "")
	c.WorkloadClusterNamespace = getenv("WORKLOAD_CLUSTER_NAMESPACE", "default")
	c.WorkloadClusterNameExplicit = envBoolLoose("WORKLOAD_CLUSTER_NAME_EXPLICIT", false)
	c.WorkloadClusterNamespaceExplicit = envBoolLoose("WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT", false)
	c.WorkloadKubernetesVersion = getenv("WORKLOAD_KUBERNETES_VERSION", "v1.35.0")
	c.ControlPlaneMachineCount = getenv("CONTROL_PLANE_MACHINE_COUNT", "1")
	c.WorkerMachineCount = getenv("WORKER_MACHINE_COUNT", "2")

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
