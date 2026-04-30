// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/platform/kubectl"
	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// SyncBootstrapConfigToKind syncs the current cfg snapshot into the
// kind management cluster as a Secret. Requires kubectl + kind on
// PATH and the resolved kind context to match an existing kind
// cluster.
func SyncBootstrapConfigToKind(cfg *config.Config) error {
	if !shell.CommandExists("kind") {
		return nil
	}
	ctx, ok := kubectl.ResolveBootstrapContext(cfg)
	if !ok {
		return nil
	}
	name := strings.TrimPrefix(ctx, "kind-")
	if !kindClusterExists(name) {
		return nil
	}
	return applyBootstrapConfigToManagementCluster(cfg, ctx)
}

// RolloutRestartCapmoxController triggers a rollout-restart of the
// capmox-controller-manager Deployment. Best-effort: if the deployment
// is not ready, warn and continue rather than failing.
func RolloutRestartCapmoxController(cfg *config.Config) {
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		logx.Warn("capmox-controller-manager restart skipped or not ready (check capmox-system).")
		return
	}
	if err := rolloutRestartDeployment(cli, "capmox-system", "capmox-controller-manager"); err != nil {
		logx.Warn("capmox-controller-manager restart skipped or not ready (check capmox-system).")
		return
	}
	if err := waitDeploymentReady(cli, "capmox-system", "capmox-controller-manager", 180*time.Second); err != nil {
		logx.Warn("capmox-controller-manager restart skipped or not ready (check capmox-system).")
	}
}

// RolloutRestartProxmoxCSIOnWorkload restarts proxmox-csi-plugin-
// controller in the CSI namespace on the workload cluster. Fetches
// the workload cluster's kubeconfig from the capi Secret on kind,
// builds an in-process client against it, and triggers the rollout.
// No-op when the kubeconfig secret or the target deployment are
// missing.
func RolloutRestartProxmoxCSIOnWorkload(cfg *config.Config) {
	kctx := "kind-" + cfg.KindClusterName
	kcfg, err := writeWorkloadKubeconfig(cfg, kctx)
	if err != nil {
		logx.Warn("No workload kubeconfig — skip Proxmox CSI controller restart on workload.")
		return
	}
	defer removeFile(kcfg)

	cli, err := k8sclient.ForKubeconfigFile(kcfg)
	if err != nil {
		logx.Warn("Failed to load workload kubeconfig — skip Proxmox CSI controller restart.")
		return
	}
	ns := cfg.Providers.Proxmox.CSINamespace
	bg := context.Background()
	if _, err := cli.Typed.AppsV1().Deployments(ns).Get(bg, "proxmox-csi-plugin-controller", metav1.GetOptions{}); err != nil {
		logx.Warn("proxmox-csi controller deployment not found in %s — skip restart.", ns)
		return
	}
	_ = rolloutRestartDeployment(cli, ns, "proxmox-csi-plugin-controller")
	_ = waitDeploymentReady(cli, ns, "proxmox-csi-plugin-controller", 300*time.Second)
	logx.Log("Restarted Proxmox CSI controller on workload %s.", cfg.WorkloadClusterName)
}

// rolloutRestartDeployment mirrors `kubectl rollout restart deploy/X` —
// patches the spec.template.metadata.annotations[kubectl.kubernetes.io/restartedAt]
// with the current RFC3339 timestamp; the deployment controller picks
// that up as a pod-template change and rolls.
func rolloutRestartDeployment(cli *k8sclient.Client, ns, name string) error {
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`,
		time.Now().Format(time.RFC3339),
	)
	_, err := cli.Typed.AppsV1().Deployments(ns).Patch(
		context.Background(), name, types.StrategicMergePatchType,
		[]byte(patch), metav1.PatchOptions{},
	)
	return err
}

// waitDeploymentReady mirrors `kubectl rollout status deploy/X --timeout=...`.
// Polls the Deployment status until updated/ready replicas match the
// spec or timeout elapses.
func waitDeploymentReady(cli *k8sclient.Client, ns, name string, timeout time.Duration) error {
	return k8sclient.PollUntil(context.Background(), 2*time.Second, timeout,
		func(ctx context.Context) (bool, error) {
			d, err := cli.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, nil
			}
			if d.Generation > d.Status.ObservedGeneration {
				return false, nil
			}
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			if d.Status.UpdatedReplicas < desired {
				return false, nil
			}
			if d.Status.AvailableReplicas < desired {
				return false, nil
			}
			return true, nil
		})
}