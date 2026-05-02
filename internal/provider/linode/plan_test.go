// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package linode

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/plan"
)

// TestDescribeIdentity confirms the identity section title is emitted
// and the body explains the operator-managed token pattern.
func TestDescribeIdentity(t *testing.T) {
	p := &Provider{}
	cw := plan.NewCapturingWriter()
	p.DescribeIdentity(cw, &config.Config{})

	sections := cw.Sections()
	if len(sections) != 1 || sections[0] != "Identity bootstrap — Linode" {
		t.Fatalf("DescribeIdentity sections: got %v, want [Identity bootstrap — Linode]", sections)
	}

	var skips []string
	for _, e := range cw.Events {
		if e.Kind == plan.EventSkip {
			skips = append(skips, e.Text)
		}
	}
	if len(skips) == 0 {
		t.Fatal("DescribeIdentity: expected at least one Skip line, got none")
	}
	found := false
	for _, s := range skips {
		if strings.Contains(s, "LINODE_TOKEN") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DescribeIdentity: no Skip line mentions LINODE_TOKEN; got %v", skips)
	}
}

// TestDescribeWorkload confirms the workload section emits cluster
// coordinates, control-plane bullet, and at least one Skip for CSI.
func TestDescribeWorkload(t *testing.T) {
	cfg := &config.Config{}
	cfg.WorkloadClusterNamespace = "default"
	cfg.WorkloadClusterName = "demo"
	cfg.WorkloadKubernetesVersion = "v1.32.0"
	cfg.ControlPlaneMachineCount = "3"
	cfg.WorkerMachineCount = "2"
	cfg.Providers.Linode.Region = "us-east"
	cfg.Providers.Linode.ControlPlaneType = "g6-standard-4"
	cfg.Providers.Linode.NodeType = "g6-standard-2"
	cfg.Providers.Linode.OverheadTier = "prod"

	p := &Provider{}
	cw := plan.NewCapturingWriter()
	p.DescribeWorkload(cw, cfg)

	sections := cw.Sections()
	if len(sections) != 1 || sections[0] != "Workload Cluster — Linode" {
		t.Fatalf("DescribeWorkload sections: got %v", sections)
	}

	var bullets, skips []string
	for _, e := range cw.Events {
		switch e.Kind {
		case plan.EventBullet:
			bullets = append(bullets, e.Text)
		case plan.EventSkip:
			skips = append(skips, e.Text)
		}
	}

	if len(bullets) == 0 {
		t.Fatal("DescribeWorkload: expected bullets, got none")
	}
	if len(skips) == 0 {
		t.Fatal("DescribeWorkload: expected at least one Skip (CSI), got none")
	}

	clusterBullet := bullets[0]
	for _, want := range []string{"default", "demo", "v1.32.0", "us-east"} {
		if !strings.Contains(clusterBullet, want) {
			t.Errorf("cluster bullet missing %q: %q", want, clusterBullet)
		}
	}
}

// TestDescribeWorkload_NoWorkers confirms the workers bullet is
// suppressed when WorkerMachineCount is "0".
func TestDescribeWorkload_NoWorkers(t *testing.T) {
	cfg := &config.Config{}
	cfg.WorkloadClusterName = "single-cp"
	cfg.ControlPlaneMachineCount = "1"
	cfg.WorkerMachineCount = "0"

	p := &Provider{}
	cw := plan.NewCapturingWriter()
	p.DescribeWorkload(cw, cfg)

	for _, e := range cw.Events {
		if e.Kind == plan.EventBullet && strings.Contains(e.Text, "workers:") {
			t.Errorf("workers bullet present with count 0: %q", e.Text)
		}
	}
}

// TestDescribePivot confirms the pivot section is emitted with a
// Skip line (Linode has no PivotTarget yet).
func TestDescribePivot(t *testing.T) {
	p := &Provider{}
	cw := plan.NewCapturingWriter()
	p.DescribePivot(cw, &config.Config{})

	sections := cw.Sections()
	if len(sections) != 1 || sections[0] != "Pivot to managed mgmt cluster" {
		t.Fatalf("DescribePivot sections: got %v", sections)
	}

	var skips []string
	for _, e := range cw.Events {
		if e.Kind == plan.EventSkip {
			skips = append(skips, e.Text)
		}
	}
	if len(skips) == 0 {
		t.Fatal("DescribePivot: expected a Skip line, got none")
	}
}
