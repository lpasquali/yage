package pivot

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/k8sclient"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// InstallCAPIOnManagement runs `clusterctl init` against the management
// cluster kubeconfig with the same providers used on kind:
// capi-provider-proxmox, in-cluster IPAM, CAAPH (helm addon).
//
// Idempotent: probes capi-system, capmox-system, caaph-system on the
// mgmt cluster first; when all three exist, returns nil without re-init.
//
// Mirrors the kind-side init in internal/bootstrap/bootstrap.go phase 8.
func InstallCAPIOnManagement(cfg *config.Config, mgmtKubeconfig string) error {
	if !cfg.PivotEnabled {
		return nil
	}
	if mgmtKubeconfig == "" {
		return fmt.Errorf("InstallCAPIOnManagement: empty mgmt kubeconfig path")
	}

	cli, err := k8sclient.ForKubeconfigFile(mgmtKubeconfig)
	if err != nil {
		return fmt.Errorf("load mgmt kubeconfig: %w", err)
	}
	bg := context.Background()

	// Idempotency probe: any of the three provider namespaces present
	// means clusterctl init has already run on this cluster.
	have := func(ns string) bool {
		_, err := cli.Typed.CoreV1().Namespaces().Get(bg, ns, metav1.GetOptions{})
		return err == nil
	}
	capiPresent := have("capi-system")
	capmoxPresent := have("capmox-system")
	caaphPresent := have("caaph-system")
	if capiPresent && capmoxPresent && caaphPresent {
		logx.Log("CAPI providers already installed on management cluster (capi-system + capmox-system + caaph-system found); skipping clusterctl init.")
		return nil
	}

	clusterctlCfg := cfg.ClusterctlCfg
	if clusterctlCfg == "" {
		logx.Warn("ClusterctlCfg is empty; clusterctl init on mgmt will use defaults (set via SyncClusterctlConfigFile in the kind init phase).")
	}

	logx.Log("Initializing CAPI on management cluster (infrastructure=%s, ipam=%s, addon=helm)…",
		cfg.InfraProvider, cfg.IPAMProvider)

	args := []string{"clusterctl",
		"--kubeconfig", mgmtKubeconfig,
		"init",
		"--infrastructure", cfg.InfraProvider,
		"--ipam", cfg.IPAMProvider,
		"--addon", "helm",
	}
	if clusterctlCfg != "" {
		args = append(args, "--config", clusterctlCfg)
	}

	env := []string{
		"EXP_CLUSTER_RESOURCE_SET=" + boolStrOr(cfg.ExpClusterResourceSet, "false"),
		"CLUSTER_TOPOLOGY=" + boolStrOr(cfg.ClusterTopology, "true"),
		"EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION=" + boolStrOr(cfg.ExpKubeadmBootstrapFormatIgnition, "true"),
	}
	if err := shell.RunWithEnv(env, args...); err != nil {
		return fmt.Errorf("clusterctl init (mgmt) failed: %w", err)
	}

	// Wait for the core controllers to come Ready before returning.
	logx.Log("Waiting for core CAPI controllers on management cluster…")
	for _, d := range []struct{ ns, name string }{
		{"capi-system", "capi-controller-manager"},
		{"capi-kubeadm-bootstrap-system", "capi-kubeadm-bootstrap-controller-manager"},
		{"capi-kubeadm-control-plane-system", "capi-kubeadm-control-plane-controller-manager"},
	} {
		if err := waitDeploymentReady(cli, d.ns, d.name, 5*time.Minute); err != nil {
			return fmt.Errorf("%s did not become Available on mgmt: %w", d.name, err)
		}
	}
	return nil
}

// MoveCAPIState runs `clusterctl move --to-kubeconfig=<mgmt>` from the
// kind context to the new management cluster. Mirrors the bash CAPI
// pivot step. Wrapped in a single retry (the move is read-then-recreate
// across two clusters; transient API blips on either side make a single
// retry pay for itself).
//
// Stops kind-side reconcilers (capmox, CAAPH) before the move so they
// don't race the migration. Stop = scale Deployment replicas to 0 via
// merge patch on the kind context.
//
// Returns nil on success.
func MoveCAPIState(cfg *config.Config, mgmtKubeconfig string) error {
	if !cfg.PivotEnabled {
		return nil
	}
	if mgmtKubeconfig == "" {
		return fmt.Errorf("MoveCAPIState: empty mgmt kubeconfig path")
	}
	if !shell.CommandExists("clusterctl") {
		return fmt.Errorf("clusterctl not on PATH; install it before pivoting")
	}

	kindCtx := "kind-" + cfg.KindClusterName
	if !k8sclient.ContextExists(kindCtx) {
		return fmt.Errorf("kind context %s not found; cannot run clusterctl move", kindCtx)
	}

	// Materialize the merged kubeconfig to a temp file; clusterctl move
	// needs a file path. Same pattern as
	// internal/bootstrap/purge.go::WorkloadRolloutCAPITouchRollout.
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cc, err := rules.Load()
	if err != nil || cc == nil {
		return fmt.Errorf("load kubeconfig for clusterctl move: %w", err)
	}
	body, err := clientcmd.Write(*cc)
	if err != nil {
		return fmt.Errorf("serialise kubeconfig: %w", err)
	}
	kindKcfg, cleanup, err := k8sclient.WriteTempKubeconfig("kind-clusterctl-move", body)
	if err != nil {
		return fmt.Errorf("write tmp kubeconfig: %w", err)
	}
	defer cleanup()

	// Pause reconciliation on kind (best-effort).
	if err := scaleDeploymentsToZero(kindCtx); err != nil {
		logx.Warn("Could not pause kind-side reconcilers before move: %v (continuing with move; clusterctl pauses Clusters)", err)
	}

	// Discover namespaces that hold CAPI resources to move. By default
	// `clusterctl move` operates on a single namespace at a time; we
	// run it for the workload + mgmt namespaces so both cluster
	// objects + their dependants land on mgmt.
	namespaces := dedupe([]string{
		cfg.WorkloadClusterNamespace,
		cfg.MgmtClusterNamespace,
		cfg.ProxmoxBootstrapSecretNamespace,
	})

	for _, ns := range namespaces {
		if ns == "" {
			continue
		}
		args := []string{"clusterctl",
			"--kubeconfig", kindKcfg,
			"--kubeconfig-context", kindCtx,
			"move",
			"--to-kubeconfig", mgmtKubeconfig,
			"-n", ns,
		}
		if cfg.PivotDryRun {
			args = append(args, "--dry-run")
			logx.Log("clusterctl move --dry-run: namespace=%s (logging plan only — no state will move)", ns)
			if err := shell.Run(args...); err != nil {
				return fmt.Errorf("clusterctl move --dry-run (ns=%s): %w", ns, err)
			}
			continue
		}
		var lastErr error
		for attempt := 1; attempt <= 2; attempt++ {
			logx.Log("clusterctl move: namespace=%s attempt %d/2", ns, attempt)
			if err := shell.Run(args...); err == nil {
				lastErr = nil
				break
			} else {
				lastErr = err
				logx.Warn("clusterctl move (ns=%s) attempt %d failed: %v", ns, attempt, err)
				if attempt < 2 {
					time.Sleep(10 * time.Second)
				}
			}
		}
		if lastErr != nil {
			return fmt.Errorf("clusterctl move (ns=%s) failed after retries: %w", ns, lastErr)
		}
	}
	return nil
}

// scaleDeploymentsToZero stops the kind-side capmox + CAAPH controllers
// before clusterctl move runs. The same controllers come up again on
// the mgmt cluster from InstallCAPIOnManagement; pausing them on kind
// avoids races with the move.
func scaleDeploymentsToZero(kindCtx string) error {
	cli, err := k8sclient.ForContext(kindCtx)
	if err != nil {
		return err
	}
	bg := context.Background()
	targets := []struct{ ns, name string }{
		{"capmox-system", "capmox-controller-manager"},
		{"caaph-system", "caaph-controller-manager"},
	}
	for _, t := range targets {
		_, err := cli.Typed.AppsV1().Deployments(t.ns).Get(bg, t.name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			logx.Warn("scale-to-zero: lookup %s/%s: %v", t.ns, t.name, err)
			continue
		}
		body := []byte(`{"spec":{"replicas":0}}`)
		_, err = cli.Typed.AppsV1().Deployments(t.ns).Patch(
			bg, t.name, "application/strategic-merge-patch+json", body, metav1.PatchOptions{},
		)
		if err != nil {
			logx.Warn("scale-to-zero: patch %s/%s: %v", t.ns, t.name, err)
			continue
		}
		logx.Log("Paused kind-side reconciler %s/%s (replicas=0) before clusterctl move.", t.ns, t.name)
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func boolStrOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

