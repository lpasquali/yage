// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package kind ports kind cluster lifecycle + bootstrap-state
// backup/restore functions.
//
// Bash source map (the original bash port):
//   - ensure_kind()                                   — internal/installer.Kind (now no-op)
//   - kind_bootstrap_state_backup_namespaces          — BackupNamespaces    (L2287-L2296)
//   - kind_bootstrap_state_backup_write_kind_dir      — writeKindDir        (L2157-L2284)
//   - kind_bootstrap_state_backup                     — Backup              (L2300-L2446)
//   - kind_bootstrap_state_restore                    — Restore             (L2450-L2600)
//
// TODO: cluster-create / cluster-delete / get-clusters / kubeconfig-export
// will move here, fronted by `sigs.k8s.io/kind/pkg/cluster.Provider`. The
// import is intentionally not added in this revision because go.mod is owned
// by another slice of the rewrite — see the kind library audit notes in the
// review. Once the dependency is added, the wrappers should look roughly
// like:
//
//	func CreateCluster(name string, configBytes []byte) error {
//		p := cluster.NewProvider(cluster.ProviderWithLogger(noopLogger{}))
//		opts := []cluster.CreateOption{}
//		if len(configBytes) > 0 {
//			opts = append(opts, cluster.CreateWithRawConfig(configBytes))
//		}
//		return p.Create(name, opts...)
//	}
//	func DeleteCluster(name string) error            { return cluster.NewProvider().Delete(name, "") }
//	func ListClusters() ([]string, error)            { return cluster.NewProvider().List() }
//	func KubeConfig(name string) (string, error)     { return cluster.NewProvider().KubeConfig(name, false) }
//
// Until those land, the existing `shell.Run("kind", …)` / `shell.Capture(
// "kind", …)` callers in internal/bootstrap/*.go continue to drive the kind
// binary; with installer.Kind() now a no-op, those call sites either need to
// install kind themselves or be migrated to the wrappers above.
package kind