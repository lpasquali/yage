// Package cli mirrors the bash parse_options() flag surface exactly.
//
// Every case in the bash `case` statement has a matching case here, with the
// same semantics:
//   - boolean flags set the corresponding Config field
//   - valued flags consume one argument
//   - two flags (--kind-backup, --argocd-print-access, --argocd-port-forward,
//     --workload-rollout) accept an OPTIONAL positional argument that must
//     not start with "--"
//   - --template-vmid is a deprecated alias for --template-id
//   - --argocd-version is hard-removed; it emits a Die message
package cli

import (
	"os"
	"strconv"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// Parse consumes argv (without program name) and writes results into c.
// On --help it prints usage and returns ExitHelp so main() can exit(0).
// On unknown flags it calls logx.Die which exits(1), matching bash behavior.
func Parse(c *config.Config, argv []string) {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		// shiftVal pulls the value for a "--flag <v>" pair. Dies if missing.
		shiftVal := func(name string) string {
			if i+1 >= len(argv) {
				logx.Die("Missing value for %s", name)
			}
			i++
			return argv[i]
		}
		// optPositional: peek ahead; if argv[i+1] exists and does not start
		// with "--", consume it and return; otherwise return "" and leave i.
		optPositional := func() (string, bool) {
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
				i++
				return argv[i], true
			}
			return "", false
		}

		switch a {
		case "-b", "--build-all":
			c.BuildAll = true
		case "-f", "--force":
			c.Force = true
		case "--no-delete-kind":
			c.NoDeleteKind = true
		case "--persist-local-secrets":
			c.BootstrapPersistLocalSecrets = true
		case "--kind-cluster-name":
			c.KindClusterName = shiftVal(a)
		case "--kind-config":
			c.KindConfig = shiftVal(a)
		case "--proxmox-bootstrap-admin-secret":
			c.Providers.Proxmox.BootstrapAdminSecretName = shiftVal(a)
		case "--capi-manifest":
			c.CAPIManifest = shiftVal(a)
			c.BootstrapCAPIManifestUserSet = true
		case "--regenerate-capi-manifest":
			c.BootstrapRegenerateCAPIManifest = true
		case "--bootstrap-config-file":
			c.Providers.Proxmox.BootstrapConfigFile = shiftVal(a)
		case "-p", "--purge":
			c.Purge = true
		case "-u", "--admin-username":
			c.Providers.Proxmox.AdminUsername = shiftVal(a)
		case "-t", "--admin-token":
			c.Providers.Proxmox.AdminToken = shiftVal(a)
		case "--proxmox-url":
			c.Providers.Proxmox.URL = shiftVal(a)
		case "--proxmox-token":
			c.Providers.Proxmox.Token = shiftVal(a)
		case "--proxmox-secret":
			c.Providers.Proxmox.Secret = shiftVal(a)
		case "-r", "--region":
			c.Providers.Proxmox.Region = shiftVal(a)
		case "-n", "--node":
			c.Providers.Proxmox.Node = shiftVal(a)
		case "--template-id", "--template-vmid":
			c.Providers.Proxmox.TemplateID = shiftVal(a)
		case "--workload-control-plane-template-id":
			c.WorkloadControlPlaneTemplateID = shiftVal(a)
		case "--workload-worker-template-id":
			c.WorkloadWorkerTemplateID = shiftVal(a)
		case "--mgmt-control-plane-template-id":
			c.Providers.Proxmox.Mgmt.ControlPlaneTemplateID = shiftVal(a)
		case "--mgmt-worker-template-id":
			c.Providers.Proxmox.Mgmt.WorkerTemplateID = shiftVal(a)
		case "--bridge":
			c.Providers.Proxmox.Bridge = shiftVal(a)
		case "--proxmox-pool":
			c.Providers.Proxmox.Pool = shiftVal(a)
		case "--mgmt-proxmox-pool":
			c.Providers.Proxmox.Mgmt.Pool = shiftVal(a)
		case "--control-plane-endpoint-ip":
			c.ControlPlaneEndpointIP = shiftVal(a)
		case "--control-plane-endpoint-port":
			c.ControlPlaneEndpointPort = shiftVal(a)
		case "--node-ip-ranges":
			c.NodeIPRangesExplicit = true
			c.NodeIPRanges = shiftVal(a)
		case "--gateway":
			c.GatewayExplicit = true
			c.Gateway = shiftVal(a)
		case "--ip-prefix":
			c.IPPrefixExplicit = true
			c.IPPrefix = shiftVal(a)
		case "--dns-servers":
			c.DNSServersExplicit = true
			c.DNSServers = shiftVal(a)
		case "--allowed-nodes":
			c.AllowedNodesExplicit = true
			c.AllowedNodes = shiftVal(a)
		case "--csi-url":
			c.Providers.Proxmox.CSIURL = shiftVal(a)
		case "--csi-token-id":
			c.Providers.Proxmox.CSITokenID = shiftVal(a)
		case "--csi-token-secret":
			c.Providers.Proxmox.CSITokenSecret = shiftVal(a)
		case "--csi-user-id":
			c.Providers.Proxmox.CSIUserID = shiftVal(a)
		case "--csi-token-prefix":
			c.Providers.Proxmox.CSITokenPrefix = shiftVal(a)
		case "--csi-insecure":
			c.Providers.Proxmox.CSIInsecure = shiftVal(a)
		case "--csi-storage-class":
			c.Providers.Proxmox.CSIStorageClassName = shiftVal(a)
		case "--csi-storage":
			c.Providers.Proxmox.CSIStorage = shiftVal(a)
		case "--cloudinit-storage":
			c.Providers.Proxmox.CloudinitStorage = shiftVal(a)
		case "--memory-adjustment":
			c.Providers.Proxmox.MemoryAdjustment = shiftVal(a)
		case "--disable-argocd":
			c.ArgoCDEnabled = false
		case "--disable-workload-argocd":
			c.WorkloadArgoCDEnabled = false
		case "--argocd-version":
			logx.Die("--argocd-version was removed; use --argocd-app-version for Argo CD image / ArgoCD CR spec.version (ARGOCD_VERSION).")
		case "--argocd-app-version":
			c.ArgoCDVersion = shiftVal(a)
		case "--argocd-server-insecure":
			c.ArgoCDServerInsecure = shiftVal(a)
		case "--workload-gitops-mode":
			v := shiftVal(a)
			if v != "caaph" {
				logx.Die("Only --workload-gitops-mode caaph is supported (got: %s). The legacy kind-argocd/management Argo path was removed.", v)
			}
			c.WorkloadGitopsMode = "caaph"
		case "--workload-app-of-apps-git-url":
			c.WorkloadAppOfAppsGitURL = shiftVal(a)
		case "--workload-app-of-apps-git-path":
			c.WorkloadAppOfAppsGitPath = shiftVal(a)
		case "--workload-app-of-apps-git-ref":
			c.WorkloadAppOfAppsGitRef = shiftVal(a)
		case "--argocd-print-access", "--argocd-print-access-only":
			c.ArgoCDPrintAccessStandalone = true
			if v, ok := optPositional(); ok {
				switch v {
				case "workload":
					c.ArgoCDPrintAccessTarget = "workload"
				case "kind", "both":
					logx.Warn("Argo CD on the management (kind) cluster is not used by this script — use workload only.")
					c.ArgoCDPrintAccessTarget = "workload"
				}
			}
		case "--argocd-port-forward", "--argocd-port-forward-only":
			c.ArgoCDPortForwardStandalone = true
			if v, ok := optPositional(); ok {
				switch v {
				case "workload":
					c.ArgoCDPortForwardTarget = "workload"
					if c.ArgoCDPrintAccessTarget == "" {
						c.ArgoCDPrintAccessTarget = "workload"
					}
				case "kind", "both":
					logx.Warn("Port-forward targets the provisioned cluster only (workload) — not kind.")
					c.ArgoCDPortForwardTarget = "workload"
				}
			}
		case "--workload-rollout":
			c.WorkloadRolloutStandalone = true
			if v, ok := optPositional(); ok {
				switch v {
				case "argocd", "capi", "all":
					c.WorkloadRolloutMode = v
				}
			}
		case "--workload-rollout-no-wait":
			c.WorkloadRolloutNoWait = true
		case "--kind-backup":
			c.BootstrapKindStateOp = "backup"
			if v, ok := optPositional(); ok {
				c.BootstrapKindBackupOut = v
			}
		case "--kind-restore":
			v, ok := optPositional()
			if !ok {
				logx.Die("--kind-restore requires an archive path (.tar.gz, .tar.gz.age, or .tar.gz.enc)")
			}
			c.BootstrapKindStateOp = "restore"
			c.BootstrapKindStatePath = v
		case "--disable-proxmox-csi":
			c.Providers.Proxmox.CSIEnabled = false
		case "--proxmox-csi-version":
			c.Providers.Proxmox.CSIChartVersion = shiftVal(a)
		case "--disable-proxmox-csi-smoketest":
			c.Providers.Proxmox.CSISmokeEnabled = false
		case "--disable-argocd-workload-postsync-hooks":
			c.ArgoWorkloadPostsyncHooksEnabled = false
		case "--argocd-workload-postsync-hooks-git-url":
			c.ArgoWorkloadPostsyncHooksGitURL = shiftVal(a)
		case "--argocd-workload-postsync-hooks-git-path":
			c.ArgoWorkloadPostsyncHooksGitPath = shiftVal(a)
		case "--argocd-workload-postsync-hooks-git-ref":
			c.ArgoWorkloadPostsyncHooksGitRef = shiftVal(a)
		case "--disable-kyverno":
			c.KyvernoEnabled = false
		case "--kyverno-version":
			c.KyvernoChartVersion = shiftVal(a)
		case "--disable-cert-manager":
			c.CertManagerEnabled = false
		case "--cert-manager-version":
			c.CertManagerChartVersion = shiftVal(a)
		case "--disable-crossplane":
			c.CrossplaneEnabled = false
		case "--crossplane-version":
			c.CrossplaneChartVersion = shiftVal(a)
		case "--disable-cnpg":
			c.CNPGEnabled = false
		case "--cnpg-version":
			c.CNPGChartVersion = shiftVal(a)
		case "--disable-victoriametrics":
			c.VictoriaMetricsEnabled = false
		case "--victoriametrics-version":
			c.VictoriaMetricsChartVersion = shiftVal(a)
		case "--csi-reclaim-policy":
			c.Providers.Proxmox.CSIReclaimPolicy = shiftVal(a)
		case "--csi-fstype":
			c.Providers.Proxmox.CSIFsType = shiftVal(a)
		case "--csi-default-class":
			c.Providers.Proxmox.CSIDefaultClass = shiftVal(a)
		case "--capi-user-id":
			c.Providers.Proxmox.CAPIUserID = shiftVal(a)
		case "--capi-token-prefix":
			c.Providers.Proxmox.CAPITokenPrefix = shiftVal(a)
		case "--cluster-set-id":
			c.ClusterSetID = shiftVal(a)
		case "--recreate-proxmox-identities":
			c.Providers.Proxmox.RecreateIdentities = true
		case "--recreate-proxmox-identities-scope":
			c.Providers.Proxmox.IdentityRecreateScope = shiftVal(a)
		case "--recreate-proxmox-identities-state-rm":
			c.Providers.Proxmox.IdentityRecreateStateRm = true
		case "--control-plane-boot-volume-device":
			c.Providers.Proxmox.ControlPlaneBootVolumeDevice = shiftVal(a)
		case "--control-plane-boot-volume-size":
			c.Providers.Proxmox.ControlPlaneBootVolumeSize = shiftVal(a)
		case "--control-plane-num-sockets":
			c.Providers.Proxmox.ControlPlaneNumSockets = shiftVal(a)
		case "--control-plane-num-cores":
			c.Providers.Proxmox.ControlPlaneNumCores = shiftVal(a)
		case "--control-plane-memory-mib":
			c.Providers.Proxmox.ControlPlaneMemoryMiB = shiftVal(a)
		case "--worker-boot-volume-device":
			c.Providers.Proxmox.WorkerBootVolumeDevice = shiftVal(a)
		case "--worker-boot-volume-size":
			c.Providers.Proxmox.WorkerBootVolumeSize = shiftVal(a)
		case "--worker-num-sockets":
			c.Providers.Proxmox.WorkerNumSockets = shiftVal(a)
		case "--worker-num-cores":
			c.Providers.Proxmox.WorkerNumCores = shiftVal(a)
		case "--worker-memory-mib":
			c.Providers.Proxmox.WorkerMemoryMiB = shiftVal(a)
		case "--workload-cluster-name":
			c.WorkloadClusterName = shiftVal(a)
			c.WorkloadClusterNameExplicit = true
		case "--workload-cluster-namespace":
			c.WorkloadClusterNamespace = shiftVal(a)
			c.WorkloadClusterNamespaceExplicit = true
		case "--workload-cilium-cluster-id":
			c.WorkloadCiliumClusterID = shiftVal(a)
		case "--workload-k8s-version":
			c.WorkloadKubernetesVersion = shiftVal(a)
		case "--control-plane-count":
			c.ControlPlaneMachineCount = shiftVal(a)
		case "--worker-count":
			c.WorkerMachineCount = shiftVal(a)
		case "--pivot":
			c.PivotEnabled = true
		case "--no-pivot":
			c.PivotEnabled = false
		case "--pivot-keep-kind":
			c.PivotKeepKind = true
		case "--pivot-dry-run":
			c.PivotDryRun = true
		case "--dry-run":
			c.DryRun = true
		case "--cost-compare":
			c.CostCompare = true
		case "--budget-usd-month":
			if f, err := strconv.ParseFloat(shiftVal(a), 64); err == nil {
				c.BudgetUSDMonth = f
			}
		case "--print-pricing-setup":
			// Special: not really a flag, more of a subcommand.
			// Print the IAM/token setup snippet for the named
			// vendor (or all vendors with "all") and exit.
			c.PrintPricingSetup = strings.ToLower(strings.TrimSpace(shiftVal(a)))
		case "--xapiri":
			// Launch the interactive configuration TUI and exit.
			// See package internal/xapiri for the cultural note.
			c.Xapiri = true
		case "--airgapped":
			// Disable every internet-requiring path (cloud providers,
			// pricing fetchers, geo + FX). On-prem only. See §17.
			c.Airgapped = true
		case "--allow-resource-overcommit":
			c.AllowResourceOvercommit = true
		case "--overcommit-tolerance-pct":
			if f, err := strconv.ParseFloat(shiftVal(a), 64); err == nil && f >= 0 {
				c.OvercommitTolerancePct = f
			}
		case "--hardware-cost-usd":
			if f, err := strconv.ParseFloat(shiftVal(a), 64); err == nil && f >= 0 {
				c.HardwareCostUSD = f
			}
		case "--hardware-useful-life-years":
			if f, err := strconv.ParseFloat(shiftVal(a), 64); err == nil && f > 0 {
				c.HardwareUsefulLifeYears = f
			}
		case "--hardware-watts":
			if f, err := strconv.ParseFloat(shiftVal(a), 64); err == nil && f >= 0 {
				c.HardwareWatts = f
			}
		case "--hardware-kwh-rate-usd":
			if f, err := strconv.ParseFloat(shiftVal(a), 64); err == nil && f >= 0 {
				c.HardwareKWHRateUSD = f
			}
		case "--hardware-support-usd-month":
			if f, err := strconv.ParseFloat(shiftVal(a), 64); err == nil && f >= 0 {
				c.HardwareSupportUSDMonth = f
			}
		case "--system-apps-cpu-millicores":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil {
				c.SystemAppsCPUMillicores = n
			}
		case "--system-apps-memory-mib":
			if n, err := strconv.ParseInt(shiftVal(a), 10, 64); err == nil {
				c.SystemAppsMemoryMiB = n
			}
		case "--bootstrap-mode":
			c.BootstrapMode = strings.ToLower(shiftVal(a))
		case "--aws-mode":
			c.Providers.AWS.Mode = strings.ToLower(shiftVal(a))
		case "--aws-fargate-pod-count":
			c.Providers.AWS.FargatePodCount = shiftVal(a)
		case "--aws-fargate-pod-cpu":
			c.Providers.AWS.FargatePodCPU = shiftVal(a)
		case "--aws-fargate-pod-memory-gib":
			c.Providers.AWS.FargatePodMemoryGiB = shiftVal(a)
		case "--aws-overhead-tier":
			c.Providers.AWS.OverheadTier = strings.ToLower(shiftVal(a))
		case "--aws-nat-gateway-count":
			c.Providers.AWS.NATGatewayCount = shiftVal(a)
		case "--aws-alb-count":
			c.Providers.AWS.ALBCount = shiftVal(a)
		case "--aws-nlb-count":
			c.Providers.AWS.NLBCount = shiftVal(a)
		case "--aws-data-transfer-gb":
			c.Providers.AWS.DataTransferGB = shiftVal(a)
		case "--aws-cloudwatch-logs-gb":
			c.Providers.AWS.CloudWatchLogsGB = shiftVal(a)
		case "--azure-mode":
			c.Providers.Azure.Mode = strings.ToLower(shiftVal(a))
		case "--azure-location":
			c.Providers.Azure.Location = shiftVal(a)
		case "--azure-control-plane-machine-type":
			c.Providers.Azure.ControlPlaneMachineType = shiftVal(a)
		case "--azure-node-machine-type":
			c.Providers.Azure.NodeMachineType = shiftVal(a)
		case "--azure-overhead-tier":
			c.Providers.Azure.OverheadTier = strings.ToLower(shiftVal(a))
		case "--gcp-mode":
			c.Providers.GCP.Mode = strings.ToLower(shiftVal(a))
		case "--gcp-region":
			c.Providers.GCP.Region = shiftVal(a)
		case "--gcp-project":
			c.Providers.GCP.Project = shiftVal(a)
		case "--gcp-control-plane-machine-type":
			c.Providers.GCP.ControlPlaneMachineType = shiftVal(a)
		case "--gcp-node-machine-type":
			c.Providers.GCP.NodeMachineType = shiftVal(a)
		case "--gcp-overhead-tier":
			c.Providers.GCP.OverheadTier = strings.ToLower(shiftVal(a))
		case "--hetzner-control-plane-machine-type":
			c.Providers.Hetzner.ControlPlaneMachineType = shiftVal(a)
		case "--hetzner-node-machine-type":
			c.Providers.Hetzner.NodeMachineType = shiftVal(a)
		case "--hetzner-location":
			c.Providers.Hetzner.Location = shiftVal(a)
		case "--hetzner-overhead-tier":
			c.Providers.Hetzner.OverheadTier = strings.ToLower(shiftVal(a))
		case "--resource-budget-fraction":
			v := shiftVal(a)
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				c.ResourceBudgetFraction = f
			}
		case "--pivot-verify-timeout":
			c.PivotVerifyTimeout = shiftVal(a)
		case "--mgmt-cluster-name":
			c.Mgmt.ClusterName = shiftVal(a)
		case "--mgmt-cluster-namespace":
			c.Mgmt.ClusterNamespace = shiftVal(a)
		case "--mgmt-k8s-version":
			c.Mgmt.KubernetesVersion = shiftVal(a)
		case "--mgmt-cilium-cluster-id":
			c.Mgmt.CiliumClusterID = shiftVal(a)
		case "--mgmt-control-plane-machine-count":
			c.Mgmt.ControlPlaneMachineCount = shiftVal(a)
		case "--mgmt-worker-machine-count":
			c.Mgmt.WorkerMachineCount = shiftVal(a)
		case "--mgmt-control-plane-endpoint-ip":
			c.Mgmt.ControlPlaneEndpointIP = shiftVal(a)
		case "--mgmt-control-plane-endpoint-port":
			c.Mgmt.ControlPlaneEndpointPort = shiftVal(a)
		case "--mgmt-node-ip-ranges":
			c.Mgmt.NodeIPRanges = shiftVal(a)
		case "--mgmt-capi-manifest":
			c.Mgmt.CAPIManifest = shiftVal(a)
		case "--mgmt-control-plane-num-sockets":
			c.Providers.Proxmox.Mgmt.ControlPlaneNumSockets = shiftVal(a)
		case "--mgmt-control-plane-num-cores":
			c.Providers.Proxmox.Mgmt.ControlPlaneNumCores = shiftVal(a)
		case "--mgmt-control-plane-memory-mib":
			c.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB = shiftVal(a)
		case "--mgmt-control-plane-boot-volume-device":
			c.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeDevice = shiftVal(a)
		case "--mgmt-control-plane-boot-volume-size":
			c.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize = shiftVal(a)
		case "--capi-proxmox-machine-template-spec-rev-skip":
			c.Providers.Proxmox.CAPIMachineTemplateSpecRev = false
		case "--cilium-wait-duration":
			c.CiliumWaitDuration = shiftVal(a)
		case "--cilium-ingress":
			c.CiliumIngress = shiftVal(a)
		case "--cilium-kube-proxy-replacement":
			c.CiliumKubeProxyReplacement = shiftVal(a)
		case "--cilium-lb-ipam":
			c.CiliumLBIPAM = shiftVal(a)
		case "--cilium-lb-ipam-pool-cidr":
			c.CiliumLBIPAMPoolCIDR = shiftVal(a)
		case "--cilium-lb-ipam-pool-start":
			c.CiliumLBIPAMPoolStart = shiftVal(a)
		case "--cilium-lb-ipam-pool-stop":
			c.CiliumLBIPAMPoolStop = shiftVal(a)
		case "--cilium-lb-ipam-pool-name":
			c.CiliumLBIPAMPoolName = shiftVal(a)
		case "--cilium-ipam-cluster-pool-ipv4":
			c.CiliumIPAMClusterPoolIPv4 = shiftVal(a)
		case "--cilium-ipam-cluster-pool-ipv4-mask-size":
			c.CiliumIPAMClusterPoolIPv4MaskSize = shiftVal(a)
		case "--cilium-gateway-api":
			c.CiliumGatewayAPIEnabled = shiftVal(a)
		case "--argocd-disable-operator-ingress":
			c.ArgoCDDisableOperatorManagedIngress = shiftVal(a)
		case "--cilium-hubble":
			c.CiliumHubble = shiftVal(a)
		case "--cilium-hubble-ui":
			c.CiliumHubbleUI = shiftVal(a)
		case "--exp-cluster-resource-set":
			c.ExpClusterResourceSet = shiftVal(a)
		case "--cluster-topology":
			c.ClusterTopology = shiftVal(a)
		case "--exp-kubeadm-bootstrap-format-ignition":
			c.ExpKubeadmBootstrapFormatIgnition = shiftVal(a)
		case "--disable-metrics-server":
			c.EnableMetricsServer = false
		case "--disable-workload-metrics-server":
			c.EnableWorkloadMetricsServer = false
		case "--infra-provider", "--infrastructure-provider":
			// Sets the active infrastructure provider. Mirrors the
			// INFRA_PROVIDER env var (which Load() reads at startup).
			// usage.txt has long referenced --infrastructure-provider as
			// if it existed — this commit makes it real.
			c.InfraProvider = strings.ToLower(strings.TrimSpace(shiftVal(a)))
		case "-h", "--help":
			PrintUsage(nil)
			os.Exit(0)
		default:
			logx.Die("Unknown option: %s", a)
		}
	}
}
