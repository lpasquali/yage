// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Bootstrap-config snapshot schema.
//
// The management kind cluster holds a Secret
// ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME}/config.yaml whose body is a flat
// YAML map of `KEY: "value"` lines — the non-secret bootstrap state. The
// same schema is used for two directions:
//
//  1. Emit: _get_all_bootstrap_variables_as_yaml writes the snapshot.
//  2. Load: merge_proxmox_bootstrap_secrets_from_kind reads it back and
//     overlays into the current process state; keys locked by CLI via
//     *_EXPLICIT flags are not overwritten.
//
// This file defines the snapshot set once — as a list of named accessors
// — so both directions consume the same source of truth.
package config

import (
	"encoding/json"
	"strconv"
	"strings"
)

// SnapshotField is a single entry in the snapshot schema.
type SnapshotField struct {
	// EnvName is the upper-snake env-var key used on the wire.
	EnvName string
	// ExplicitName is the name of the companion *_EXPLICIT env flag that
	// tells merge logic to keep the current value (CLI-locked). Empty
	// when no such guard exists.
	ExplicitName string
	// Get returns the current field value as a wire string.
	Get func() string
	// Set assigns a wire string to the field.
	Set func(string)
}

// Snapshot returns the ordered field schema bound to c. Callers may
// iterate this slice to emit YAML or walk it to overlay a KV map.
//
// Fields are listed in a stable visual order so the round-tripped
// config.yaml stays diff-friendly. Boolean fields are serialised as
// "true"/"false".
func (c *Config) Snapshot() []SnapshotField {
	sp := func(envName string, p *string) SnapshotField {
		return SnapshotField{EnvName: envName, Get: func() string { return *p }, Set: func(v string) { *p = v }}
	}
	bp := func(envName string, p *bool) SnapshotField {
		return SnapshotField{
			EnvName: envName,
			Get:     func() string { return strconv.FormatBool(*p) },
			Set: func(v string) {
				switch strings.ToLower(strings.TrimSpace(v)) {
				case "1", "true", "yes", "y", "on":
					*p = true
				case "0", "false", "no", "n", "off":
					*p = false
				}
			},
		}
	}
	// spEx attaches an *_EXPLICIT guard to a string accessor.
	spEx := func(envName, explicitName string, p *string) SnapshotField {
		f := sp(envName, p)
		f.ExplicitName = explicitName
		return f
	}
	return []SnapshotField{
		// --- versions ---
		sp("KIND_VERSION", &c.KindVersion),
		sp("KUBECTL_VERSION", &c.KubectlVersion),
		sp("CLUSTERCTL_VERSION", &c.ClusterctlVersion),
		sp("OPENTOFU_VERSION", &c.OpenTofuVersion),
		sp("CILIUM_CLI_VERSION", &c.CiliumCLIVersion),
		sp("CILIUM_VERSION", &c.CiliumVersion),
		sp("CILIUM_WAIT_DURATION", &c.CiliumWaitDuration),
		// --- Cilium ---
		sp("CILIUM_INGRESS", &c.CiliumIngress),
		sp("CILIUM_KUBE_PROXY_REPLACEMENT", &c.CiliumKubeProxyReplacement),
		sp("CILIUM_LB_IPAM", &c.CiliumLBIPAM),
		sp("CILIUM_LB_IPAM_POOL_CIDR", &c.CiliumLBIPAMPoolCIDR),
		sp("CILIUM_LB_IPAM_POOL_START", &c.CiliumLBIPAMPoolStart),
		sp("CILIUM_LB_IPAM_POOL_STOP", &c.CiliumLBIPAMPoolStop),
		sp("CILIUM_LB_IPAM_POOL_NAME", &c.CiliumLBIPAMPoolName),
		sp("CILIUM_HUBBLE", &c.CiliumHubble),
		sp("CILIUM_HUBBLE_UI", &c.CiliumHubbleUI),
		sp("CILIUM_IPAM_CLUSTER_POOL_IPV4", &c.CiliumIPAMClusterPoolIPv4),
		sp("CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE", &c.CiliumIPAMClusterPoolIPv4MaskSize),
		sp("CILIUM_GATEWAY_API_ENABLED", &c.CiliumGatewayAPIEnabled),
		sp("ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS", &c.ArgoCD.DisableOperatorManagedIngress),
		// --- CAPMOX ---
		sp("CAPMOX_VERSION", &c.CAPMOXVersion),
		sp("IPAM_IMAGE", &c.IPAMImage),
		// --- ArgoCD ---
		sp("ARGOCD_VERSION", &c.ArgoCD.Version),
		// --- Workload GitOps ---
		sp("WORKLOAD_GITOPS_MODE", &c.WorkloadGitopsMode),
		sp("WORKLOAD_APP_OF_APPS_GIT_URL", &c.ArgoCD.AppOfAppsGitURL),
		sp("WORKLOAD_APP_OF_APPS_GIT_PATH", &c.ArgoCD.AppOfAppsGitPath),
		sp("WORKLOAD_APP_OF_APPS_GIT_REF", &c.ArgoCD.AppOfAppsGitRef),
		// --- Add-on chart versions ---
		sp("PROXMOX_CSI_CHART_VERSION", &c.Providers.Proxmox.CSIChartVersion),
		sp("KYVERNO_CHART_VERSION", &c.KyvernoChartVersion),
		sp("KYVERNO_CLI_VERSION", &c.KyvernoCLIVersion),
		sp("CERT_MANAGER_CHART_VERSION", &c.CertManagerChartVersion),
		sp("CMCTL_VERSION", &c.CmctlVersion),
		sp("CROSSPLANE_CHART_VERSION", &c.CrossplaneChartVersion),
		sp("CNPG_CHART_VERSION", &c.CNPGChartVersion),
		sp("EXTERNAL_SECRETS_CHART_VERSION", &c.ExternalSecretsChartVersion),
		sp("INFISICAL_CHART_VERSION", &c.InfisicalChartVersion),
		sp("SPIRE_CHART_VERSION", &c.SPIREChartVersion),
		sp("SPIRE_CRDS_CHART_VERSION", &c.SPIRECRDsChartVersion),
		sp("OTEL_CHART_VERSION", &c.OTELChartVersion),
		sp("GRAFANA_CHART_VERSION", &c.GrafanaChartVersion),
		sp("VICTORIAMETRICS_CHART_VERSION", &c.VictoriaMetricsChartVersion),
		sp("BACKSTAGE_CHART_VERSION", &c.BackstageChartVersion),
		sp("KEYCLOAK_CHART_VERSION", &c.KeycloakChartVersion),
		// --- kind / Proxmox core ---
		sp("KIND_CLUSTER_NAME", &c.KindClusterName),
		sp("CLUSTER_ID", &c.ClusterID),
		sp("PROXMOX_URL", &c.Providers.Proxmox.URL),
		sp("PROXMOX_ADMIN_INSECURE", &c.Providers.Proxmox.AdminInsecure),
		sp("PROXMOX_REGION", &c.Providers.Proxmox.Region),
		sp("PROXMOX_NODE", &c.Providers.Proxmox.Node),
		sp("PROXMOX_SOURCENODE", &c.Providers.Proxmox.SourceNode),
		sp("PROXMOX_CLOUDINIT_STORAGE", &c.Providers.Proxmox.CloudinitStorage),
		sp("PROXMOX_TEMPLATE_ID", &c.Providers.Proxmox.TemplateID),
		sp("PROXMOX_BRIDGE", &c.Providers.Proxmox.Bridge),
		spEx("PROXMOX_POOL", "PROXMOX_POOL_EXPLICIT", &c.Providers.Proxmox.Pool),
		spEx("MGMT_PROXMOX_POOL", "MGMT_PROXMOX_POOL_EXPLICIT", &c.Providers.Proxmox.Mgmt.Pool),
		// --- Network (EXPLICIT-guarded) ---
		sp("CONTROL_PLANE_ENDPOINT_IP", &c.ControlPlaneEndpointIP),
		sp("CONTROL_PLANE_ENDPOINT_PORT", &c.ControlPlaneEndpointPort),
		spEx("NODE_IP_RANGES", "NODE_IP_RANGES_EXPLICIT", &c.NodeIPRanges),
		spEx("GATEWAY", "GATEWAY_EXPLICIT", &c.Gateway),
		spEx("IP_PREFIX", "IP_PREFIX_EXPLICIT", &c.IPPrefix),
		spEx("DNS_SERVERS", "DNS_SERVERS_EXPLICIT", &c.DNSServers),
		spEx("ALLOWED_NODES", "ALLOWED_NODES_EXPLICIT", &c.AllowedNodes),
		sp("VM_SSH_KEYS", &c.VMSSHKeys),
		// --- Bootstrap state / flags ---
		sp("PROXMOX_BOOTSTRAP_CONFIG_FILE", &c.Providers.Proxmox.BootstrapConfigFile),
		bp("BOOTSTRAP_REGENERATE_CAPI_MANIFEST", &c.BootstrapRegenerateCAPIManifest),
		bp("BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING", &c.BootstrapSkipImmutableManifestWarning),
		bp("CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV", &c.Providers.Proxmox.CAPIMachineTemplateSpecRev),
		// --- Proxmox CAPI identity ---
		sp("PROXMOX_CAPI_USER_ID", &c.Providers.Proxmox.CAPIUserID),
		sp("PROXMOX_CAPI_TOKEN_PREFIX", &c.Providers.Proxmox.CAPITokenPrefix),
		sp("PROXMOX_MEMORY_ADJUSTMENT", &c.Providers.Proxmox.MemoryAdjustment),
		// --- VM sizing (control plane) ---
		sp("CONTROL_PLANE_BOOT_VOLUME_DEVICE", &c.Providers.Proxmox.ControlPlaneBootVolumeDevice),
		sp("CONTROL_PLANE_BOOT_VOLUME_SIZE", &c.Providers.Proxmox.ControlPlaneBootVolumeSize),
		sp("CONTROL_PLANE_NUM_SOCKETS", &c.Providers.Proxmox.ControlPlaneNumSockets),
		sp("CONTROL_PLANE_NUM_CORES", &c.Providers.Proxmox.ControlPlaneNumCores),
		sp("CONTROL_PLANE_MEMORY_MIB", &c.Providers.Proxmox.ControlPlaneMemoryMiB),
		// --- VM sizing (workers) ---
		sp("WORKER_BOOT_VOLUME_DEVICE", &c.Providers.Proxmox.WorkerBootVolumeDevice),
		sp("WORKER_BOOT_VOLUME_SIZE", &c.Providers.Proxmox.WorkerBootVolumeSize),
		sp("WORKER_NUM_SOCKETS", &c.Providers.Proxmox.WorkerNumSockets),
		sp("WORKER_NUM_CORES", &c.Providers.Proxmox.WorkerNumCores),
		sp("WORKER_MEMORY_MIB", &c.Providers.Proxmox.WorkerMemoryMiB),
		// --- Workload cluster (EXPLICIT-guarded for NAME/NAMESPACE) ---
		spEx("YAGE_CONFIG_NAME", "YAGE_CONFIG_NAME_EXPLICIT", &c.ConfigName),
		spEx("WORKLOAD_CLUSTER_NAME", "WORKLOAD_CLUSTER_NAME_EXPLICIT", &c.WorkloadClusterName),
		spEx("WORKLOAD_CLUSTER_NAMESPACE", "WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT", &c.WorkloadClusterNamespace),
		sp("WORKLOAD_CILIUM_CLUSTER_ID", &c.WorkloadCiliumClusterID),
		// Use explicit guard + always-overwrite so snapshot restores the
		// user's chosen version over the hardcoded v1.35.0 default.
		{
			EnvName:      "WORKLOAD_KUBERNETES_VERSION",
			ExplicitName: "WORKLOAD_KUBERNETES_VERSION_EXPLICIT",
			Get:          func() string { return c.WorkloadKubernetesVersion },
			Set:          func(v string) { c.WorkloadKubernetesVersion = v },
		},
		// --- Pivot (management cluster on Proxmox) ---
		bp("PIVOT_ENABLED", &c.Pivot.Enabled),
		spEx("MGMT_CLUSTER_NAME", "MGMT_CLUSTER_NAME_EXPLICIT", &c.Mgmt.ClusterName),
		spEx("MGMT_CLUSTER_NAMESPACE", "MGMT_CLUSTER_NAMESPACE_EXPLICIT", &c.Mgmt.ClusterNamespace),
		{
			EnvName:      "MGMT_KUBERNETES_VERSION",
			ExplicitName: "MGMT_KUBERNETES_VERSION_EXPLICIT",
			Get:          func() string { return c.Mgmt.KubernetesVersion },
			Set:          func(v string) { c.Mgmt.KubernetesVersion = v },
		},
		sp("MGMT_CILIUM_CLUSTER_ID", &c.Mgmt.CiliumClusterID),
		sp("MGMT_CONTROL_PLANE_MACHINE_COUNT", &c.Mgmt.ControlPlaneMachineCount),
		sp("MGMT_WORKER_MACHINE_COUNT", &c.Mgmt.WorkerMachineCount),
		sp("MGMT_CONTROL_PLANE_ENDPOINT_IP", &c.Mgmt.ControlPlaneEndpointIP),
		sp("MGMT_CONTROL_PLANE_ENDPOINT_PORT", &c.Mgmt.ControlPlaneEndpointPort),
		sp("MGMT_NODE_IP_RANGES", &c.Mgmt.NodeIPRanges),
		sp("MGMT_CONTROL_PLANE_NUM_SOCKETS", &c.Providers.Proxmox.Mgmt.ControlPlaneNumSockets),
		sp("MGMT_CONTROL_PLANE_NUM_CORES", &c.Providers.Proxmox.Mgmt.ControlPlaneNumCores),
		sp("MGMT_CONTROL_PLANE_MEMORY_MIB", &c.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB),
		sp("MGMT_CILIUM_HUBBLE", &c.Mgmt.CiliumHubble),
		sp("MGMT_CILIUM_LB_IPAM", &c.Mgmt.CiliumLBIPAM),
		bp("MGMT_PROXMOX_CSI_ENABLED", &c.Providers.Proxmox.Mgmt.CSIEnabled),
		// Per-machine-type Proxmox template overrides
		sp("WORKLOAD_CONTROL_PLANE_TEMPLATE_ID", &c.WorkloadControlPlaneTemplateID),
		sp("WORKLOAD_WORKER_TEMPLATE_ID", &c.WorkloadWorkerTemplateID),
		sp("MGMT_CONTROL_PLANE_TEMPLATE_ID", &c.Providers.Proxmox.Mgmt.ControlPlaneTemplateID),
		sp("MGMT_WORKER_TEMPLATE_ID", &c.Providers.Proxmox.Mgmt.WorkerTemplateID),
		sp("CONTROL_PLANE_MACHINE_COUNT", &c.ControlPlaneMachineCount),
		sp("WORKER_MACHINE_COUNT", &c.WorkerMachineCount),
		// --- ArgoCD toggles ---
		bp("ARGOCD_ENABLED", &c.ArgoCD.Enabled),
		bp("WORKLOAD_ARGOCD_ENABLED", &c.ArgoCD.WorkloadEnabled),
		sp("ARGOCD_SERVER_INSECURE", &c.ArgoCD.ServerInsecure),
		sp("WORKLOAD_ARGOCD_NAMESPACE", &c.ArgoCD.WorkloadNamespace),
		sp("ARGOCD_OPERATOR_VERSION", &c.ArgoCD.OperatorVersion),
		sp("ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED", &c.ArgoCD.PrometheusEnabled),
		sp("ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED", &c.ArgoCD.MonitoringEnabled),
		// --- metrics-server ---
		bp("ENABLE_METRICS_SERVER", &c.EnableMetricsServer),
		bp("ENABLE_WORKLOAD_METRICS_SERVER", &c.EnableWorkloadMetricsServer),
		sp("WORKLOAD_METRICS_SERVER_INSECURE_TLS", &c.WorkloadMetricsServerInsecureTLS),
		sp("METRICS_SERVER_MANIFEST_URL", &c.MetricsServerManifestURL),
		sp("METRICS_SERVER_GIT_CHART_TAG", &c.MetricsServerGitChartTag),
		// --- Add-on on/off flags ---
		bp("PROXMOX_CSI_ENABLED", &c.Providers.Proxmox.CSIEnabled),
		bp("PROXMOX_CSI_SMOKE_ENABLED", &c.Providers.Proxmox.CSISmokeEnabled),
		bp("ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED", &c.ArgoCD.PostsyncHooksEnabled),
		sp("ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL", &c.ArgoCD.PostsyncHooksGitURL),
		sp("ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH", &c.ArgoCD.PostsyncHooksGitPath),
		sp("ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF", &c.ArgoCD.PostsyncHooksGitRef),
		bp("KYVERNO_ENABLED", &c.KyvernoEnabled),
		bp("CERT_MANAGER_ENABLED", &c.CertManagerEnabled),
		bp("CROSSPLANE_ENABLED", &c.CrossplaneEnabled),
		bp("CNPG_ENABLED", &c.CNPGEnabled),
		bp("VICTORIAMETRICS_ENABLED", &c.VictoriaMetricsEnabled),
		bp("EXTERNAL_SECRETS_ENABLED", &c.ExternalSecretsEnabled),
		bp("INFISICAL_OPERATOR_ENABLED", &c.InfisicalOperatorEnabled),
		bp("SPIRE_ENABLED", &c.SPIREEnabled),
		bp("OTEL_ENABLED", &c.OTELEnabled),
		bp("GRAFANA_ENABLED", &c.GrafanaEnabled),
		bp("BACKSTAGE_ENABLED", &c.BackstageEnabled),
		bp("KEYCLOAK_ENABLED", &c.KeycloakEnabled),
		// --- Experimental feature flags ---
		sp("EXP_CLUSTER_RESOURCE_SET", &c.ExpClusterResourceSet),
		sp("CLUSTER_TOPOLOGY", &c.ClusterTopology),
		sp("EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION", &c.ExpKubeadmBootstrapFormatIgnition),
		// --- Identity ---
		sp("CLUSTER_SET_ID", &c.ClusterSetID),
		sp("PROXMOX_IDENTITY_SUFFIX", &c.Providers.Proxmox.IdentitySuffix),
		// --- xapiri walkthrough shape (restored on second run) ---
		sp("XAPIRI_WORKLOAD_ENVIRONMENT", &c.Workload.Environment),
		sp("XAPIRI_WORKLOAD_RESILIENCE", &c.Workload.Resilience),
		{
			EnvName: "XAPIRI_WORKLOAD_DB_GB",
			Get: func() string {
				if c.Workload.DatabaseGB <= 0 {
					return ""
				}
				return strconv.Itoa(c.Workload.DatabaseGB)
			},
			Set: func(v string) {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
					c.Workload.DatabaseGB = n
				}
			},
		},
		{
			EnvName: "XAPIRI_WORKLOAD_EGRESS_GB_MONTH",
			Get: func() string {
				if c.Workload.EgressGBMonth <= 0 {
					return ""
				}
				return strconv.Itoa(c.Workload.EgressGBMonth)
			},
			Set: func(v string) {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
					c.Workload.EgressGBMonth = n
				}
			},
		},
		{
			EnvName: "XAPIRI_WORKLOAD_APPS",
			Get: func() string {
				if len(c.Workload.Apps) == 0 {
					return ""
				}
				parts := make([]string, 0, len(c.Workload.Apps))
				for _, a := range c.Workload.Apps {
					if a.Count > 0 && a.Template != "" {
						parts = append(parts, strconv.Itoa(a.Count)+" "+a.Template)
					}
				}
				return strings.Join(parts, " ")
			},
			Set: func(v string) {
				if v == "" {
					return
				}
				r := strings.NewReplacer(",", " ", "x", " ", "×", " ")
				tokens := strings.Fields(r.Replace(v))
				var apps []AppGroup
				for i := 0; i+1 < len(tokens); i += 2 {
					n, err := strconv.Atoi(tokens[i])
					if err != nil || n <= 0 {
						continue
					}
					tpl := strings.ToLower(tokens[i+1])
					if tpl != "light" && tpl != "medium" && tpl != "heavy" {
						continue
					}
					apps = append(apps, AppGroup{Count: n, Template: tpl})
				}
				if len(apps) > 0 {
					c.Workload.Apps = apps
				}
			},
		},
		bp("XAPIRI_WORKLOAD_HAS_QUEUE", &c.Workload.HasQueue),
		bp("XAPIRI_WORKLOAD_HAS_OBJ_STORE", &c.Workload.HasObjStore),
		bp("XAPIRI_WORKLOAD_HAS_CACHE", &c.Workload.HasCache),
	}
}

// SnapshotYAML emits one `KEY: "value"` line per non-empty snapshot
// field, with the value JSON-quoted.
//
// Token/secret values never appear — the set is non-secret by
// construction (those keys are absent from the snapshot schema). No
// leading comment: callers prepend their own EONOTICE header.
func (c *Config) SnapshotYAML() string {
	var sb strings.Builder
	for _, f := range c.Snapshot() {
		v := f.Get()
		if v == "" {
			continue
		}
		q, _ := json.Marshal(v)
		sb.WriteString(f.EnvName)
		sb.WriteString(": ")
		sb.Write(q)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// ApplySnapshotKV ports the "snapshot key overlay" portion of
// merge_proxmox_bootstrap_secrets_from_kind. For each (k, v) pair:
//   - If k maps to a snapshot field that has an ExplicitName guard and
//     the corresponding *_EXPLICIT flag is set on cfg, skip (CLI wins).
//   - Otherwise overlay the value into the field.
//
// Keys not in the schema are ignored — the Go port is authoritative about
// which fields are bootstrap-config-addressable. This differs from bash
// (which exports any upper-case key whose local env is empty) because in
// Go we can't mutate arbitrary identifiers; unknown keys would just sit
// in a map nobody reads. Loss of forward-compat is acceptable: the schema
// is the spec.
func (c *Config) ApplySnapshotKV(kv map[string]string) {
	explicit := c.explicitSet()
	schema := c.Snapshot()
	index := make(map[string]SnapshotField, len(schema))
	for _, f := range schema {
		index[f.EnvName] = f
	}
	for k, v := range kv {
		f, ok := index[k]
		if !ok || v == "" {
			continue
		}
		if f.ExplicitName != "" && explicit[f.ExplicitName] {
			continue
		}
		f.Set(v)
	}
}

// explicitSet returns the set of *_EXPLICIT flags currently true on cfg.
func (c *Config) explicitSet() map[string]bool {
	return map[string]bool{
		"NODE_IP_RANGES_EXPLICIT":             c.NodeIPRangesExplicit,
		"GATEWAY_EXPLICIT":                    c.GatewayExplicit,
		"IP_PREFIX_EXPLICIT":                  c.IPPrefixExplicit,
		"DNS_SERVERS_EXPLICIT":                c.DNSServersExplicit,
		"ALLOWED_NODES_EXPLICIT":              c.AllowedNodesExplicit,
		"YAGE_CONFIG_NAME_EXPLICIT":            c.ConfigNameExplicit,
		"WORKLOAD_CLUSTER_NAME_EXPLICIT":       c.WorkloadClusterNameExplicit,
		"WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT":  c.WorkloadClusterNamespaceExplicit,
		"WORKLOAD_KUBERNETES_VERSION_EXPLICIT": c.WorkloadKubernetesVersionExplicit,
		"MGMT_CLUSTER_NAME_EXPLICIT":          c.Mgmt.ClusterNameExplicit,
		"MGMT_CLUSTER_NAMESPACE_EXPLICIT":     c.Mgmt.ClusterNamespaceExplicit,
		"MGMT_KUBERNETES_VERSION_EXPLICIT":    c.Mgmt.KubernetesVersionExplicit,
		"PROXMOX_POOL_EXPLICIT":               c.Providers.Proxmox.PoolExplicit,
		"MGMT_PROXMOX_POOL_EXPLICIT":          c.Providers.Proxmox.Mgmt.PoolExplicit,
	}
}