// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package orchestrator

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/plan"

	// Provider self-registrations for tests.
	_ "github.com/lpasquali/yage/internal/provider/aws"
	_ "github.com/lpasquali/yage/internal/provider/hetzner"
	_ "github.com/lpasquali/yage/internal/provider/linode"
	_ "github.com/lpasquali/yage/internal/provider/proxmox"
)

// TestPlanDelegation_Proxmox confirms the Proxmox provider's
// PlanDescriber hooks fire and emit the expected named sections
// (no bash phase numbers per §8).
func TestPlanDelegation_Proxmox(t *testing.T) {
	cfg := minCfg()
	cfg.InfraProvider = "proxmox"
	cw := plan.NewCapturingWriter()
	prov, err := provider.For(cfg)
	if err != nil {
		t.Fatalf("provider.For: %v", err)
	}
	prov.DescribeIdentity(cw, cfg)
	prov.DescribeWorkload(cw, cfg)
	prov.DescribePivot(cw, cfg)

	want := []string{
		"Identity bootstrap — Proxmox (OpenTofu)",
		"Workload Cluster — Proxmox",
		"Pivot to managed mgmt cluster — Proxmox",
	}
	got := cw.Sections()
	if !sliceEq(got, want) {
		t.Fatalf("Proxmox sections:\n got:  %v\n want: %v", got, want)
	}
}

// TestPlanDelegation_AWS confirms AWS dry-run no longer prints
// Proxmox phases. The minimum-bar (§13.4) DescribeWorkload prints
// cluster shape; DescribeIdentity is Skip-only; DescribePivot is
// also Skip-only because AWS has no PivotTarget yet.
func TestPlanDelegation_AWS(t *testing.T) {
	cfg := minCfg()
	cfg.InfraProvider = "aws"
	cfg.Providers.AWS.Region = "us-east-1"
	cfg.Providers.AWS.Mode = "unmanaged"
	cw := plan.NewCapturingWriter()
	prov, err := provider.For(cfg)
	if err != nil {
		t.Fatalf("provider.For: %v", err)
	}
	prov.DescribeIdentity(cw, cfg)
	prov.DescribeWorkload(cw, cfg)
	prov.DescribePivot(cw, cfg)

	wantSec := []string{
		"Identity bootstrap — AWS IAM",
		"Workload Cluster — AWS (mode: unmanaged)",
		"Pivot to managed mgmt cluster",
	}
	if !sliceEq(cw.Sections(), wantSec) {
		t.Fatalf("AWS sections:\n got:  %v\n want: %v", cw.Sections(), wantSec)
	}

	// The whole point of Phase B: AWS plan section TITLES must not
	// be Proxmox-shaped. (Skip lines may legitimately reference
	// Proxmox CSI to explain what AWS doesn't do — that's a feature.)
	for _, e := range cw.Events {
		if e.Kind == plan.EventSection && strings.Contains(e.Text, "Proxmox") {
			t.Fatalf("AWS plan contains Proxmox-shaped section: %q", e.Text)
		}
	}
}

// TestPlanDelegation_Hetzner confirms Hetzner has its own plan
// section (still simple — no pivot, no CSI from yage).
func TestPlanDelegation_Hetzner(t *testing.T) {
	cfg := minCfg()
	cfg.InfraProvider = "hetzner"
	cfg.Providers.Hetzner.Location = "fsn1"
	cw := plan.NewCapturingWriter()
	prov, err := provider.For(cfg)
	if err != nil {
		t.Fatalf("provider.For: %v", err)
	}
	prov.DescribeIdentity(cw, cfg)
	prov.DescribeWorkload(cw, cfg)
	prov.DescribePivot(cw, cfg)

	wantSec := []string{
		"Identity bootstrap — Hetzner Cloud",
		"Workload Cluster — Hetzner Cloud",
		"Pivot to managed mgmt cluster",
	}
	if !sliceEq(cw.Sections(), wantSec) {
		t.Fatalf("Hetzner sections:\n got:  %v\n want: %v", cw.Sections(), wantSec)
	}
}

// TestPlanDelegation_Linode confirms the Linode provider emits its
// own named sections (not Proxmox-shaped) and that the pivot section
// is a Skip (no PivotTarget yet).
func TestPlanDelegation_Linode(t *testing.T) {
	cfg := minCfg()
	cfg.InfraProvider = "linode"
	cfg.Providers.Linode.Region = "us-east"
	cfg.Providers.Linode.ControlPlaneType = "g6-standard-4"
	cfg.Providers.Linode.NodeType = "g6-standard-2"
	cw := plan.NewCapturingWriter()
	prov, err := provider.For(cfg)
	if err != nil {
		t.Fatalf("provider.For: %v", err)
	}
	prov.DescribeIdentity(cw, cfg)
	prov.DescribeWorkload(cw, cfg)
	prov.DescribePivot(cw, cfg)

	wantSec := []string{
		"Identity bootstrap — Linode",
		"Workload Cluster — Linode",
		"Pivot to managed mgmt cluster",
	}
	if !sliceEq(cw.Sections(), wantSec) {
		t.Fatalf("Linode sections:\n got:  %v\n want: %v", cw.Sections(), wantSec)
	}

	for _, e := range cw.Events {
		if e.Kind == plan.EventSection && strings.Contains(e.Text, "Proxmox") {
			t.Fatalf("Linode plan contains Proxmox-shaped section: %q", e.Text)
		}
	}
}

// TestPlanDelegation_AirgappedRefusesAWS confirms an airgapped
// orchestrator gets ErrAirgapped from provider.For for cloud
// providers — the dry-run path then surfaces a Skip line.
func TestPlanDelegation_AirgappedRefusesAWS(t *testing.T) {
	cfg := minCfg()
	cfg.InfraProvider = "aws"
	cfg.Airgapped = true
	_, err := provider.For(cfg)
	if err == nil {
		t.Fatal("expected ErrAirgapped, got nil")
	}
	// Don't import provider here just for the sentinel; substring is enough.
	if !strings.Contains(err.Error(), "airgapped") {
		t.Fatalf("expected error to mention airgapped, got %v", err)
	}
}

func minCfg() *config.Config {
	c := &config.Config{}
	c.WorkloadClusterNamespace = "default"
	c.WorkloadClusterName = "capi-quickstart"
	c.WorkloadKubernetesVersion = "v1.32.0"
	c.ControlPlaneMachineCount = "1"
	c.WorkerMachineCount = "2"
	c.ControlPlaneEndpointIP = "10.0.0.1"
	c.ControlPlaneEndpointPort = "6443"
	c.NodeIPRanges = "10.0.0.10-10.0.0.30"
	c.Gateway = "10.0.0.1"
	c.IPPrefix = "24"
	c.DNSServers = "1.1.1.1,8.8.8.8"
	return c
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}