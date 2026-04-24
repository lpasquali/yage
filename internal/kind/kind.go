// Package kind will host kind cluster lifecycle + bootstrap-state
// backup/restore functions. Currently a stub — ported functions will land
// here in subsequent passes.
//
// Bash source map (bootstrap-capi.sh):
//   - ensure_kind() — moved to internal/installer.Kind (stays there)
//   - kind_bootstrap_state_backup_write_kind_dir      ~L2157-2284
//   - kind_bootstrap_state_backup_namespaces          ~L2287-2296
//   - kind_bootstrap_state_backup                     ~L2300-2446
//   - kind_bootstrap_state_restore                    ~L2450-2600
//   - plus cluster-create / cluster-delete / selection logic further down
package kind

import "github.com/lpasquali/bootstrap-capi/internal/config"

// Backup is a TODO(port) stub for kind_bootstrap_state_backup.
func Backup(cfg *config.Config, outPath string) error {
	return todo("kind.Backup — port kind_bootstrap_state_backup (L2300-2446)")
}

// Restore is a TODO(port) stub for kind_bootstrap_state_restore.
func Restore(cfg *config.Config, archivePath string) error {
	return todo("kind.Restore — port kind_bootstrap_state_restore (L2450-2600)")
}
