// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package openstack_test

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
	_ "github.com/lpasquali/yage/internal/provider/openstack"
)

func TestOpenStackProviderSmoke(t *testing.T) {
	p, err := provider.Get("openstack")
	if err != nil {
		t.Fatalf("provider.Get(openstack) failed: %v", err)
	}
	if got := p.Name(); got != "openstack" {
		t.Errorf("Name() = %q, want %q", got, "openstack")
	}
	if got := p.InfraProviderName(); got != "openstack" {
		t.Errorf("InfraProviderName() = %q, want %q", got, "openstack")
	}
	args := p.ClusterctlInitArgs(nil)
	if len(args) != 2 || args[0] != "--infrastructure" || args[1] != "openstack" {
		t.Errorf("ClusterctlInitArgs = %v, want [--infrastructure openstack]", args)
	}
	tpl, err := p.K3sTemplate(nil, false)
	if err != nil {
		t.Fatalf("K3sTemplate err: %v", err)
	}
	if len(tpl) == 0 {
		t.Fatal("K3sTemplate returned empty string")
	}
	for _, want := range []string{
		"OpenStackCluster",
		"OpenStackMachineTemplate",
		"KThreesControlPlane",
		"KThreesConfigTemplate",
		"${OPENSTACK_NODE_MACHINE_FLAVOR}",
		"${OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR}",
	} {
		if !strings.Contains(tpl, want) {
			t.Errorf("K3sTemplate missing %q", want)
		}
	}
}

// TestTemplateVarsKeysMatchTemplate asserts that the keys returned by
// TemplateVars align with the placeholder names in the K3s template. This is a
// regression test for the two env-key mismatches fixed in this PR:
//
//	"OPENSTACK_CONTROL_PLANE_FLAVOR" → "OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR"
//	"OPENSTACK_WORKER_FLAVOR"        → "OPENSTACK_NODE_MACHINE_FLAVOR"
func TestTemplateVarsKeysMatchTemplate(t *testing.T) {
	p, err := provider.Get("openstack")
	if err != nil {
		t.Fatalf("provider.Get(openstack): %v", err)
	}
	cfg := &config.Config{}
	cfg.Providers.OpenStack.ControlPlaneFlavor = "m1.xlarge"
	cfg.Providers.OpenStack.WorkerFlavor = "m1.large"

	vars := p.TemplateVars(cfg)

	// Both corrected keys must be present.
	for _, key := range []string{
		"OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR",
		"OPENSTACK_NODE_MACHINE_FLAVOR",
	} {
		if _, ok := vars[key]; !ok {
			t.Errorf("TemplateVars missing key %q", key)
		}
	}

	// Old (wrong) keys must NOT be present.
	for _, oldKey := range []string{
		"OPENSTACK_CONTROL_PLANE_FLAVOR",
		"OPENSTACK_WORKER_FLAVOR",
	} {
		if _, ok := vars[oldKey]; ok {
			t.Errorf("TemplateVars still contains stale key %q", oldKey)
		}
	}

	// Values must match what we set.
	if got := vars["OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR"]; got != "m1.xlarge" {
		t.Errorf("OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR = %q, want %q", got, "m1.xlarge")
	}
	if got := vars["OPENSTACK_NODE_MACHINE_FLAVOR"]; got != "m1.large" {
		t.Errorf("OPENSTACK_NODE_MACHINE_FLAVOR = %q, want %q", got, "m1.large")
	}

	// Verify keys also appear in the K3s template (belt-and-suspenders).
	tpl, _ := p.K3sTemplate(nil, false)
	for _, key := range []string{
		"OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR",
		"OPENSTACK_NODE_MACHINE_FLAVOR",
	} {
		if !strings.Contains(tpl, "${"+key+"}") {
			t.Errorf("K3s template does not reference ${%s}", key)
		}
	}
}