// Package argocdx ports the Argo CD helpers: initial-admin password
// retrieval, workload kubeconfig discovery for standalone modes, access
// info printing, port-forward, and the argocd-redis seed Secret.
//
// Bash source map:
//   - argocd_read_initial_admin_password                          ~L5854-5862
//   - argocd_standalone_discover_workload_kubeconfig_ref          ~L5867-5919
//   - argocd_print_access_info                                    ~L5922-5964
//   - argocd_run_port_forwards                                    ~L5967-6007
//   - apply_workload_argocd_redis_secret_to_workload_cluster      ~L6047-6092
package argocdx

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/k8sclient"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/sysinfo"
)

// ReadInitialAdminPassword ports argocd_read_initial_admin_password.
// Tries the Helm-chart Secret argocd-initial-admin-secret, the Operator's
// argocd-cluster Secret, and the legacy argocd-secret (skipping the bcrypt
// hash bash skips), returning the password or "" if none found.
//
// Note: the first parameter ctx is the kubectl-context name (a string),
// not a context.Context. Preserving the original signature so callers in
// other packages keep compiling.
func ReadInitialAdminPassword(ctx, namespace string) string {
	if namespace == "" {
		namespace = "argocd"
	}
	cli, err := k8sclient.ForContext(ctx)
	if err != nil {
		return ""
	}
	return readAdminPasswordFromClient(cli, namespace)
}

// readAdminPasswordFromClient is the shared probe loop used by both
// ReadInitialAdminPassword (kube-context flavour) and
// ReadInitialAdminPasswordWithKubeconfig (kubeconfig-file flavour).
func readAdminPasswordFromClient(cli *k8sclient.Client, namespace string) string {
	probes := []struct {
		sec, key   string
		skipBcrypt bool
	}{
		{"argocd-initial-admin-secret", "password", false},
		{"argocd-cluster", "admin.password", false},
		{"argocd-secret", "admin.password", true},
	}
	bg := context.Background()
	for _, probe := range probes {
		sec, err := cli.Typed.CoreV1().Secrets(namespace).Get(bg, probe.sec, metav1.GetOptions{})
		if err != nil {
			continue
		}
		// Secret.Data is already base64-decoded by client-go.
		raw, ok := sec.Data[probe.key]
		if !ok || len(raw) == 0 {
			continue
		}
		if probe.skipBcrypt && strings.HasPrefix(string(raw), "$2") {
			continue
		}
		return string(raw)
	}
	return ""
}

// StandaloneDiscoverWorkloadKubeconfigRef ports
// argocd_standalone_discover_workload_kubeconfig_ref. Returns nil on
// success (cfg.WorkloadClusterName/Namespace may have been updated);
// returns an error when a unique match could not be inferred.
func StandaloneDiscoverWorkloadKubeconfigRef(cfg *config.Config) error {
	kctx := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(kctx) {
		return fmt.Errorf("kube context %s not found", kctx)
	}
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return fmt.Errorf("kube client for %s: %w", kctx, err)
	}
	bg := context.Background()
	secretName := cfg.WorkloadClusterName + "-kubeconfig"
	// Fast path: secret already present.
	if _, err := cli.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
		Get(bg, secretName, metav1.GetOptions{}); err == nil {
		return nil
	}

	clusterGVR := schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta2",
		Resource: "clusters",
	}

	// Try to resolve namespace from the unique CAPI Cluster named
	// cfg.WorkloadClusterName.
	list, err := cli.Dynamic.Resource(clusterGVR).Namespace("").List(bg, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing CAPI clusters: %w", err)
	}
	var nsMatches []string
	type nsName struct{ ns, name string }
	var allClusters []nsName
	for _, item := range list.Items {
		ns := item.GetNamespace()
		nm := item.GetName()
		if nm == "" {
			continue
		}
		allClusters = append(allClusters, nsName{ns, nm})
		if nm == cfg.WorkloadClusterName {
			nsMatches = append(nsMatches, ns)
		}
	}
	if len(nsMatches) == 1 {
		if cfg.WorkloadClusterNamespace != nsMatches[0] {
			cfg.WorkloadClusterNamespace = nsMatches[0]
			logx.Log("Resolved namespace to %s (CAPI Cluster %s).",
				cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
		}
		if _, err := cli.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
			Get(bg, cfg.WorkloadClusterName+"-kubeconfig", metav1.GetOptions{}); err == nil {
			return nil
		}
	} else if len(nsMatches) > 1 {
		logx.Warn("Multiple CAPI Clusters named %s in namespaces: %s. Set --workload-cluster-namespace.",
			cfg.WorkloadClusterName, strings.Join(nsMatches, " "))
		return fmt.Errorf("ambiguous workload namespace")
	}

	if cfg.WorkloadClusterNameExplicit || cfg.WorkloadClusterNamespaceExplicit {
		return fmt.Errorf("no workload kubeconfig found and explicit flags lock name/ns")
	}

	// Fallback: pick the only CAPI cluster with a *-kubeconfig Secret.
	var cands []string
	for _, c := range allClusters {
		if c.ns == "" || c.name == "" {
			continue
		}
		if _, err := cli.Typed.CoreV1().Secrets(c.ns).
			Get(bg, c.name+"-kubeconfig", metav1.GetOptions{}); err == nil {
			cands = append(cands, c.ns+"/"+c.name)
		}
	}
	switch len(cands) {
	case 0:
		return fmt.Errorf("no CAPI clusters with a kubeconfig Secret")
	case 1:
		parts := strings.SplitN(cands[0], "/", 2)
		cfg.WorkloadClusterNamespace = parts[0]
		cfg.WorkloadClusterName = parts[1]
		logx.Log("Using workload %s/%s (the only CAPI cluster on %s with a *-kubeconfig Secret).",
			cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, kctx)
		return nil
	default:
		logx.Warn("More than one CAPI cluster with a kubeconfig Secret on %s: %s. Use --workload-cluster-name and --workload-cluster-namespace, or CAPI_MANIFEST, for discover_workload_cluster_identity.",
			kctx, strings.Join(cands, " "))
		return fmt.Errorf("ambiguous workload selection")
	}
}

// PrintAccessInfo ports argocd_print_access_info. Prints a block
// describing how to port-forward + log in to Argo CD on the workload
// cluster, and the initial admin password when available.
func PrintAccessInfo(cfg *config.Config) {
	kctx := "kind-" + cfg.KindClusterName
	port := cfg.ArgoCDPortForwardPort
	if port == "" {
		port = "8443"
	}
	wlPFAddr := "127.0.0.1:" + port
	loginExtra := "--grpc-web"
	if sysinfo.IsTrue(cfg.ArgoCDServerInsecure) {
		loginExtra = "--insecure --grpc-web"
	}
	fmt.Println("\n\033[1;36m── Argo CD access (initial admin; rotate after first login) — provisioned cluster only ──\033[0m")
	fmt.Printf("\n\033[1;33m[CAPI / Proxmox workload] cluster %s / Argo namespace %s\033[0m\n",
		cfg.WorkloadClusterName, cfg.WorkloadArgoCDNamespace)

	mgmt, err := k8sclient.ForContext(kctx)
	if err != nil {
		logx.Warn("Cannot access management context %s: %v", kctx, err)
		return
	}
	bg := context.Background()
	if _, err := mgmt.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
		Get(bg, cfg.WorkloadClusterName+"-kubeconfig", metav1.GetOptions{}); err != nil {
		logx.Warn("Secret %s/%s-kubeconfig not found. Use --workload-cluster-name / --workload-cluster-namespace (or env), CAPI_MANIFEST, or wait until CAPI creates this Secret in the Cluster's namespace.",
			cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
		clusterGVR := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta2", Resource: "clusters"}
		if list, lerr := mgmt.Dynamic.Resource(clusterGVR).Namespace("").List(bg, metav1.ListOptions{}); lerr == nil {
			parts := make([]string, 0, len(list.Items))
			for _, it := range list.Items {
				parts = append(parts, it.GetNamespace()+"/"+it.GetName())
			}
			logx.Log("  CAPI Clusters (management): %s", strings.Join(parts, " "))
		}
		return
	}
	kcfg, err := writeWorkloadKubeconfig(cfg, kctx)
	if err == nil {
		defer os.Remove(kcfg)
		// Try a typed client against the workload kubeconfig and check namespace presence.
		if wcli, werr := k8sclient.ForKubeconfigFile(kcfg); werr == nil {
			if _, nserr := wcli.Typed.CoreV1().Namespaces().Get(bg, cfg.WorkloadArgoCDNamespace, metav1.GetOptions{}); nserr == nil {
				pw := readAdminPasswordFromClient(wcli, cfg.WorkloadArgoCDNamespace)
				if pw != "" {
					logx.Log("  Initial admin password: %s", pw)
				} else {
					logx.Warn("  Admin password not found in %s (checked argocd-initial-admin-secret, argocd-cluster, argocd-secret — not installed or password rotated?).",
						cfg.WorkloadArgoCDNamespace)
				}
			} else {
				logx.Warn("  Namespace %s not on workload — run bootstrap with workload Argo enabled first.",
					cfg.WorkloadArgoCDNamespace)
			}
		}
	}
	fmt.Printf("  Write kubeconfig and port-forward (local port matches ARGOCD_PORT_FORWARD_PORT, default %s):\n", port)
	fmt.Printf("    kubectl --context \"%s\" -n \"%s\" get secret \"%s-kubeconfig\" -o jsonpath={.data.value} | base64 -d > /tmp/%s-kubeconfig.yaml\n",
		kctx, cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, cfg.WorkloadClusterName)
	fmt.Printf("    export KUBECONFIG=/tmp/%s-kubeconfig.yaml\n", cfg.WorkloadClusterName)
	fmt.Printf("    kubectl port-forward --address 127.0.0.1 -n \"%s\" svc/argocd-server %s:443\n", cfg.WorkloadArgoCDNamespace, port)
	fmt.Printf("  Login to Argo on the CAPI cluster:\n")
	fmt.Printf("    argocd login %s --username admin --password '<password>' %s\n\n", wlPFAddr, loginExtra)
}

// ReadInitialAdminPasswordWithKubeconfig reads the admin password using
// a specific kubeconfig file (the workload cluster). Probes, in order:
//   - argocd-initial-admin-secret / password   (Helm chart default)
//   - argocd-cluster / admin.password          (Argo CD Operator)
//   - argocd-secret / admin.password           (fallback: only if plaintext, not bcrypt)
func ReadInitialAdminPasswordWithKubeconfig(kubeconfig, namespace string) string {
	if namespace == "" {
		namespace = "argocd"
	}
	cli, err := k8sclient.ForKubeconfigFile(kubeconfig)
	if err != nil {
		return ""
	}
	return readAdminPasswordFromClient(cli, namespace)
}

// RunPortForwards ports argocd_run_port_forwards. Blocks until the
// user interrupts; always removes the tmp kubeconfig.
//
// NOTE: this is intentionally still a kubectl shell-out. Implementing
// SPDY port-forward in-process (k8s.io/client-go/tools/portforward +
// SPDY transport + own ready/done channels + signal/close coordination)
// is roughly 80 lines of plumbing for a user-blocking print-and-exit
// command. The pre-flight checks (context exists, kubeconfig secret
// present, namespace present) are done in-process via typed clients;
// only the long-lived forwarding child process is a subprocess.
func RunPortForwards(cfg *config.Config) {
	kctx := "kind-" + cfg.KindClusterName
	port := cfg.ArgoCDPortForwardPort
	if port == "" {
		port = "8443"
	}
	if cfg.ArgoCDPortForwardTarget != "" && cfg.ArgoCDPortForwardTarget != "workload" {
		logx.Warn("port-forward: only the provisioned cluster is supported (ARGOCD_PORT_FORWARD_TARGET=workload); ignoring %s.",
			cfg.ArgoCDPortForwardTarget)
	}
	if !k8sclient.ContextExists(kctx) {
		logx.Die("port-forward: kubectl context %s not found (need management cluster to read %s-kubeconfig).",
			kctx, cfg.WorkloadClusterName)
	}
	mgmt, err := k8sclient.ForContext(kctx)
	if err != nil {
		logx.Die("port-forward: cannot connect to %s: %v", kctx, err)
	}
	bg := context.Background()
	if _, err := mgmt.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
		Get(bg, cfg.WorkloadClusterName+"-kubeconfig", metav1.GetOptions{}); err != nil {
		logx.Die("port-forward: %s/%s-kubeconfig not found (is the CAPI cluster ready?).",
			cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
	}
	kcfg, err := writeWorkloadKubeconfig(cfg, kctx)
	if err != nil {
		logx.Die("port-forward: could not read workload kubeconfig")
	}
	wcli, err := k8sclient.ForKubeconfigFile(kcfg)
	if err != nil {
		os.Remove(kcfg)
		logx.Die("port-forward: cannot read workload kubeconfig: %v", err)
	}
	if _, err := wcli.Typed.CoreV1().Namespaces().Get(bg, cfg.WorkloadArgoCDNamespace, metav1.GetOptions{}); err != nil {
		os.Remove(kcfg)
		logx.Die("port-forward: namespace %s not found on the CAPI cluster — is workload Argo installed?",
			cfg.WorkloadArgoCDNamespace)
	}
	// Long-lived shell-out: see function-level NOTE for rationale.
	cmd := exec.Command("kubectl", "port-forward", "--address", "127.0.0.1",
		"-n", cfg.WorkloadArgoCDNamespace,
		"svc/argocd-server", port+":443")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kcfg)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		os.Remove(kcfg)
		logx.Die("port-forward: failed to start: %v", err)
	}
	logx.Log("Port-forward: CAPI / Proxmox workload Argo → 127.0.0.1:%s (pid %d) — Ctrl+C to stop — see --argocd-print-access for password.",
		port, cmd.Process.Pid)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ch:
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-done
	case <-done:
	}
	os.Remove(kcfg)
	time.Sleep(100 * time.Millisecond) // let stty sane take effect
}

// ApplyRedisSecretToWorkload ports
// apply_workload_argocd_redis_secret_to_workload_cluster. Creates
// argocd-redis with a random auth value in the workload's Argo
// namespace, unless it already exists (idempotent).
func ApplyRedisSecretToWorkload(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	if !cfg.WorkloadArgoCDEnabled {
		return
	}
	wk, err := writeWorkloadKubeconfig()
	if err != nil || wk == "" {
		logx.Die("Cannot read workload kubeconfig for argocd-redis: no data in %s/%s-kubeconfig on kind-%s, and clusterctl get kubeconfig failed (set namespace/name, or wait until the Cluster is Available).",
			cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, cfg.KindClusterName)
	}
	defer os.Remove(wk)

	cli, err := k8sclient.ForKubeconfigFile(wk)
	if err != nil {
		logx.Die("Cannot connect to workload cluster for argocd-redis: %v", err)
	}
	bg := context.Background()

	ns := cfg.WorkloadArgoCDNamespace
	if ns == "" {
		ns = "argocd"
	}
	if err := cli.EnsureNamespace(bg, ns); err != nil {
		logx.Die("Failed to ensure namespace %s on the workload for argocd-redis.", ns)
	}
	if _, err := cli.Typed.CoreV1().Secrets(ns).Get(bg, "argocd-redis", metav1.GetOptions{}); err == nil {
		logx.Log("Workload %s/argocd-redis already present — not overwriting (idempotent bootstrap).", ns)
		return
	} else if !apierrors.IsNotFound(err) {
		logx.Die("Failed to check %s/argocd-redis on the workload cluster: %v", ns, err)
	}
	pw := randomBase64(32)
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "argocd-redis",
			Namespace: ns,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"auth": []byte(pw),
		},
	}
	if err := applyTypedSecret(bg, cli, sec); err != nil {
		logx.Die("Failed to create argocd-redis on the workload cluster: %v", err)
	}
	logx.Log("Created %s/argocd-redis on the workload (key auth) via kind/management bootstrap.", ns)
}

// applyTypedSecret server-side-applies a corev1.Secret using the typed
// CoreV1 SecretInterface, mirroring the apply path of k8sclient but for
// a strongly-typed object.
func applyTypedSecret(ctx context.Context, cli *k8sclient.Client, sec *corev1.Secret) error {
	jdata, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("marshal secret: %w", err)
	}
	jsonBody, err := yaml.YAMLToJSON(jdata)
	if err != nil {
		return fmt.Errorf("yaml→json: %w", err)
	}
	_, err = cli.Typed.CoreV1().Secrets(sec.Namespace).Patch(
		ctx, sec.Name, types.ApplyPatchType, jsonBody,
		metav1.PatchOptions{
			FieldManager: k8sclient.FieldManager,
			Force:        boolPtr(true),
		},
	)
	if err != nil {
		return err
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

// --- helpers ---

// writeWorkloadKubeconfig reads the workload kubeconfig Secret from the
// management cluster (typed client) and materialises it to a temp file.
func writeWorkloadKubeconfig(cfg *config.Config, mgmtCtx string) (string, error) {
	cli, err := k8sclient.ForContext(mgmtCtx)
	if err != nil {
		return "", err
	}
	sec, err := cli.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
		Get(context.Background(), cfg.WorkloadClusterName+"-kubeconfig", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	val, ok := sec.Data["value"]
	if !ok || len(val) == 0 {
		return "", fmt.Errorf("workload kubeconfig secret empty")
	}
	f, err := os.CreateTemp("", "workload-kubeconfig-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(val); err != nil {
		return "", err
	}
	_ = f.Chmod(0o600)
	return f.Name(), nil
}

// randomBase64 returns a URL-safe base64 string of n random bytes.
// Matches bash `openssl rand -base64 32 | tr -d '\n\r'`.
func randomBase64(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}
