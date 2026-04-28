// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// onprem_integration_test.go — stdin-driven integration test for the
// full on-prem walkthrough (issue #10).
//
// Drives runOnPremFork() directly via newStateWithReader so that:
//   - The kind prelude (EnsureClusterUp) is never invoked.
//   - kindsync.Merge* calls are skipped (no cluster available in CI).
//   - writeBootstrapConfigSecret is stubbed to avoid disk writes.
//
// The test verifies that the resolved *config.Config is correctly
// populated after the complete 8-step on-prem flow.
//
// Loopback path (capacity infeasible → re-run step 3): runFeasibilityCheckOnPrem
// is a regular function — not a package-level var — so it cannot be
// mocked without a production-code refactor. The loopback is therefore
// not covered here; a future refactor can extract it behind a var.

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// providerStubOnce ensures the proxmox stub is registered exactly
// once across all tests in the package (provider.Register panics on
// duplicates).
var providerStubOnce sync.Once

// registerProxmoxStub registers a minimal airgap-compatible proxmox
// stub so step4_onprem_providerPick finds at least one on-prem
// provider, and runFeasibilityCheckOnPrem's Inventory() call returns
// ErrNotApplicable (→ FeasibilityUnchecked → soft pass, no re-prompt).
func registerProxmoxStub() {
	providerStubOnce.Do(func() {
		provider.Register("proxmox", func() provider.Provider {
			return proxmoxStub{}
		})
	})
}

// proxmoxStub is the thinnest possible Provider implementation that
// satisfies the full interface.  It embeds MinStub for all the
// boilerplate and overrides only the three methods that must return
// non-panic values for the on-prem walkthrough.
type proxmoxStub struct {
	provider.MinStub
}

func (proxmoxStub) Name() string                   { return "proxmox" }
func (proxmoxStub) InfraProviderName() string      { return "proxmox" }
func (proxmoxStub) ClusterctlInitArgs(_ *config.Config) []string {
	return []string{"--infrastructure", "proxmox"}
}
func (proxmoxStub) EstimateMonthlyCostUSD(_ *config.Config) (provider.CostEstimate, error) {
	return provider.CostEstimate{}, provider.ErrNotApplicable
}

// TestOnPremWalkthrough_FullRun drives the complete on-prem flow for
// a proxmox provider and asserts the resulting config fields.
//
// Feed summary:
//   step 4  provider        → proxmox (only registered provider; index 1)
//   step 1  environment     → dev (index 1)
//   step 2  resilience      → ha-across-hosts (index 2)
//   step 3  workload        → apps="2 medium", db=50, no queue, no obj-store, no cache
//   stepBM  bootstrap mode  → "" (accept pre-selected k3s default)
//   step 5  capacity        → FeasibilityUnchecked; no stdin consumed
//   step 5.5 TCO            → n (skip)
//   step 6  proxmox URL     → https://pve.example.com:8006
//           node            → pve
//           region          → dc1
//           bridge          → "" (accept default vmbr0)
//           pool            → "" (no pool)
//           cloudinit store → "" (accept default local)
//           template ID     → "" (accept default 104)
//           cred mode       → 1 (managed)
//           admin user      → "" (accept default)
//           admin token     → test-token
//   step 6.5 VIP            → "" (accept default 192.168.0.20)
//            node range     → "" (accept default)
//            gateway        → "" (accept default)
//            prefix         → "" (accept default)
//            DNS            → "" (accept default)
//            SSH keys       → "" (empty line = no keys, finish)
//            workload name  → "" (accept default capi-quickstart)
//   step 7  review          → y (write to kind)
//   step 8  persist (stubbed) → deploy now? → n
func TestOnPremWalkthrough_FullRun(t *testing.T) {
	registerProxmoxStub()

	// Stub writeBootstrapConfigSecret for the duration of this test so
	// step8_persistAndDecide does not attempt a real kind write.
	orig := writeBootstrapConfigSecret
	writeBootstrapConfigSecret = func(_ *config.Config) error { return nil }
	t.Cleanup(func() { writeBootstrapConfigSecret = orig })

	// Build the stdin feed — one entry per prompt line.
	lines := []string{
		// step4: provider pick — "proxmox" is the first (and only) entry
		"1",
		// step1: environment
		"1", // dev
		// step2: resilience (on-prem choices: 1=single-host, 2=ha-across-hosts)
		"2",
		// step3: workload shape
		"2 medium", // apps
		"50",       // database GB
		// no egress prompt on on-prem
		"n", // queue
		"n", // object storage
		"n", // cache
		// stepBootstrapMode: totalVMs=4 → pre-selected k3s; send empty to accept
		"",
		// step5_onprem_capacity: FeasibilityUnchecked → no stdin
		// step5_5_onprem_tco: skip TCO
		"n",
		// step6_proxmox — core fields
		"https://pve.example.com:8006", // URL
		"pve",                          // node
		"dc1",                          // region
		"",                             // bridge (accept default vmbr0)
		"",                             // pool (no pool)
		"",                             // cloudinit storage (accept default local)
		"",                             // template ID (accept default 104)
		// credential mode: 1 = managed
		"1",
		// step6_proxmox_managed
		"",           // admin username (accept default)
		"test-token", // admin token (promptSecretHidden falls back to readLine)
		// step6_5_proxmox_network
		"", // control-plane VIP (accept default 192.168.0.20)
		"", // node IP range (accept default)
		"", // gateway (accept default)
		"", // prefix (accept default)
		"", // DNS (accept default)
		// promptSSHKeys: empty line terminates immediately (no keys)
		"",
		"", // workload cluster name (accept default capi-quickstart)
		// step7_review: "write to kind?"
		"y",
		// step8_persistAndDecide: "deploy now?"
		"n",
	}
	input := strings.NewReader(strings.Join(lines, "\n") + "\n")

	cfg := &config.Config{}
	var out bytes.Buffer
	s := newStateWithReader(&out, cfg, input)
	// Force the on-prem fork so runOnPremFork doesn't depend on the env.
	s.fork = forkOnPrem

	rc := s.runOnPremFork()
	if rc != 0 {
		t.Fatalf("runOnPremFork() returned %d (non-zero); walkthrough output:\n%s", rc, out.String())
	}

	// --- Assertions ---

	if cfg.InfraProvider != "proxmox" {
		t.Errorf("InfraProvider = %q, want %q", cfg.InfraProvider, "proxmox")
	}

	if cfg.Workload.Environment != "dev" {
		t.Errorf("Workload.Environment = %q, want %q", cfg.Workload.Environment, "dev")
	}

	if cfg.Workload.Resilience != "ha" {
		t.Errorf("Workload.Resilience = %q, want %q", cfg.Workload.Resilience, "ha")
	}

	if cfg.ControlPlaneMachineCount != "3" {
		t.Errorf("ControlPlaneMachineCount = %q, want %q", cfg.ControlPlaneMachineCount, "3")
	}

	if len(cfg.Workload.Apps) != 1 {
		t.Fatalf("len(Workload.Apps) = %d, want 1; apps = %+v", len(cfg.Workload.Apps), cfg.Workload.Apps)
	}
	if cfg.Workload.Apps[0].Count != 2 {
		t.Errorf("Workload.Apps[0].Count = %d, want 2", cfg.Workload.Apps[0].Count)
	}
	if cfg.Workload.Apps[0].Template != "medium" {
		t.Errorf("Workload.Apps[0].Template = %q, want %q", cfg.Workload.Apps[0].Template, "medium")
	}

	if cfg.Workload.DatabaseGB != 50 {
		t.Errorf("Workload.DatabaseGB = %d, want 50", cfg.Workload.DatabaseGB)
	}

	if cfg.BootstrapMode != "k3s" {
		t.Errorf("BootstrapMode = %q, want %q", cfg.BootstrapMode, "k3s")
	}

	if cfg.ArgoCD.Enabled {
		t.Errorf("ArgoCD.Enabled = true for env=dev, want false")
	}

	if cfg.Workload.HasQueue || cfg.Workload.HasObjStore || cfg.Workload.HasCache {
		t.Errorf("add-ons should all be false: queue=%v objstore=%v cache=%v",
			cfg.Workload.HasQueue, cfg.Workload.HasObjStore, cfg.Workload.HasCache)
	}

	// Note: loopback path (capacity infeasible → re-run step 3) is not
	// covered here because runFeasibilityCheckOnPrem is a regular function
	// (not a package-level var) and cannot be injected without a production
	// refactor. That path should be tested separately once the function is
	// made injectable.
}
