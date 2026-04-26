// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package openstack_test

import (
	"strings"
	"testing"

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