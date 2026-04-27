// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package proxmox

import (
	"time"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
)

// PivotTarget implements provider.Pivoter for CAPMOX. It returns the
// destination kubeconfig path, the namespace list that clusterctl move
// should transfer (workload + mgmt cluster namespace + Proxmox bootstrap
// Secret namespace), and the provider-specific Secrets that VerifyParity
// should confirm are present on the management cluster after the handoff.
//
// The orchestrator sets cfg.MgmtKubeconfigPath after
// EnsureManagementCluster returns; PivotTarget reads it back here so the
// provider stays stateless (no cached path).
func (p *Provider) PivotTarget(cfg *config.Config) (provider.PivotTarget, error) {
	if cfg.MgmtKubeconfigPath == "" {
		// Orchestrator has not yet called EnsureManagementCluster or
		// the provider check is happening before the mgmt cluster is up
		// (e.g. the "does this provider implement PivotTarget?" probe in
		// orchestrator.Run). Return a non-error target with an empty
		// KubeconfigPath — the orchestrator will not have a kubeconfig
		// to pass to MoveCAPIState yet, which is fine at probe time.
		return provider.PivotTarget{
			Namespaces:   proxmoxPivotNamespaces(cfg),
			ReadyTimeout: proxmoxPivotTimeout(cfg),
			VerifySecrets: proxmoxVerifySecrets(cfg),
		}, nil
	}
	return provider.PivotTarget{
		KubeconfigPath: cfg.MgmtKubeconfigPath,
		Namespaces:     proxmoxPivotNamespaces(cfg),
		ReadyTimeout:   proxmoxPivotTimeout(cfg),
		VerifySecrets:  proxmoxVerifySecrets(cfg),
	}, nil
}

// proxmoxPivotNamespaces returns the ordered, deduplicated list of
// Kubernetes namespaces that clusterctl move operates on during a
// Proxmox pivot. The list is workload + mgmt CAPI namespace +
// the Proxmox bootstrap-Secret namespace (yage-system by default).
func proxmoxPivotNamespaces(cfg *config.Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(ns string) {
		if ns != "" && !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	add(cfg.WorkloadClusterNamespace)
	add(cfg.Mgmt.ClusterNamespace)
	bsNS := cfg.Providers.Proxmox.BootstrapSecretNamespace
	if bsNS == "" {
		bsNS = "yage-system"
	}
	add(bsNS)
	return out
}

// proxmoxPivotTimeout returns the ReadyTimeout configured for the pivot
// verify step. Falls back to 10 minutes when the string field is empty or
// unparseable.
func proxmoxPivotTimeout(cfg *config.Config) time.Duration {
	if cfg.Pivot.VerifyTimeout == "" {
		return 10 * time.Minute
	}
	d, err := time.ParseDuration(cfg.Pivot.VerifyTimeout)
	if err != nil {
		return 10 * time.Minute
	}
	return d
}

// proxmoxVerifySecrets returns the list of provider-specific Secrets that
// VerifyParity checks on the management cluster. Mirrors the set written
// by kindsync.HandOffBootstrapSecretsToManagement.
func proxmoxVerifySecrets(cfg *config.Config) []provider.VerifySecret {
	ns := cfg.Providers.Proxmox.BootstrapSecretNamespace
	if ns == "" {
		ns = "yage-system"
	}
	names := []string{
		cfg.Providers.Proxmox.BootstrapConfigSecretName,
		cfg.Providers.Proxmox.BootstrapCAPMOXSecretName,
		cfg.Providers.Proxmox.BootstrapCSISecretName,
		cfg.Providers.Proxmox.BootstrapAdminSecretName,
	}
	// Apply config defaults (mirrors config.go DefaultProxmoxConfig).
	defaults := map[*string]string{
		&names[0]: "proxmox-bootstrap-config",
		&names[1]: "proxmox-bootstrap-capmox-credentials",
		&names[2]: "proxmox-bootstrap-csi-credentials",
		&names[3]: "proxmox-bootstrap-admin-credentials",
	}
	for ptr, def := range defaults {
		if *ptr == "" {
			*ptr = def
		}
	}
	out := make([]provider.VerifySecret, 0, len(names))
	for _, name := range names {
		if name != "" {
			out = append(out, provider.VerifySecret{Namespace: ns, Name: name})
		}
	}
	return out
}
