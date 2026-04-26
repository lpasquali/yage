package openstack

// Phase D state-handoff hooks for OpenStack.
//
// See docs/abstraction-plan.md §11 + §14.D + §13's OpenStack
// validation report. OpenStack is one of the two flat-quota clouds
// alongside Proxmox (per §13's per-provider fit summary), so it's
// also a candidate for a future real Inventory implementation.

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists OpenStack runtime config. clouds.yaml
// content (OS_AUTH_URL, project name, app credentials) flows from
// env / operator-supplied file and stays out of the kind Secret.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	// OpenStackConfig fields are minimal today; this function returns
	// an empty map until per-provider config-tree gaps from §13.5
	// are closed (cloud name, project, region, flavors, image,
	// failure domain). Sketch in §13's OpenStack agent report.
	return nil
}

// TemplateVars: same — placeholders for the K3s template are read
// directly from env via clouds.yaml. Returning empty until the
// per-provider config-tree gaps are filled.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return nil
}
