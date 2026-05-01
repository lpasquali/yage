// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import "context"

// Runner is the common interface for executing OpenTofu modules. A module
// is the subdirectory name within the cloned yage-tofu repo (e.g.
// "registry", "issuing-ca", "proxmox").
//
// Two implementations are provided:
//   - LocalRunner: runs `tofu` as a local subprocess (dev/test; used before
//     a management cluster exists).
//   - JobRunner: creates Kubernetes resources (ConfigMap, Secret, Job)
//     on the management cluster and streams pod logs. State is stored in
//     Kubernetes Secrets via the OpenTofu kubernetes backend (no PVCs).
type Runner interface {
	// Apply runs `tofu init && tofu apply -auto-approve` with the given
	// variable map, creating or updating resources.
	Apply(ctx context.Context, module string, vars map[string]string) error

	// Destroy runs `tofu destroy -auto-approve`, removing resources managed
	// by the given module.
	Destroy(ctx context.Context, module string) error

	// Output runs `tofu output -json` and returns the decoded map. The map
	// values are JSON-typed (string, float64, bool, map, slice, etc.) as
	// returned by encoding/json.Unmarshal.
	Output(ctx context.Context, module string) (map[string]any, error)
}
