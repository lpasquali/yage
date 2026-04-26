// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

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

// KindSyncFields persists OpenStack runtime config landed on cfg in
// commit f6ca113. clouds.yaml content (OS_AUTH_URL, project name,
// app credentials) flows from env / operator-supplied file and
// stays out of the kind Secret; the named-cloud / project / region /
// flavor / image / failure-domain / SSH-key fields here are
// non-secret runtime config the next yage run needs.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("cloud", cfg.Providers.OpenStack.Cloud)
	add("project_name", cfg.Providers.OpenStack.ProjectName)
	add("region", cfg.Providers.OpenStack.Region)
	add("failure_domain", cfg.Providers.OpenStack.FailureDomain)
	add("image_name", cfg.Providers.OpenStack.ImageName)
	add("control_plane_flavor", cfg.Providers.OpenStack.ControlPlaneFlavor)
	add("worker_flavor", cfg.Providers.OpenStack.WorkerFlavor)
	add("dns_nameservers", cfg.Providers.OpenStack.DNSNameservers)
	add("ssh_key_name", cfg.Providers.OpenStack.SSHKeyName)
	return out
}

// TemplateVars returns the OpenStack-specific clusterctl manifest
// substitution map. clouds.yaml-driven secrets (OS_USERNAME /
// OS_PASSWORD / OS_AUTH_URL etc.) flow via env into the
// cloud-config Secret CAPO references and are NOT in this map.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"OPENSTACK_CLOUD":                cfg.Providers.OpenStack.Cloud,
		"OPENSTACK_PROJECT_NAME":         cfg.Providers.OpenStack.ProjectName,
		"OPENSTACK_REGION":               cfg.Providers.OpenStack.Region,
		"OPENSTACK_FAILURE_DOMAIN":       cfg.Providers.OpenStack.FailureDomain,
		"OPENSTACK_IMAGE_NAME":           cfg.Providers.OpenStack.ImageName,
		"OPENSTACK_CONTROL_PLANE_FLAVOR": cfg.Providers.OpenStack.ControlPlaneFlavor,
		"OPENSTACK_WORKER_FLAVOR":        cfg.Providers.OpenStack.WorkerFlavor,
		"OPENSTACK_DNS_NAMESERVERS":      cfg.Providers.OpenStack.DNSNameservers,
		"OPENSTACK_SSH_KEY_NAME":         cfg.Providers.OpenStack.SSHKeyName,
	}
}

// AbsorbConfigYAML is the reverse of KindSyncFields: reads the
// lowercase bare-key map the new yage-system/bootstrap-config
// Secret schema writes (per-provider section; orchestrator strips
// the "openstack." prefix before dispatching) and fills empty cfg
// fields with non-empty values. Universal keys (provider,
// cluster_name, …) are handled by the orchestrator.
func (p *Provider) AbsorbConfigYAML(cfg *config.Config, kv map[string]string) bool {
	assigned := false
	assign := func(cur *string, v string) {
		if *cur == "" && v != "" {
			*cur = v
			assigned = true
		}
	}
	for k, v := range kv {
		switch k {
		case "cloud":
			assign(&cfg.Providers.OpenStack.Cloud, v)
		case "project_name":
			assign(&cfg.Providers.OpenStack.ProjectName, v)
		case "region":
			assign(&cfg.Providers.OpenStack.Region, v)
		case "failure_domain":
			assign(&cfg.Providers.OpenStack.FailureDomain, v)
		case "image_name":
			assign(&cfg.Providers.OpenStack.ImageName, v)
		case "control_plane_flavor":
			assign(&cfg.Providers.OpenStack.ControlPlaneFlavor, v)
		case "worker_flavor":
			assign(&cfg.Providers.OpenStack.WorkerFlavor, v)
		case "dns_nameservers":
			assign(&cfg.Providers.OpenStack.DNSNameservers, v)
		case "ssh_key_name":
			assign(&cfg.Providers.OpenStack.SSHKeyName, v)
		}
	}
	return assigned
}