// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package cli parses the yage flag surface.
//
// Semantics:
//   - boolean flags set the corresponding Config field
//   - valued flags consume one argument
//   - two flags (--kind-backup, --argocd-print-access, --argocd-port-forward,
//     --workload-rollout) accept an OPTIONAL positional argument that must
//     not start with "--"
//   - --template-vmid is an alias for --template-id
//   - --argocd-version emits a Die message (not a recognized flag)
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
// On unknown flags it calls logx.Die which exits(1).
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
		case "--config":
			// Already consumed by config.ConfigFilePath before Parse runs.
			// Consume the argument so it isn't treated as an unknown flag.
			c.ConfigFile = shiftVal(a)
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
		case "--proxmox-capi-token":
			c.Providers.Proxmox.CAPIToken = shiftVal(a)
		case "--proxmox-capi-secret":
			c.Providers.Proxmox.CAPISecret = shiftVal(a)
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
		case "--vm-ssh-keys":
			c.VMSSHKeys = shiftVal(a)
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
			c.ArgoCD.Enabled = false
		case "--disable-workload-argocd":
			c.ArgoCD.WorkloadEnabled = false
		case "--argocd-version":
			logx.Die("--argocd-version was removed; use --argocd-app-version for Argo CD image / ArgoCD CR spec.version (ARGOCD_VERSION).")
		case "--argocd-app-version":
			c.ArgoCD.Version = shiftVal(a)
		case "--argocd-server-insecure":
			c.ArgoCD.ServerInsecure = shiftVal(a)
		case "--workload-gitops-mode":
			v := shiftVal(a)
			if v != "caaph" {
				logx.Die("Only --workload-gitops-mode caaph is supported (got: %s).", v)
			}
			c.WorkloadGitopsMode = "caaph"
		case "--workload-app-of-apps-git-url":
			c.ArgoCD.AppOfAppsGitURL = shiftVal(a)
		case "--workload-app-of-apps-git-path":
			c.ArgoCD.AppOfAppsGitPath = shiftVal(a)
		case "--workload-app-of-apps-git-ref":
			c.ArgoCD.AppOfAppsGitRef = shiftVal(a)
		case "--argocd-print-access", "--argocd-print-access-only":
			c.ArgoCD.PrintAccessStandalone = true
			if v, ok := optPositional(); ok {
				switch v {
				case "workload":
					c.ArgoCD.PrintAccessTarget = "workload"
				case "kind", "both":
					logx.Warn("Argo CD on the management (kind) cluster is not used by this script — use workload only.")
					c.ArgoCD.PrintAccessTarget = "workload"
				}
			}
		case "--argocd-port-forward", "--argocd-port-forward-only":
			c.ArgoCD.PortForwardStandalone = true
			if v, ok := optPositional(); ok {
				switch v {
				case "workload":
					c.ArgoCD.PortForwardTarget = "workload"
					if c.ArgoCD.PrintAccessTarget == "" {
						c.ArgoCD.PrintAccessTarget = "workload"
					}
				case "kind", "both":
					logx.Warn("Port-forward targets the provisioned cluster only (workload) — not kind.")
					c.ArgoCD.PortForwardTarget = "workload"
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
			c.ArgoCD.PostsyncHooksEnabled = false
		case "--argocd-workload-postsync-hooks-git-url":
			c.ArgoCD.PostsyncHooksGitURL = shiftVal(a)
		case "--argocd-workload-postsync-hooks-git-path":
			c.ArgoCD.PostsyncHooksGitPath = shiftVal(a)
		case "--argocd-workload-postsync-hooks-git-ref":
			c.ArgoCD.PostsyncHooksGitRef = shiftVal(a)
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
			// §20: the canonical home for default-class is the top-
			// level cfg.CSI.DefaultClass (multi-driver registry).
			// Mirror into Providers.Proxmox.CSIDefaultClass so the
			// Proxmox CSI install path keeps reading the same value.
			v := shiftVal(a)
			c.CSI.DefaultClass = v
			c.Providers.Proxmox.CSIDefaultClass = v
		case "--csi-driver":
			// Repeatable: each occurrence appends one driver name to
			// cfg.CSI.Drivers. Empty value rejects (consistent with
			// shiftVal's missing-value Die for other flags).
			c.CSI.Drivers = append(c.CSI.Drivers, shiftVal(a))
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
			c.Pivot.Enabled = true
		case "--no-pivot":
			c.Pivot.Enabled = false
		case "--pivot-keep-kind":
			c.Pivot.KeepKind = true
		case "--pivot-dry-run":
			c.Pivot.DryRun = true
		case "--stop-before-workload":
			// Exit after the pivot completes but before the workload
			// manifest is applied. Integration-test escape hatch.
			c.Pivot.StopBeforeWorkload = true
		case "--dry-run":
			c.DryRun = true
		case "--cost-compare":
			c.CostCompare = true
		case "--skip-providers":
			// Comma-separated registry names to omit from the cost
			// compare table. Doesn't affect the active --infra-provider
			// — only the comparison view filters them. Env:
			// YAGE_SKIP_PROVIDERS.
			c.SkipProviders = strings.TrimSpace(shiftVal(a))
		case "--allowed-providers":
			// Comma-separated allowlist for the cost compare table.
			// Inverse of --skip-providers. Composes with it (allowlist
			// applies first; --skip-providers subtracts from the
			// result). Env: YAGE_ALLOWED_PROVIDERS.
			c.AllowedProviders = strings.TrimSpace(shiftVal(a))
		case "--no-managed-postgres":
			// Force in-cluster CloudNativePG even when the active
			// vendor offers managed Postgres. Default uses the
			// vendor's SaaS DB (RDS/Aurora/Cloud SQL/etc).
			// Env: YAGE_USE_MANAGED_POSTGRES.
			c.UseManagedPostgres = false
		case "--postgres-cpu-millicores":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.PostgresCPUMillicoresOverride = n
			}
		case "--postgres-memory-mib":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.PostgresMemoryMiBOverride = n
			}
		case "--postgres-volume-gb":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.PostgresVolumeGBOverride = n
			}
		case "--mq-cpu-millicores":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.MQCPUMillicoresOverride = n
			}
		case "--mq-memory-mib":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.MQMemoryMiBOverride = n
			}
		case "--mq-volume-gb":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.MQVolumeGBOverride = n
			}
		case "--objstore-cpu-millicores":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.ObjStoreCPUMillicoresOverride = n
			}
		case "--objstore-memory-mib":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.ObjStoreMemoryMiBOverride = n
			}
		case "--objstore-volume-gb":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.ObjStoreVolumeGBOverride = n
			}
		case "--cache-cpu-millicores":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.CacheCPUMillicoresOverride = n
			}
		case "--cache-memory-mib":
			if n, err := strconv.Atoi(shiftVal(a)); err == nil && n >= 0 {
				c.CacheMemoryMiBOverride = n
			}
		case "--budget-usd-month":
			if f, err := strconv.ParseFloat(shiftVal(a), 64); err == nil {
				c.BudgetUSDMonth = f
			}
		case "--data-center-location":
			// ISO-3166 alpha-2 country code (e.g. IT, DE, US). Drives
			// nearest-region defaults for every provider with a
			// centroid table AND the active taller currency. See
			// CostCurrency.DataCenterLocation for the resolution
			// order. Env: YAGE_DATA_CENTER_LOCATION.
			c.Cost.Currency.DataCenterLocation = strings.ToUpper(
				strings.TrimSpace(shiftVal(a)))
		case "--print-pricing-setup":
			// Special: not really a flag, more of a subcommand.
			// Print the IAM/token setup snippet for the named
			// vendor (or all vendors with "all") and exit.
			c.PrintPricingSetup = strings.ToLower(strings.TrimSpace(shiftVal(a)))
		case "--print-command":
			// Standalone subcommand: render the equivalent `yage
			// <flags>` invocation and exit. Optional next-arg picks
			// the sensitive-value mode (env|raw|masked); default
			// "env" emits $VAR refs so the output is shell-runnable
			// without committing secrets to the pipeline.
			c.PrintCommand = "env"
			if v, ok := optPositional(); ok {
				lv := strings.ToLower(strings.TrimSpace(v))
				if lv == "env" || lv == "raw" || lv == "masked" {
					c.PrintCommand = lv
				} else {
					logx.Die("--print-command mode must be one of: env, raw, masked")
				}
			}
		case "--xapiri":
			// Launch the interactive configuration TUI and exit.
			// See package internal/xapiri for the cultural note.
			c.Xapiri = true
		case "--airgapped":
			// Disable every internet-requiring path (cloud providers,
			// pricing fetchers, geo + FX). On-prem only. See §17.
			c.Airgapped = true
		case "--image-registry-mirror":
			// Internal-mirror prefix for CAPI provider images (used
			// in airgapped deployments). See §17 follow-up.
			c.ImageRegistryMirror = strings.TrimRight(strings.TrimSpace(shiftVal(a)), "/")
		case "--internal-ca-bundle":
			// PEM bundle path; honored by every yage HTTP call and
			// every child process via SSL_CERT_FILE. §17 / §21.4.
			c.InternalCABundle = strings.TrimSpace(shiftVal(a))
		case "--helm-repo-mirror":
			// Single base URL that yage rewrites every chart-repo
			// reference onto. Strip trailing slash so the rewriter's
			// concat is unambiguous. §17 / §21.4.
			c.HelmRepoMirror = strings.TrimRight(strings.TrimSpace(shiftVal(a)), "/")
		case "--node-image":
			// kind worker base-image override (kindest/node:vX.Y.Z).
			// §17 / §21.4.
			c.NodeImage = strings.TrimSpace(shiftVal(a))
		case "--allow-resource-overcommit":
			c.Capacity.AllowOvercommit = true
		case "--overcommit-tolerance-pct":
			if f, err := strconv.ParseFloat(shiftVal(a), 64); err == nil && f >= 0 {
				c.Capacity.OvercommitTolerancePct = f
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
				c.Capacity.SystemAppsCPUMillicores = n
			}
		case "--system-apps-memory-mib":
			if n, err := strconv.ParseInt(shiftVal(a), 10, 64); err == nil {
				c.Capacity.SystemAppsMemoryMiB = n
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
		case "--azure-subscription-id":
			c.Providers.Azure.SubscriptionID = shiftVal(a)
		case "--azure-tenant-id":
			c.Providers.Azure.TenantID = shiftVal(a)
		case "--azure-resource-group":
			c.Providers.Azure.ResourceGroup = shiftVal(a)
		case "--azure-vnet-name":
			c.Providers.Azure.VNetName = shiftVal(a)
		case "--azure-subnet-name":
			c.Providers.Azure.SubnetName = shiftVal(a)
		case "--azure-client-id":
			c.Providers.Azure.ClientID = shiftVal(a)
		case "--azure-identity-model":
			c.Providers.Azure.IdentityModel = strings.ToLower(shiftVal(a))
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
		case "--gcp-network-name":
			c.Providers.GCP.Network = shiftVal(a)
		case "--gcp-image-family":
			c.Providers.GCP.ImageFamily = shiftVal(a)
		case "--gcp-identity-model":
			c.Providers.GCP.IdentityModel = strings.ToLower(shiftVal(a))
		case "--openstack-cloud":
			c.Providers.OpenStack.Cloud = shiftVal(a)
		case "--openstack-project-name":
			c.Providers.OpenStack.ProjectName = shiftVal(a)
		case "--openstack-region":
			c.Providers.OpenStack.Region = shiftVal(a)
		case "--openstack-failure-domain":
			c.Providers.OpenStack.FailureDomain = shiftVal(a)
		case "--openstack-image-name":
			c.Providers.OpenStack.ImageName = shiftVal(a)
		case "--openstack-control-plane-flavor":
			c.Providers.OpenStack.ControlPlaneFlavor = shiftVal(a)
		case "--openstack-worker-flavor":
			c.Providers.OpenStack.WorkerFlavor = shiftVal(a)
		case "--openstack-dns-nameservers":
			c.Providers.OpenStack.DNSNameservers = shiftVal(a)
		case "--openstack-ssh-key-name":
			c.Providers.OpenStack.SSHKeyName = shiftVal(a)
		case "--vsphere-server":
			c.Providers.Vsphere.Server = shiftVal(a)
		case "--vsphere-datacenter":
			c.Providers.Vsphere.Datacenter = shiftVal(a)
		case "--vsphere-folder":
			c.Providers.Vsphere.Folder = shiftVal(a)
		case "--vsphere-resource-pool":
			c.Providers.Vsphere.ResourcePool = shiftVal(a)
		case "--vsphere-datastore":
			c.Providers.Vsphere.Datastore = shiftVal(a)
		case "--vsphere-network":
			c.Providers.Vsphere.Network = shiftVal(a)
		case "--vsphere-template":
			c.Providers.Vsphere.Template = shiftVal(a)
		case "--vsphere-tls-thumbprint":
			c.Providers.Vsphere.TLSThumbprint = shiftVal(a)
		case "--vsphere-username":
			c.Providers.Vsphere.Username = shiftVal(a)
		case "--vsphere-password":
			c.Providers.Vsphere.Password = shiftVal(a)
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
				c.Capacity.ResourceBudgetFraction = f
			}
		case "--pivot-verify-timeout":
			c.Pivot.VerifyTimeout = shiftVal(a)
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
			c.ArgoCD.DisableOperatorManagedIngress = shiftVal(a)
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
			// No silent default — main() rejects an empty value and
			// directs the user at --xapiri or this flag (§18).
			c.InfraProvider = strings.ToLower(strings.TrimSpace(shiftVal(a)))
			c.InfraProviderDefaulted = false // explicit choice
		case "-h", "--help":
			// Optional next-arg topic: drill into a specific help
			// section. The arg must not start with "-".
			topic := ""
			if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				i++
				topic = argv[i]
			}
			PrintHelp(os.Stdout, topic)
			os.Exit(0)
		case "--completion":
			if i+1 >= len(argv) {
				logx.Die("--completion requires a shell name (bash, zsh, fish)")
			}
			i++
			shell := argv[i]
			if err := PrintShellCompletion(os.Stdout, shell); err != nil {
				logx.Die("%v", err)
			}
			os.Exit(0)
		default:
			logx.Die("Unknown option: %s", a)
		}
	}
}