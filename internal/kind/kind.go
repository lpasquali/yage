// Package kind ports kind cluster lifecycle + bootstrap-state
// backup/restore functions.
//
// Bash source map (bootstrap-capi.sh):
//   - ensure_kind()                                   — internal/installer.Kind
//   - kind_bootstrap_state_backup_namespaces          — BackupNamespaces    (L2287-L2296)
//   - kind_bootstrap_state_backup_write_kind_dir      — writeKindDir        (L2157-L2284)
//   - kind_bootstrap_state_backup                     — Backup              (L2300-L2446)
//   - kind_bootstrap_state_restore                    — Restore             (L2450-L2600)
//
// Still to port: cluster-create / cluster-delete / selection logic further
// down the script (ensure_kind_cluster, kind_cluster_select, …).
package kind
