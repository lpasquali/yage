// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pivot

// E.5 — CAPD-as-target sanity test (provider-agnostic move).
//
// Purpose. The Phase E.4 commit (0e3db75) extended MoveCAPIState so
// the active provider's PivotTarget owns the namespace list passed
// to clusterctl move. Before E.4 the namespace set was hard-coded to
// the Proxmox-flavoured trio (workload + mgmt + Proxmox bootstrap
// Secret). After E.4 the trio is the *fallback* — any provider whose
// Pivoter returns a non-nil Namespaces overrides it.
//
// CAPD (the in-tree CAPI Docker provider, internal/provider/capd) is
// the right test target per §14.E.5 because it ships only the
// MinStub-default Pivoter (PivotTarget returns ErrNotApplicable) — so
// driving the helper with the real CAPD provider proves the fallback
// path doesn't secretly depend on Proxmox state. Pairing that with a
// fake Pivoter that DOES return a non-nil Namespaces list proves the
// override path works for any future provider with a managed-mgmt
// story.
//
// We test the namespace-selection helper (move.go::selectPivotNamespaces)
// rather than the full MoveCAPIState flow: the rest of MoveCAPIState
// (kubeconfig load, reconciler scale-to-zero, clusterctl invocation,
// retry loop) is provider-agnostic by construction and would require
// a kind context + clusterctl on PATH to exercise — out of scope for
// a unit test.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/provider/capd"
)

// fakePivoter is a stand-in Pivoter for the override branch. We can't
// rely on the real CAPD provider returning a non-nil Namespaces (it
// inherits MinStub's ErrNotApplicable today), and we deliberately
// don't modify capd.go — the whole point of E.5 is to verify the
// pivot mechanism is provider-agnostic without provider-specific
// knobs.
type fakePivoter struct {
	target provider.PivotTarget
	err    error
}

func (f fakePivoter) PivotTarget(cfg *config.Config) (provider.PivotTarget, error) {
	return f.target, f.err
}

// capdTargetingConfig builds a minimal *config.Config that targets
// CAPD with pivot enabled and a real (but empty) temp file as the
// management kubeconfig. The kubeconfig path only has to be non-empty
// and on disk for the namespace-selection helper to be exercised
// realistically; we never actually load it.
func capdTargetingConfig(t *testing.T) *config.Config {
	t.Helper()
	mgmtKubeconfig := filepath.Join(t.TempDir(), "mgmt.kubeconfig")
	// Touch the file so any future helper that stat()s it doesn't
	// trip; selectPivotNamespaces itself doesn't read the file but
	// keeping the path real matches §14.E.5's intent ("real temp
	// file path") and protects the test from drift.
	if err := writeEmptyFile(mgmtKubeconfig); err != nil {
		t.Fatalf("create temp mgmt kubeconfig: %v", err)
	}
	cfg := &config.Config{}
	cfg.Pivot.Enabled = true
	cfg.InfraProvider = "docker" // CAPD registers under "docker"
	cfg.MgmtKubeconfigPath = mgmtKubeconfig
	cfg.WorkloadClusterNamespace = "default"
	cfg.Mgmt.ClusterNamespace = "default"
	return cfg
}

func writeEmptyFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// TestSelectPivotNamespaces_CAPDFallsBackToDefault is the headline
// E.5 assertion: when the active provider's Pivoter returns
// ErrNotApplicable (CAPD's MinStub-inherited default), the namespace
// selector falls back to the generic workload + mgmt namespace pair.
// Provider-specific bootstrap namespaces (e.g. Proxmox's yage-system)
// are NOT part of the generic fallback — providers that need additional
// namespaces must implement PivotTarget and return them in Namespaces.
// This proves the pivot mechanism doesn't break for providers that
// haven't (yet) implemented PivotTarget, and that it doesn't leak
// Proxmox-specific namespace knowledge into the generic path.
func TestSelectPivotNamespaces_CAPDFallsBackToDefault(t *testing.T) {
	cfg := capdTargetingConfig(t)
	cfg.WorkloadClusterNamespace = "wl-ns"
	cfg.Mgmt.ClusterNamespace = "mgmt-ns"

	// Sanity: confirm CAPD really does inherit the default
	// ErrNotApplicable. If a future commit overrides PivotTarget on
	// CAPD this assertion will fail loudly and the test should be
	// updated with intent (rather than silently asserting the wrong
	// branch).
	capdProv := &capd.Provider{}
	if _, err := capdProv.PivotTarget(cfg); !errors.Is(err, provider.ErrNotApplicable) {
		t.Fatalf("CAPD PivotTarget: got err=%v, want ErrNotApplicable; capd.go's PivotTarget contract has changed and this test needs updating", err)
	}

	got := selectPivotNamespaces(cfg, capdProv)
	want := []string{"wl-ns", "mgmt-ns"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CAPD fallback: got %v, want %v", got, want)
	}
}

// TestSelectPivotNamespaces_NilProviderFallsBackToDefault covers the
// path where provider.For(cfg) errors (unknown provider, air-gap
// rejection). MoveCAPIState passes nil in that case; the helper must
// still produce the generic workload + mgmt fallback rather than panic.
func TestSelectPivotNamespaces_NilProviderFallsBackToDefault(t *testing.T) {
	cfg := capdTargetingConfig(t)
	cfg.WorkloadClusterNamespace = "wl"
	cfg.Mgmt.ClusterNamespace = "mgmt"

	got := selectPivotNamespaces(cfg, nil)
	want := []string{"wl", "mgmt"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nil provider fallback: got %v, want %v", got, want)
	}
}

// TestSelectPivotNamespaces_ProviderOverridesDefault is the override
// branch: when a Pivoter returns a non-nil Namespaces list, the
// selector hands those through verbatim (modulo dedupe) and does NOT
// fall back to the default trio. This is the post-E.4 contract that
// makes the move provider-agnostic.
//
// Using a fakePivoter (rather than a real provider) keeps the test
// hermetic: no global registry mutation, no reliance on whichever
// providers happen to be linked into the test binary.
func TestSelectPivotNamespaces_ProviderOverridesDefault(t *testing.T) {
	cfg := capdTargetingConfig(t)
	// Set defaults that should NOT appear in the result; if the
	// helper ignored the provider and fell back, we'd see these.
	cfg.WorkloadClusterNamespace = "wl-should-not-appear"
	cfg.Mgmt.ClusterNamespace = "mgmt-should-not-appear"
	cfg.Providers.Proxmox.BootstrapSecretNamespace = "should-not-appear"

	prov := fakePivoter{
		target: provider.PivotTarget{
			KubeconfigPath: cfg.MgmtKubeconfigPath,
			Namespaces:     []string{"capi-system", "capd-system"},
			ReadyTimeout:   10 * time.Minute,
		},
	}

	got := selectPivotNamespaces(cfg, prov)
	want := []string{"capi-system", "capd-system"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("provider override: got %v, want %v", got, want)
	}
	for _, ns := range got {
		if ns == "wl-should-not-appear" || ns == "mgmt-should-not-appear" || ns == "should-not-appear" {
			t.Errorf("default-trio namespace %q leaked into override result %v", ns, got)
		}
	}
}

// TestSelectPivotNamespaces_ProviderErrorFallsBackToDefault covers
// the third branch: provider IS present but PivotTarget returns a
// real error (not ErrNotApplicable, not nil — e.g. mgmt kubeconfig
// not yet ready). Helper falls back to the generic workload + mgmt
// fallback.
func TestSelectPivotNamespaces_ProviderErrorFallsBackToDefault(t *testing.T) {
	cfg := capdTargetingConfig(t)
	cfg.WorkloadClusterNamespace = "wl"
	cfg.Mgmt.ClusterNamespace = "mgmt"

	prov := fakePivoter{err: errors.New("mgmt kubeconfig not yet ready")}

	got := selectPivotNamespaces(cfg, prov)
	want := []string{"wl", "mgmt"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("error fallback: got %v, want %v", got, want)
	}
}

// TestSelectPivotNamespaces_ProviderEmptyNamespacesIsRespected covers
// a subtle edge: an empty (but non-nil) Namespaces slice. The post-
// E.4 contract uses nil as the "use default" sentinel; an explicitly
// empty list means "the provider knows it has nothing to move". The
// helper preserves the distinction — empty in, empty out — so we
// drive zero `clusterctl move` calls (caller's loop runs zero times)
// rather than silently falling back to the default trio.
func TestSelectPivotNamespaces_ProviderEmptyNamespacesIsRespected(t *testing.T) {
	cfg := capdTargetingConfig(t)

	prov := fakePivoter{
		target: provider.PivotTarget{
			KubeconfigPath: cfg.MgmtKubeconfigPath,
			Namespaces:     []string{}, // empty, NOT nil
		},
	}

	got := selectPivotNamespaces(cfg, prov)
	// dedupe([]string{}) returns an empty slice, which is
	// distinguishable from nil. Helper trusts the provider: empty
	// in, empty out.
	if len(got) != 0 {
		t.Errorf("explicitly-empty namespaces should not trigger fallback; got %v", got)
	}
}