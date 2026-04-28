// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cli

// render.go — produce a `yage <flags>` invocation that reproduces a
// resolved cfg. Used by:
//   - --print-command            standalone: read cfg via the normal
//                                env+CLI+kind-Secret merge, print the
//                                equivalent CLI, exit
//   - xapiri's persist step      after the wizard succeeds, echo the
//                                equivalent CLI so the operator can
//                                save it for pipelines
//
// Output is multi-line bash with line-continuation backslashes so
// it pastes cleanly into a shell or a pipeline yaml. Sensitive
// values (tokens, API keys, passwords) emit as "$ENV_VAR" refs by
// default — directly runnable in any shell that has those env vars
// set, without committing secrets to a pipeline definition. Pass
// SensitiveRaw to inline the literal value (full reproducibility,
// for trusted hosts), or SensitiveMasked for screenshot-safe output.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lpasquali/yage/internal/config"
)

// SensitiveMode controls how RenderCommand emits credentials.
type SensitiveMode int

const (
	// SensitiveAsEnv emits "$VAR" references for sensitive values.
	// Output is shell-runnable as-is when the env is populated.
	SensitiveAsEnv SensitiveMode = iota
	// SensitiveRaw inlines the literal value. Reproducible, but the
	// output contains secrets; the caller must protect storage.
	SensitiveRaw
	// SensitiveMasked emits "********" placeholders. Not reproducible;
	// useful for documentation, screenshots, and review-loop outputs.
	SensitiveMasked
)

// RenderCommand returns a multi-line yage invocation reproducing
// cfg. The first line is "yage \", subsequent lines are
// "    --flag value \", and the final line drops the trailing
// backslash. Each emitter omits flags whose cfg value is empty /
// zero, so the output stays compact.
func RenderCommand(cfg *config.Config, mode SensitiveMode) string {
	if cfg == nil {
		return "yage"
	}
	r := &renderer{mode: mode}
	r.line("yage")

	r.universal(cfg)
	r.network(cfg)
	r.cost(cfg)
	r.tco(cfg)
	r.workload(cfg)
	r.pivot(cfg)
	r.airgap(cfg)

	switch cfg.InfraProvider {
	case "proxmox":
		r.proxmox(cfg)
	case "aws":
		r.aws(cfg)
	case "azure":
		r.azure(cfg)
	case "gcp":
		r.gcp(cfg)
	case "hetzner":
		r.hetzner(cfg)
	case "digitalocean":
		r.digitalocean(cfg)
	case "linode":
		r.linode(cfg)
	case "oci":
		r.oci(cfg)
	case "ibmcloud":
		r.ibmcloud(cfg)
	case "openstack":
		r.openstack(cfg)
	case "vsphere":
		r.vsphere(cfg)
	}
	return r.assemble()
}

// renderer holds the building lines and the sensitive-value mode.
type renderer struct {
	mode  SensitiveMode
	lines []string
}

func (r *renderer) line(s string)      { r.lines = append(r.lines, s) }
func (r *renderer) flag(name, value string) {
	if value == "" {
		return
	}
	r.lines = append(r.lines, fmt.Sprintf("    %s %s", name, shellQuote(value)))
}

// flagInt emits the flag when n > 0.
func (r *renderer) flagInt(name string, n int) {
	if n <= 0 {
		return
	}
	r.flag(name, strconv.Itoa(n))
}

// flagIntStr emits the flag when s parses as a positive int (cfg
// fields stored as strings — keeps the parse rule in one place).
func (r *renderer) flagIntStr(name, s string) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err == nil && n > 0 {
		r.flag(name, strconv.Itoa(n))
	}
}

// flagFloat emits the flag when f > 0.
func (r *renderer) flagFloat(name string, f float64) {
	if f <= 0 {
		return
	}
	r.flag(name, strconv.FormatFloat(f, 'f', -1, 64))
}

// flagBool emits a bare flag (no value) when b is true.
func (r *renderer) flagBool(name string, b bool) {
	if !b {
		return
	}
	r.lines = append(r.lines, "    "+name)
}

// secret emits a sensitive flag according to r.mode. envVar is the
// shell variable the SensitiveAsEnv mode references (POSIX-style,
// double-quoted so it expands at run time).
func (r *renderer) secret(name, value, envVar string) {
	if value == "" {
		return
	}
	switch r.mode {
	case SensitiveRaw:
		r.flag(name, value)
	case SensitiveMasked:
		r.flag(name, "********")
	default:
		if envVar == "" {
			r.lines = append(r.lines,
				fmt.Sprintf("    %s \"********\"  # supply via env / vault", name))
			return
		}
		r.lines = append(r.lines,
			fmt.Sprintf("    %s \"$%s\"", name, envVar))
	}
}

// assemble joins lines with " \\\n" between them so the result
// pastes cleanly into a shell.
func (r *renderer) assemble() string {
	if len(r.lines) == 0 {
		return ""
	}
	return strings.Join(r.lines, " \\\n")
}

// shellQuote returns a POSIX-safe representation of value. Empty
// strings, plain identifiers, and numbers go through unquoted;
// anything with whitespace or shell-special chars gets single-
// quoted (with embedded single quotes escaped as '\'').
func shellQuote(v string) string {
	if v == "" {
		return "''"
	}
	if isShellSafe(v) {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

func isShellSafe(v string) bool {
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-' || c == '_' || c == '.' || c == '/' || c == ':' || c == ',' || c == '=' || c == '+':
			continue
		default:
			return false
		}
	}
	return true
}

// --- per-section emitters -------------------------------------------------

func (r *renderer) universal(cfg *config.Config) {
	r.flag("--kind-cluster-name", cfg.KindClusterName)
	r.flag("--infra-provider", cfg.InfraProvider)
	r.flag("--bootstrap-mode", cfg.BootstrapMode)
}

func (r *renderer) network(cfg *config.Config) {
	r.flag("--control-plane-endpoint-ip", cfg.ControlPlaneEndpointIP)
	r.flag("--control-plane-endpoint-port", cfg.ControlPlaneEndpointPort)
	r.flag("--node-ip-ranges", cfg.NodeIPRanges)
	r.flag("--gateway", cfg.Gateway)
	r.flag("--ip-prefix", cfg.IPPrefix)
	r.flag("--dns-servers", cfg.DNSServers)
	r.flag("--allowed-nodes", cfg.AllowedNodes)
	r.flag("--vm-ssh-keys", cfg.VMSSHKeys)
}

func (r *renderer) cost(cfg *config.Config) {
	r.flag("--data-center-location", cfg.Cost.Currency.DataCenterLocation)
	r.flagBool("--cost-compare", cfg.CostCompare)
	r.flagFloat("--budget-usd-month", cfg.BudgetUSDMonth)
	r.flagBool("--cost-compare-config", cfg.CostCompareEnabled)
	r.flag("--skip-providers", cfg.SkipProviders)
	if !cfg.UseManagedPostgres {
		r.lines = append(r.lines, "    --no-managed-postgres")
	}
	r.flagInt("--postgres-cpu-millicores", cfg.PostgresCPUMillicoresOverride)
	r.flagInt("--postgres-memory-mib", cfg.PostgresMemoryMiBOverride)
	r.flagInt("--postgres-volume-gb", cfg.PostgresVolumeGBOverride)
	r.flagInt("--mq-cpu-millicores", cfg.MQCPUMillicoresOverride)
	r.flagInt("--mq-memory-mib", cfg.MQMemoryMiBOverride)
	r.flagInt("--mq-volume-gb", cfg.MQVolumeGBOverride)
	r.flagInt("--objstore-cpu-millicores", cfg.ObjStoreCPUMillicoresOverride)
	r.flagInt("--objstore-memory-mib", cfg.ObjStoreMemoryMiBOverride)
	r.flagInt("--objstore-volume-gb", cfg.ObjStoreVolumeGBOverride)
	r.flagInt("--cache-cpu-millicores", cfg.CacheCPUMillicoresOverride)
	r.flagInt("--cache-memory-mib", cfg.CacheMemoryMiBOverride)
}

func (r *renderer) tco(cfg *config.Config) {
	r.flagFloat("--hardware-cost-usd", cfg.HardwareCostUSD)
	r.flagFloat("--hardware-useful-life-years", cfg.HardwareUsefulLifeYears)
	r.flagFloat("--hardware-watts", cfg.HardwareWatts)
	r.flagFloat("--hardware-kwh-rate-usd", cfg.HardwareKWHRateUSD)
	r.flagFloat("--hardware-support-usd-month", cfg.HardwareSupportUSDMonth)
}

func (r *renderer) workload(cfg *config.Config) {
	r.flag("--workload-cluster-name", cfg.WorkloadClusterName)
	r.flag("--workload-cluster-namespace", cfg.WorkloadClusterNamespace)
	r.flag("--workload-k8s-version", cfg.WorkloadKubernetesVersion)
	r.flagIntStr("--control-plane-count", cfg.ControlPlaneMachineCount)
	r.flagIntStr("--worker-count", cfg.WorkerMachineCount)
}

func (r *renderer) pivot(cfg *config.Config) {
	// --pivot is the new default; emit --no-pivot only when the
	// operator explicitly opted out.
	if !cfg.Pivot.Enabled {
		r.lines = append(r.lines, "    --no-pivot")
	}
	r.flagBool("--pivot-keep-kind", cfg.Pivot.KeepKind)
	r.flagBool("--pivot-dry-run", cfg.Pivot.DryRun)
	r.flagBool("--stop-before-workload", cfg.Pivot.StopBeforeWorkload)
}

func (r *renderer) airgap(cfg *config.Config) {
	r.flagBool("--airgapped", cfg.Airgapped)
	r.flag("--image-registry-mirror", cfg.ImageRegistryMirror)
	r.flag("--internal-ca-bundle", cfg.InternalCABundle)
	r.flag("--helm-repo-mirror", cfg.HelmRepoMirror)
	r.flag("--node-image", cfg.NodeImage)
	r.flag("--trace-endpoint", cfg.TraceEndpoint)
}

func (r *renderer) proxmox(cfg *config.Config) {
	p := cfg.Providers.Proxmox
	r.flag("--proxmox-url", p.URL)
	r.flag("--admin-username", p.AdminUsername)
	r.secret("--admin-token", p.AdminToken, "PROXMOX_ADMIN_TOKEN")
	r.secret("--proxmox-capi-token", p.CAPIToken, "PROXMOX_CAPI_TOKEN")
	r.secret("--proxmox-capi-secret", p.CAPISecret, "PROXMOX_CAPI_SECRET")
	r.flag("--region", p.Region)
	r.flag("--node", p.Node)
	r.flag("--bridge", p.Bridge)
	r.flag("--template-id", p.TemplateID)
	r.flag("--proxmox-pool", p.Pool)
	r.flag("--cloudinit-storage", p.CloudinitStorage)
	r.secret("--csi-token-id", p.CSITokenID, "PROXMOX_CSI_TOKEN_ID")
	r.secret("--csi-token-secret", p.CSITokenSecret, "PROXMOX_CSI_TOKEN_SECRET")
	// VM sizing — only emit when non-empty (non-default).
	r.flag("--control-plane-num-sockets", p.ControlPlaneNumSockets)
	r.flag("--control-plane-num-cores", p.ControlPlaneNumCores)
	r.flag("--control-plane-memory-mib", p.ControlPlaneMemoryMiB)
	r.flag("--control-plane-boot-volume-size", p.ControlPlaneBootVolumeSize)
	r.flag("--worker-num-sockets", p.WorkerNumSockets)
	r.flag("--worker-num-cores", p.WorkerNumCores)
	r.flag("--worker-memory-mib", p.WorkerMemoryMiB)
	r.flag("--worker-boot-volume-size", p.WorkerBootVolumeSize)
}

func (r *renderer) aws(cfg *config.Config) {
	a := cfg.Providers.AWS
	r.flag("--aws-mode", a.Mode)
	r.flag("--aws-overhead-tier", a.OverheadTier)
	r.flagIntStr("--aws-nat-gateway-count", a.NATGatewayCount)
	r.flagIntStr("--aws-alb-count", a.ALBCount)
	r.flagIntStr("--aws-nlb-count", a.NLBCount)
	r.flagIntStr("--aws-data-transfer-gb", a.DataTransferGB)
	r.flagIntStr("--aws-cloudwatch-logs-gb", a.CloudWatchLogsGB)
	r.flagIntStr("--aws-route53-hosted-zones", a.Route53HostedZones)
	r.flagIntStr("--aws-fargate-pod-count", a.FargatePodCount)
	r.flag("--aws-fargate-pod-cpu", a.FargatePodCPU)
	r.flag("--aws-fargate-pod-memory-gib", a.FargatePodMemoryGiB)
	// AWS credentials come from the SDK chain (env, ~/.aws/config).
}

func (r *renderer) azure(cfg *config.Config) {
	a := cfg.Providers.Azure
	r.flag("--azure-mode", a.Mode)
	r.flag("--azure-subscription-id", a.SubscriptionID)
	r.flag("--azure-tenant-id", a.TenantID)
	r.flag("--azure-resource-group", a.ResourceGroup)
	r.flag("--azure-vnet-name", a.VNetName)
	r.flag("--azure-subnet-name", a.SubnetName)
	r.flag("--azure-client-id", a.ClientID)
	r.flag("--azure-identity-model", a.IdentityModel)
}

func (r *renderer) gcp(cfg *config.Config) {
	g := cfg.Providers.GCP
	r.flag("--gcp-mode", g.Mode)
	r.flag("--gcp-network-name", g.Network)
	r.flag("--gcp-image-family", g.ImageFamily)
	r.flag("--gcp-identity-model", g.IdentityModel)
}

func (r *renderer) hetzner(cfg *config.Config) {
	h := cfg.Providers.Hetzner
	r.secret("--hcloud-token", h.Token, "HCLOUD_TOKEN")
}

func (r *renderer) digitalocean(_ *config.Config) {
	r.secret("--do-token", "", "DIGITALOCEAN_TOKEN")
}

func (r *renderer) linode(_ *config.Config) {
	r.secret("--linode-token", "", "LINODE_TOKEN")
}

func (r *renderer) oci(_ *config.Config) {
	// OCI auth is config-file-based; nothing to emit beyond the
	// shape's region/family selection that already flows through
	// environment variables (OCI_CONFIG_FILE).
}

func (r *renderer) ibmcloud(_ *config.Config) {
	r.secret("--ibmcloud-api-key", "", "IBMCLOUD_API_KEY")
}

func (r *renderer) openstack(cfg *config.Config) {
	o := cfg.Providers.OpenStack
	r.flag("--openstack-cloud", o.Cloud)
	r.flag("--openstack-project-name", o.ProjectName)
	r.flag("--openstack-region", o.Region)
	r.flag("--openstack-failure-domain", o.FailureDomain)
	r.flag("--openstack-image-name", o.ImageName)
	r.flag("--openstack-control-plane-flavor", o.ControlPlaneFlavor)
	r.flag("--openstack-worker-flavor", o.WorkerFlavor)
	r.flag("--openstack-dns-nameservers", o.DNSNameservers)
	r.flag("--openstack-ssh-key-name", o.SSHKeyName)
}

func (r *renderer) vsphere(cfg *config.Config) {
	v := cfg.Providers.Vsphere
	r.flag("--vsphere-server", v.Server)
	r.flag("--vsphere-datacenter", v.Datacenter)
	r.flag("--vsphere-folder", v.Folder)
	r.flag("--vsphere-resource-pool", v.ResourcePool)
	r.flag("--vsphere-datastore", v.Datastore)
	r.flag("--vsphere-network", v.Network)
	r.flag("--vsphere-template", v.Template)
	r.flag("--vsphere-tls-thumbprint", v.TLSThumbprint)
	r.flag("--vsphere-username", v.Username)
	r.secret("--vsphere-password", v.Password, "VSPHERE_PASSWORD")
}
