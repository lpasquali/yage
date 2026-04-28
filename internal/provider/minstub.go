// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package provider

import (
	"github.com/lpasquali/yage/internal/config"
)

// MinStub embeds the boilerplate every CAPI infrastructure provider
// has when yage only wires it for cost estimation +
// clusterctl init. Concrete cloud packages embed MinStub, override
// Name() / InfraProviderName() / EstimateMonthlyCostUSD(), and get
// the rest (EnsureIdentity / EnsureGroup return ErrNotApplicable;
// Inventory returns ErrNotApplicable; PatchManifest no-op;
// K3sTemplate ErrNotApplicable until the per-cloud K3s flavor is
// wired). CSI lives on its own registry now — see internal/csi.
//
// New clouds can be added in <100 LOC — Name + InfraProviderName +
// ClusterctlInitArgs + EstimateMonthlyCostUSD live in the cloud's
// package; the rest is here.
type MinStub struct{}

func (MinStub) EnsureIdentity(cfg *config.Config) error        { return ErrNotApplicable }
func (MinStub) EnsureGroup(cfg *config.Config, n string) error { return ErrNotApplicable }
func (MinStub) Inventory(cfg *config.Config) (*Inventory, error) {
	return nil, ErrNotApplicable
}
func (MinStub) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return "", ErrNotApplicable
}
func (MinStub) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	return nil
}

// PlanDescriber defaults: no-op. Providers that want plan output
// override these to call w.Section / w.Bullet / w.Skip. Cost-only
// providers leave them as no-ops and the dry-run plan simply omits
// their sections (acceptable per §8 — section absence == not
// applicable to this provider).
func (MinStub) DescribeIdentity(w PlanWriter, cfg *config.Config)      {}
func (MinStub) DescribeWorkload(w PlanWriter, cfg *config.Config)      {}
func (MinStub) DescribePivot(w PlanWriter, cfg *config.Config)         {}
func (MinStub) DescribeClusterctlInit(w PlanWriter, cfg *config.Config) {}

// KindSyncer default: no fields to persist. Most cost-only and
// not-yet-implemented providers have nothing to round-trip through
// kindsync.
func (MinStub) KindSyncFields(cfg *config.Config) map[string]string {
	return nil
}

// AbsorbConfigYAML default: nothing to absorb. Providers that ship
// a real Secret schema (Proxmox today) override this; everyone else
// receives the kindsync map and ignores it.
func (MinStub) AbsorbConfigYAML(cfg *config.Config, kv map[string]string) bool {
	return false
}

// BootstrapSecrets default: no credential Secrets beyond the generic
// config.yaml snapshot. Cost-only and not-yet-implemented providers
// return nil and the kindsync layer skips the credential-fetch step.
func (MinStub) BootstrapSecrets(cfg *config.Config) []BootstrapSecretRef {
	return nil
}

// Purger default: no cleanup needed. Returns nil (NOT
// ErrNotApplicable) — the orchestrator's --purge flow can call
// this safely on every provider.
func (MinStub) Purge(cfg *config.Config) error {
	return nil
}

// TemplateVars default: empty map. Providers that need to inject
// vendor-specific values into the clusterctl manifest template
// override this.
func (MinStub) TemplateVars(cfg *config.Config) map[string]string {
	return nil
}

// RenderMgmtManifest default: ErrNotApplicable. Providers without a
// management-cluster bootstrap story (i.e. kind stays as permanent
// management cluster) return this; the orchestrator skips the phase.
func (MinStub) RenderMgmtManifest(cfg *config.Config, clusterctlCfgPath string) (string, error) {
	return "", ErrNotApplicable
}

// PivotTarget default: ErrNotApplicable. Most providers don't yet
// have a managed mgmt-cluster bootstrap story; only Proxmox
// returns a real target today.
func (MinStub) PivotTarget(cfg *config.Config) (PivotTarget, error) {
	return PivotTarget{}, ErrNotApplicable
}