// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kind

import (
	"sort"
	"strings"

	"github.com/lpasquali/yage/internal/config"
)

// BackupNamespaces ports kind_bootstrap_state_backup_namespaces. When
// BOOTSTRAP_KIND_BACKUP_NAMESPACES is set, it is split on commas/spaces,
// trimmed, deduplicated, and sorted. Otherwise the list is the union of
// PROXMOX_BOOTSTRAP_SECRET_NAMESPACE and WORKLOAD_CLUSTER_NAMESPACE (if
// either is set), sorted and deduplicated.
func BackupNamespaces(cfg *config.Config) []string {
	var raw []string
	if cfg.BootstrapKindBackupNamespaces != "" {
		// split on any of ',' or whitespace
		for _, f := range strings.FieldsFunc(cfg.BootstrapKindBackupNamespaces, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
		}) {
			s := strings.TrimSpace(f)
			if s != "" {
				raw = append(raw, s)
			}
		}
	} else {
		if cfg.Providers.Proxmox.BootstrapSecretNamespace != "" {
			raw = append(raw, cfg.Providers.Proxmox.BootstrapSecretNamespace)
		}
		if cfg.WorkloadClusterNamespace != "" {
			raw = append(raw, cfg.WorkloadClusterNamespace)
		}
	}
	// sort + uniq
	sort.Strings(raw)
	uniq := raw[:0]
	var prev string
	for i, s := range raw {
		if i == 0 || s != prev {
			uniq = append(uniq, s)
		}
		prev = s
	}
	return uniq
}