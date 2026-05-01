// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package csi

import (
	"errors"
	"testing"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/plan"
)

// fakeDriver is a minimal Driver impl for registry tests. Lives in
// the csi package itself so it can exercise the unexported registry
// state without leaking the test-only driver out to other tests.
type fakeDriver struct{ name string }

func (f *fakeDriver) Name() string             { return f.name }
func (f *fakeDriver) K8sCSIDriverName() string { return f.name + ".test" }
func (f *fakeDriver) Defaults() []string       { return nil }
func (f *fakeDriver) HelmChart(*config.Config) (string, string, string, error) {
	return "https://example.invalid", f.name, "v0.0.0", nil
}
func (f *fakeDriver) RenderValues(*config.Config) (string, error) { return "", nil }
func (f *fakeDriver) EnsureSecret(*config.Config, string) error           { return nil }
func (f *fakeDriver) DefaultStorageClass() string                          { return "" }
func (f *fakeDriver) DescribeInstall(plan.Writer, *config.Config)          {}
func (f *fakeDriver) EnsureManagementInstall(*config.Config, string) error { return ErrNotApplicable }

func TestRegisterIdempotent(t *testing.T) {
	d := &fakeDriver{name: "registry-idempotent-test"}
	Register(d)
	Register(d) // same instance — must not panic
	got, err := Get(d.name)
	if err != nil {
		t.Fatalf("Get(%q) = %v", d.name, err)
	}
	if got != d {
		t.Errorf("Get returned different instance")
	}
}

func TestRegisterDuplicateDifferentInstancePanics(t *testing.T) {
	a := &fakeDriver{name: "registry-dupe-test"}
	b := &fakeDriver{name: "registry-dupe-test"}
	Register(a)
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic registering a different driver under the same name")
		}
	}()
	Register(b)
}

func TestRegisterNilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic on Register(nil)")
		}
	}()
	Register(nil)
}

func TestGetUnregisteredErrors(t *testing.T) {
	_, err := Get("definitely-not-registered-anywhere")
	if err == nil {
		t.Errorf("expected error for unregistered driver")
	}
}

func TestErrNotApplicableIsExported(t *testing.T) {
	if !errors.Is(ErrNotApplicable, ErrNotApplicable) {
		t.Errorf("ErrNotApplicable should match itself via errors.Is")
	}
}