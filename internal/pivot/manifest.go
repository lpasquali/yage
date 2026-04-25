package pivot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/capimanifest"
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
)

// renderManagementManifest generates the CAPI manifest for the management
// cluster on Proxmox by shelling out to `clusterctl generate cluster`,
// mirroring capimanifest.GenerateWorkloadManifestIfMissing but with MGMT_*
// inputs. Writes the rendered manifest to cfg.MgmtCAPIManifest (or a temp
// file when empty) and returns the path on disk.
//
// After generation, applies the same four patches the workload manifest
// gets, except via mgmt-scoped helpers (the workload helpers in
// capimanifest read cfg.CAPIManifest / cfg.WorkloadCluster*; we wrap them
// by temporarily redirecting the relevant fields).
//
// clusterctlCfgPath is the same ephemeral clusterctl config file the kind
// init phase generated; reuse it so PROXMOX_URL / token wiring matches.
func renderManagementManifest(cfg *config.Config, clusterctlCfgPath string) (string, error) {
	if clusterctlCfgPath == "" {
		return "", fmt.Errorf("renderManagementManifest: clusterctlCfgPath is empty (call SyncClusterctlConfigFile first)")
	}
	if _, err := os.Stat(clusterctlCfgPath); err != nil {
		return "", fmt.Errorf("renderManagementManifest: clusterctl config %s: %w", clusterctlCfgPath, err)
	}

	// Determine output path: cfg.MgmtCAPIManifest wins; otherwise a
	// stable temp file under os.TempDir so subsequent runs reuse it.
	out := cfg.MgmtCAPIManifest
	if out == "" {
		f, err := os.CreateTemp("", "capi-mgmt-*.yaml")
		if err != nil {
			return "", fmt.Errorf("create tmp manifest: %w", err)
		}
		out = f.Name()
		f.Close()
		cfg.MgmtCAPIManifest = out
	}

	// Reuse an existing non-empty manifest. The mgmt manifest has no
	// "stale vs config.yaml" check — there is no per-mgmt analogue of
	// the workload bootstrap config — so we keep it simple: if the
	// file is present and non-empty, reuse it.
	if fi, err := os.Stat(out); err == nil && fi.Size() > 0 {
		logx.Log("Reusing existing management cluster manifest %s.", out)
		return out, nil
	}

	logx.Log("Generating management cluster manifest with clusterctl (target: Proxmox)…")

	// Required inputs.
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
	if cfg.MgmtControlPlaneEndpointIP == "" {
		missing = append(missing, "MGMT_CONTROL_PLANE_ENDPOINT_IP")
	}
	if cfg.MgmtNodeIPRanges == "" {
		missing = append(missing, "MGMT_NODE_IP_RANGES")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing inputs for management manifest: %s", strings.Join(missing, " "))
	}

	bridge := cfg.ProxmoxBridge
	sourceNode := cfg.ProxmoxSourceNode
	if sourceNode == "" {
		sourceNode = cfg.ProxmoxNode
	}
	sshKeys := cfg.VMSSHKeys
	if sshKeys == "" {
		sshKeys = readAuthorizedKeys()
	}

	// Stage to a tmp file and rename atomically when complete (matches
	// capimanifest.GenerateWorkloadManifestIfMissing).
	tmp, err := os.CreateTemp("", "capi-mgmt-gen-*.yaml")
	if err != nil {
		return "", fmt.Errorf("tmp manifest: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()

	// `clusterctl generate cluster` reads template variables from env;
	// the workload phase exports WORKLOAD_*-equivalent vars (BOOT_VOLUME_*,
	// NUM_*, MEMORY_MIB, NODE_IP_RANGES, CONTROL_PLANE_ENDPOINT_IP, etc.).
	// We override those with MGMT_* values so the same Proxmox provider
	// template renders the management cluster.
	cmd := exec.Command("clusterctl", "generate", "cluster", cfg.MgmtClusterName,
		"--config", clusterctlCfgPath,
		"--kubernetes-version", cfg.MgmtKubernetesVersion,
		"--control-plane-machine-count", cfg.MgmtControlPlaneMachineCount,
		"--worker-machine-count", cfg.MgmtWorkerMachineCount,
		"--infrastructure", cfg.InfraProvider,
	)
	cmd.Env = append(os.Environ(),
		"PROXMOX_URL="+cfg.ProxmoxURL,
		"PROXMOX_REGION="+cfg.ProxmoxRegion,
		"PROXMOX_NODE="+cfg.ProxmoxNode,
		"PROXMOX_TEMPLATE_ID="+cfg.ProxmoxTemplateID,
		"TEMPLATE_VMID="+cfg.ProxmoxTemplateID,
		"BRIDGE="+bridge,
		"PROXMOX_SOURCENODE="+sourceNode,
		"VM_SSH_KEYS="+sshKeys,
		"CONTROL_PLANE_ENDPOINT_IP="+cfg.MgmtControlPlaneEndpointIP,
		"NODE_IP_RANGES="+cfg.MgmtNodeIPRanges,
		"GATEWAY="+cfg.Gateway,
		"IP_PREFIX="+cfg.IPPrefix,
		"DNS_SERVERS="+cfg.DNSServers,
		"ALLOWED_NODES="+cfg.AllowedNodes,
		"PROXMOX_CLOUDINIT_STORAGE="+cfg.ProxmoxCloudinitStorage,
		// MGMT_* hardware sizing maps onto the same template vars the
		// workload uses (the worker* knobs in cfg). Both control-plane
		// and worker entries in the rendered manifest get these values;
		// patchPMTBlockMgmt below rewrites them per-role afterwards.
		"BOOT_VOLUME_DEVICE="+cfg.MgmtControlPlaneBootVolumeDevice,
		"BOOT_VOLUME_SIZE="+cfg.MgmtControlPlaneBootVolumeSize,
		"NUM_SOCKETS="+cfg.MgmtControlPlaneNumSockets,
		"NUM_CORES="+cfg.MgmtControlPlaneNumCores,
		"MEMORY_MIB="+cfg.MgmtControlPlaneMemoryMiB,
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
		// Cross-device rename fallback.
		src, _ := os.Open(tmpPath)
		dst, _ := os.Create(out)
		_, _ = io.Copy(dst, src)
		src.Close()
		dst.Close()
		os.Remove(tmpPath)
	}

	// --- Apply the four patches the workload manifest gets, but
	// targeting the mgmt manifest path + mgmt cfg values. The helpers
	// in internal/capimanifest read cfg.CAPIManifest /
	// cfg.WorkloadCluster*; redirect those temporarily.
	if err := applyMgmtPatches(cfg, out); err != nil {
		return "", fmt.Errorf("apply mgmt patches: %w", err)
	}

	logx.Log("Generated management cluster manifest %s.", out)
	return out, nil
}

// applyMgmtPatches runs the same 4 patches the workload manifest gets
// (apply_role_resource_overrides, patch_csi_topology_labels,
// patch_kubeadm_skip_kube_proxy_for_cilium,
// patch_proxmox_machine_template_spec_revisions) but against the
// management-cluster manifest at manifestPath, with MGMT_* hardware
// values taking precedence.
//
// The capimanifest helpers operate on cfg.CAPIManifest globally, so we
// shadow the relevant fields for the duration of the call. We also
// re-implement role-resource overrides locally because the bash helper
// (and its Go port) hard-codes the workload cp/worker config and we
// need to substitute MGMT_CONTROL_PLANE_* for control-plane PMTs.
func applyMgmtPatches(cfg *config.Config, manifestPath string) error {
	// 1) Role/resource overrides — locally implemented (mgmt sizing).
	if err := mgmtRoleResourceOverrides(cfg, manifestPath); err != nil {
		return fmt.Errorf("role resource overrides: %w", err)
	}

	// Save + redirect cfg fields the capimanifest helpers read.
	saveManifest := cfg.CAPIManifest
	cfg.CAPIManifest = manifestPath
	defer func() { cfg.CAPIManifest = saveManifest }()

	// 2) CSI topology labels — same region/zone wiring as workload.
	if err := capimanifest.PatchProxmoxCSITopologyLabels(cfg); err != nil {
		return fmt.Errorf("csi topology labels: %w", err)
	}

	// 3) skip addon/kube-proxy for Cilium when KPR is on.
	if err := capimanifest.PatchKubeadmSkipKubeProxyForCilium(cfg); err != nil {
		return fmt.Errorf("kubeadm skip kube-proxy: %w", err)
	}

	// 4) ProxmoxMachineTemplate spec-revision rename.
	if _, err := capimanifest.PatchProxmoxMachineTemplateSpecRevisions(cfg); err != nil {
		return fmt.Errorf("PMT spec revisions: %w", err)
	}
	return nil
}

// mgmtRoleResourceOverrides is the mgmt-cluster analogue of
// capimanifest.ApplyRoleResourceOverrides. We re-implement it here
// instead of borrowing because the workload helper hard-codes the
// workload control-plane vs. worker sizing; the management cluster
// uses MGMT_CONTROL_PLANE_* for both the (typically single) control
// plane node and any management workers.
func mgmtRoleResourceOverrides(cfg *config.Config, manifestPath string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	text := string(raw)

	// Patch every PMT (control-plane or worker) using the mgmt sizing.
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
		// Replace the five hardware fields. Same regex shapes as the
		// workload helper (capimanifest.patchPMTBlock).
		doc = replaceFirstPerLine(doc, `(disk:\s*)[^\n]+`, "${1}"+cfg.MgmtControlPlaneBootVolumeDevice)
		doc = replaceFirstPerLine(doc, `(sizeGb:\s*)[^\n]+`, "${1}"+cfg.MgmtControlPlaneBootVolumeSize)
		doc = replaceFirstPerLine(doc, `(numSockets:\s*)[^\n]+`, "${1}"+cfg.MgmtControlPlaneNumSockets)
		doc = replaceFirstPerLine(doc, `(numCores:\s*)[^\n]+`, "${1}"+cfg.MgmtControlPlaneNumCores)
		doc = replaceFirstPerLine(doc, `(memoryMiB:\s*)[^\n]+`, "${1}"+cfg.MgmtControlPlaneMemoryMiB)
		parts[i] = doc
	}
	text = strings.Join(parts, "\n---\n")

	// Convert comma-scalar fields to YAML lists (allowedNodes, dnsServers,
	// addresses) — same shape as the workload helper.
	text = scalarCSVToYAMLList(text, "allowedNodes")
	text = scalarCSVToYAMLList(text, "dnsServers")
	text = scalarCSVToYAMLList(text, "addresses")
	text = injectMemoryAdjustment(text, cfg.ProxmoxMemoryAdjustment)

	return os.WriteFile(manifestPath, []byte(text), 0o644)
}

// renderMgmtCiliumHelmChartProxy renders a HelmChartProxy YAML doc that
// targets the management cluster (not the workload). It uses the
// existing CAAPH selector convention: select Clusters labelled
// `caaph: enabled` and `caaph.cilium.cluster-id: <MgmtCiliumClusterID>`.
//
// Returns the YAML doc as a string; the caller server-side-applies it.
//
// Mirrors caaph.CiliumHelmChartProxyYAML conceptually but kept local to
// avoid coupling that helper to mgmt-vs-workload selection logic.
func renderMgmtCiliumHelmChartProxy(cfg *config.Config) string {
	ver := strings.TrimPrefix(cfg.CiliumVersion, "v")
	if ver == "" {
		ver = "1.19.3"
	}
	ns := cfg.MgmtClusterNamespace
	if ns == "" {
		ns = "default"
	}
	name := cfg.MgmtClusterName
	if name == "" {
		name = "capi-management"
	}
	clusterID := cfg.MgmtCiliumClusterID
	if clusterID == "" {
		// Stable derivation: reuse the workload cluster ID if set,
		// else derive from the cluster name. Differ from workload by
		// suffixing — keeps the two cluster-id values distinct.
		clusterID = deriveMgmtClusterID(cfg)
	}

	var vt strings.Builder
	g := func(s string) string { return "{{ " + s + " }}" }
	vt.WriteString("cluster:\n")
	fmt.Fprintf(&vt, "  name: %s\n", g(".Cluster.metadata.name"))
	fmt.Fprintf(&vt, "  id: %s\n", clusterID)
	vt.WriteString("kubeProxyReplacement: true\n")
	fmt.Fprintf(&vt, "k8sServiceHost: %s\n", cfg.MgmtControlPlaneEndpointIP)
	port := cfg.MgmtControlPlaneEndpointPort
	if port == "" {
		port = "6443"
	}
	fmt.Fprintf(&vt, "k8sServicePort: %s\n", port)
	pool := strings.ReplaceAll(strings.TrimSpace(cfg.CiliumIPAMClusterPoolIPv4), `"`, "")
	if pool == "" {
		pool = "10.244.0.0/16"
	}
	mask := strings.TrimSpace(cfg.CiliumIPAMClusterPoolIPv4MaskSize)
	if mask == "" {
		mask = "24"
	}
	fmt.Fprintf(&vt, "ipam:\n  operator:\n    clusterPoolIPv4PodCIDRList: [%q]\n    clusterPoolIPv4MaskSize: %s\n", pool, mask)

	var sb strings.Builder
	fmt.Fprintln(&sb, "apiVersion: addons.cluster.x-k8s.io/v1alpha1")
	fmt.Fprintln(&sb, "kind: HelmChartProxy")
	fmt.Fprintln(&sb, "metadata:")
	fmt.Fprintf(&sb, "  name: %s-caaph-cilium\n", name)
	fmt.Fprintf(&sb, "  namespace: %s\n", ns)
	fmt.Fprintln(&sb, "spec:")
	fmt.Fprintln(&sb, "  clusterSelector:")
	fmt.Fprintln(&sb, "    matchLabels:")
	fmt.Fprintln(&sb, "      caaph: enabled")
	fmt.Fprintf(&sb, "      caaph.cilium.cluster-id: %q\n", clusterID)
	fmt.Fprintln(&sb, "  chartName: cilium")
	fmt.Fprintln(&sb, "  repoURL: https://helm.cilium.io/")
	fmt.Fprintf(&sb, "  version: %q\n", ver)
	fmt.Fprintln(&sb, "  namespace: kube-system")
	fmt.Fprintln(&sb, "  options:")
	fmt.Fprintln(&sb, "    wait: true")
	fmt.Fprintln(&sb, "    waitForJobs: true")
	fmt.Fprintln(&sb, "    timeout: 15m0s")
	fmt.Fprintln(&sb, "    install:")
	fmt.Fprintln(&sb, "      createNamespace: true")
	fmt.Fprintln(&sb, "  valuesTemplate: |")
	for _, ln := range strings.Split(strings.TrimRight(vt.String(), "\n"), "\n") {
		fmt.Fprintln(&sb, "    "+ln)
	}
	return sb.String()
}

// patchMgmtClusterCAAPHLabels writes the cilium-cluster-id label and
// `caaph: enabled` into the mgmt Cluster doc on disk so CAAPH selects
// the right cluster. The mgmt Cluster object exists on the kind cluster
// after the manifest is applied; we patch the YAML so the label is
// applied at create time. Live-patching after apply happens via the
// same dynamic-client patch in pivot.go.
func patchMgmtClusterCAAPHLabels(cfg *config.Config, manifestPath string) error {
	if cfg.MgmtCiliumClusterID == "" {
		cfg.MgmtCiliumClusterID = deriveMgmtClusterID(cfg)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	text := string(raw)
	docs := strings.Split(text, "\n---\n")
	port := cfg.MgmtControlPlaneEndpointPort
	if port == "" {
		port = "6443"
	}
	labels := []struct{ k, v string }{
		{"caaph", "enabled"},
		{"caaph.cilium.cluster-id", cfg.MgmtCiliumClusterID},
		{"caaph.cilium.k8s-service-host", cfg.MgmtControlPlaneEndpointIP},
		{"caaph.cilium.k8s-service-port", port},
	}

	for i, doc := range docs {
		lines := strings.Split(doc, "\n")
		if !sliceHasLine(lines, "kind: Cluster") {
			continue
		}
		// Must be the CAPI Cluster (cluster.x-k8s.io), not the
		// ProxmoxCluster or other infra Cluster kinds.
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
		metaIdx := indexOfLine(lines, "metadata:")
		if metaIdx < 0 {
			continue
		}
		end := len(lines)
		for j := metaIdx + 1; j < len(lines); j++ {
			if lines[j] != "" && !strings.HasPrefix(lines[j], " ") {
				end = j
				break
			}
		}
		labelsIdx := -1
		for j := metaIdx + 1; j < end; j++ {
			if strings.TrimSpace(lines[j]) == "labels:" {
				labelsIdx = j
				break
			}
		}
		newLines := append([]string{}, lines...)
		for _, lbl := range labels {
			if lbl.v == "" && lbl.k != "caaph" {
				continue
			}
			keyRE := regexp.MustCompile(`^    ` + regexp.QuoteMeta(lbl.k) + `:`)
			have := false
			for _, x := range newLines[metaIdx:end] {
				if keyRE.MatchString(x) {
					have = true
					break
				}
			}
			if have {
				continue
			}
			var entry string
			if lbl.v == "enabled" && lbl.k == "caaph" {
				entry = "    caaph: enabled"
			} else {
				entry = fmt.Sprintf("    %s: %q", lbl.k, lbl.v)
			}
			if labelsIdx >= 0 {
				insertAt := labelsIdx + 1
				for insertAt < end && strings.HasPrefix(newLines[insertAt], "    ") &&
					!strings.HasPrefix(newLines[insertAt], "      ") {
					insertAt++
				}
				newLines = insertAtIndex(newLines, insertAt, entry)
				end++
			} else {
				insertAt := metaIdx + 1
				for insertAt < end && strings.HasPrefix(newLines[insertAt], "  ") {
					insertAt++
				}
				newLines = insertAtIndex(newLines, insertAt, "  labels:")
				newLines = insertAtIndex(newLines, insertAt+1, entry)
				labelsIdx = insertAt
				end += 2
			}
		}
		docs[i] = strings.Join(newLines, "\n")
	}
	return os.WriteFile(manifestPath, []byte(strings.Join(docs, "\n---\n")), 0o644)
}

// deriveMgmtClusterID derives a stable Cilium cluster-id value for the
// mgmt cluster from its name (so workload + mgmt clusters never share
// the same ClusterMesh ID). 1..255 range, same shape as
// proxmox.DeriveCiliumClusterID.
func deriveMgmtClusterID(cfg *config.Config) string {
	if cfg.MgmtCiliumClusterID != "" {
		return cfg.MgmtCiliumClusterID
	}
	src := cfg.MgmtClusterName
	if src == "" {
		src = "capi-management"
	}
	sum := sha256.Sum256([]byte(src))
	hexStr := hex.EncodeToString(sum[:8])
	// Map to [1, 255].
	var n uint64
	for _, ch := range hexStr {
		n = n*16 + uint64(hexDigit(ch))
	}
	return fmt.Sprintf("%d", (n%255)+1)
}

func hexDigit(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return int(r - '0')
	case r >= 'a' && r <= 'f':
		return int(r-'a') + 10
	case r >= 'A' && r <= 'F':
		return int(r-'A') + 10
	}
	return 0
}

// --- small helpers (copies from internal/capimanifest, scoped to pivot) ---

func replaceFirstPerLine(s, pattern, repl string) string {
	re := regexp.MustCompile(pattern)
	return re.ReplaceAllString(s, repl)
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

func sliceHasLine(s []string, want string) bool {
	for _, x := range s {
		if strings.TrimSpace(x) == want {
			return true
		}
	}
	return false
}

func indexOfLine(s []string, want string) int {
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

// sortedStrings is unused at call-time but kept as a helper so VerifyParity
// can present stable label/secret ordering when logging. Kept here to be
// shared by pivot.go.
func sortedStrings(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}
