// Package caaph ports the Cluster API Add-on Provider Helm workflow —
// HelmChartProxy rendering + apply for Cilium, plus the Argo CD Operator
// + ArgoCD CR installation on the workload cluster.
//
// Bash source map (bootstrap-capi.sh):
//   - patch_capi_cluster_caaph_helm_labels                     ~L5362-5429
//   - caaph_print_helmchartproxy_cilium_yaml                   ~L5433-5513
//   - apply_workload_cilium_helmchartproxy                     ~L5515-5533
//   - apply_workload_cilium_lbb_to_workload_if_enabled         ~L5536-5558
//   - apply_workload_argocd_operator_and_argocd_cr             ~L5562-5695
package caaph

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/ciliumx"
	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/proxmox"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
	"github.com/lpasquali/bootstrap-capi/internal/sysinfo"
)

// PatchClusterCAAPHHelmLabels ports patch_capi_cluster_caaph_helm_labels.
// Adds four labels to the Cluster doc in the workload manifest:
//
//	caaph: enabled
//	caaph.cilium.cluster-id: "<WORKLOAD_CILIUM_CLUSTER_ID>"
//	caaph.cilium.k8s-service-host: "<CONTROL_PLANE_ENDPOINT_IP>"
//	caaph.cilium.k8s-service-port: "<CONTROL_PLANE_ENDPOINT_PORT>"
//
// Empty values are skipped (except the "caaph" key which is always added
// with value "enabled").
func PatchClusterCAAPHHelmLabels(cfg *config.Config, manifestPath string) error {
	if manifestPath == "" {
		manifestPath = cfg.CAPIManifest
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil || len(raw) == 0 {
		return nil
	}
	proxmox.RefreshDerivedCiliumClusterID(cfg)
	text := string(raw)
	docs := strings.Split(text, "\n---\n")
	port := cfg.ControlPlaneEndpointPort
	if port == "" {
		port = "6443"
	}
	labels := []struct {
		k, v string
	}{
		{"caaph", "enabled"},
		{"caaph.cilium.cluster-id", cfg.WorkloadCiliumClusterID},
		{"caaph.cilium.k8s-service-host", cfg.ControlPlaneEndpointIP},
		{"caaph.cilium.k8s-service-port", port},
	}

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

// CiliumHelmChartProxyYAML ports caaph_print_helmchartproxy_cilium_yaml.
// Returns the full HelmChartProxy YAML document the caller will
// `kubectl apply -f -` against the management cluster.
func CiliumHelmChartProxyYAML(cfg *config.Config, kprOn bool) string {
	ver := strings.TrimPrefix(cfg.CiliumVersion, "v")
	if ver == "" {
		ver = "1.19.3"
	}
	ns := cfg.WorkloadClusterNamespace
	if ns == "" {
		ns = "default"
	}
	name := cfg.WorkloadClusterName
	if name == "" {
		name = "cluster"
	}
	var vt strings.Builder
	g := func(s string) string { return "{{ " + s + " }}" }
	vt.WriteString("cluster:\n")
	fmt.Fprintf(&vt, "  name: %s\n", g(".Cluster.metadata.name"))
	fmt.Fprintf(&vt, "  id: %s\n", g(`index .Cluster.metadata.labels "caaph.cilium.cluster-id"`))
	if kprOn {
		vt.WriteString("kubeProxyReplacement: true\n")
		fmt.Fprintf(&vt, "k8sServiceHost: %s\n", g(`index .Cluster.metadata.labels "caaph.cilium.k8s-service-host"`))
		fmt.Fprintf(&vt, "k8sServicePort: %s\n", g(`index .Cluster.metadata.labels "caaph.cilium.k8s-service-port"`))
	} else {
		vt.WriteString("kubeProxyReplacement: false\n")
	}
	if sysinfo.IsTrue(cfg.CiliumIngress) {
		vt.WriteString("ingressController:\n  enabled: true\n  default: true\n")
	}
	if sysinfo.IsTrue(cfg.CiliumHubble) {
		vt.WriteString("hubble:\n  enabled: true\n  relay:\n    enabled: true\n")
		if sysinfo.IsTrue(cfg.CiliumHubbleUI) {
			vt.WriteString("  ui:\n    enabled: true\n")
		}
	}
	pool := strings.ReplaceAll(strings.TrimSpace(cfg.CiliumIPAMClusterPoolIPv4), `"`, "")
	if pool == "" {
		pool = "10.244.0.0/16"
	}
	mask := strings.TrimSpace(cfg.CiliumIPAMClusterPoolIPv4MaskSize)
	if mask == "" || !regexp.MustCompile(`^[0-9]+$`).MatchString(mask) {
		mask = "24"
	}
	fmt.Fprintf(&vt, "ipam:\n  operator:\n    clusterPoolIPv4PodCIDRList: [%q]\n    clusterPoolIPv4MaskSize: %s\n", pool, mask)
	if sysinfo.IsTrue(cfg.CiliumGatewayAPIEnabled) {
		vt.WriteString("gatewayAPI:\n  enabled: true\n")
	}

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

// ApplyWorkloadCiliumHelmChartProxy ports
// apply_workload_cilium_helmchartproxy. Validates the kube-proxy /
// Hubble config, renders the HCP, and kubectl applies it against the
// kind management cluster.
func ApplyWorkloadCiliumHelmChartProxy(cfg *config.Config) {
	mctx := "kind-" + cfg.KindClusterName
	logx.Log("Cilium: installing via Cluster API add-on provider Helm (HelmChartProxy → workload cluster) per https://cluster-api.sigs.k8s.io/tasks/workload-bootstrap-gitops …")
	kpr := ciliumx.NeedsKubeProxyReplacement(cfg)
	if kpr {
		logx.Log("Cilium: kube-proxy replacement (k8sServiceHost/Port from Cluster labels: caaph.cilium.k8s-service-*; kubeadm skips addon/kube-proxy when patched).")
	} else {
		logx.Log("Cilium: kube-proxy replacement off — node kube-proxy in use.")
	}
	if sysinfo.IsTrue(cfg.CiliumHubbleUI) && !sysinfo.IsTrue(cfg.CiliumHubble) {
		logx.Die("CILIUM_HUBBLE_UI requires CILIUM_HUBBLE=true")
	}
	doc := CiliumHelmChartProxyYAML(cfg, kpr)
	if err := shell.Pipe(doc, "kubectl", "--context", mctx, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to apply HelmChartProxy (Cilium) on the management cluster.")
	}
	gwLog := ""
	if sysinfo.IsTrue(cfg.CiliumGatewayAPIEnabled) {
		gwLog = fmt.Sprintf("; Gateway API enabled in Cilium helm values (ensure gateway.networking.k8s.io CRDs per Cilium %s docs)",
			strings.TrimPrefix(cfg.CiliumVersion, "v"))
	}
	logx.Log("Applied HelmChartProxy %s-caaph-cilium (Cilium v%s; IPAM pool IPv4 %s /%s per node; changing CIDR usually requires a new Cluster)%s.",
		cfg.WorkloadClusterName,
		strings.TrimPrefix(cfg.CiliumVersion, "v"),
		cfg.CiliumIPAMClusterPoolIPv4,
		cfg.CiliumIPAMClusterPoolIPv4MaskSize,
		gwLog,
	)
}

// ApplyWorkloadCiliumLBBToWorkload ports
// apply_workload_cilium_lbb_to_workload_if_enabled. When CILIUM_LB_IPAM
// is true, renders a CiliumLoadBalancerIPPool (via ciliumx) and applies
// it through the workload kubeconfig.
func ApplyWorkloadCiliumLBBToWorkload(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	if !sysinfo.IsTrue(cfg.CiliumLBIPAM) {
		return
	}
	f, err := os.CreateTemp("", "cilium-lbb-*.yaml")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	f.Close()
	// Start with empty file; AppendLBIPAMPoolManifest only appends to
	// existing files, so touch it first.
	if err := os.WriteFile(f.Name(), []byte{}, 0o600); err != nil {
		return
	}
	ciliumx.AppendLBIPAMPoolManifest(cfg, f.Name())
	st, err := os.Stat(f.Name())
	if err != nil || st.Size() == 0 {
		return
	}
	kcfg, err := writeWorkloadKubeconfig()
	if err != nil || kcfg == "" {
		return
	}
	defer os.Remove(kcfg)

	logx.Log("Cilium LB-IPAM: applying CiliumLoadBalancerIPPool to workload (%s / %s).",
		fallbackStr(cfg.CiliumLBIPAMPoolName, cfg.WorkloadClusterName+"-lb-pool"),
		fallbackStr(cfg.CiliumLBIPAMPoolCIDR, "derived"))
	_ = shell.Run("kubectl", "--kubeconfig", kcfg, "apply", "-f", f.Name())
}

// ApplyWorkloadArgoCDOperatorAndCR ports
// apply_workload_argocd_operator_and_argocd_cr (L5562-L5695).
// writeWorkloadKubeconfig returns the path to a tmp kubeconfig that
// targets the workload cluster (managed by the caller so the cleanup is
// predictable).
func ApplyWorkloadArgoCDOperatorAndCR(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	wk, err := writeWorkloadKubeconfig()
	if err != nil || wk == "" {
		logx.Die("Cannot read workload kubeconfig (%s-kubeconfig) — install the Argo CD Operator only after the workload API is ready.",
			cfg.WorkloadClusterName)
	}
	defer os.Remove(wk)

	opURL := fmt.Sprintf("https://github.com/argoproj-labs/argocd-operator/config/default?ref=%s", cfg.ArgoCDOperatorVersion)
	logx.Log("Installing Argo CD Operator on the workload cluster (ref %s; kubectl apply -k --server-side %s)…",
		cfg.ArgoCDOperatorVersion, opURL)
	if err := shell.Run("kubectl", "--kubeconfig", wk, "apply",
		"--server-side", "--force-conflicts",
		"--field-manager=bootstrap-capi-argocd-operator",
		"-k", opURL); err != nil {
		logx.Die("Failed to apply Argo CD Operator (network, ref %s, or kubectl that supports --server-side; need >= 1.18).",
			cfg.ArgoCDOperatorVersion)
	}

	logx.Log("Waiting for Argo CD Operator controller (initial start)…")
	if err := shell.Run("kubectl", "--kubeconfig", wk, "wait",
		"-n", "argocd-operator-system",
		"deploy/argocd-operator-controller-manager",
		"--for=condition=Available", "--timeout=300s"); err != nil {
		logx.Die("Argo CD Operator controller is not Available in argocd-operator-system (see pods in that namespace).")
	}

	ns := cfg.WorkloadArgoCDNamespace
	if ns == "" {
		ns = "argocd"
	}
	logx.Log("Allowing cluster-scoped sync from Argo in %s (ARGOCD_CLUSTER_CONFIG_NAMESPACES on the operator)…", ns)
	if err := shell.Run("kubectl", "--kubeconfig", wk,
		"-n", "argocd-operator-system",
		"set", "env", "deploy/argocd-operator-controller-manager",
		"ARGOCD_CLUSTER_CONFIG_NAMESPACES="+ns, "--overwrite"); err != nil {
		logx.Die("Failed to patch argocd-operator-controller-manager with ARGOCD_CLUSTER_CONFIG_NAMESPACES.")
	}
	if err := shell.Run("kubectl", "--kubeconfig", wk,
		"-n", "argocd-operator-system",
		"rollout", "status", "deploy/argocd-operator-controller-manager",
		"--timeout=300s"); err != nil {
		logx.Warn("Argo CD Operator rollout after env patch not reported ready in 300s — continuing.")
	}
	if err := shell.Run("kubectl", "--kubeconfig", wk, "wait",
		"-n", "argocd-operator-system",
		"deploy/argocd-operator-controller-manager",
		"--for=condition=Available", "--timeout=300s"); err != nil {
		logx.Die("Argo CD Operator controller is not Available after config patch.")
	}

	nsDoc := fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q}}`, ns)
	if err := shell.Pipe(nsDoc, "kubectl", "--kubeconfig", wk, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to ensure namespace %s on the workload cluster.", ns)
	}

	promEnabled := sysinfo.IsTrue(cfg.ArgoCDOperatorArgoCDPrometheusEnabled)
	monEnabled := sysinfo.IsTrue(cfg.ArgoCDOperatorArgoCDMonitoringEnabled)
	disableIngress := sysinfo.IsTrue(cfg.ArgoCDDisableOperatorManagedIngress)
	serverInsecure := sysinfo.IsTrue(cfg.ArgoCDServerInsecure)

	cr := buildArgoCDCR(cfg.ArgoCDVersion, ns, promEnabled, monEnabled, disableIngress, serverInsecure)
	logx.Log("Creating ArgoCD custom resource (argocd/%s)…", ns)
	if err := shell.Pipe(cr, "kubectl", "--kubeconfig", wk, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to apply ArgoCD custom resource on the workload cluster.")
	}
	if disableIngress {
		logx.Log("ArgoCD CR: operator-managed server/gRPC Ingress disabled — expose Argo with Gateway API (e.g. workload-app-of-apps examples/gateway-api) or port-forward.")
	}
	if err := shell.Run("kubectl", "--kubeconfig", wk,
		"-n", ns, "get", "deploy", "argocd-server"); err == nil {
		_ = shell.Run("kubectl", "--kubeconfig", wk,
			"-n", ns, "rollout", "restart", "deploy/argocd-server")
		logx.Log("Restarted argocd-server after ArgoCD CR (argocd-redis pre-provisioned from bootstrap).")
	}
	logx.Log("Argo CD Operator will reconcile Argo CD in %s (admin password: secret argocd-cluster, key admin.password, when ready).", ns)
}

// buildArgoCDCR emits the ArgoCD CR YAML. Flags match the bash
// heredoc conditionals (L5593-L5681).
func buildArgoCDCR(version, ns string, prom, mon, disableIngress, serverInsecure bool) string {
	if version == "" {
		version = "v3.3.8"
	}
	var sb strings.Builder
	fmt.Fprintln(&sb, "apiVersion: argoproj.io/v1beta1")
	fmt.Fprintln(&sb, "kind: ArgoCD")
	fmt.Fprintln(&sb, "metadata:")
	fmt.Fprintln(&sb, "  name: argocd")
	fmt.Fprintf(&sb, "  namespace: %s\n", ns)
	fmt.Fprintln(&sb, "spec:")
	fmt.Fprintf(&sb, "  version: %s\n", version)
	fmt.Fprintln(&sb, "  prometheus:")
	fmt.Fprintf(&sb, "    enabled: %t\n", prom)
	fmt.Fprintln(&sb, "  monitoring:")
	fmt.Fprintf(&sb, "    enabled: %t\n", mon)
	fmt.Fprintln(&sb, "  notifications:")
	fmt.Fprintln(&sb, "    enabled: true")
	fmt.Fprintln(&sb, "  server:")
	switch {
	case disableIngress && serverInsecure:
		fmt.Fprintln(&sb, "    insecure: true")
		fmt.Fprintln(&sb, "    ingress:")
		fmt.Fprintln(&sb, "      enabled: false")
		fmt.Fprintln(&sb, "    grpc:")
		fmt.Fprintln(&sb, "      ingress:")
		fmt.Fprintln(&sb, "        enabled: false")
	case disableIngress:
		fmt.Fprintln(&sb, "    ingress:")
		fmt.Fprintln(&sb, "      enabled: false")
		fmt.Fprintln(&sb, "    grpc:")
		fmt.Fprintln(&sb, "      ingress:")
		fmt.Fprintln(&sb, "        enabled: false")
	case serverInsecure:
		fmt.Fprintln(&sb, "    insecure: true")
		fmt.Fprintln(&sb, "    grpc:")
		fmt.Fprintln(&sb, "      ingress:")
		fmt.Fprintln(&sb, "        enabled: true")
	default:
		fmt.Fprintln(&sb, "    grpc:")
		fmt.Fprintln(&sb, "      ingress:")
		fmt.Fprintln(&sb, "        enabled: true")
	}
	return sb.String()
}

// --- helpers ---

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

func fallbackStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
