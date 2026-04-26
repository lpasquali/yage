// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package proxmox

import (
	"errors"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// TestPivotTarget_Disabled confirms ErrNotApplicable when
// PIVOT_ENABLED=false (the dominant case today).
func TestPivotTarget_Disabled(t *testing.T) {
	p := &Provider{}
	cfg := &config.Config{}
	cfg.PivotEnabled = false
	_, err := p.PivotTarget(cfg)
	if !errors.Is(err, provider.ErrNotApplicable) {
		t.Fatalf("got err=%v, want ErrNotApplicable", err)
	}
}

// TestPivotTarget_MissingKubeconfig confirms an explicit error when
// pivot is on but the orchestrator hasn't populated
// cfg.MgmtKubeconfigPath yet (per §13.4 #5).
func TestPivotTarget_MissingKubeconfig(t *testing.T) {
	p := &Provider{}
	cfg := &config.Config{}
	cfg.PivotEnabled = true
	_, err := p.PivotTarget(cfg)
	if err == nil {
		t.Fatal("expected error when MgmtKubeconfigPath is empty")
	}
	if errors.Is(err, provider.ErrNotApplicable) {
		t.Fatalf("got ErrNotApplicable, want a real error explaining the missing path")
	}
}

// TestPivotTarget_Ready confirms the happy path: pivot enabled +
// kubeconfig path populated → real target.
func TestPivotTarget_Ready(t *testing.T) {
	p := &Provider{}
	cfg := &config.Config{}
	cfg.PivotEnabled = true
	cfg.MgmtKubeconfigPath = "/tmp/mgmt.kubeconfig"
	target, err := p.PivotTarget(cfg)
	if err != nil {
		t.Fatalf("PivotTarget: %v", err)
	}
	if target.KubeconfigPath != "/tmp/mgmt.kubeconfig" {
		t.Errorf("KubeconfigPath: got %q, want /tmp/mgmt.kubeconfig", target.KubeconfigPath)
	}
	if target.Namespaces != nil {
		t.Errorf("Namespaces: got %v, want nil (sentinel for 'all CAPI namespaces')", target.Namespaces)
	}
	if target.ReadyTimeout == 0 {
		t.Errorf("ReadyTimeout zero — should default to 10m")
	}
}