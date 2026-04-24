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
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
	"github.com/lpasquali/bootstrap-capi/internal/sysinfo"
)

// ReadInitialAdminPassword ports argocd_read_initial_admin_password.
// Tries both the Helm-chart Secret argocd-initial-admin-secret and the
// Operator's argocd-cluster Secret, returning the decoded password or
// "" if neither has a readable password.
func ReadInitialAdminPassword(ctx, namespace string) string {
	if namespace == "" {
		namespace = "argocd"
	}
	build := func(sec, key string) []string {
		a := []string{"kubectl"}
		if ctx != "" {
			a = append(a, "--context", ctx)
		}
		return append(a, "get", "secret", sec, "-n", namespace,
			"-o", "jsonpath={.data."+key+"}")
	}
	for _, probe := range []struct{ sec, key string }{
		{"argocd-initial-admin-secret", "password"},
		{"argocd-cluster", `admin\.password`},
	} {
		out, _, _ := shell.Capture(build(probe.sec, probe.key)...)
		out = strings.TrimSpace(out)
		if out == "" {
			continue
		}
		dec, err := base64.StdEncoding.DecodeString(out)
		if err != nil || len(dec) == 0 {
			continue
		}
		return string(dec)
	}
	return ""
}

// StandaloneDiscoverWorkloadKubeconfigRef ports
// argocd_standalone_discover_workload_kubeconfig_ref. Returns nil on
// success (cfg.WorkloadClusterName/Namespace may have been updated);
// returns an error when a unique match could not be inferred.
func StandaloneDiscoverWorkloadKubeconfigRef(cfg *config.Config) error {
	kctx := "kind-" + cfg.KindClusterName
	if !contextExists(kctx) {
		return fmt.Errorf("kube context %s not found", kctx)
	}
	// Fast path: secret already present.
	if err := shell.Run("kubectl", "--context", kctx,
		"get", "secret", cfg.WorkloadClusterName+"-kubeconfig",
		"-n", cfg.WorkloadClusterNamespace); err == nil {
		return nil
	}
	// Try to resolve namespace from the unique CAPI Cluster named
	// cfg.WorkloadClusterName.
	out, _, _ := shell.Capture(
		"kubectl", "--context", kctx, "get", "cluster", "-A",
		"-o", "custom-columns=NS:.metadata.namespace,NAME:.metadata.name", "--no-headers",
	)
	var nsMatches []string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		fields := strings.Fields(ln)
		if len(fields) == 2 && fields[1] == cfg.WorkloadClusterName {
			nsMatches = append(nsMatches, fields[0])
		}
	}
	if len(nsMatches) == 1 {
		if cfg.WorkloadClusterNamespace != nsMatches[0] {
			cfg.WorkloadClusterNamespace = nsMatches[0]
			logx.Log("Resolved namespace to %s (CAPI Cluster %s).",
				cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
		}
		if err := shell.Run("kubectl", "--context", kctx, "get", "secret",
			cfg.WorkloadClusterName+"-kubeconfig", "-n", cfg.WorkloadClusterNamespace); err == nil {
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
	out2, _, _ := shell.Capture(
		"kubectl", "--context", kctx, "get", "cluster", "-A",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}\t{.metadata.name}\n{end}",
	)
	var cands []string
	for _, ln := range strings.Split(out2, "\n") {
		parts := strings.SplitN(ln, "\t", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		if err := shell.Run("kubectl", "--context", kctx,
			"get", "secret", parts[1]+"-kubeconfig", "-n", parts[0]); err == nil {
			cands = append(cands, parts[0]+"/"+parts[1])
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
		port = "8080"
	}
	wlPFAddr := "127.0.0.1:" + port
	loginExtra := "--grpc-web"
	if sysinfo.IsTrue(cfg.ArgoCDServerInsecure) {
		loginExtra = "--insecure --grpc-web"
	}
	fmt.Println("\n\033[1;36m── Argo CD access (initial admin; rotate after first login) — provisioned cluster only ──\033[0m")
	fmt.Printf("\n\033[1;33m[CAPI / Proxmox workload] cluster %s / Argo namespace %s\033[0m\n",
		cfg.WorkloadClusterName, cfg.WorkloadArgoCDNamespace)

	if err := shell.Run("kubectl", "--context", kctx, "get", "secret",
		cfg.WorkloadClusterName+"-kubeconfig", "-n", cfg.WorkloadClusterNamespace); err != nil {
		logx.Warn("Secret %s/%s-kubeconfig not found. Use --workload-cluster-name / --workload-cluster-namespace (or env), CAPI_MANIFEST, or wait until CAPI creates this Secret in the Cluster's namespace.",
			cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
		if err := shell.Run("kubectl", "--context", kctx, "get", "cluster", "-A"); err == nil {
			out, _, _ := shell.Capture("kubectl", "--context", kctx,
				"get", "cluster", "-A",
				"-o", "custom-columns=NS:.metadata.namespace,NAME:.metadata.name", "--no-headers")
			logx.Log("  CAPI Clusters (management): %s", strings.ReplaceAll(strings.TrimSpace(out), "\n", " "))
		}
		return
	}
	kcfg, err := writeWorkloadKubeconfig(cfg, kctx)
	if err == nil {
		defer os.Remove(kcfg)
		if err := runWith("KUBECONFIG", kcfg, "kubectl", "get", "namespace", cfg.WorkloadArgoCDNamespace); err == nil {
			pw := ReadInitialAdminPasswordWithKubeconfig(kcfg, cfg.WorkloadArgoCDNamespace)
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
	fmt.Printf("  Write kubeconfig and port-forward (local port matches ARGOCD_PORT_FORWARD_PORT, default %s):\n", port)
	fmt.Printf("    kubectl --context \"%s\" -n \"%s\" get secret \"%s-kubeconfig\" -o jsonpath={.data.value} | base64 -d > /tmp/%s-kubeconfig.yaml\n",
		kctx, cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, cfg.WorkloadClusterName)
	fmt.Printf("    export KUBECONFIG=/tmp/%s-kubeconfig.yaml\n", cfg.WorkloadClusterName)
	fmt.Printf("    kubectl port-forward -n \"%s\" svc/argocd-server %s:443\n", cfg.WorkloadArgoCDNamespace, wlPFAddr)
	fmt.Printf("  Login to Argo on the CAPI cluster:\n")
	fmt.Printf("    argocd login %s --username admin --password '<password>' %s\n\n", wlPFAddr, loginExtra)
}

// ReadInitialAdminPasswordWithKubeconfig reads the admin password using
// KUBECONFIG env override (for the workload cluster).  Probes, in order:
//   - argocd-initial-admin-secret / password   (Helm chart default)
//   - argocd-cluster / admin.password          (Argo CD Operator)
//   - argocd-secret / admin.password           (fallback: only if plaintext, not bcrypt)
func ReadInitialAdminPasswordWithKubeconfig(kubeconfig, namespace string) string {
	if namespace == "" {
		namespace = "argocd"
	}
	for _, probe := range []struct{ sec, key string }{
		{"argocd-initial-admin-secret", "password"},
		{"argocd-cluster", `admin\.password`},
		{"argocd-secret", `admin\.password`},
	} {
		c := exec.Command("kubectl", "get", "secret", probe.sec,
			"-n", namespace, "-o", "jsonpath={.data."+probe.key+"}")
		c.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		out, err := c.Output()
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(out))
		if s == "" {
			continue
		}
		dec, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(dec) == 0 {
			continue
		}
		// argocd-secret stores a bcrypt hash — skip if it looks like one.
		if strings.HasPrefix(string(dec), "$2") {
			continue
		}
		return string(dec)
	}
	return ""
}

// RunPortForwards ports argocd_run_port_forwards. Blocks until the
// user interrupts; always removes the tmp kubeconfig.
func RunPortForwards(cfg *config.Config) {
	kctx := "kind-" + cfg.KindClusterName
	port := cfg.ArgoCDPortForwardPort
	if port == "" {
		port = "8080"
	}
	if cfg.ArgoCDPortForwardTarget != "" && cfg.ArgoCDPortForwardTarget != "workload" {
		logx.Warn("port-forward: only the provisioned cluster is supported (ARGOCD_PORT_FORWARD_TARGET=workload); ignoring %s.",
			cfg.ArgoCDPortForwardTarget)
	}
	if !contextExists(kctx) {
		logx.Die("port-forward: kubectl context %s not found (need management cluster to read %s-kubeconfig).",
			kctx, cfg.WorkloadClusterName)
	}
	if err := shell.Run("kubectl", "--context", kctx, "get", "secret",
		cfg.WorkloadClusterName+"-kubeconfig", "-n", cfg.WorkloadClusterNamespace); err != nil {
		logx.Die("port-forward: %s/%s-kubeconfig not found (is the CAPI cluster ready?).",
			cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
	}
	kcfg, err := writeWorkloadKubeconfig(cfg, kctx)
	if err != nil {
		logx.Die("port-forward: could not read workload kubeconfig")
	}
	if err := runWith("KUBECONFIG", kcfg, "kubectl", "get", "namespace", cfg.WorkloadArgoCDNamespace); err != nil {
		os.Remove(kcfg)
		logx.Die("port-forward: namespace %s not found on the CAPI cluster — is workload Argo installed?",
			cfg.WorkloadArgoCDNamespace)
	}
	cmd := exec.Command("kubectl", "port-forward", "-n", cfg.WorkloadArgoCDNamespace,
		"svc/argocd-server", "127.0.0.1:"+port+":443")
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
	ns := cfg.WorkloadArgoCDNamespace
	if ns == "" {
		ns = "argocd"
	}
	nsDoc := fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q}}`, ns)
	if err := shell.Pipe(nsDoc, "kubectl", "--kubeconfig", wk, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to ensure namespace %s on the workload for argocd-redis.", ns)
	}
	if err := shell.Run("kubectl", "--kubeconfig", wk, "-n", ns, "get", "secret", "argocd-redis"); err == nil {
		logx.Log("Workload %s/argocd-redis already present — not overwriting (idempotent bootstrap).", ns)
		return
	}
	pw := randomBase64(32)
	doc := fmt.Sprintf(`{"apiVersion":"v1","kind":"Secret","type":"Opaque","metadata":{"name":"argocd-redis","namespace":%q},"data":{"auth":%q}}`,
		ns, base64.StdEncoding.EncodeToString([]byte(pw)))
	if err := shell.Pipe(doc, "kubectl", "--kubeconfig", wk, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to create argocd-redis on the workload cluster.")
	}
	logx.Log("Created %s/argocd-redis on the workload (key auth) via kind/management bootstrap.", ns)
}

// --- helpers ---

func contextExists(ctx string) bool {
	out, _, _ := shell.Capture("kubectl", "config", "get-contexts", "-o", "name")
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if ln == ctx {
			return true
		}
	}
	return false
}

func writeWorkloadKubeconfig(cfg *config.Config, mgmtCtx string) (string, error) {
	out, _, _ := shell.Capture(
		"kubectl", "--context", mgmtCtx,
		"-n", cfg.WorkloadClusterNamespace,
		"get", "secret", cfg.WorkloadClusterName+"-kubeconfig",
		"-o", "jsonpath={.data.value}",
	)
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("workload kubeconfig secret empty")
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "workload-kubeconfig-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(dec); err != nil {
		return "", err
	}
	_ = f.Chmod(0o600)
	return f.Name(), nil
}

func runWith(envKey, envVal string, argv ...string) error {
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = append(os.Environ(), envKey+"="+envVal)
	c.Stdout = nil
	c.Stderr = nil
	return c.Run()
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
