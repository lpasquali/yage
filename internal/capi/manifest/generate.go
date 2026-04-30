// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

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
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/provider/proxmox/api"
)

// TryFillWorkloadInputsFromManagement is a best-effort fill from
// the management cluster: pulls PROXMOX_TEMPLATE_ID / PROXMOX_NODE
// from existing ProxmoxMachineTemplates, and network + DNS from the
// live ProxmoxCluster when the workload is selected. Guards respect
// the *_EXPLICIT flags so CLI-locked keys are not overwritten.
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
		// CRD missing or not yet installed — bail.
		if apierrors.IsNotFound(err) || isNoMatchError(err) {
			return
		}
		return
	}

	if cfg.Providers.Proxmox.TemplateID == "" {
		idRE := regexp.MustCompile(`^[0-9]+$`)
		for _, item := range pmtList.Items {
			id, _, _ := unstructuredString(item.Object, "spec", "template", "spec", "templateID")
			id = strings.TrimSpace(id)
			if id != "" && idRE.MatchString(id) {
				cfg.Providers.Proxmox.TemplateID = id
				logx.Log("Set PROXMOX_TEMPLATE_ID=%s from an existing ProxmoxMachineTemplate on %s.", id, mctx)
				break
			}
		}
	}

	if cfg.Providers.Proxmox.Node == "" {
		for _, item := range pmtList.Items {
			node, _, _ := unstructuredString(item.Object, "spec", "template", "spec", "sourceNode")
			node = strings.TrimSpace(node)
			if node != "" {
				cfg.Providers.Proxmox.Node = node
				if !cfg.AllowedNodesExplicit && cfg.AllowedNodes == "" {
					cfg.AllowedNodes = cfg.Providers.Proxmox.Node
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
		// Fallback to workload cluster name.
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

// GenerateWorkloadManifestIfMissing reuses an existing manifest
// unless stale or the file is empty; otherwise runs `clusterctl
// generate cluster` with the appropriate Proxmox env vars set,
// writes to cfg.CAPIManifest, then runs ApplyRoleResourceOverrides.
//
// ensureClusterctlConfig is injected by the orchestrator — it's expected
// to return the path to a clusterctl config YAML (the ephemeral one from
// orchestrator.SyncClusterctlConfigFile).
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

	switch cfg.InfraProvider {
	case "aws":
		if os.Getenv("AWS_ACCESS_KEY_ID") == "" || os.Getenv("AWS_SECRET_ACCESS_KEY") == "" {
			logx.Die("Set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY before generating the workload cluster manifest.")
		}
		if cfg.Providers.AWS.SSHKeyName == "" {
			logx.Die("Set AWS_SSH_KEY_NAME before generating an AWS workload manifest (required by the upstream CAPA cluster template).")
		}
	case "proxmox":
		var missing []string
		if cfg.Providers.Proxmox.URL == "" {
			missing = append(missing, "PROXMOX_URL")
		}
		if cfg.Providers.Proxmox.Region == "" {
			missing = append(missing, "PROXMOX_REGION")
		}
		if cfg.Providers.Proxmox.Node == "" {
			missing = append(missing, "PROXMOX_NODE")
		}
		if cfg.Providers.Proxmox.TemplateID == "" {
			missing = append(missing, "PROXMOX_TEMPLATE_ID")
		}
		if len(missing) > 0 {
			logx.Warn("Missing workload manifest inputs: %s", strings.Join(missing, " "))
			if cfg.BootstrapCAPIUseSecret {
				logx.Die("Set them as command-line options or environment variables before generating the workload cluster manifest.")
			}
			logx.Die("Set them as command-line options or environment variables before generating %s.", cfg.CAPIManifest)
		}
	default:
		logx.Die("automatic clusterctl generate cluster for --infrastructure %q is not implemented; use proxmox or supply a pre-rendered manifest (CAPI_MANIFEST / --capi-manifest).", cfg.InfraProvider)
	}

	if cfg.BootstrapCAPIUseSecret {
		logx.Log("Generating workload cluster manifest with clusterctl (Secret is updated after discover/label, before apply)...")
	} else {
		logx.Log("%s not found — generating workload cluster manifest with clusterctl...", cfg.CAPIManifest)
	}

	// TODO(#71): derive CSIURL via Provider method rather than direct Proxmox field access.
	if cfg.InfraProvider == "proxmox" && cfg.Providers.Proxmox.CSIURL == "" {
		cfg.Providers.Proxmox.CSIURL = api.APIJSONURL(cfg)
	}

	// K3s mode renders an embedded template (k3s_template.yaml). Upstream
	// CAPMOX has no K3s flavor; rather than fork a whole repo for one
	// file, we ship the K3s flavor here and skip clusterctl generate.
	// The four post-generate patches (in patches.go) still apply — they
	// match shared CAPI shapes (PMT, ProxmoxCluster) and tolerate the
	// K3s-only KThreesControlPlane / KThreesConfigTemplate documents
	// (PatchKubeadmSkipKubeProxyForCilium is a no-op against them).
	if cfg.BootstrapMode == "k3s" {
		// TODO(#71): replace with prov.K3sTemplate check (ErrNotApplicable)
		// once non-Proxmox K3s support is planned.
		if cfg.InfraProvider != "proxmox" {
			logx.Die("BOOTSTRAP_MODE=k3s is only supported with --infra-provider proxmox today.")
		}
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
	// TODO(#71): move clusterctl config requirement check into Provider or
	// SyncClusterctlConfigFile so this explicit provider check goes away.
	if cfg.InfraProvider == "proxmox" {
		if ctlCfg == "" || !fileExists(ctlCfg) {
			logx.Die("clusterctl config is not available after bootstrap_sync_clusterctl_config_file (set PROXMOX_URL, PROXMOX_TOKEN, PROXMOX_SECRET, or CLUSTERCTL_CFG).")
		}
	} else if ctlCfg != "" && !fileExists(ctlCfg) {
		logx.Die("clusterctl config file does not exist: %s", ctlCfg)
	}

	tmp, err := os.CreateTemp("", "capi-gen-*.yaml")
	if err != nil {
		logx.Die("Cannot create tmp manifest: %v", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()

	var cmd *exec.Cmd
	switch cfg.InfraProvider {
	case "proxmox":
		bridge := cfg.Providers.Proxmox.Bridge
		sourceNode := cfg.Providers.Proxmox.SourceNode
		if sourceNode == "" {
			sourceNode = cfg.Providers.Proxmox.Node
		}
		sshKeys := cfg.VMSSHKeys
		if sshKeys == "" {
			sshKeys = readAuthorizedKeys()
			if sshKeys != "" {
				logx.Log("Loading SSH keys from local ~/.ssh/authorized_keys (override with VM_SSH_KEYS or config.yaml)...")
			}
		}
		args := []string{
			"generate", "cluster", cfg.WorkloadClusterName,
			"--config", ctlCfg,
			"--kubernetes-version", cfg.WorkloadKubernetesVersion,
			"--control-plane-machine-count", cfg.ControlPlaneMachineCount,
			"--worker-machine-count", cfg.WorkerMachineCount,
			"--infrastructure", cfg.InfraProvider,
		}
		cmd = exec.Command("clusterctl", args...)
		cmd.Env = BuildEnv(
			"PROXMOX_URL="+cfg.Providers.Proxmox.URL,
			"PROXMOX_REGION="+cfg.Providers.Proxmox.Region,
			"PROXMOX_NODE="+cfg.Providers.Proxmox.Node,
			"PROXMOX_TEMPLATE_ID="+cfg.Providers.Proxmox.TemplateID,
			"PROXMOX_POOL="+cfg.Providers.Proxmox.Pool,
			"TEMPLATE_VMID="+cfg.Providers.Proxmox.TemplateID,
			"BRIDGE="+bridge,
			"PROXMOX_SOURCENODE="+sourceNode,
			"VM_SSH_KEYS="+sshKeys,
			"CONTROL_PLANE_ENDPOINT_IP="+cfg.ControlPlaneEndpointIP,
			"NODE_IP_RANGES="+cfg.NodeIPRanges,
			"GATEWAY="+cfg.Gateway,
			"IP_PREFIX="+cfg.IPPrefix,
			"DNS_SERVERS="+cfg.DNSServers,
			"ALLOWED_NODES="+cfg.AllowedNodes,
			"PROXMOX_CLOUDINIT_STORAGE="+cfg.Providers.Proxmox.CloudinitStorage,
			"BOOT_VOLUME_DEVICE="+cfg.Providers.Proxmox.WorkerBootVolumeDevice,
			"BOOT_VOLUME_SIZE="+cfg.Providers.Proxmox.WorkerBootVolumeSize,
			"NUM_SOCKETS="+cfg.Providers.Proxmox.WorkerNumSockets,
			"NUM_CORES="+cfg.Providers.Proxmox.WorkerNumCores,
			"MEMORY_MIB="+cfg.Providers.Proxmox.WorkerMemoryMiB,
		)
	case "aws":
		args := []string{
			"generate", "cluster", cfg.WorkloadClusterName,
		}
		if ctlCfg != "" {
			args = append(args, "--config", ctlCfg)
		}
		args = append(args,
			"--kubernetes-version", cfg.WorkloadKubernetesVersion,
			"--control-plane-machine-count", cfg.ControlPlaneMachineCount,
			"--worker-machine-count", cfg.WorkerMachineCount,
			"--infrastructure", "aws",
		)
		cmd = exec.Command("clusterctl", args...)
		region := cfg.Providers.AWS.Region
		if region == "" {
			region = "us-east-1"
		}
		cpType := cfg.Providers.AWS.ControlPlaneMachineType
		if cpType == "" {
			cpType = "t3.large"
		}
		nodeType := cfg.Providers.AWS.NodeMachineType
		if nodeType == "" {
			nodeType = "t3.medium"
		}
		cmd.Env = BuildEnv(
			"AWS_REGION="+region,
			"AWS_CONTROL_PLANE_MACHINE_TYPE="+cpType,
			"AWS_NODE_MACHINE_TYPE="+nodeType,
			"AWS_SSH_KEY_NAME="+cfg.Providers.AWS.SSHKeyName,
			"AWS_AMI_ID="+cfg.Providers.AWS.AMIID,
			"EXP_CLUSTER_RESOURCE_SET=false",
		)
	default:
		logx.Die("internal error: unhandled infrastructure %q in clusterctl generate path", cfg.InfraProvider)
	}

	out, err := os.Create(tmpPath)
	if err != nil {
		logx.Die("Cannot open tmp manifest: %v", err)
	}
	cmd.Stdout = out
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		out.Close()
		os.Remove(tmpPath)
		if cfg.InfraProvider == "aws" {
			logx.Die("clusterctl generate cluster failed for AWS. Verify AWS_* template variables, SSH key, and credentials.")
		}
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

// DiscoverWorkloadClusterIdentity reads the manifest, finds the
// (first) Cluster doc, and sets cfg.WorkloadClusterName +
// cfg.WorkloadClusterNamespace.
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

// EnsureWorkloadClusterLabel adds
// `cluster.x-k8s.io/cluster-name: "<name>"` to the first Cluster
// doc's metadata.labels (creating the labels block if absent),
// writing the manifest back.
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

// BuildEnv returns os.Environ() with any keys present in overrides
// removed, then appends overrides. This prevents duplicate env
// entries — and avoids E2BIG ("argument list too long") — when a
// var we are about to set (e.g. VM_SSH_KEYS) is already present and
// possibly large in the parent-process environment.
func BuildEnv(overrides ...string) []string {
	block := make(map[string]struct{}, len(overrides))
	for _, kv := range overrides {
		if i := strings.IndexByte(kv, '='); i > 0 {
			block[kv[:i]] = struct{}{}
		}
	}
	base := os.Environ()
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i > 0 {
			if _, dup := block[kv[:i]]; dup {
				continue
			}
		}
		out = append(out, kv)
	}
	return append(out, overrides...)
}

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