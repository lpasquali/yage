// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package kind hosts kind cluster lifecycle + bootstrap-state
// backup/restore functions.
//
// TODO: cluster-create / cluster-delete / get-clusters /
// kubeconfig-export will live here, fronted by
// `sigs.k8s.io/kind/pkg/cluster.Provider`. Until that lands the
// orchestrator drives the kind binary directly via
// `shell.Run("kind", …)`.
package kind