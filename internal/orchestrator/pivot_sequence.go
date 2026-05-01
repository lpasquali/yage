// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// postPivotDeps holds the function dependencies for runPostPivotSequence.
// Production code wires these to the real package-level functions; tests
// inject recorders to assert call order.
type postPivotDeps struct {
	moveCAPIState    func(cfg *config.Config, mgmtKubeconfig string) error
	handoff          func(cfg *config.Config, kindCtx, mgmtKubeconfig string) (kindsync.HandoffResult, error)
	mgmtClient       func(mgmtKubeconfig string) (*k8sclient.Client, error)
	ensureYageSystem func(ctx context.Context, cli *k8sclient.Client) error
	// prepareCSI fires LoadVarsFromConfig and sets cfg.Providers.Proxmox.CSIURL when
	// empty — pure cfg mutation, no I/O in tests.
	prepareCSI   func(cfg *config.Config)
	csiDrivers   func(cfg *config.Config) []csi.Driver
	ensureRepoSync func(ctx context.Context, cli *k8sclient.Client, cfg *config.Config) error
	verifyParity func(cfg *config.Config, mgmtKubeconfig string) error
	rebind       func(cfg *config.Config, mgmtKubeconfig string) error
}

// runPostPivotSequence executes the ADR 0011 §7 post-pivot phase sequence:
//
//	MoveCAPIState → HandOff → EnsureYageSystemOnCluster
//	  → CSI EnsureManagementInstall → EnsureRepoSync → VerifyParity → rebind
//
// When dryRun is true, only MoveCAPIState runs and the function returns nil;
// the caller is responsible for printing the dry-run message and returning
// early. Handoff failure is logged as a warning and does not abort the
// sequence. All other step failures cause an immediate return of a descriptive
// error; the caller must treat that error as fatal (logx.Die).
func runPostPivotSequence(ctx context.Context, cfg *config.Config, mgmtKubeconfig string, deps postPivotDeps, dryRun bool) error {
	if err := deps.moveCAPIState(cfg, mgmtKubeconfig); err != nil {
		return fmt.Errorf("MoveCAPIState: %w", err)
	}
	if dryRun {
		return nil
	}

	hr, err := deps.handoff(cfg, "kind-"+cfg.KindClusterName, mgmtKubeconfig)
	if err != nil {
		logx.Warn("pivot: handoff Secrets returned error after %d named + %d labeled copies: %v",
			hr.NamedCopied, hr.LabelCopied, err)
	} else {
		logx.Log("pivot: handoff complete (%d named + %d labeled Secrets copied to mgmt cluster).",
			hr.NamedCopied, hr.LabelCopied)
	}

	pivotMgmtCli, pivotCliErr := deps.mgmtClient(mgmtKubeconfig)
	if pivotCliErr != nil {
		return fmt.Errorf("kube client for mgmt cluster: %w", pivotCliErr)
	}

	if err := deps.ensureYageSystem(ctx, pivotMgmtCli); err != nil {
		return fmt.Errorf("EnsureYageSystemOnCluster on management cluster: %w", err)
	}

	deps.prepareCSI(cfg)
	for _, d := range deps.csiDrivers(cfg) {
		if merr := d.EnsureManagementInstall(cfg, mgmtKubeconfig); merr != nil && !errors.Is(merr, csi.ErrNotApplicable) {
			return fmt.Errorf("EnsureManagementInstall (%s): %w", d.Name(), merr)
		}
	}

	if err := deps.ensureRepoSync(ctx, pivotMgmtCli, cfg); err != nil {
		return fmt.Errorf("EnsureRepoSync on management cluster: %w", err)
	}

	if err := deps.verifyParity(cfg, mgmtKubeconfig); err != nil {
		return fmt.Errorf("VerifyParity: %w", err)
	}

	if err := deps.rebind(cfg, mgmtKubeconfig); err != nil {
		return fmt.Errorf("rebind kind-%s context to mgmt kubeconfig: %w", cfg.KindClusterName, err)
	}
	return nil
}
