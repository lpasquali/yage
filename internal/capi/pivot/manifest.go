// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pivot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/airgap"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
	"github.com/lpasquali/yage/internal/provider"
)

// renderManagementManifest dispatches to the active provider's
// RenderMgmtManifest implementation. All provider-specific logic
// (clusterctl env vars, manifest patches, template IDs) lives in the
// provider package — this wrapper keeps the pivot package provider-agnostic.
func renderManagementManifest(cfg *config.Config, clusterctlCfgPath string) (string, error) {
	p, err := provider.For(cfg)
	if err != nil {
		return "", err
	}
	return p.RenderMgmtManifest(cfg, clusterctlCfgPath)
}

// renderMgmtCiliumHelmChartProxy renders a HelmChartProxy YAML doc that
// targets the management cluster (not the workload). It uses the
// existing CAAPH selector convention: select Clusters labelled
// `caaph: enabled` and `caaph.cilium.cluster-id: <Mgmt.CiliumClusterID>`.
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
	ns := cfg.Mgmt.ClusterNamespace
	if ns == "" {
		ns = "default"
	}
	name := cfg.Mgmt.ClusterName
	if name == "" {
		name = "capi-management"
	}
	clusterID := cfg.Mgmt.CiliumClusterID
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
	fmt.Fprintf(&vt, "k8sServiceHost: %s\n", cfg.Mgmt.ControlPlaneEndpointIP)
	port := cfg.Mgmt.ControlPlaneEndpointPort
	if port == "" {
		port = "6443"
	}
	fmt.Fprintf(&vt, "k8sServicePort: %s\n", port)
	// Hubble: on by default for the management cluster (cheap on a
	// single-node cluster, valuable for observability).
	if sysinfo.IsTrue(cfg.Mgmt.CiliumHubble) {
		vt.WriteString("hubble:\n  enabled: true\n  relay:\n    enabled: true\n")
	}
	// LB-IPAM: off by default for management (no Service type=LoadBalancer
	// targets on a stateless mgmt cluster). Setting `loadBalancer.l2.enabled`
	// to false here is informational; the operator default is already off,
	// but we render the key explicitly so an inspector can confirm.
	if !sysinfo.IsTrue(cfg.Mgmt.CiliumLBIPAM) {
		vt.WriteString("loadBalancer:\n  l2:\n    enabled: false\n")
	}
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
	fmt.Fprintf(&sb, "  repoURL: %s\n", airgap.RewriteHelmRepo("https://helm.cilium.io/"))
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
	if cfg.Mgmt.CiliumClusterID == "" {
		cfg.Mgmt.CiliumClusterID = deriveMgmtClusterID(cfg)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	text := string(raw)
	docs := strings.Split(text, "\n---\n")
	port := cfg.Mgmt.ControlPlaneEndpointPort
	if port == "" {
		port = "6443"
	}
	labels := []struct{ k, v string }{
		{"caaph", "enabled"},
		{"caaph.cilium.cluster-id", cfg.Mgmt.CiliumClusterID},
		{"caaph.cilium.k8s-service-host", cfg.Mgmt.ControlPlaneEndpointIP},
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
	if cfg.Mgmt.CiliumClusterID != "" {
		return cfg.Mgmt.CiliumClusterID
	}
	src := cfg.Mgmt.ClusterName
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

// sortedStrings is unused at call-time but kept as a helper so VerifyParity
// can present stable label/secret ordering when logging. Kept here to be
// shared by pivot.go.
func sortedStrings(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}