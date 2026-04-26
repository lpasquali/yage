package vsphere

// Phase D state-handoff hooks for vSphere.
//
// See docs/abstraction-plan.md §11 + §14.D + §13's vSphere
// validation report. Per §13.5, vSphere config-tree fields aren't
// in cfg.Providers.Vsphere.* yet (Phase C didn't add them — the
// vSphere provider package is still cost-only). Once the gap is
// closed this function grows; for now it returns empty maps.

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields for vSphere: empty until config gap is closed.
// Sketch in §13's vSphere agent report (server, datacenter,
// folder, resource pool, datastore, network, template,
// tls_thumbprint).
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	return nil
}

// TemplateVars for vSphere: empty until config gap is closed.
// Sketch (~10 placeholders): VSPHERE_SERVER, VSPHERE_DATACENTER,
// VSPHERE_FOLDER, VSPHERE_RESOURCE_POOL, VSPHERE_DATASTORE,
// VSPHERE_NETWORK, VSPHERE_TEMPLATE, VSPHERE_USERNAME,
// VSPHERE_PASSWORD, VSPHERE_TLS_THUMBPRINT.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return nil
}
