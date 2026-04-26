// Package capimanifest ports the YAML-patch functions that mutate the
// workload CAPI manifest on disk. Every function reads a file,
// transforms the text, writes it back. The source file is always the
// value of cfg.CAPIManifest.
//
// Bash source map (the original bash port):
//   - apply_role_resource_overrides                           ~L4678-4762
//   - patch_capi_manifest_proxmox_csi_topology_labels         ~L4768-4825
//   - patch_capi_manifest_kubeadm_skip_kube_proxy_for_cilium  ~L4832-4895
//   - patch_capi_manifest_proxmox_machine_template_spec_revisions
//                                                             ~L4899-5023
//
// The bash versions use inline Python with `re`. These ports use Go's
// regexp package (RE2) — Go regex doesn't support backreferences or
// lookarounds, so a few substitutions are re-shaped without changing the
// output.
package capimanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/lpasquali/yage/internal/capi/cilium"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
)

// ApplyRoleResourceOverrides ports apply_role_resource_overrides.
// Rewrites disk/sizeGb/numSockets/numCores/memoryMiB for every
// ProxmoxMachineTemplate block whose metadata.name contains "control-plane"
// or "worker", converts a few comma-scalar fields to YAML lists
// (allowedNodes, dnsServers, addresses), and ensures a
// schedulerHints.memoryAdjustment block on ProxmoxCluster.
func ApplyRoleResourceOverrides(cfg *config.Config) error {
	raw, err := os.ReadFile(cfg.CAPIManifest)
	if err != nil {
		return err
	}
	text := string(raw)

	type hw struct {
		disk, size, sockets, cores, mem, template string
	}
	// Per-role template overrides fall back to the catch-all
	// cfg.Providers.Proxmox.TemplateID when empty.
	cpTpl := firstNonEmpty(cfg.WorkloadControlPlaneTemplateID, cfg.Providers.Proxmox.TemplateID)
	wkTpl := firstNonEmpty(cfg.WorkloadWorkerTemplateID, cfg.Providers.Proxmox.TemplateID)
	cp := hw{
		disk: cfg.Providers.Proxmox.ControlPlaneBootVolumeDevice, size: cfg.Providers.Proxmox.ControlPlaneBootVolumeSize,
		sockets: cfg.Providers.Proxmox.ControlPlaneNumSockets, cores: cfg.Providers.Proxmox.ControlPlaneNumCores, mem: cfg.Providers.Proxmox.ControlPlaneMemoryMiB,
		template: cpTpl,
	}
	wk := hw{
		disk: cfg.Providers.Proxmox.WorkerBootVolumeDevice, size: cfg.Providers.Proxmox.WorkerBootVolumeSize,
		sockets: cfg.Providers.Proxmox.WorkerNumSockets, cores: cfg.Providers.Proxmox.WorkerNumCores, mem: cfg.Providers.Proxmox.WorkerMemoryMiB,
		template: wkTpl,
	}
	text = patchPMTBlock(text, "control-plane", cp)
	text = patchPMTBlock(text, "worker", wk)
	text = scalarCSVToYAMLList(text, "allowedNodes")
	text = scalarCSVToYAMLList(text, "dnsServers")
	text = scalarCSVToYAMLList(text, "addresses")
	text = injectMemoryAdjustment(text, cfg.Providers.Proxmox.MemoryAdjustment)

	return os.WriteFile(cfg.CAPIManifest, []byte(text), 0o644)
}

// firstNonEmpty returns the first non-empty string from its arguments.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// patchPMTBlock replaces the five sizing fields (and optionally
// templateID) inside every ProxmoxMachineTemplate block whose
// metadata.name contains `role`. An empty `h.template` leaves the
// templateID untouched (the manifest keeps whatever clusterctl
// substituted from the catch-all PROXMOX_TEMPLATE_ID).
func patchPMTBlock(text, role string, h struct {
	disk, size, sockets, cores, mem, template string
},
) string {
	// Bash uses a single regex with dotall + greedy; Go's RE2 can do that
	// but not with negative lookahead on the doc separator. We split
	// doc-by-doc, apply per-doc, and rejoin.
	parts := strings.Split(text, "\n---\n")
	for i, doc := range parts {
		if !strings.Contains(doc, "kind: ProxmoxMachineTemplate") {
			continue
		}
		// Find metadata.name line; require it to contain `role`.
		nameRE := regexp.MustCompile(`(?m)^  name:\s*(\S+)\s*$`)
		m := nameRE.FindStringSubmatch(doc)
		if m == nil || !strings.Contains(m[1], role) {
			continue
		}
		doc = replaceFirstPerLine(doc, `(disk:\s*)[^\n]+`, "${1}"+h.disk)
		doc = replaceFirstPerLine(doc, `(sizeGb:\s*)[^\n]+`, "${1}"+h.size)
		doc = replaceFirstPerLine(doc, `(numSockets:\s*)[^\n]+`, "${1}"+h.sockets)
		doc = replaceFirstPerLine(doc, `(numCores:\s*)[^\n]+`, "${1}"+h.cores)
		doc = replaceFirstPerLine(doc, `(memoryMiB:\s*)[^\n]+`, "${1}"+h.mem)
		if h.template != "" {
			doc = replaceFirstPerLine(doc, `(templateID:\s*)[^\n]+`, "${1}"+h.template)
		}
		parts[i] = doc
	}
	return strings.Join(parts, "\n---\n")
}

func replaceFirstPerLine(s, pattern, repl string) string {
	re := regexp.MustCompile(pattern)
	return re.ReplaceAllString(s, repl)
}

// scalarCSVToYAMLList converts `<key>: "a,b,c"` (or single value) into a
// YAML block sequence. Matches the bash scalar_to_yaml_list closure.
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

// injectMemoryAdjustment adds schedulerHints.memoryAdjustment under the
// ProxmoxCluster .spec when it is not already present. Identifies the
// correct document by matching kind + apiVersion at line start (not
// strings.Contains, which false-positives on the CAPI Cluster's
// infrastructureRef). Matches bash inject_memory_adjustment.
var reKindProxmoxCluster = regexp.MustCompile(`(?m)^kind:\s*ProxmoxCluster\s*$`)
var reAPIInfraProxmox = regexp.MustCompile(`(?m)^apiVersion:\s*infrastructure\.cluster\.x-k8s\.io/`)
var reSchedulerHints = regexp.MustCompile(`(?m)^  schedulerHints:`)
var reSpecBlock = regexp.MustCompile(`(?m)^(spec:\n(?:  .*\n)+)`)

func injectMemoryAdjustment(text, mem string) string {
	// Doc-by-doc: find the ProxmoxCluster document (by top-level kind +
	// apiVersion at line start) with a spec block that doesn't already
	// contain `^  schedulerHints:`. Append the two lines to the spec.
	// Using strings.Contains would false-positive on the CAPI Cluster
	// document whose infrastructureRef contains "kind: ProxmoxCluster".
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

// PatchProxmoxCSITopologyLabels ports
// patch_capi_manifest_proxmox_csi_topology_labels. Inserts a kubelet
// extraArg `- name: node-labels` right after `- name: provider-id` in
// every KubeadmConfig-style block, with
// topology.kubernetes.io/region=<region>, topology.kubernetes.io/zone=<zone>.
func PatchProxmoxCSITopologyLabels(cfg *config.Config) error {
	if !sysinfo.IsTrue(cfg.Providers.Proxmox.CSITopologyLabels) {
		return nil
	}
	region := cfg.Providers.Proxmox.TopologyRegion
	if region == "" {
		region = cfg.Providers.Proxmox.Region
	}
	zone := cfg.Providers.Proxmox.TopologyZone
	if zone == "" {
		zone = cfg.Providers.Proxmox.Node
	}
	if region == "" || zone == "" {
		logx.Warn("Skipping Proxmox CSI topology node-labels: set PROXMOX_REGION and PROXMOX_NODE (region must match CSI clusters[].region; PROXMOX_TOPOLOGY_REGION / PROXMOX_TOPOLOGY_ZONE override defaults).")
		return nil
	}
	raw, err := os.ReadFile(cfg.CAPIManifest)
	if err != nil {
		return err
	}
	text := string(raw)

	// Remove any stale node-labels line pair with topology.kubernetes.io/*.
	staleRE := regexp.MustCompile(
		`(?m)^[ \t]*- name: node-labels\s*\n[ \t]*value:\s*` +
			`(?:"[^"]*topology\.kubernetes\.io/region=[^"]*,\s*topology\.kubernetes\.io/zone=[^"]*"|` +
			`'[^']*topology\.kubernetes\.io/region=[^']*,\s*topology\.kubernetes\.io/zone=[^']*')\s*\n`,
	)
	text = staleRE.ReplaceAllString(text, "")

	labels := "topology.kubernetes.io/region=" + region +
		",topology.kubernetes.io/zone=" + zone
	yamlVal := `"` + strings.ReplaceAll(strings.ReplaceAll(labels, `\`, `\\`), `"`, `\"`) + `"`

	// Match `- name: provider-id\n      value: ...proxmox://{{...}}...` and
	// append the node-labels pair with matching indent.
	pat := regexp.MustCompile(
		`(?m)^([ \t]*)- name: provider-id\s*\n` +
			`([ \t]*)value:\s*` +
			`(?:"proxmox://'\{\{ ds\.meta_data\.instance_id \}\}'"|proxmox://'\{\{ ds\.meta_data\.instance_id \}\}')\s*\n`,
	)
	newText := pat.ReplaceAllStringFunc(text, func(m string) string {
		submatch := pat.FindStringSubmatch(m)
		if submatch == nil {
			return m
		}
		i1, i2 := submatch[1], submatch[2]
		return m + i1 + "- name: node-labels\n" + i2 + "value: " + yamlVal + "\n"
	})
	if newText == text {
		return nil
	}
	return os.WriteFile(cfg.CAPIManifest, []byte(newText), 0o644)
}

// PatchKubeadmSkipKubeProxyForCilium ports
// patch_capi_manifest_kubeadm_skip_kube_proxy_for_cilium. When Cilium
// takes over kube-proxy, inject `skipPhases: - addon/kube-proxy` into
// KubeadmControlPlane initConfiguration; otherwise remove any such
// block when Cilium is configured off.
func PatchKubeadmSkipKubeProxyForCilium(cfg *config.Config) error {
	raw, err := os.ReadFile(cfg.CAPIManifest)
	if err != nil {
		return err
	}
	text := string(raw)
	docs := strings.Split(text, "\n---\n")

	kcpRE := regexp.MustCompile(`(?m)^kind:\s*KubeadmControlPlane\s*$`)
	alreadyAddedRE := regexp.MustCompile(`(?m)^\s+-\s+addon/kube-proxy\s*$`)
	initRE := regexp.MustCompile(`(?m)(^    initConfiguration:\n)`)
	removeRE := regexp.MustCompile(
		`(?m)(^    initConfiguration:\n)\s+skipPhases:\n\s+-\s+addon/kube-proxy\n`,
	)

	add := cilium.NeedsKubeProxyReplacement(cfg)
	for i, doc := range docs {
		if !kcpRE.MatchString(doc) {
			continue
		}
		if add {
			if alreadyAddedRE.MatchString(doc) {
				continue
			}
			docs[i] = initRE.ReplaceAllString(doc,
				"$1      skipPhases:\n        - addon/kube-proxy\n")
		} else {
			docs[i] = removeRE.ReplaceAllString(doc, "$1")
		}
	}
	newText := strings.Join(docs, "\n---\n")
	if newText == text {
		return nil
	}
	return os.WriteFile(cfg.CAPIManifest, []byte(newText), 0o644)
}

// PatchProxmoxMachineTemplateSpecRevisions ports
// patch_capi_manifest_proxmox_machine_template_spec_revisions. Renames
// each ProxmoxMachineTemplate to <stem>-t<sha256(spec)[:8]> and updates
// every name:-<stem> reference in the manifest so KubeadmControlPlane /
// MachineDeployment infrastructureRef points at the renamed template.
//
// Returns the list of new template names (comma-joined, for logging).
func PatchProxmoxMachineTemplateSpecRevisions(cfg *config.Config) (string, error) {
	if !cfg.Providers.Proxmox.CAPIMachineTemplateSpecRev {
		return "", nil
	}
	raw, err := os.ReadFile(cfg.CAPIManifest)
	if err != nil {
		return "", err
	}
	text := string(raw)

	pmtKind := regexp.MustCompile(`(?m)^kind:\s*ProxmoxMachineTemplate\s*$`)
	infraAPI := regexp.MustCompile(`(?m)^apiVersion:\s*infrastructure\.cluster\.x-k8s\.io/`)
	// metadata block up to `spec:` (indent 0) — bash uses non-greedy .*?
	// with DOTALL; Go's RE2 needs us to find the boundary ourselves.
	nameRE := regexp.MustCompile(`(?m)^  name:\s*(\S+)\s*$`)
	// Trailing -t<8hex> strip.
	stemRE := regexp.MustCompile(`^(.*)-t[0-9a-f]{8}$`)

	parts := []string{}
	for _, p := range strings.Split(text, "\n---\n") {
		s := strings.TrimSpace(p)
		if s != "" {
			parts = append(parts, s+"\n")
		}
	}
	refMap := map[string]string{}
	newParts := make([]string, 0, len(parts))

	for _, doc := range parts {
		if !pmtKind.MatchString(doc) || !infraAPI.MatchString(doc) {
			newParts = append(newParts, doc)
			continue
		}
		// metadata section ends at the first top-level `spec:` line.
		specIdx := regexp.MustCompile(`(?m)^spec:\n`).FindStringIndex(doc)
		if specIdx == nil {
			newParts = append(newParts, doc)
			continue
		}
		meta := doc[:specIdx[0]]
		specBody := doc[specIdx[0]:]
		m := nameRE.FindStringSubmatch(meta)
		if m == nil {
			newParts = append(newParts, doc)
			continue
		}
		readName := m[1]
		sum := sha256.Sum256([]byte(specBody))
		h := hex.EncodeToString(sum[:])[:8]
		stem := readName
		if sm := stemRE.FindStringSubmatch(readName); sm != nil {
			stem = sm[1]
		}
		newName := stem + "-t" + h
		refMap[stem] = newName
		if readName == newName {
			newParts = append(newParts, doc)
		} else {
			newMeta := nameRE.ReplaceAllString(meta, "  name: "+newName)
			newParts = append(newParts, newMeta+specBody)
		}
	}
	out := strings.Join(newParts, "\n---\n")

	// Longest-stem-first name: ref substitutions (so e.g. `<name>-worker`
	// doesn't also match `<name>`).
	stems := make([]string, 0, len(refMap))
	for k := range refMap {
		stems = append(stems, k)
	}
	sort.Slice(stems, func(i, j int) bool { return len(stems[i]) > len(stems[j]) })
	for _, stem := range stems {
		newn := refMap[stem]
		if stem == newn {
			continue
		}
		re := regexp.MustCompile(`(?m)^([ \t]*name:[ \t]*)` + regexp.QuoteMeta(stem) + `([ \t]*)$`)
		out = re.ReplaceAllString(out, "${1}"+newn+"${2}")
	}

	if out == text {
		return "", nil
	}
	if err := os.WriteFile(cfg.CAPIManifest, []byte(out), 0o644); err != nil {
		return "", err
	}

	// Collect renamed PMT names for logging.
	var names []string
	for _, d := range strings.Split(out, "\n---\n") {
		if !pmtKind.MatchString(d) || !infraAPI.MatchString(d) {
			continue
		}
		specIdx := regexp.MustCompile(`(?m)^spec:\n`).FindStringIndex(d)
		if specIdx == nil {
			continue
		}
		m := nameRE.FindStringSubmatch(d[:specIdx[0]])
		if m != nil {
			names = append(names, m[1])
		}
	}
	if len(names) == 0 {
		return "0", nil
	}
	return strings.Join(names, ","), nil
}
