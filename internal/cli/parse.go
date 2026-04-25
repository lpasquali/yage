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

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
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
			c.ProxmoxBootstrapAdminSecretName = shiftVal(a)
		case "--capi-manifest":
			c.CAPIManifest = shiftVal(a)
			c.BootstrapCAPIManifestUserSet = true
		case "--regenerate-capi-manifest":
			c.BootstrapRegenerateCAPIManifest = true
		case "--bootstrap-config-file":
			c.ProxmoxBootstrapConfigFile = shiftVal(a)
		case "-p", "--purge":
			c.Purge = true
		case "-u", "--admin-username":
			c.ProxmoxAdminUsername = shiftVal(a)
		case "-t", "--admin-token":
			c.ProxmoxAdminToken = shiftVal(a)
		case "--proxmox-url":
			c.ProxmoxURL = shiftVal(a)
		case "--proxmox-token":
			c.ProxmoxToken = shiftVal(a)
		case "--proxmox-secret":
			c.ProxmoxSecret = shiftVal(a)
		case "-r", "--region":
			c.ProxmoxRegion = shiftVal(a)
		case "-n", "--node":
			c.ProxmoxNode = shiftVal(a)
		case "--template-id", "--template-vmid":
			c.ProxmoxTemplateID = shiftVal(a)
		case "--workload-control-plane-template-id":
			c.WorkloadControlPlaneTemplateID = shiftVal(a)
		case "--workload-worker-template-id":
			c.WorkloadWorkerTemplateID = shiftVal(a)
		case "--mgmt-control-plane-template-id":
			c.MgmtControlPlaneTemplateID = shiftVal(a)
		case "--mgmt-worker-template-id":
			c.MgmtWorkerTemplateID = shiftVal(a)
		case "--bridge":
			c.ProxmoxBridge = shiftVal(a)
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
			c.ProxmoxCSIURL = shiftVal(a)
		case "--csi-token-id":
			c.ProxmoxCSITokenID = shiftVal(a)
		case "--csi-token-secret":
			c.ProxmoxCSITokenSecret = shiftVal(a)
		case "--csi-user-id":
			c.ProxmoxCSIUserID = shiftVal(a)
		case "--csi-token-prefix":
			c.ProxmoxCSITokenPrefix = shiftVal(a)
		case "--csi-insecure":
			c.ProxmoxCSIInsecure = shiftVal(a)
		case "--csi-storage-class":
			c.ProxmoxCSIStorageClassName = shiftVal(a)
		case "--csi-storage":
			c.ProxmoxCSIStorage = shiftVal(a)
		case "--cloudinit-storage":
			c.ProxmoxCloudinitStorage = shiftVal(a)
		case "--memory-adjustment":
			c.ProxmoxMemoryAdjustment = shiftVal(a)
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
			c.ProxmoxCSIEnabled = false
		case "--proxmox-csi-version":
			c.ProxmoxCSIChartVersion = shiftVal(a)
		case "--disable-proxmox-csi-smoketest":
			c.ProxmoxCSISmokeEnabled = false
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
			c.ProxmoxCSIReclaimPolicy = shiftVal(a)
		case "--csi-fstype":
			c.ProxmoxCSIFsType = shiftVal(a)
		case "--csi-default-class":
			c.ProxmoxCSIDefaultClass = shiftVal(a)
		case "--capi-user-id":
			c.ProxmoxCAPIUserID = shiftVal(a)
		case "--capi-token-prefix":
			c.ProxmoxCAPITokenPrefix = shiftVal(a)
		case "--cluster-set-id":
			c.ClusterSetID = shiftVal(a)
		case "--recreate-proxmox-identities":
			c.RecreateProxmoxIdentities = true
		case "--recreate-proxmox-identities-scope":
			c.ProxmoxIdentityRecreateScope = shiftVal(a)
		case "--recreate-proxmox-identities-state-rm":
			c.ProxmoxIdentityRecreateStateRm = true
		case "--control-plane-boot-volume-device":
			c.ControlPlaneBootVolumeDevice = shiftVal(a)
		case "--control-plane-boot-volume-size":
			c.ControlPlaneBootVolumeSize = shiftVal(a)
		case "--control-plane-num-sockets":
			c.ControlPlaneNumSockets = shiftVal(a)
		case "--control-plane-num-cores":
			c.ControlPlaneNumCores = shiftVal(a)
		case "--control-plane-memory-mib":
			c.ControlPlaneMemoryMiB = shiftVal(a)
		case "--worker-boot-volume-device":
			c.WorkerBootVolumeDevice = shiftVal(a)
		case "--worker-boot-volume-size":
			c.WorkerBootVolumeSize = shiftVal(a)
		case "--worker-num-sockets":
			c.WorkerNumSockets = shiftVal(a)
		case "--worker-num-cores":
			c.WorkerNumCores = shiftVal(a)
		case "--worker-memory-mib":
			c.WorkerMemoryMiB = shiftVal(a)
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
		case "--allow-resource-overcommit":
			c.AllowResourceOvercommit = true
		case "--bootstrap-mode":
			c.BootstrapMode = strings.ToLower(shiftVal(a))
		case "--resource-budget-fraction":
			v := shiftVal(a)
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				c.ResourceBudgetFraction = f
			}
		case "--pivot-verify-timeout":
			c.PivotVerifyTimeout = shiftVal(a)
		case "--mgmt-cluster-name":
			c.MgmtClusterName = shiftVal(a)
		case "--mgmt-cluster-namespace":
			c.MgmtClusterNamespace = shiftVal(a)
		case "--mgmt-k8s-version":
			c.MgmtKubernetesVersion = shiftVal(a)
		case "--mgmt-cilium-cluster-id":
			c.MgmtCiliumClusterID = shiftVal(a)
		case "--mgmt-control-plane-machine-count":
			c.MgmtControlPlaneMachineCount = shiftVal(a)
		case "--mgmt-worker-machine-count":
			c.MgmtWorkerMachineCount = shiftVal(a)
		case "--mgmt-control-plane-endpoint-ip":
			c.MgmtControlPlaneEndpointIP = shiftVal(a)
		case "--mgmt-control-plane-endpoint-port":
			c.MgmtControlPlaneEndpointPort = shiftVal(a)
		case "--mgmt-node-ip-ranges":
			c.MgmtNodeIPRanges = shiftVal(a)
		case "--mgmt-capi-manifest":
			c.MgmtCAPIManifest = shiftVal(a)
		case "--mgmt-control-plane-num-sockets":
			c.MgmtControlPlaneNumSockets = shiftVal(a)
		case "--mgmt-control-plane-num-cores":
			c.MgmtControlPlaneNumCores = shiftVal(a)
		case "--mgmt-control-plane-memory-mib":
			c.MgmtControlPlaneMemoryMiB = shiftVal(a)
		case "--mgmt-control-plane-boot-volume-device":
			c.MgmtControlPlaneBootVolumeDevice = shiftVal(a)
		case "--mgmt-control-plane-boot-volume-size":
			c.MgmtControlPlaneBootVolumeSize = shiftVal(a)
		case "--capi-proxmox-machine-template-spec-rev-skip":
			c.CAPIProxmoxMachineTemplateSpecRev = false
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
		case "-h", "--help":
			PrintUsage(nil)
			os.Exit(0)
		default:
			logx.Die("Unknown option: %s", a)
		}
	}
}
