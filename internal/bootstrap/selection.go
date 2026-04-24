package bootstrap

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/promptx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// MaybeInteractiveSelectKindCluster ports
// maybe_interactive_select_kind_cluster (L4547-L4646). Offers:
//
//  1. Use the kubectl current-context when it's a kind-* name matching
//     an existing kind cluster.
//  2. When no kind clusters are listed but the kubeconfig has a kind-*
//     context that responds, offer to use that.
//  3. Otherwise print a numbered menu.
//
// cfg.Force skips the picker entirely. Non-interactive sessions get
// narrowed logic (the bash function has the same branches).
func MaybeInteractiveSelectKindCluster(cfg *config.Config) {
	if cfg.Force {
		return
	}
	if !shell.CommandExists("kind") || !shell.CommandExists("kubectl") {
		return
	}
	curCtx, _, _ := shell.Capture("kubectl", "config", "current-context")
	curCtx = strings.TrimSpace(curCtx)
	raw, _, _ := shell.Capture("kind", "get", "clusters")
	var names []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}

	if !promptx.CanPrompt() {
		switch {
		case len(names) == 1:
			cfg.KindClusterName = names[0]
			logx.Log("Non-interactive session: using the only kind cluster on this host ('%s').", cfg.KindClusterName)
		case len(names) > 1 && strings.HasPrefix(curCtx, "kind-"):
			fromCtx := strings.TrimPrefix(curCtx, "kind-")
			if containsString(names, fromCtx) {
				cfg.KindClusterName = fromCtx
				logx.Log("Non-interactive session: kubectl context %s matches an existing kind cluster — using '%s'.", curCtx, cfg.KindClusterName)
			} else {
				logx.Warn("Non-interactive session: multiple kind clusters (%s); kubectl context is %s. Set KIND_CLUSTER_NAME or --kind-cluster-name (default '%s' may create a second cluster).",
					strings.Join(names, " "), curCtx, cfg.KindClusterName)
			}
		case len(names) > 1:
			logx.Warn("Non-interactive session: multiple kind clusters (%s). Set KIND_CLUSTER_NAME or --kind-cluster-name, or run in a real terminal for the interactive picker.",
				strings.Join(names, " "))
		}
		return
	}

	// 1. current-context matches a real kind cluster.
	if strings.HasPrefix(curCtx, "kind-") {
		fromCtx := strings.TrimPrefix(curCtx, "kind-")
		if containsString(names, fromCtx) {
			if fromCtx != cfg.KindClusterName {
				fmt.Fprintf(os.Stderr, "\n\033[1;36mkubectl\033[0m current-context is \033[1m%s\033[0m (kind cluster \033[1m%s\033[0m).\n", curCtx, fromCtx)
				fmt.Fprintf(os.Stderr, "\033[1;33m[?]\033[0m Use it for this run instead of creating or switching to KIND_CLUSTER_NAME=%s? [Y/n]: ", cfg.KindClusterName)
				resp := promptx.ReadLine()
				if resp == "" || resp[0] == 'Y' || resp[0] == 'y' {
					cfg.KindClusterName = fromCtx
					logx.Log("Using kind cluster '%s' from kubectl current-context.", cfg.KindClusterName)
					return
				}
			} else {
				logx.Log("Using kind cluster '%s' from kubectl current-context.", cfg.KindClusterName)
				return
			}
		}
	}

	// 2. no kind clusters but kubeconfig points at kind-*
	if len(names) == 0 && strings.HasPrefix(curCtx, "kind-") {
		fromCtx := strings.TrimPrefix(curCtx, "kind-")
		if err := shell.Run("kubectl", "cluster-info", "--request-timeout=5s"); err == nil {
			fmt.Fprintf(os.Stderr, "\n\033[1;33m[?]\033[0m No clusters reported by 'kind get clusters', but kubectl context is \033[1m%s\033[0m and the API answers.\n", curCtx)
			fmt.Fprintf(os.Stderr, "    Use kind cluster '%s' for updates instead of KIND_CLUSTER_NAME=%s? [Y/n]: ", fromCtx, cfg.KindClusterName)
			resp := promptx.ReadLine()
			if resp == "" || resp[0] == 'Y' || resp[0] == 'y' {
				cfg.KindClusterName = fromCtx
				logx.Log("Using kind cluster '%s' from kubeconfig (cluster reachable).", cfg.KindClusterName)
				return
			}
		}
		return
	}

	if len(names) == 0 {
		return
	}

	// 3. menu
	fmt.Fprintln(os.Stderr, "\n\033[1;36mExisting kind cluster(s) on this machine:\033[0m")
	for i, n := range names {
		fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, n)
	}
	if strings.HasPrefix(curCtx, "kind-") {
		fmt.Fprintf(os.Stderr, "  (kubectl context: %s)\n", curCtx)
	}
	if len(names) == 1 {
		fmt.Fprintf(os.Stderr, "\033[1;33m[?]\033[0m Enter \033[1m1\033[0m to use that cluster, or press Enter to keep KIND_CLUSTER_NAME=%s (a new cluster may be created): ", cfg.KindClusterName)
	} else {
		fmt.Fprintf(os.Stderr, "\033[1;33m[?]\033[0m Enter a number from \033[1m1\033[0m to \033[1m%d\033[0m to use that cluster, or press Enter to keep KIND_CLUSTER_NAME=%s (a new cluster may be created): ", len(names), cfg.KindClusterName)
	}
	choice := promptx.NormalizeNumericMenuChoice(promptx.ReadLine(), len(names))
	if choice != "" {
		if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(names) {
			cfg.KindClusterName = names[n-1]
			logx.Log("Using kind cluster '%s' (selected from existing clusters).", cfg.KindClusterName)
			return
		}
	}
	logx.Log("Keeping KIND_CLUSTER_NAME='%s' (no existing cluster selected).", cfg.KindClusterName)
}

// MaybeInteractiveSelectWorkloadCluster ports
// maybe_interactive_select_workload_cluster_from_management (L4478-L4544).
// Lists CAPI Cluster resources on the management cluster; on multiple
// matches offers a numbered picker; on exactly one, either auto-picks
// (interactive) or auto-uses (non-interactive) that Cluster.
func MaybeInteractiveSelectWorkloadCluster(cfg *config.Config) {
	if cfg.Force {
		return
	}
	ctx := "kind-" + cfg.KindClusterName
	if !contextExists(ctx) {
		return
	}
	if err := shell.Run("kubectl", "--context", ctx, "get", "crd", "clusters.cluster.x-k8s.io"); err != nil {
		return
	}
	raw, _, _ := shell.Capture(
		"kubectl", "--context", ctx, "get", "clusters.cluster.x-k8s.io", "-A",
		"-o", "jsonpath={range .items[*]}{.metadata.namespace}\t{.metadata.name}\n{end}",
	)
	if strings.TrimSpace(raw) == "" {
		return
	}
	var cNS, cName []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		ns := parts[0]
		if ns == "" {
			ns = "default"
		}
		cNS = append(cNS, ns)
		cName = append(cName, parts[1])
	}
	if len(cName) == 0 {
		return
	}

	if !promptx.CanPrompt() {
		switch {
		case len(cName) == 1:
			cfg.WorkloadClusterNamespace = cNS[0]
			cfg.WorkloadClusterName = cName[0]
			RefreshDefaultCAPIManifestPath(cfg)
			logx.Log("Non-interactive session: using the only Cluster '%s/%s' on %s.",
				cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, ctx)
		case len(cName) > 1:
			matched := false
			for i := range cName {
				if cName[i] == cfg.WorkloadClusterName && cNS[i] == cfg.WorkloadClusterNamespace {
					matched = true
					break
				}
			}
			if matched {
				logx.Log("Non-interactive session: WORKLOAD_CLUSTER_NAME/namespace match an existing Cluster; keeping '%s/%s'.",
					cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName)
			} else {
				var sb strings.Builder
				for i := range cName {
					fmt.Fprintf(&sb, "%s/%s ", cNS[i], cName[i])
				}
				logx.Warn("Non-interactive session: Cluster API Clusters on %s: %s. Set WORKLOAD_CLUSTER_NAME and WORKLOAD_CLUSTER_NAMESPACE to match one, or use a terminal for the picker.",
					ctx, strings.TrimSpace(sb.String()))
			}
		}
		return
	}

	fmt.Fprintf(os.Stderr, "\n\033[1;36mExisting Cluster API workload Cluster(s) on %s:\033[0m\n", ctx)
	for i := range cName {
		fmt.Fprintf(os.Stderr, "  %d) namespace \033[1m%s\033[0m  cluster \033[1m%s\033[0m\n", i+1, cNS[i], cName[i])
	}
	currentNS := cfg.WorkloadClusterNamespace
	if currentNS == "" {
		currentNS = "default"
	}
	if len(cName) == 1 {
		fmt.Fprintf(os.Stderr, "\033[1;33m[?]\033[0m Enter \033[1m1\033[0m to reuse that cluster (updates manifest path), or press Enter to keep WORKLOAD_CLUSTER_NAME=\033[1m%s\033[0m namespace=\033[1m%s\033[0m: ",
			cfg.WorkloadClusterName, currentNS)
	} else {
		fmt.Fprintf(os.Stderr, "\033[1;33m[?]\033[0m Enter a number from \033[1m1\033[0m to \033[1m%d\033[0m to reuse that cluster (updates manifest path), or press Enter to keep WORKLOAD_CLUSTER_NAME=\033[1m%s\033[0m namespace=\033[1m%s\033[0m: ",
			len(cName), cfg.WorkloadClusterName, currentNS)
	}
	choice := promptx.NormalizeNumericMenuChoice(promptx.ReadLine(), len(cName))
	if choice != "" {
		if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(cName) {
			cfg.WorkloadClusterNamespace = cNS[n-1]
			cfg.WorkloadClusterName = cName[n-1]
			RefreshDefaultCAPIManifestPath(cfg)
			logx.Log("Using existing Cluster '%s/%s' from %s.",
				cfg.WorkloadClusterNamespace, cfg.WorkloadClusterName, ctx)
			return
		}
	}
	logx.Log("Keeping WORKLOAD_CLUSTER_NAME='%s' namespace='%s' (no Cluster selected from API).",
		cfg.WorkloadClusterName, currentNS)
}

func containsString(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}
