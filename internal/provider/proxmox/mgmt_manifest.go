// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package proxmox

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	capimanifest "github.com/lpasquali/yage/internal/capi/manifest"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// RenderMgmtManifest generates the CAPI manifest for the management
// cluster on Proxmox, applying all provider-specific patches.
// Implements provider.Provider.RenderMgmtManifest.
func (p *Provider) RenderMgmtManifest(cfg *config.Config, clusterctlCfgPath string) (string, error) {
	if clusterctlCfgPath == "" {
		return "", fmt.Errorf("RenderMgmtManifest: clusterctlCfgPath is empty (call SyncClusterctlConfigFile first)")
	}
	if _, err := os.Stat(clusterctlCfgPath); err != nil {
		return "", fmt.Errorf("RenderMgmtManifest: clusterctl config %s: %w", clusterctlCfgPath, err)
	}

	out := cfg.Mgmt.CAPIManifest
	if out == "" {
		f, err := os.CreateTemp("", "capi-mgmt-*.yaml")
		if err != nil {
			return "", fmt.Errorf("create tmp manifest: %w", err)
		}
		out = f.Name()
		f.Close()
		cfg.Mgmt.CAPIManifest = out
	}

	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		logx.Log("Reusing existing management cluster manifest %s.", out)
		return out, nil
	}

	logx.Log("Generating management cluster manifest with clusterctl (target: Proxmox)…")

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
	if cfg.Mgmt.ControlPlaneEndpointIP == "" {
		missing = append(missing, "MGMT_CONTROL_PLANE_ENDPOINT_IP")
	}
	if cfg.Mgmt.NodeIPRanges == "" {
		missing = append(missing, "MGMT_NODE_IP_RANGES")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing inputs for management manifest: %s", strings.Join(missing, " "))
	}

	bridge := cfg.Providers.Proxmox.Bridge
	sourceNode := cfg.Providers.Proxmox.SourceNode
	if sourceNode == "" {
		sourceNode = cfg.Providers.Proxmox.Node
	}
	sshKeys := cfg.VMSSHKeys
	if sshKeys == "" {
		sshKeys = readAuthorizedKeys()
	}

	tmp, err := os.CreateTemp("", "capi-mgmt-gen-*.yaml")
	if err != nil {
		return "", fmt.Errorf("tmp manifest: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()

	if cfg.BootstrapMode == "k3s" {
		logx.Log("BOOTSTRAP_MODE=k3s — rendering embedded K3s manifest for management cluster (no clusterctl generate).")
		if err := capimanifest.MaterializeK3sManifest(cfg, true, tmpPath); err != nil {
			return "", fmt.Errorf("render K3s mgmt manifest: %w", err)
		}
		return tmpPath, nil
	}

	args := []string{
		"generate", "cluster", cfg.Mgmt.ClusterName,
		"--config", clusterctlCfgPath,
		"--kubernetes-version", cfg.Mgmt.KubernetesVersion,
		"--control-plane-machine-count", cfg.Mgmt.ControlPlaneMachineCount,
		"--worker-machine-count", cfg.Mgmt.WorkerMachineCount,
		"--infrastructure", cfg.InfraProvider,
	}
	cmd := exec.Command("clusterctl", args...)
	cmd.Env = capimanifest.BuildEnv(
		"PROXMOX_URL="+cfg.Providers.Proxmox.URL,
		"PROXMOX_REGION="+cfg.Providers.Proxmox.Region,
		"PROXMOX_NODE="+cfg.Providers.Proxmox.Node,
		"PROXMOX_TEMPLATE_ID="+cfg.Providers.Proxmox.TemplateID,
		"PROXMOX_POOL="+cfg.Providers.Proxmox.Mgmt.Pool,
		"TEMPLATE_VMID="+cfg.Providers.Proxmox.TemplateID,
		"BRIDGE="+bridge,
		"PROXMOX_SOURCENODE="+sourceNode,
		"VM_SSH_KEYS="+sshKeys,
		"CONTROL_PLANE_ENDPOINT_IP="+cfg.Mgmt.ControlPlaneEndpointIP,
		"NODE_IP_RANGES="+cfg.Mgmt.NodeIPRanges,
		"GATEWAY="+cfg.Gateway,
		"IP_PREFIX="+cfg.IPPrefix,
		"DNS_SERVERS="+cfg.DNSServers,
		"ALLOWED_NODES="+cfg.AllowedNodes,
		"PROXMOX_CLOUDINIT_STORAGE="+cfg.Providers.Proxmox.CloudinitStorage,
		"BOOT_VOLUME_DEVICE="+cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeDevice,
		"BOOT_VOLUME_SIZE="+cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize,
		"NUM_SOCKETS="+cfg.Providers.Proxmox.Mgmt.ControlPlaneNumSockets,
		"NUM_CORES="+cfg.Providers.Proxmox.Mgmt.ControlPlaneNumCores,
		"MEMORY_MIB="+cfg.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB,
	)

	outFile, err := os.Create(tmpPath)
	if err != nil {
		return "", fmt.Errorf("open tmp manifest: %w", err)
	}
	cmd.Stdout = outFile
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		outFile.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("clusterctl generate cluster (mgmt) failed: %w", runErr)
	}
	outFile.Close()

	if fi, err := os.Stat(tmpPath); err != nil || fi.Size() == 0 {
		os.Remove(tmpPath)
		return "", fmt.Errorf("clusterctl produced an empty mgmt manifest")
	}

	if err := os.Rename(tmpPath, out); err != nil {
		src, _ := os.Open(tmpPath)
		dst, _ := os.Create(out)
		_, _ = io.Copy(dst, src)
		src.Close()
		dst.Close()
		os.Remove(tmpPath)
	}

	if err := applyMgmtPatches(cfg, out); err != nil {
		return "", fmt.Errorf("apply mgmt patches: %w", err)
	}

	logx.Log("Generated management cluster manifest %s.", out)
	return out, nil
}

// applyMgmtPatches runs the four manifest patches (role/resource overrides,
// CSI topology labels, kubeadm skip-kube-proxy, PMT spec revisions) against
// the management-cluster manifest at manifestPath.
func applyMgmtPatches(cfg *config.Config, manifestPath string) error {
	if err := mgmtRoleResourceOverrides(cfg, manifestPath); err != nil {
		return fmt.Errorf("role resource overrides: %w", err)
	}

	saveManifest := cfg.CAPIManifest
	cfg.CAPIManifest = manifestPath
	defer func() { cfg.CAPIManifest = saveManifest }()

	if err := capimanifest.PatchProxmoxCSITopologyLabels(cfg); err != nil {
		return fmt.Errorf("csi topology labels: %w", err)
	}
	if err := capimanifest.PatchKubeadmSkipKubeProxyForCilium(cfg); err != nil {
		return fmt.Errorf("kubeadm skip kube-proxy: %w", err)
	}
	if _, err := capimanifest.PatchProxmoxMachineTemplateSpecRevisions(cfg); err != nil {
		return fmt.Errorf("PMT spec revisions: %w", err)
	}
	return nil
}

// mgmtRoleResourceOverrides patches every ProxmoxMachineTemplate in the
// management-cluster manifest with the MGMT_* hardware sizing and per-role
// template overrides.
func mgmtRoleResourceOverrides(cfg *config.Config, manifestPath string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	text := string(raw)

	mgmtCPTpl := firstNonEmpty(cfg.Providers.Proxmox.Mgmt.ControlPlaneTemplateID, cfg.Providers.Proxmox.TemplateID)
	mgmtWkTpl := firstNonEmpty(cfg.Providers.Proxmox.Mgmt.WorkerTemplateID, cfg.Providers.Proxmox.TemplateID)
	parts := strings.Split(text, "\n---\n")
	nameRE := regexp.MustCompile(`(?m)^  name:\s*(\S+)\s*$`)
	for i, doc := range parts {
		if !strings.Contains(doc, "kind: ProxmoxMachineTemplate") {
			continue
		}
		m := nameRE.FindStringSubmatch(doc)
		if m == nil {
			continue
		}
		doc = replaceFirst(doc, `(disk:\s*)[^\n]+`, "${1}"+cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeDevice)
		doc = replaceFirst(doc, `(sizeGb:\s*)[^\n]+`, "${1}"+cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize)
		doc = replaceFirst(doc, `(numSockets:\s*)[^\n]+`, "${1}"+cfg.Providers.Proxmox.Mgmt.ControlPlaneNumSockets)
		doc = replaceFirst(doc, `(numCores:\s*)[^\n]+`, "${1}"+cfg.Providers.Proxmox.Mgmt.ControlPlaneNumCores)
		doc = replaceFirst(doc, `(memoryMiB:\s*)[^\n]+`, "${1}"+cfg.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB)
		tpl := mgmtCPTpl
		if strings.Contains(m[1], "worker") {
			tpl = mgmtWkTpl
		}
		if tpl != "" {
			doc = replaceFirst(doc, `(templateID:\s*)[^\n]+`, "${1}"+tpl)
		}
		parts[i] = doc
	}
	text = strings.Join(parts, "\n---\n")

	text = scalarCSVToYAMLList(text, "allowedNodes")
	text = scalarCSVToYAMLList(text, "dnsServers")
	text = scalarCSVToYAMLList(text, "addresses")
	text = injectMemoryAdjustment(text, cfg.Providers.Proxmox.MemoryAdjustment)

	return os.WriteFile(manifestPath, []byte(text), 0o644)
}

func replaceFirst(s, pattern, repl string) string {
	return regexp.MustCompile(pattern).ReplaceAllString(s, repl)
}

func scalarCSVToYAMLList(text, key string) string {
	pat := regexp.MustCompile(`(?m)^( *)(` + regexp.QuoteMeta(key) + `):\s*"?([^"\n\[]+)"?\s*$`)
	return pat.ReplaceAllStringFunc(text, func(line string) string {
		m := pat.FindStringSubmatch(line)
		if m == nil {
			return line
		}
		indent := m[1]
		raw := strings.TrimSpace(m[3])
		raw = strings.Trim(raw, `"'`)
		var items []string
		for _, v := range strings.Split(raw, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				items = append(items, v)
			}
		}
		lines := []string{indent + key + ":"}
		for _, it := range items {
			lines = append(lines, indent+"- "+it)
		}
		return strings.Join(lines, "\n")
	})
}

var (
	reKindProxmoxCluster = regexp.MustCompile(`(?m)^kind:\s*ProxmoxCluster\s*$`)
	reAPIInfraProxmox    = regexp.MustCompile(`(?m)^apiVersion:\s*infrastructure\.cluster\.x-k8s\.io/`)
	reSchedulerHints     = regexp.MustCompile(`(?m)^  schedulerHints:`)
	reSpecBlock          = regexp.MustCompile(`(?m)^(spec:\n(?:  .*\n)+)`)
)

func injectMemoryAdjustment(text, mem string) string {
	if mem == "" {
		return text
	}
	parts := strings.Split(text, "\n---\n")
	for i, doc := range parts {
		if !reKindProxmoxCluster.MatchString(doc) {
			continue
		}
		if !reAPIInfraProxmox.MatchString(doc) {
			continue
		}
		if reSchedulerHints.MatchString(doc) {
			break
		}
		if !strings.HasSuffix(doc, "\n") {
			doc += "\n"
		}
		loc := reSpecBlock.FindStringIndex(doc)
		if loc == nil {
			break
		}
		oldBlock := doc[loc[0]:loc[1]]
		newBlock := strings.TrimRight(oldBlock, "\n") +
			"\n  schedulerHints:\n    memoryAdjustment: " + mem + "\n"
		parts[i] = doc[:loc[0]] + newBlock + doc[loc[1]:]
		break
	}
	return strings.Join(parts, "\n---\n")
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
