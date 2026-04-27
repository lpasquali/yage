// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package vsphere

// State-handoff hooks for vSphere.
//
// See docs/abstraction-plan.md §11 + §14.D + §13's vSphere
// validation report. This file surfaces cfg.Providers.Vsphere.*
// fields for kindsync round-trip and CAPV manifest substitution.

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists vSphere runtime config. Username/Password
// are operator-supplied via env (VSPHERE_USERNAME / VSPHERE_PASSWORD)
// but kept on cfg so xapiri can prompt and kindsync can round-trip;
// they're encrypted at rest in the kind Secret per §11.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("server", cfg.Providers.Vsphere.Server)
	add("datacenter", cfg.Providers.Vsphere.Datacenter)
	add("folder", cfg.Providers.Vsphere.Folder)
	add("resource_pool", cfg.Providers.Vsphere.ResourcePool)
	add("datastore", cfg.Providers.Vsphere.Datastore)
	add("network", cfg.Providers.Vsphere.Network)
	add("template", cfg.Providers.Vsphere.Template)
	add("tls_thumbprint", cfg.Providers.Vsphere.TLSThumbprint)
	add("username", cfg.Providers.Vsphere.Username)
	add("password", cfg.Providers.Vsphere.Password)
	return out
}

// TemplateVars returns the vSphere-specific clusterctl manifest
// substitution map. The full set of CAPV placeholders (~10) is
// surfaced here; the manifest references them via ${VSPHERE_*}.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"VSPHERE_SERVER":         cfg.Providers.Vsphere.Server,
		"VSPHERE_DATACENTER":     cfg.Providers.Vsphere.Datacenter,
		"VSPHERE_FOLDER":         cfg.Providers.Vsphere.Folder,
		"VSPHERE_RESOURCE_POOL":  cfg.Providers.Vsphere.ResourcePool,
		"VSPHERE_DATASTORE":      cfg.Providers.Vsphere.Datastore,
		"VSPHERE_NETWORK":        cfg.Providers.Vsphere.Network,
		"VSPHERE_TEMPLATE":       cfg.Providers.Vsphere.Template,
		"VSPHERE_TLS_THUMBPRINT": cfg.Providers.Vsphere.TLSThumbprint,
		"VSPHERE_USERNAME":       cfg.Providers.Vsphere.Username,
		"VSPHERE_PASSWORD":       cfg.Providers.Vsphere.Password,
	}
}

// AbsorbConfigYAML is the reverse of KindSyncFields: reads the
// lowercase bare-key map the new yage-system/bootstrap-config
// Secret schema writes (per-provider section; orchestrator strips
// the "vsphere." prefix before dispatching) and fills empty cfg
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
		case "server":
			assign(&cfg.Providers.Vsphere.Server, v)
		case "datacenter":
			assign(&cfg.Providers.Vsphere.Datacenter, v)
		case "folder":
			assign(&cfg.Providers.Vsphere.Folder, v)
		case "resource_pool":
			assign(&cfg.Providers.Vsphere.ResourcePool, v)
		case "datastore":
			assign(&cfg.Providers.Vsphere.Datastore, v)
		case "network":
			assign(&cfg.Providers.Vsphere.Network, v)
		case "template":
			assign(&cfg.Providers.Vsphere.Template, v)
		case "tls_thumbprint":
			assign(&cfg.Providers.Vsphere.TLSThumbprint, v)
		case "username":
			assign(&cfg.Providers.Vsphere.Username, v)
		case "password":
			assign(&cfg.Providers.Vsphere.Password, v)
		}
	}
	return assigned
}