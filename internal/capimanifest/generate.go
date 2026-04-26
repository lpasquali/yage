package capimanifest

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/k8sclient"
	"github.com/lpasquali/yage/internal/logx"
	"github.com/lpasquali/yage/internal/proxmox"
)

// TryFillWorkloadInputsFromManagement ports
// try_fill_workload_manifest_inputs_from_management_cluster
// (yage.sh L5024-L5147). Best-effort fill from the management
// cluster: pulls PROXMOX_TEMPLATE_ID / PROXMOX_NODE from existing
// ProxmoxMachineTemplates, and network + DNS from the live
// ProxmoxCluster when the workload is selected. Guards respect the
// *_EXPLICIT flags so CLI-locked keys aren't overwritten.
func TryFillWorkloadInputsFromManagement(cfg *config.Config) {
	mctx := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(mctx) {
		return
	}
	cli, err := k8sclient.ForContext(mctx)
	if err != nil {
		return
	}
	ctx := context.Background()

	pmtGVR := schema.GroupVersionResource{
		Group:    "infrastructure.cluster.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "proxmoxmachinetemplates",
	}
	pcGVR := schema.GroupVersionResource{
		Group:    "infrastructure.cluster.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "proxmoxclusters",
	}

	pmtList, err := cli.Dynamic.Resource(pmtGVR).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		// CRD missing or not yet installed — bail like bash did when
		// `kubectl get crd proxmoxmachinetemplates...` failed.
		if apierrors.IsNotFound(err) || isNoMatchError(err) {
			return
		}
		return
	}

	if cfg.ProxmoxTemplateID == "" {
		idRE := regexp.MustCompile(`^[0-9]+$`)
		for _, item := range pmtList.Items {
			id, _, _ := unstructuredString(item.Object, "spec", "template", "spec", "templateID")
			id = strings.TrimSpace(id)
			if id != "" && idRE.MatchString(id) {
				cfg.ProxmoxTemplateID = id
				logx.Log("Set PROXMOX_TEMPLATE_ID=%s from an existing ProxmoxMachineTemplate on %s.", id, mctx)
				break
			}
		}
	}

	if cfg.ProxmoxNode == "" {
		for _, item := range pmtList.Items {
			node, _, _ := unstructuredString(item.Object, "spec", "template", "spec", "sourceNode")
			node = strings.TrimSpace(node)
			if node != "" {
				cfg.ProxmoxNode = node
				if !cfg.AllowedNodesExplicit && cfg.AllowedNodes == "" {
					cfg.AllowedNodes = cfg.ProxmoxNode
				}
				logx.Log("Set PROXMOX_NODE from ProxmoxMachineTemplate sourceNode on %s.", mctx)
				break
			}
		}
	}

	// Reuse network + DNS from the live ProxmoxCluster when available.
	if cfg.WorkloadClusterName == "" || cfg.WorkloadClusterNamespace == "" {
		return
	}

	clusterGVR := schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta2",
		Resource: "clusters",
	}
	clusterObj, err := cli.Dynamic.Resource(clusterGVR).
		Namespace(cfg.WorkloadClusterNamespace).
		Get(ctx, cfg.WorkloadClusterName, metav1.GetOptions{})
	if err != nil {
		return
	}
	infraKind, _, _ := unstructuredString(clusterObj.Object, "spec", "infrastructureRef", "kind")
	infraName, _, _ := unstructuredString(clusterObj.Object, "spec", "infrastructureRef", "name")
	if infraKind != "" && !strings.EqualFold(infraKind, "ProxmoxCluster") {
		return
	}
	pcName := infraName
	if pcName == "" {
		pcName = cfg.WorkloadClusterName
	}

	pcObj, err := cli.Dynamic.Resource(pcGVR).
		Namespace(cfg.WorkloadClusterNamespace).
		Get(ctx, pcName, metav1.GetOptions{})
	if err != nil {
		// Fallback to workload cluster name (matches bash retry).
		pcName = cfg.WorkloadClusterName
		pcObj, err = cli.Dynamic.Resource(pcGVR).
			Namespace(cfg.WorkloadClusterNamespace).
			Get(ctx, pcName, metav1.GetOptions{})
		if err != nil {
			return
		}
	}

	dnsServers := unstructuredStringSlice(pcObj.Object, "spec", "dnsServers")
	gw, _, _ := unstructuredString(pcObj.Object, "spec", "ipv4Config", "gateway")
	prefix, hasPrefix := unstructuredField(pcObj.Object, "spec", "ipv4Config", "prefix")
	addresses := unstructuredStringSlice(pcObj.Object, "spec", "ipv4Config", "addresses")
	allowedNodes := unstructuredStringSlice(pcObj.Object, "spec", "allowedNodes")

	if !cfg.DNSServersExplicit && len(dnsServers) > 0 {
		cfg.DNSServers = strings.Join(dnsServers, ",")
		logx.Log("Set DNS_SERVERS from ProxmoxCluster %s/%s (aligned with the running cluster).",
			cfg.WorkloadClusterNamespace, pcName)
	}
	if !cfg.GatewayExplicit && strings.TrimSpace(gw) != "" {
		cfg.Gateway = strings.TrimSpace(gw)
		logx.Log("Set GATEWAY from ProxmoxCluster %s.", pcName)
	}
	if !cfg.IPPrefixExplicit && hasPrefix && prefix != nil {
		cfg.IPPrefix = fmt.Sprint(prefix)
		logx.Log("Set IP_PREFIX from ProxmoxCluster %s.", pcName)
	}
	if !cfg.NodeIPRangesExplicit && len(addresses) > 0 {
		cfg.NodeIPRanges = strings.Join(addresses, ",")
		logx.Log("Set NODE_IP_RANGES from ProxmoxCluster %s.", pcName)
	}
	if !cfg.AllowedNodesExplicit && len(allowedNodes) > 0 {
		cfg.AllowedNodes = strings.Join(allowedNodes, ",")
		logx.Log("Set ALLOWED_NODES from ProxmoxCluster %s.", pcName)
	}
}

// isNoMatchError matches REST-mapping errors (CRD not installed). The
// dynamic client returns a meta.NoKindMatchError-shaped error which
// apierrors.IsNotFound doesn't recognise.
func isNoMatchError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no matches for kind") ||
		strings.Contains(msg, "no matches for")
}

// unstructuredString fetches a string field from a nested map.
func unstructuredString(obj map[string]any, path ...string) (string, bool, error) {
	v, ok := unstructuredField(obj, path...)
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, nil
	}
	return s, true, nil
}

// unstructuredStringSlice fetches a []string from a nested []any field.
func unstructuredStringSlice(obj map[string]any, path ...string) []string {
	v, ok := unstructuredField(obj, path...)
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func unstructuredField(obj map[string]any, path ...string) (any, bool) {
	cur := any(obj)
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[k]
		if !ok {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// GenerateWorkloadManifestIfMissing ports generate_workload_manifest_if_missing
// (L5149-L5268). Reuses an existing manifest unless stale or the file is
// empty; otherwise runs `clusterctl generate cluster` with the
// appropriate Proxmox env vars set, writes to cfg.CAPIManifest, then
// runs ApplyRoleResourceOverrides.
//
// ensureClusterctlConfig is injected by the orchestrator — it's expected
// to return the path to a clusterctl config YAML (the ephemeral one from
// bootstrap.SyncClusterctlConfigFile).
//
// syncBootstrapConfigToKind is similarly injected.
//
// TODO: `clusterctl generate cluster` is intentionally retained as a
// shell-out — sigs.k8s.io/cluster-api/cmd/clusterctl/client exists but
// pulling it would add ~50MB of CAPI/controller-runtime/client-go-extras
// dependencies for a single call site. The exec.Command boundary is the
// least-bad option until/unless the rest of the binary already depends
// on it.
func GenerateWorkloadManifestIfMissing(
	cfg *config.Config,
	isStale func() bool,
	ensureClusterctlConfig func() string,
	syncBootstrapConfigToKind func(),
) {
	cfg.BootstrapClusterctlRegeneratedManifest = false

	fi, statErr := os.Stat(cfg.CAPIManifest)
	if statErr == nil && fi.Size() > 0 {
		if isStale != nil && isStale() {
			logx.Log("Bootstrap config is newer than the last clusterctl-generated workload manifest (workload.yaml / CAPI YAML) — regenerating with clusterctl.")
			_ = os.Truncate(cfg.CAPIManifest, 0)
		} else {
			if cfg.BootstrapCAPIUseSecret {
				logx.Log("Reusing existing workload manifest from the management Secret (use --purge to clear kind state, or set CAPI_MANIFEST / --capi-manifest for a local file; after editing config.yaml use --regenerate-capi-manifest if you only changed the in-cluster Secret).")
			} else {
				logx.Log("Reusing existing workload manifest %s (remove the file or use --purge to force clusterctl generate again; edit and save config.yaml / set PROXMOX_BOOTSTRAP_CONFIG_FILE so it is newer than this file to auto-regenerate).", cfg.CAPIManifest)
			}
			return
		}
	}

	if statErr == nil && fi.Size() == 0 {
		if cfg.BootstrapCAPIUseSecret {
			logx.Warn("Ephemeral manifest file was empty; generating workload manifest with clusterctl.")
		} else {
			logx.Warn("%s exists but is empty; regenerating it.", cfg.CAPIManifest)
		}
	}

	var missing []string
	if cfg.ProxmoxURL == "" {
		missing = append(missing, "PROXMOX_URL")
	}
	if cfg.ProxmoxRegion == "" {
		missing = append(missing, "PROXMOX_REGION")
	}
	if cfg.ProxmoxNode == "" {
		missing = append(missing, "PROXMOX_NODE")
	}
	if cfg.ProxmoxTemplateID == "" {
		missing = append(missing, "PROXMOX_TEMPLATE_ID")
	}
	if len(missing) > 0 {
		logx.Warn("Missing workload manifest inputs: %s", strings.Join(missing, " "))
		if cfg.BootstrapCAPIUseSecret {
			logx.Die("Set them as command-line options or environment variables before generating the workload cluster manifest.")
		}
		logx.Die("Set them as command-line options or environment variables before generating %s.", cfg.CAPIManifest)
	}

	if cfg.BootstrapCAPIUseSecret {
		logx.Log("Generating workload cluster manifest with clusterctl (Secret is updated after discover/label, before apply)...")
	} else {
		logx.Log("%s not found — generating workload cluster manifest with clusterctl...", cfg.CAPIManifest)
	}

	if cfg.ProxmoxCSIURL == "" {
		cfg.ProxmoxCSIURL = proxmox.APIJSONURL(cfg)
	}

	// K3s mode renders an embedded template (k3s_template.yaml). Upstream
	// CAPMOX has no K3s flavor; rather than fork a whole repo for one
	// file, we ship the K3s flavor here and skip clusterctl generate.
	// The four post-generate patches (in patches.go) still apply — they
	// match shared CAPI shapes (PMT, ProxmoxCluster) and tolerate the
	// K3s-only KThreesControlPlane / KThreesConfigTemplate documents
	// (PatchKubeadmSkipKubeProxyForCilium is a no-op against them).
	if cfg.BootstrapMode == "k3s" {
		logx.Log("BOOTSTRAP_MODE=k3s — rendering embedded K3s manifest (no clusterctl generate; upstream CAPMOX ships no k3s flavor).")
		if err := MaterializeK3sManifest(cfg, false, cfg.CAPIManifest); err != nil {
			logx.Die("render K3s manifest: %v", err)
		}
		cfg.BootstrapClusterctlRegeneratedManifest = true
		if syncBootstrapConfigToKind != nil {
			syncBootstrapConfigToKind()
		}
		return
	}

	ctlCfg := ""
	if ensureClusterctlConfig != nil {
		ctlCfg = ensureClusterctlConfig()
	}
	if ctlCfg == "" || !fileExists(ctlCfg) {
		logx.Die("clusterctl config is not available after bootstrap_sync_clusterctl_config_file (set PROXMOX_URL, PROXMOX_TOKEN, PROXMOX_SECRET, or CLUSTERCTL_CFG).")
	}

	bridge := cfg.ProxmoxBridge
	sourceNode := cfg.ProxmoxSourceNode
	if sourceNode == "" {
		sourceNode = cfg.ProxmoxNode
	}
	sshKeys := cfg.VMSSHKeys
	if sshKeys == "" {
		sshKeys = readAuthorizedKeys()
		if sshKeys != "" {
			logx.Log("Loading SSH keys from local ~/.ssh/authorized_keys (override with VM_SSH_KEYS or config.yaml)...")
		}
	}

	tmp, err := os.CreateTemp("", "capi-gen-*.yaml")
	if err != nil {
		logx.Die("Cannot create tmp manifest: %v", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()

	args := []string{
		"generate", "cluster", cfg.WorkloadClusterName,
		"--config", ctlCfg,
		"--kubernetes-version", cfg.WorkloadKubernetesVersion,
		"--control-plane-machine-count", cfg.ControlPlaneMachineCount,
		"--worker-machine-count", cfg.WorkerMachineCount,
		"--infrastructure", cfg.InfraProvider,
	}
	// (K3s mode short-circuits above; reaching here means kubeadm.)
	cmd := exec.Command("clusterctl", args...)
	cmd.Env = append(os.Environ(),
		"PROXMOX_URL="+cfg.ProxmoxURL,
		"PROXMOX_REGION="+cfg.ProxmoxRegion,
		"PROXMOX_NODE="+cfg.ProxmoxNode,
		"PROXMOX_TEMPLATE_ID="+cfg.ProxmoxTemplateID,
		"PROXMOX_POOL="+cfg.ProxmoxPool,
		"TEMPLATE_VMID="+cfg.ProxmoxTemplateID,
		"BRIDGE="+bridge,
		"PROXMOX_SOURCENODE="+sourceNode,
		"VM_SSH_KEYS="+sshKeys,
		"CONTROL_PLANE_ENDPOINT_IP="+cfg.ControlPlaneEndpointIP,
		"NODE_IP_RANGES="+cfg.NodeIPRanges,
		"GATEWAY="+cfg.Gateway,
		"IP_PREFIX="+cfg.IPPrefix,
		"DNS_SERVERS="+cfg.DNSServers,
		"ALLOWED_NODES="+cfg.AllowedNodes,
		"PROXMOX_CLOUDINIT_STORAGE="+cfg.ProxmoxCloudinitStorage,
		"BOOT_VOLUME_DEVICE="+cfg.WorkerBootVolumeDevice,
		"BOOT_VOLUME_SIZE="+cfg.WorkerBootVolumeSize,
		"NUM_SOCKETS="+cfg.WorkerNumSockets,
		"NUM_CORES="+cfg.WorkerNumCores,
		"MEMORY_MIB="+cfg.WorkerMemoryMiB,
	)
	out, err := os.Create(tmpPath)
	if err != nil {
		logx.Die("Cannot open tmp manifest: %v", err)
	}
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		out.Close()
		os.Remove(tmpPath)
		logx.Die("clusterctl generate cluster failed. Verify required template variables in %s.", ctlCfg)
	}
	out.Close()

	if fi, err := os.Stat(tmpPath); err != nil || fi.Size() == 0 {
		os.Remove(tmpPath)
		logx.Die("clusterctl generate cluster produced an empty manifest. Check template variables and provider templates.")
	}

	if err := os.Rename(tmpPath, cfg.CAPIManifest); err != nil {
		// Fallback: copy byte-by-byte (cross-device rename).
		src, _ := os.Open(tmpPath)
		dst, _ := os.Create(cfg.CAPIManifest)
		_, _ = io.Copy(dst, src)
		src.Close()
		dst.Close()
		os.Remove(tmpPath)
	}

	if err := ApplyRoleResourceOverrides(cfg); err != nil {
		logx.Die("apply_role_resource_overrides failed: %v", err)
	}
	if syncBootstrapConfigToKind != nil {
		syncBootstrapConfigToKind()
	}
	cfg.BootstrapClusterctlRegeneratedManifest = true
	if cfg.BootstrapCAPIUseSecret {
		logx.Log("Generated workload cluster manifest (ephemeral file; pushed to the management Secret after discover/label, before apply).")
	} else {
		logx.Log("Generated %s.", cfg.CAPIManifest)
	}
}

// DiscoverWorkloadClusterIdentity ports discover_workload_cluster_identity
// (L5270-L5304). Reads the manifest, finds the (first) Cluster doc, and
// sets cfg.WorkloadClusterName + cfg.WorkloadClusterNamespace.
func DiscoverWorkloadClusterIdentity(cfg *config.Config, manifestPath string) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil || len(raw) == 0 {
		logx.Die("Manifest %s is missing or empty. Regenerate it before continuing.", manifestPath)
	}
	text := string(raw)
	// Find apiVersion: cluster.x-k8s.io/...\nkind: Cluster\nmetadata:
	// and pull name/namespace out of the metadata block.
	re := regexp.MustCompile(`(?s)apiVersion:\s*cluster\.x-k8s\.io/[^\n]+\nkind:\s*Cluster\nmetadata:\n((?:  .*(?:\n|$))+)`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		logx.Die("Could not determine workload cluster name/namespace from %s.", manifestPath)
	}
	meta := m[1]
	nameRE := regexp.MustCompile(`(?m)^  name:\s*"?([^"\n]+)"?$`)
	nsRE := regexp.MustCompile(`(?m)^  namespace:\s*"?([^"\n]+)"?$`)
	mn := nameRE.FindStringSubmatch(meta)
	if mn == nil {
		logx.Die("Could not determine workload cluster name/namespace from %s.", manifestPath)
	}
	cfg.WorkloadClusterName = mn[1]
	if mns := nsRE.FindStringSubmatch(meta); mns != nil {
		cfg.WorkloadClusterNamespace = mns[1]
	} else {
		cfg.WorkloadClusterNamespace = "default"
	}
}

// EnsureWorkloadClusterLabel ports ensure_workload_cluster_label
// (L5306-L5358). Adds `cluster.x-k8s.io/cluster-name: "<name>"` to the
// first Cluster doc's metadata.labels (creating the labels block if
// absent), writing the manifest back.
func EnsureWorkloadClusterLabel(cfg *config.Config, manifestPath, clusterName string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	original := string(raw)
	docs := strings.Split(original, "\n---\n")
	for i, doc := range docs {
		lines := strings.Split(doc, "\n")
		if !sliceHas(lines, "kind: Cluster") {
			continue
		}
		hasAPI := false
		for _, l := range lines {
			if strings.HasPrefix(l, "apiVersion: cluster.x-k8s.io/") {
				hasAPI = true
				break
			}
		}
		if !hasAPI {
			continue
		}
		metaIdx := indexOf(lines, "metadata:")
		if metaIdx < 0 {
			break
		}
		blockEnd := len(lines)
		for j := metaIdx + 1; j < len(lines); j++ {
			if lines[j] != "" && !strings.HasPrefix(lines[j], " ") {
				blockEnd = j
				break
			}
		}
		labelsIdx := -1
		for j := metaIdx + 1; j < blockEnd; j++ {
			if strings.TrimSpace(lines[j]) == "labels:" {
				labelsIdx = j
				break
			}
		}
		labelLine := `    cluster.x-k8s.io/cluster-name: "` + clusterName + `"`

		if labelsIdx >= 0 {
			existing := false
			for j := labelsIdx + 1; j < blockEnd && strings.HasPrefix(lines[j], "    "); j++ {
				if strings.HasPrefix(strings.TrimSpace(lines[j]), "cluster.x-k8s.io/cluster-name:") {
					existing = true
					break
				}
			}
			if !existing {
				insertAt := labelsIdx + 1
				for insertAt < blockEnd && strings.HasPrefix(lines[insertAt], "    ") {
					insertAt++
				}
				lines = insertAtIndex(lines, insertAt, labelLine)
			}
		} else {
			insertAt := metaIdx + 1
			for insertAt < blockEnd && strings.HasPrefix(lines[insertAt], "  ") {
				insertAt++
			}
			lines = insertAtIndex(lines, insertAt, "  labels:")
			lines = insertAtIndex(lines, insertAt+1, labelLine)
		}
		docs[i] = strings.Join(lines, "\n")
		break
	}
	joined := strings.Join(docs, "\n---\n")
	if strings.HasSuffix(original, "\n") && !strings.HasSuffix(joined, "\n") {
		joined += "\n"
	}
	_ = cfg // reserved for future use (e.g. passing labels from cfg)
	return os.WriteFile(manifestPath, []byte(joined), 0o644)
}

// --- helpers ---

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func readAuthorizedKeys() string {
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if err != nil {
		return ""
	}
	var keys []string
	for _, line := range strings.Split(string(raw), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		keys = append(keys, s)
	}
	return strings.Join(keys, ",")
}

func sliceHas(s []string, want string) bool {
	for _, x := range s {
		if strings.TrimSpace(x) == want {
			return true
		}
	}
	return false
}

func indexOf(s []string, want string) int {
	for i, x := range s {
		if strings.TrimSpace(x) == want {
			return i
		}
	}
	return -1
}

func insertAtIndex(s []string, i int, v string) []string {
	s = append(s, "")
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}
