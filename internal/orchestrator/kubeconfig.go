// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package orchestrator

import (
	"os"
	"os/exec"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/shell"
)

// WorkloadKubeconfigFromClusterctl ports
// _workload_kubeconfig_file_from_clusterctl (L6028-L6041). Fallback when
// the CAPI *-kubeconfig Secret is missing: run `clusterctl get
// kubeconfig` against the management cluster.
//
// Returns the tmp kubeconfig path or "" on failure.
func WorkloadKubeconfigFromClusterctl(cfg *config.Config) string {
	if !shell.CommandExists("clusterctl") {
		return ""
	}
	// BOOTSTRAP_CLUSTERCTL_CONFIG_PATH is maintained by
	// SyncClusterctlConfigFile; we don't track it in cfg, so use the
	// default clusterctl lookup (empty arg), which probes the kubeconfig
	// the current context already uses.
	f, err := os.CreateTemp("", "workload-kubeconfig-")
	if err != nil {
		return ""
	}
	defer f.Close()
	cmd := exec.Command("clusterctl", "get", "kubeconfig",
		cfg.WorkloadClusterName,
		"--namespace", cfg.WorkloadClusterNamespace)
	cmd.Stdout = f
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		os.Remove(f.Name())
		return ""
	}
	fi, err := os.Stat(f.Name())
	if err != nil || fi.Size() == 0 {
		os.Remove(f.Name())
		return ""
	}
	return f.Name()
}