// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Phase E: PivotTarget tests for the Proxmox provider.
//
// The Phase E refactor moved PivotTarget from state.go into pivot.go and
// changed its contract:
//   - Proxmox always returns a real (non-ErrNotApplicable) target because
//     it always supports the pivot story. The orchestrator's "does this
//     provider implement PivotTarget?" probe is inside `if cfg.Pivot.Enabled`
//     so it is never reached when pivot is disabled — the provider doesn't
//     need to encode that runtime flag.
//   - When cfg.MgmtKubeconfigPath is empty (probe time — the mgmt cluster
//     hasn't been provisioned yet), PivotTarget returns a target with an
//     empty KubeconfigPath rather than an error. The orchestrator uses the
//     empty-or-not-empty of the path to know whether the cluster is ready.
//   - Namespaces is a real list (workload + mgmt + bootstrap namespace)
//     rather than nil. nil was the old "all CAPI namespaces" sentinel; the
//     new design has the provider own the namespace list explicitly.
package proxmox

import (
	"errors"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// TestPivotTarget_NeverErrNotApplicable confirms that the Proxmox
// provider always returns a real PivotTarget — not ErrNotApplicable.
// Proxmox is the canonical pivot provider; returning ErrNotApplicable
// would incorrectly tell the orchestrator to skip the pivot phase.
func TestPivotTarget_NeverErrNotApplicable(t *testing.T) {
	p := &Provider{}
	cfg := &config.Config{}
	cfg.Pivot.Enabled = false // even when pivot is disabled at runtime
	_, err := p.PivotTarget(cfg)
	if errors.Is(err, provider.ErrNotApplicable) {
		t.Fatal("Proxmox PivotTarget returned ErrNotApplicable; Proxmox always supports pivot")
	}
	// nil or any other non-ErrNotApplicable error is acceptable here.
}

// TestPivotTarget_ProbeBeforeManagementCluster confirms that PivotTarget
// does NOT error when cfg.MgmtKubeconfigPath is empty (probe time).
// The orchestrator probes PivotTarget before calling EnsureManagementCluster
// to discover whether the provider supports pivot; at that point the
// kubeconfig doesn't exist yet.
func TestPivotTarget_ProbeBeforeManagementCluster(t *testing.T) {
	p := &Provider{}
	cfg := &config.Config{}
	cfg.Pivot.Enabled = true
	// MgmtKubeconfigPath is intentionally empty.
	target, err := p.PivotTarget(cfg)
	if err != nil {
		t.Fatalf("PivotTarget should not error at probe time; got: %v", err)
	}
	if target.KubeconfigPath != "" {
		t.Errorf("KubeconfigPath should be empty at probe time; got %q", target.KubeconfigPath)
	}
	// Namespaces and VerifySecrets may be populated from cfg defaults.
}

// TestPivotTarget_Ready confirms the happy path: pivot enabled +
// kubeconfig path populated → real target with correct fields.
func TestPivotTarget_Ready(t *testing.T) {
	p := &Provider{}
	cfg := &config.Config{}
	cfg.Pivot.Enabled = true
	cfg.MgmtKubeconfigPath = "/tmp/mgmt.kubeconfig"
	cfg.WorkloadClusterNamespace = "default"
	cfg.Mgmt.ClusterNamespace = "mgmt-ns"
	cfg.Providers.Proxmox.BootstrapSecretNamespace = "yage-system"
	target, err := p.PivotTarget(cfg)
	if err != nil {
		t.Fatalf("PivotTarget: %v", err)
	}
	if target.KubeconfigPath != "/tmp/mgmt.kubeconfig" {
		t.Errorf("KubeconfigPath: got %q, want /tmp/mgmt.kubeconfig", target.KubeconfigPath)
	}
	// Namespaces must include the workload, mgmt, and bootstrap namespaces.
	nsSet := map[string]bool{}
	for _, ns := range target.Namespaces {
		nsSet[ns] = true
	}
	for _, want := range []string{"default", "mgmt-ns", "yage-system"} {
		if !nsSet[want] {
			t.Errorf("Namespaces missing %q; got %v", want, target.Namespaces)
		}
	}
	if target.ReadyTimeout == 0 {
		t.Errorf("ReadyTimeout zero — should default to 10m")
	}
	// VerifySecrets must be populated with the four Proxmox bootstrap Secrets.
	if len(target.VerifySecrets) == 0 {
		t.Error("VerifySecrets empty — Proxmox provider must supply the bootstrap Secret list")
	}
	for _, vs := range target.VerifySecrets {
		if vs.Namespace == "" || vs.Name == "" {
			t.Errorf("VerifySecret has empty Namespace or Name: %+v", vs)
		}
	}
}