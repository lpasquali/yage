// Package caaph ports the Cluster API Add-on Provider Helm workflow —
// HelmChartProxy rendering + apply for Cilium, plus the Argo CD Operator
// + ArgoCD CR installation on the workload cluster.
//
// Bash source map (the original bash port):
//   - patch_capi_cluster_caaph_helm_labels                     ~L5362-5429
//   - caaph_print_helmchartproxy_cilium_yaml                   ~L5433-5513
//   - apply_workload_cilium_helmchartproxy                     ~L5515-5533
//   - apply_workload_cilium_lbb_to_workload_if_enabled         ~L5536-5558
//   - apply_workload_argocd_operator_and_argocd_cr             ~L5562-5695
//
// All kubectl shell-outs in this package have been migrated to the
// in-process k8sclient (client-go) layer except `kubectl apply -k <git-url>`
// for the Argo CD Operator install — replicating kustomize-from-Git in Go
// would require pulling sigs.k8s.io/kustomize/api (~10MB of deps) for a
// single call site, so that one shell-out is intentionally retained.
package caaph

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/yage/internal/capi/cilium"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/provider/proxmox/pveapi"
	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
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
// with value "enabled"). The manifest file on disk is updated, and (when
// the management cluster context exists) the same labels are patched onto
// the live Cluster object via a JSON merge patch through the dynamic
// client.
func PatchClusterCAAPHHelmLabels(cfg *config.Config, manifestPath string) error {
	if manifestPath == "" {
		manifestPath = cfg.CAPIManifest
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil || len(raw) == 0 {
		return nil
	}
	pveapi.RefreshDerivedCiliumClusterID(cfg)
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
	if err := os.WriteFile(manifestPath, []byte(strings.Join(docs, "\n---\n")), 0o644); err != nil {
		return err
	}

	// Also patch the live Cluster object's labels (if the management cluster
	// is reachable). The workload manifest update above only matters when
	// the manifest is re-applied; patching the live object ensures CAAPH
	// notices the labels immediately.
	mctx := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(mctx) {
		return nil
	}
	if cfg.WorkloadClusterName == "" || cfg.WorkloadClusterNamespace == "" {
		return nil
	}
	cli, err := k8sclient.ForContext(mctx)
	if err != nil {
		return nil
	}
	patchLabels := map[string]string{}
	for _, lbl := range labels {
		if lbl.v == "" && lbl.k != "caaph" {
			continue
		}
		patchLabels[lbl.k] = lbl.v
	}
	if len(patchLabels) == 0 {
		return nil
	}
	body, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"labels": patchLabels,
		},
	})
	gvk := schema.GroupVersionKind{
		Group:   "cluster.x-k8s.io",
		Version: "v1beta2",
		Kind:    "Cluster",
	}
	mapping, mErr := cli.Mapper.RESTMapping(gvk.GroupKind())
	if mErr != nil {
		return nil
	}
	_, _ = cli.Dynamic.Resource(mapping.Resource).
		Namespace(cfg.WorkloadClusterNamespace).
		Patch(context.Background(), cfg.WorkloadClusterName,
			types.MergePatchType, body, metav1.PatchOptions{
				FieldManager: k8sclient.FieldManager,
			})
	return nil
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
// Hubble config, renders the HCP, and SSAs it through the in-process
// dynamic client against the kind management cluster.
func ApplyWorkloadCiliumHelmChartProxy(cfg *config.Config) {
	mctx := "kind-" + cfg.KindClusterName
	logx.Log("Cilium: installing via Cluster API add-on provider Helm (HelmChartProxy → workload cluster) per https://cluster-api.sigs.k8s.io/tasks/workload-bootstrap-gitops …")
	kpr := cilium.NeedsKubeProxyReplacement(cfg)
	if kpr {
		logx.Log("Cilium: kube-proxy replacement (k8sServiceHost/Port from Cluster labels: caaph.cilium.k8s-service-*; kubeadm skips addon/kube-proxy when patched).")
	} else {
		logx.Log("Cilium: kube-proxy replacement off — node kube-proxy in use.")
	}
	if sysinfo.IsTrue(cfg.CiliumHubbleUI) && !sysinfo.IsTrue(cfg.CiliumHubble) {
		logx.Die("CILIUM_HUBBLE_UI requires CILIUM_HUBBLE=true")
	}
	doc := CiliumHelmChartProxyYAML(cfg, kpr)
	cli, err := k8sclient.ForContext(mctx)
	if err != nil {
		logx.Die("Failed to load management context %s: %v", mctx, err)
	}
	if err := cli.ApplyYAML(context.Background(), []byte(doc)); err != nil {
		logx.Die("Failed to apply HelmChartProxy (Cilium) on the management cluster: %v", err)
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
	cilium.AppendLBIPAMPoolManifest(cfg, f.Name())
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
	manifest, err := os.ReadFile(f.Name())
	if err != nil {
		return
	}
	cli, err := k8sclient.ForKubeconfigFile(kcfg)
	if err != nil {
		return
	}
	_ = cli.ApplyMultiDocYAML(context.Background(), manifest)
}

// ApplyWorkloadArgoCDOperatorAndCR ports
// apply_workload_argocd_operator_and_argocd_cr (L5562-L5695).
// writeWorkloadKubeconfig returns the path to a tmp kubeconfig that
// targets the workload cluster (managed by the caller so the cleanup is
// predictable).
//
// The Argo CD Operator install itself is `kubectl apply -k <git-url>` —
// kustomize-from-Git is intentionally retained as a shell-out (see
// package doc). Everything else (waits, env-patch, ArgoCD CR apply) goes
// through the in-process k8sclient.
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
	// TODO: kustomize-from-Git is hard to replicate without
	// sigs.k8s.io/kustomize/api/krusty (~10MB more deps). Keep this one
	// kubectl shell-out; everything else in this function uses k8sclient.
	if err := shell.Run("kubectl", "--kubeconfig", wk, "apply",
		"--server-side", "--force-conflicts",
		"--field-manager=yage-argocd-operator",
		"-k", opURL); err != nil {
		logx.Die("Failed to apply Argo CD Operator (network, ref %s, or kubectl that supports --server-side; need >= 1.18).",
			cfg.ArgoCDOperatorVersion)
	}

	cli, err := k8sclient.ForKubeconfigFile(wk)
	if err != nil {
		logx.Die("Failed to load workload kubeconfig: %v", err)
	}
	ctx := context.Background()

	logx.Log("Waiting for Argo CD Operator controller (initial start)…")
	if err := waitDeploymentAvailable(ctx, cli, "argocd-operator-system",
		"argocd-operator-controller-manager", 5*time.Minute); err != nil {
		logx.Die("Argo CD Operator controller is not Available in argocd-operator-system (see pods in that namespace).")
	}

	ns := cfg.WorkloadArgoCDNamespace
	if ns == "" {
		ns = "argocd"
	}
	logx.Log("Allowing cluster-scoped sync from Argo in %s (ARGOCD_CLUSTER_CONFIG_NAMESPACES on the operator)…", ns)
	if err := patchOperatorEnv(ctx, cli, "argocd-operator-system",
		"argocd-operator-controller-manager", "ARGOCD_CLUSTER_CONFIG_NAMESPACES", ns); err != nil {
		logx.Die("Failed to patch argocd-operator-controller-manager with ARGOCD_CLUSTER_CONFIG_NAMESPACES: %v", err)
	}
	if err := waitDeploymentAvailable(ctx, cli, "argocd-operator-system",
		"argocd-operator-controller-manager", 5*time.Minute); err != nil {
		logx.Die("Argo CD Operator controller is not Available after config patch.")
	}

	if err := cli.EnsureNamespace(ctx, ns); err != nil {
		logx.Die("Failed to ensure namespace %s on the workload cluster: %v", ns, err)
	}

	promEnabled := sysinfo.IsTrue(cfg.ArgoCDOperatorArgoCDPrometheusEnabled)
	monEnabled := sysinfo.IsTrue(cfg.ArgoCDOperatorArgoCDMonitoringEnabled)
	disableIngress := sysinfo.IsTrue(cfg.ArgoCDDisableOperatorManagedIngress)
	serverInsecure := sysinfo.IsTrue(cfg.ArgoCDServerInsecure)

	cr := buildArgoCDCR(cfg.ArgoCDVersion, ns, promEnabled, monEnabled, disableIngress, serverInsecure)
	logx.Log("Creating ArgoCD custom resource (argocd/%s)…", ns)
	if err := cli.ApplyYAML(ctx, []byte(cr)); err != nil {
		logx.Die("Failed to apply ArgoCD custom resource on the workload cluster: %v", err)
	}
	if disableIngress {
		logx.Log("ArgoCD CR: operator-managed server/gRPC Ingress disabled — expose Argo with Gateway API (e.g. workload-app-of-apps examples/gateway-api) or port-forward.")
	}
	// Restart argocd-server if it already exists (so the new CR settings
	// take effect immediately and the pre-provisioned argocd-redis Secret
	// is picked up).
	if _, err := cli.Typed.AppsV1().Deployments(ns).Get(ctx, "argocd-server", metav1.GetOptions{}); err == nil {
		// "kubectl rollout restart" is a strategic-merge patch that bumps
		// spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt].
		ts := time.Now().UTC().Format(time.RFC3339)
		body := fmt.Sprintf(
			`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
			ts,
		)
		if _, err := cli.Typed.AppsV1().Deployments(ns).Patch(ctx, "argocd-server",
			types.StrategicMergePatchType, []byte(body), metav1.PatchOptions{
				FieldManager: k8sclient.FieldManager,
			}); err == nil {
			logx.Log("Restarted argocd-server after ArgoCD CR (argocd-redis pre-provisioned from bootstrap).")
		}
	}
	logx.Log("Argo CD Operator will reconcile Argo CD in %s (admin password: secret argocd-cluster, key admin.password, when ready).", ns)
}

// waitDeploymentAvailable polls until the named Deployment has at least
// one available replica AND its Available=True condition is set.
func waitDeploymentAvailable(ctx context.Context, cli *k8sclient.Client, ns, name string, timeout time.Duration) error {
	return k8sclient.PollUntil(ctx, 5*time.Second, timeout, func(c context.Context) (bool, error) {
		dep, err := cli.Typed.AppsV1().Deployments(ns).Get(c, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			// Transient API errors during operator startup — keep polling.
			return false, nil
		}
		if dep.Status.AvailableReplicas <= 0 {
			return false, nil
		}
		for _, cond := range dep.Status.Conditions {
			// The corev1 import ensures we link the right symbol; the
			// condition type strings come from appsv1 but match well-known
			// values.
			if string(cond.Type) == "Available" && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

// patchOperatorEnv applies a strategic-merge patch that sets one env var
// on the first container of the named Deployment.
func patchOperatorEnv(ctx context.Context, cli *k8sclient.Client, ns, name, envKey, envVal string) error {
	// Read the current deployment so we know the first container's name —
	// strategic-merge patches that target list-elements need the merge key
	// (`name`) populated.
	dep, err := cli.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if len(dep.Spec.Template.Spec.Containers) == 0 {
		return fmt.Errorf("deployment %s/%s has no containers", ns, name)
	}
	cname := dep.Spec.Template.Spec.Containers[0].Name
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{
						{
							"name": cname,
							"env": []map[string]any{
								{"name": envKey, "value": envVal},
							},
						},
					},
				},
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = cli.Typed.AppsV1().Deployments(ns).Patch(ctx, name,
		types.StrategicMergePatchType, body, metav1.PatchOptions{
			FieldManager: k8sclient.FieldManager,
		})
	return err
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

