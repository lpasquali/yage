// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
)

// Fetcher resolves yage-tofu module paths from the in-cluster yage-repos PVC.
// The PVC is mounted at /repos inside Job pods. Use EnsureRepoSync (issue #144)
// to populate it before calling ModulePath.
type Fetcher struct {
	// MountRoot is the path at which the yage-repos PVC is mounted in the
	// current pod. Defaults to /repos when empty.
	MountRoot string
}

// ModulePath returns the absolute path to the named module directory.
// e.g. ModulePath("proxmox") → "/repos/yage-tofu/proxmox"
func (f *Fetcher) ModulePath(module string) string {
	root := f.MountRoot
	if root == "" {
		root = "/repos"
	}
	return filepath.Join(root, "yage-tofu", module)
}

// EnsureRepoSync creates the yage-repos PVC (if absent) and runs the
// yage-repo-sync Job to clone/fetch yage-tofu and yage-manifests.
// Full implementation is in issue #144; stub here to establish the
// function signature in this package.
func EnsureRepoSync(ctx context.Context, cli *k8sclient.Client, cfg *config.Config) error {
	return fmt.Errorf("EnsureRepoSync: not yet implemented (see issue #144)")
}
