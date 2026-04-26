package provider

import (
	"github.com/lpasquali/yage/internal/config"
)

// MinStub embeds the boilerplate every CAPI infrastructure provider
// has when yage only wires it for cost estimation +
// clusterctl init. Concrete cloud packages embed MinStub, override
// Name() / InfraProviderName() / EstimateMonthlyCostUSD(), and get
// the rest (EnsureIdentity / EnsureGroup / EnsureCSISecret return
// ErrNotApplicable; Inventory returns ErrNotApplicable;
// PatchManifest no-op; K3sTemplate ErrNotApplicable until the
// per-cloud K3s flavor is wired).
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
func (MinStub) EnsureCSISecret(cfg *config.Config, kubeconfigPath string) error {
	return ErrNotApplicable
}

// PlanDescriber defaults: no-op. Providers that want plan output
// override these to call w.Section / w.Bullet / w.Skip. Cost-only
// providers leave them as no-ops and the dry-run plan simply omits
// their sections (acceptable per §8 — section absence == not
// applicable to this provider).
func (MinStub) DescribeIdentity(w PlanWriter, cfg *config.Config) {}
func (MinStub) DescribeWorkload(w PlanWriter, cfg *config.Config) {}
func (MinStub) DescribePivot(w PlanWriter, cfg *config.Config)    {}
