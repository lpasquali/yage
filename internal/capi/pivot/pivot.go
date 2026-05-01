// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pivot

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/kind/pkg/cluster"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/platform/kubectl"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// yageSystemNamespace / yageReposPVCName / yageManagedBySelector mirror
// the constants used by internal/cluster/kindsync. Inlined (rather than
// importing kindsync for three string constants) to keep this package's
// dep graph minimal.
const (
	yageSystemNamespace   = "yage-system"
	yageReposPVCName      = "yage-repos"
	yageManagedBySelector = "app.kubernetes.io/managed-by=yage"
)

// EnsureManagementCluster provisions the management cluster on Proxmox
// via kind+CAPI: renders the management CAPI manifest, applies it on the
// kind context (with up to 3 retries to ride out webhook flakes), waits
// for the Cluster to reach Available=True, fetches the workload
// kubeconfig from the kind-side Secret, materialises it to a temp file,
// and returns the kubeconfig path.
//
// No-op when cfg.Pivot.Enabled is false (returns "", nil).
//
// Caller is responsible for `os.Remove(kubeconfigPath)` once the pivot
// is done; we don't hold the temp file ourselves so subsequent steps
// (InstallCAPIOnManagement, MoveCAPIState, VerifyParity) can keep
// using it.
//
// Inputs read: cfg.Mgmt.ClusterName/Namespace, cfg.Mgmt.KubernetesVersion,
// cfg.MgmtControlPlane*, cfg.ClusterctlCfg (set by the kind init phase),
// cfg.KindClusterName (kubeconfig context lookup).
func EnsureManagementCluster(cfg *config.Config) (string, error) {
	if !cfg.Pivot.Enabled {
		return "", nil
	}

	kindCtx := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(kindCtx) {
		return "", fmt.Errorf("EnsureManagementCluster: kind context %s missing; bootstrap kind first", kindCtx)
	}

	// 1) Render the mgmt CAPI manifest. The clusterctl config file is
	//    the same one the kind init phase wrote (cfg.ClusterctlCfg).
	manifestPath, err := renderManagementManifest(cfg, cfg.ClusterctlCfg)
	if err != nil {
		return "", fmt.Errorf("render mgmt manifest: %w", err)
	}

	// Inject CAAPH labels (caaph: enabled + cilium-cluster-id) on the
	// mgmt Cluster doc so the mgmt-scoped HelmChartProxy selects it.
	if err := patchMgmtClusterCAAPHLabels(cfg, manifestPath); err != nil {
		return "", fmt.Errorf("patch mgmt CAAPH labels: %w", err)
	}

	// 2) Apply the manifest against kind. Use the same
	//    kubectl.ApplyWorkloadManifestToManagementCluster helper as
	//    the workload phase — it already handles the
	//    ProxmoxCluster-skip-on-exists edge case + dynamic-client SSA.
	//    It selects the kind context from cfg.KindClusterName, which
	//    is what we want here (kind IS the management cluster at this
	//    stage of the pivot).
	logx.Log("Applying management cluster manifest %s on kind…", manifestPath)
	saveCAPIManifest := cfg.CAPIManifest
	cfg.CAPIManifest = manifestPath
	for attempt := 1; attempt <= 3; attempt++ {
		err := kubectl.ApplyWorkloadManifestToManagementCluster(cfg, manifestPath)
		if err == nil {
			break
		}
		if attempt == 3 {
			cfg.CAPIManifest = saveCAPIManifest
			return "", fmt.Errorf("apply mgmt manifest after 3 attempts: %w", err)
		}
		logx.Warn("Apply mgmt manifest failed (attempt %d/3): %v — retrying in 10s", attempt, err)
		time.Sleep(10 * time.Second)
	}
	cfg.CAPIManifest = saveCAPIManifest

	// 3) Render + apply the mgmt-scoped HelmChartProxy for Cilium.
	//    Targets the mgmt Cluster via cluster-id label (set above).
	cli, err := k8sclient.ForContext(kindCtx)
	if err != nil {
		return "", fmt.Errorf("load kind context: %w", err)
	}
	hcpYAML := renderMgmtCiliumHelmChartProxy(cfg)
	if err := cli.ApplyYAML(context.Background(), []byte(hcpYAML)); err != nil {
		return "", fmt.Errorf("apply mgmt Cilium HelmChartProxy: %w", err)
	}
	logx.Log("Applied HelmChartProxy %s-caaph-cilium for management cluster (Cilium delivered to mgmt by CAAPH).",
		cfg.Mgmt.ClusterName)

	// Live-patch the labels on the existing mgmt Cluster object too
	// so CAAPH picks the labels up immediately (the manifest patch
	// only matters at create-time).
	if err := patchLiveMgmtClusterLabels(cfg, cli); err != nil {
		logx.Warn("Could not patch live mgmt Cluster labels: %v (manifest labels still in effect)", err)
	}

	// 4) Wait for the mgmt cluster to come Available. Same poll loop
	//    used for the workload cluster (60-minute ceiling).
	logx.Log("Waiting for management cluster %s/%s Available…",
		cfg.Mgmt.ClusterNamespace, cfg.Mgmt.ClusterName)
	if err := waitClusterAvailable(cli, context.Background(),
		cfg.Mgmt.ClusterNamespace, cfg.Mgmt.ClusterName, 60*time.Minute); err != nil {
		return "", fmt.Errorf("management cluster did not become Available: %w", err)
	}
	logx.Log("Management cluster %s reached Available=True.", cfg.Mgmt.ClusterName)

	// 5) Fetch the kubeconfig Secret. CAPI writes
	//    <name>-kubeconfig in the same namespace as the Cluster.
	kcfgPath, err := fetchManagementKubeconfig(cli, cfg.Mgmt.ClusterNamespace, cfg.Mgmt.ClusterName)
	if err != nil {
		return "", fmt.Errorf("fetch mgmt kubeconfig: %w", err)
	}
	logx.Log("Fetched management cluster kubeconfig: %s", kcfgPath)
	return kcfgPath, nil
}

// VerifyParity polls the management cluster until it carries identical
// CAPI inventory + bootstrap Secrets to the kind cluster, or the
// PivotVerifyTimeout elapses. Specifically:
//
//  1. clusters.cluster.x-k8s.io list contains the workload cluster
//     (the move brings the workload Cluster object across).
//  2. Any provider-specific bootstrap Secrets listed in the active
//     provider's PivotTarget.VerifySecrets are present. Providers that
//     have no bootstrap Secrets to verify return an empty list.
//  3. capi-system Deployment reports Available=True.
//
// The set of Deployments and Secrets verified is driven by the active
// provider's PivotTarget so that VerifyParity is provider-agnostic:
// Proxmox verifies capmox-system + its bootstrap Secrets; a future
// AWS provider would verify capa-system + its own Secrets.
//
// Returns nil on parity, otherwise the first failure encountered after
// the timeout.
func VerifyParity(cfg *config.Config, mgmtKubeconfig string) error {
	if !cfg.Pivot.Enabled {
		return nil
	}
	if mgmtKubeconfig == "" {
		return fmt.Errorf("VerifyParity: empty mgmt kubeconfig path")
	}
	cli, err := k8sclient.ForKubeconfigFile(mgmtKubeconfig)
	if err != nil {
		return fmt.Errorf("load mgmt kubeconfig: %w", err)
	}
	timeout := parseDuration(cfg.Pivot.VerifyTimeout, 10*time.Minute)
	deadline := time.Now().Add(timeout)

	// Ask the active provider for the Secrets it expects to find on
	// the management cluster after the handoff step. Providers that
	// have no managed-mgmt story return ErrNotApplicable; in that case
	// we skip Secret verification (parity is still checked via CAPI
	// Cluster objects + capi-system Deployment).
	var providerVerifySecrets []provider.VerifySecret
	if prov, provErr := provider.For(cfg); provErr == nil {
		if pt, ptErr := prov.PivotTarget(cfg); ptErr == nil {
			providerVerifySecrets = pt.VerifySecrets
		}
	}

	logx.Log("Verifying mgmt-vs-kind parity (timeout %s)…", timeout)

	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = nil

		// (a) CAPI Cluster objects on mgmt.
		bg := context.Background()
		clusters, err := cli.Dynamic.Resource(capiClusterGVR).Namespace("").List(bg, metav1.ListOptions{})
		if err != nil {
			lastErr = fmt.Errorf("list capi clusters on mgmt: %w", err)
		} else {
			names := map[string]bool{}
			for _, it := range clusters.Items {
				names[it.GetName()] = true
			}
			if cfg.WorkloadClusterName != "" && !names[cfg.WorkloadClusterName] {
				lastErr = fmt.Errorf("workload cluster %q not found on mgmt", cfg.WorkloadClusterName)
			}
		}

		// (b) Provider-specific bootstrap Secrets present. The set is
		// empty for providers without a bootstrap-Secret story; checking
		// is skipped entirely in that case so the function stays
		// provider-agnostic.
		if lastErr == nil && len(providerVerifySecrets) > 0 {
			// Collect the unique namespaces so we can verify each
			// namespace exists before looking for its Secrets.
			nsSeen := map[string]bool{}
			for _, vs := range providerVerifySecrets {
				if vs.Namespace != "" && !nsSeen[vs.Namespace] {
					nsSeen[vs.Namespace] = true
					if _, err := cli.Typed.CoreV1().Namespaces().Get(bg, vs.Namespace, metav1.GetOptions{}); err != nil {
						if apierrors.IsNotFound(err) {
							lastErr = fmt.Errorf("namespace %s not on mgmt yet", vs.Namespace)
						} else {
							lastErr = fmt.Errorf("get %s ns: %w", vs.Namespace, err)
						}
						break
					}
				}
			}
			if lastErr == nil {
				var missing []string
				for _, vs := range providerVerifySecrets {
					if vs.Namespace == "" || vs.Name == "" {
						continue
					}
					if _, err := cli.Typed.CoreV1().Secrets(vs.Namespace).Get(bg, vs.Name, metav1.GetOptions{}); err != nil {
						if apierrors.IsNotFound(err) {
							missing = append(missing, vs.Namespace+"/"+vs.Name)
						} else {
							lastErr = fmt.Errorf("get secret %s/%s: %w", vs.Namespace, vs.Name, err)
							break
						}
					}
				}
				if lastErr == nil && len(missing) > 0 {
					lastErr = fmt.Errorf("bootstrap Secrets missing on mgmt: %s",
						strings.Join(sortedStrings(missing), ","))
				}
			}
		}

		// (c) capi-system Deployment Available — provider-agnostic.
		// Provider-specific controller Deployments are verified by
		// InstallCAPIOnManagement + MoveCAPIState; VerifyParity owns
		// only the universal CAPI core check.
		if lastErr == nil {
			dep, err := cli.Typed.AppsV1().Deployments("capi-system").Get(bg, "capi-controller-manager", metav1.GetOptions{})
			if err != nil {
				lastErr = fmt.Errorf("get deployment capi-system/capi-controller-manager on mgmt: %w", err)
			} else {
				ok := false
				for _, c := range dep.Status.Conditions {
					if string(c.Type) == "Available" && string(c.Status) == "True" {
						ok = true
						break
					}
				}
				if !ok {
					lastErr = fmt.Errorf("capi-system/capi-controller-manager not Available on mgmt")
				}
			}
		}

		// (d) yage-system namespace present on mgmt (ADR 0011 §6.a).
		if lastErr == nil {
			if err := checkYageSystemNamespace(bg, cli.Typed); err != nil {
				lastErr = err
			}
		}

		// (e) Labeled yage-system Secrets present (ADR 0011 §6.b).
		// Zero is a warning, not fatal — a first-run with no tofu modules
		// executed yet has no tfstate Secrets to copy.
		if lastErr == nil {
			if err := checkLabeledYageSystemSecrets(bg, cli.Typed); err != nil {
				lastErr = err
			}
		}

		// (f) yage-repos PVC Bound (ADR 0011 §6.c).
		if lastErr == nil {
			if err := checkYageReposPVCBound(bg, cli.Typed); err != nil {
				lastErr = err
			}
		}

		if lastErr == nil {
			logx.Log("Parity verified: workload Cluster + bootstrap Secrets + CAPI controllers + yage-system + yage-repos PVC all present on mgmt.")
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("parity not reached within %s: %v", timeout, lastErr)
}

// checkYageSystemNamespace verifies the yage-system namespace exists on
// the management cluster (created by HandOff via EnsureNamespace, then
// re-asserted by EnsureYageSystemOnCluster). Returns a typed error so
// VerifyParity's poll loop can retry it transparently.
func checkYageSystemNamespace(ctx context.Context, typed kubernetes.Interface) error {
	if _, err := typed.CoreV1().Namespaces().Get(ctx, yageSystemNamespace, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("namespace %s not on mgmt yet", yageSystemNamespace)
		}
		return fmt.Errorf("get namespace %s on mgmt: %w", yageSystemNamespace, err)
	}
	return nil
}

// checkLabeledYageSystemSecrets lists Secrets in yage-system carrying
// app.kubernetes.io/managed-by=yage on the management cluster. Per
// ADR 0011 §6.b zero matches is a WARNING (handed-off cleanly but no
// tofu state had been written when pivot ran) and one+ is the steady
// state. Listing errors are reported so the poll loop can retry.
func checkLabeledYageSystemSecrets(ctx context.Context, typed kubernetes.Interface) error {
	list, err := typed.CoreV1().Secrets(yageSystemNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: yageManagedBySelector,
	})
	if err != nil {
		return fmt.Errorf("list labeled %s Secrets on mgmt: %w", yageSystemNamespace, err)
	}
	if len(list.Items) == 0 {
		logx.Warn("VerifyParity: zero Secrets in %s carry %s on mgmt — first-run with no tofu state yet?",
			yageSystemNamespace, yageManagedBySelector)
	}
	return nil
}

// checkYageReposPVCBound verifies the yage-repos PVC in yage-system on
// the management cluster is in the Bound phase. A Pending PVC here
// indicates a StorageClass problem — surface it with a clear diagnostic
// before the first post-pivot tofu Job runs (ADR 0011 §6.c).
func checkYageReposPVCBound(ctx context.Context, typed kubernetes.Interface) error {
	pvc, err := typed.CoreV1().PersistentVolumeClaims(yageSystemNamespace).Get(ctx, yageReposPVCName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("pvc %s/%s not on mgmt yet", yageSystemNamespace, yageReposPVCName)
		}
		return fmt.Errorf("get pvc %s/%s on mgmt: %w", yageSystemNamespace, yageReposPVCName, err)
	}
	if pvc.Status.Phase != corev1.ClaimBound {
		return fmt.Errorf("pvc %s/%s phase=%s, want Bound (StorageClass problem on mgmt?)",
			yageSystemNamespace, yageReposPVCName, pvc.Status.Phase)
	}
	return nil
}

// TeardownKind deletes the kind cluster after a successful pivot. Honors
// cfg.Pivot.KeepKind and cfg.NoDeleteKind — when either is set the
// function logs and returns without doing anything.
func TeardownKind(cfg *config.Config) error {
	if !cfg.Pivot.Enabled {
		return nil
	}
	if cfg.Pivot.KeepKind {
		logx.Log("PivotKeepKind=true; leaving kind cluster %s alive.", cfg.KindClusterName)
		return nil
	}
	if cfg.NoDeleteKind {
		logx.Log("NoDeleteKind=true; leaving kind cluster %s alive after pivot.", cfg.KindClusterName)
		return nil
	}
	if cfg.KindClusterName == "" {
		logx.Warn("TeardownKind: cfg.KindClusterName is empty — nothing to delete.")
		return nil
	}
	provider := cluster.NewProvider()
	names, err := provider.List()
	if err != nil {
		return fmt.Errorf("list kind clusters: %w", err)
	}
	found := false
	for _, n := range names {
		if n == cfg.KindClusterName {
			found = true
			break
		}
	}
	if !found {
		logx.Log("kind cluster %s already absent; nothing to tear down.", cfg.KindClusterName)
		return nil
	}
	logx.Log("Tearing down ephemeral kind cluster %s after successful pivot…", cfg.KindClusterName)
	if err := provider.Delete(cfg.KindClusterName, ""); err != nil {
		return fmt.Errorf("delete kind cluster %s: %w", cfg.KindClusterName, err)
	}
	logx.Log("kind cluster %s deleted.", cfg.KindClusterName)
	return nil
}

// fetchManagementKubeconfig reads the CAPI-generated <name>-kubeconfig
// Secret on the kind cluster, base64-decodes data.value, and writes the
// result to a temp file via k8sclient.WriteTempKubeconfig. Returns the
// temp file path. The caller owns the file (no auto-cleanup).
func fetchManagementKubeconfig(kindCli *k8sclient.Client, ns, name string) (string, error) {
	bg := context.Background()
	secretName := name + "-kubeconfig"
	sec, err := kindCli.Typed.CoreV1().Secrets(ns).Get(bg, secretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get %s/%s: %w", ns, secretName, err)
	}
	raw, ok := sec.Data["value"]
	if !ok || len(raw) == 0 {
		return "", fmt.Errorf("secret %s/%s has empty data.value", ns, secretName)
	}
	// CAPI normally stores raw kubeconfig bytes (already decoded by the
	// typed client). If it looks base64-encoded, decode it once.
	body := raw
	if maybeBase64(raw) {
		dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err == nil && len(dec) > 0 {
			body = dec
		}
	}
	path, _, err := k8sclient.WriteTempKubeconfig("mgmt-cluster", body)
	if err != nil {
		return "", fmt.Errorf("write tmp kubeconfig: %w", err)
	}
	return path, nil
}

// maybeBase64 reports whether b looks like an ASCII-base64 blob (no
// newlines / non-printable bytes apart from a trailing newline). The
// typed client decodes Secret data values by default; this is a belt
// for clusters where someone applied the Secret already-base64-encoded.
func maybeBase64(b []byte) bool {
	s := strings.TrimSpace(string(b))
	if len(s) == 0 {
		return false
	}
	if strings.HasPrefix(s, "apiVersion:") || strings.HasPrefix(s, "kind:") {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z') &&
			!(r >= 'a' && r <= 'z') &&
			!(r >= '0' && r <= '9') &&
			r != '+' && r != '/' && r != '=' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// patchLiveMgmtClusterLabels merge-patches the live mgmt Cluster on
// kind with the same CAAPH labels that patchMgmtClusterCAAPHLabels
// wrote into the manifest. Same shape as
// caaph.PatchClusterCAAPHHelmLabels' live-patch tail.
func patchLiveMgmtClusterLabels(cfg *config.Config, kindCli *k8sclient.Client) error {
	if cfg.Mgmt.ClusterName == "" || cfg.Mgmt.ClusterNamespace == "" {
		return nil
	}
	port := cfg.Mgmt.ControlPlaneEndpointPort
	if port == "" {
		port = "6443"
	}
	patchLabels := map[string]string{"caaph": "enabled"}
	if cfg.Mgmt.CiliumClusterID != "" {
		patchLabels["caaph.cilium.cluster-id"] = cfg.Mgmt.CiliumClusterID
	}
	if cfg.Mgmt.ControlPlaneEndpointIP != "" {
		patchLabels["caaph.cilium.k8s-service-host"] = cfg.Mgmt.ControlPlaneEndpointIP
	}
	patchLabels["caaph.cilium.k8s-service-port"] = port

	body := []byte(`{"metadata":{"labels":` + jsonStringMap(patchLabels) + `}}`)
	gvk := schema.GroupVersionKind{Group: "cluster.x-k8s.io", Version: "v1beta2", Kind: "Cluster"}
	mapping, err := kindCli.Mapper.RESTMapping(gvk.GroupKind())
	if err != nil {
		return err
	}
	bg := context.Background()
	_, err = kindCli.Dynamic.Resource(mapping.Resource).
		Namespace(cfg.Mgmt.ClusterNamespace).
		Patch(bg, cfg.Mgmt.ClusterName, "application/merge-patch+json", body, metav1.PatchOptions{
			FieldManager: k8sclient.FieldManager,
		})
	return err
}

// jsonStringMap renders a map[string]string into a deterministic JSON
// object literal so the resulting merge-patch body is byte-stable per
// run. Avoids pulling encoding/json at the file scope just for this.
func jsonStringMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	keys = sortedStrings(keys)
	var sb strings.Builder
	sb.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"`)
		sb.WriteString(jsonEscape(k))
		sb.WriteString(`":"`)
		sb.WriteString(jsonEscape(m[k]))
		sb.WriteString(`"`)
	}
	sb.WriteString("}")
	return sb.String()
}

func jsonEscape(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return r.Replace(s)
}