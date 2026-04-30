// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

import (
	"context"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/platform/shell"
)

// kindClusterExists is the Go equivalent of
// `kind get clusters | contains_line "$cname"`.
func kindClusterExists(name string) bool {
	if !shell.CommandExists("kind") {
		return false
	}
	out, _, _ := shell.Capture("kind", "get", "clusters")
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if strings.TrimSpace(ln) == name {
			return true
		}
	}
	return false
}

// writeWorkloadKubeconfig fetches the workload cluster's kubeconfig out
// of the Cluster's CAPI-managed Secret (<name>-kubeconfig on kind) and
// writes the decoded body to a tmp file. The file is readable by the
// current user only; callers should delete it when done.
func writeWorkloadKubeconfig(cfg *config.Config, kctx string) (string, error) {
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return "", err
	}
	sec, err := cli.Typed.CoreV1().Secrets(cfg.WorkloadClusterNamespace).
		Get(context.Background(), cfg.WorkloadClusterName+"-kubeconfig", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	body := sec.Data["value"]
	if len(body) == 0 {
		return "", os.ErrNotExist
	}
	f, err := os.CreateTemp("", "workload-kubeconfig-")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// removeFile is a small defer-friendly wrapper.
func removeFile(p string) { _ = os.Remove(p) }

// applyBootstrapConfigToManagementCluster keeps the internal (package-
// private) call-site used by SyncBootstrapConfigToKind. It just forwards
// to the ported ApplyBootstrapConfigToManagementCluster.
func applyBootstrapConfigToManagementCluster(cfg *config.Config, _ string) error {
	return ApplyBootstrapConfigToManagementCluster(cfg)
}