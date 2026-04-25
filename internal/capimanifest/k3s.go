// K3s flavor renderer.
//
// Upstream CAPMOX (cluster-api-provider-proxmox) ships only kubeadm
// flavor templates, so `clusterctl generate cluster --flavor k3s
// --infrastructure proxmox` returns "flavor not found". Rather than
// fork CAPMOX or carry a separate release just for one template, we
// embed the K3s flavor here and render it in-process when
// cfg.BootstrapMode == "k3s".
//
// The template wires KCP-K3s (KThreesControlPlane) +
// CABK3s (KThreesConfigTemplate) to the existing CAPMOX
// ProxmoxMachineTemplate. The K3s providers themselves (the
// controllers that reconcile those CRDs) are installed by
// `clusterctl init --control-plane k3s --bootstrap k3s` in
// internal/bootstrap.
//
// The four post-generate patches in patches.go all operate on shared
// CAPI shapes (Cluster, ProxmoxCluster, ProxmoxMachineTemplate) and
// apply to this output unchanged — except PatchKubeadmSkipKubeProxyForCilium
// which is kubeadm-specific. That patch is a safe no-op against this
// template (no `kind: KubeadmConfig*` documents, so its regex never
// matches). K3s disables kube-proxy via serverConfig.disableComponents
// in this template directly.

package capimanifest

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
)

//go:embed k3s_template.yaml
var k3sTemplate string

// RenderK3sManifest fills the embedded K3s template with values from
// cfg. When mgmt is true the management-cluster fields are used (mgmt
// name, sizing, IPs, machine counts); otherwise the workload fields.
//
// Returns the rendered multi-doc YAML. Caller writes it to
// cfg.CAPIManifest (or the mgmt manifest path) and then runs the same
// post-generate patches the kubeadm flow uses.
func RenderK3sManifest(cfg *config.Config, mgmt bool) string {
	values := k3sValues(cfg, mgmt)
	mapper := func(key string) string {
		if v, ok := values[key]; ok {
			return v
		}
		// Unknown variables stay empty rather than literal — matches
		// clusterctl's behavior. Callers can spot empty fields in the
		// rendered output (cfg should have populated everything).
		return ""
	}
	return os.Expand(k3sTemplate, mapper)
}

// k3sValues builds the env-style map os.Expand walks. Field selection
// branches on mgmt: workload uses cfg.WorkloadClusterName,
// cfg.ControlPlane*, cfg.Worker*; mgmt uses cfg.MgmtClusterName,
// cfg.MgmtControlPlane*, and the same Worker* fields (mgmt usually has
// 0 workers; if MgmtWorkerMachineCount > 0 the worker block lands on
// the same VM template as the workload).
func k3sValues(cfg *config.Config, mgmt bool) map[string]string {
	v := map[string]string{
		"PROXMOX_URL":                       cfg.ProxmoxURL,
		"PROXMOX_REGION":                    cfg.ProxmoxRegion,
		"PROXMOX_NODE":                      cfg.ProxmoxNode,
		"PROXMOX_TEMPLATE_ID":               cfg.ProxmoxTemplateID,
		"PROXMOX_SOURCENODE":                stringOrEmpty(cfg.ProxmoxSourceNode, cfg.ProxmoxNode),
		"BRIDGE":                            cfg.ProxmoxBridge,
		"PROXMOX_CLOUDINIT_STORAGE":         cfg.ProxmoxCloudinitStorage,
		"PROXMOX_MEMORY_ADJUSTMENT":         cfg.ProxmoxMemoryAdjustment,
		"DNS_SERVERS":                       cfg.DNSServers,
		"VM_SSH_KEYS":                       readAuthorizedKeysOrConfig(cfg),
		// Pool is the Proxmox pool tag VMs land in. Workload uses
		// ProxmoxPool; mgmt overrides below.
		"PROXMOX_POOL":                      cfg.ProxmoxPool,
	}
	if mgmt {
		v["NAMESPACE"] = cfg.MgmtClusterNamespace
		v["CLUSTER_NAME"] = cfg.MgmtClusterName
		v["PROXMOX_POOL"] = cfg.MgmtProxmoxPool
		v["KUBERNETES_VERSION"] = cfg.MgmtKubernetesVersion
		v["CONTROL_PLANE_MACHINE_COUNT"] = cfg.MgmtControlPlaneMachineCount
		v["WORKER_MACHINE_COUNT"] = cfg.MgmtWorkerMachineCount
		v["CONTROL_PLANE_ENDPOINT_IP"] = cfg.MgmtControlPlaneEndpointIP
		v["CONTROL_PLANE_ENDPOINT_PORT"] = cfg.MgmtControlPlaneEndpointPort
		v["NODE_IP_RANGES"] = cfg.MgmtNodeIPRanges
		v["GATEWAY"] = cfg.Gateway
		v["IP_PREFIX"] = cfg.IPPrefix
		v["ALLOWED_NODES"] = cfg.AllowedNodes
		v["CONTROL_PLANE_BOOT_VOLUME_DEVICE"] = cfg.MgmtControlPlaneBootVolumeDevice
		v["CONTROL_PLANE_BOOT_VOLUME_SIZE"] = cfg.MgmtControlPlaneBootVolumeSize
		v["CONTROL_PLANE_NUM_SOCKETS"] = cfg.MgmtControlPlaneNumSockets
		v["CONTROL_PLANE_NUM_CORES"] = cfg.MgmtControlPlaneNumCores
		v["CONTROL_PLANE_MEMORY_MIB"] = cfg.MgmtControlPlaneMemoryMiB
		// Mgmt has no separate worker sizing; reuse workload's worker
		// fields (the worker block likely has 0 replicas anyway).
		v["WORKER_BOOT_VOLUME_DEVICE"] = cfg.WorkerBootVolumeDevice
		v["WORKER_BOOT_VOLUME_SIZE"] = cfg.WorkerBootVolumeSize
		v["WORKER_NUM_SOCKETS"] = cfg.WorkerNumSockets
		v["WORKER_NUM_CORES"] = cfg.WorkerNumCores
		v["WORKER_MEMORY_MIB"] = cfg.WorkerMemoryMiB
	} else {
		v["NAMESPACE"] = cfg.WorkloadClusterNamespace
		v["CLUSTER_NAME"] = cfg.WorkloadClusterName
		v["KUBERNETES_VERSION"] = cfg.WorkloadKubernetesVersion
		v["CONTROL_PLANE_MACHINE_COUNT"] = cfg.ControlPlaneMachineCount
		v["WORKER_MACHINE_COUNT"] = cfg.WorkerMachineCount
		v["CONTROL_PLANE_ENDPOINT_IP"] = cfg.ControlPlaneEndpointIP
		v["CONTROL_PLANE_ENDPOINT_PORT"] = cfg.ControlPlaneEndpointPort
		v["NODE_IP_RANGES"] = cfg.NodeIPRanges
		v["GATEWAY"] = cfg.Gateway
		v["IP_PREFIX"] = cfg.IPPrefix
		v["ALLOWED_NODES"] = cfg.AllowedNodes
		v["CONTROL_PLANE_BOOT_VOLUME_DEVICE"] = cfg.ControlPlaneBootVolumeDevice
		v["CONTROL_PLANE_BOOT_VOLUME_SIZE"] = cfg.ControlPlaneBootVolumeSize
		v["CONTROL_PLANE_NUM_SOCKETS"] = cfg.ControlPlaneNumSockets
		v["CONTROL_PLANE_NUM_CORES"] = cfg.ControlPlaneNumCores
		v["CONTROL_PLANE_MEMORY_MIB"] = cfg.ControlPlaneMemoryMiB
		v["WORKER_BOOT_VOLUME_DEVICE"] = cfg.WorkerBootVolumeDevice
		v["WORKER_BOOT_VOLUME_SIZE"] = cfg.WorkerBootVolumeSize
		v["WORKER_NUM_SOCKETS"] = cfg.WorkerNumSockets
		v["WORKER_NUM_CORES"] = cfg.WorkerNumCores
		v["WORKER_MEMORY_MIB"] = cfg.WorkerMemoryMiB
	}
	// Defensive: ensure CONTROL_PLANE_ENDPOINT_PORT has a value (the
	// template doesn't carry a default). Same for IP_PREFIX.
	if v["CONTROL_PLANE_ENDPOINT_PORT"] == "" {
		v["CONTROL_PLANE_ENDPOINT_PORT"] = "6443"
	}
	if v["IP_PREFIX"] == "" {
		v["IP_PREFIX"] = "24"
	}
	return v
}

func stringOrEmpty(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

// readAuthorizedKeysOrConfig prefers cfg.VMSSHKeys; falls back to
// reading ~/.ssh/authorized_keys. Mirrors the kubeadm path's helper.
func readAuthorizedKeysOrConfig(cfg *config.Config) string {
	if strings.TrimSpace(cfg.VMSSHKeys) != "" {
		return cfg.VMSSHKeys
	}
	keys := readAuthorizedKeys()
	if keys == "" {
		logx.Warn("VM_SSH_KEYS is empty and ~/.ssh/authorized_keys is unreadable — generated K3s VMs won't accept SSH logins.")
	}
	return keys
}

// MaterializeK3sManifest renders the K3s flavor and writes it to
// destPath (typically cfg.CAPIManifest or cfg.MgmtCAPIManifest).
// Returns nil on success.
func MaterializeK3sManifest(cfg *config.Config, mgmt bool, destPath string) error {
	body := RenderK3sManifest(cfg, mgmt)
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("rendered K3s manifest is empty (template missing or unset cfg)")
	}
	if err := os.WriteFile(destPath, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write K3s manifest to %s: %w", destPath, err)
	}
	return nil
}
