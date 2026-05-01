// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/ui/logx"
)

const (
	tofuRepoURL   = "https://github.com/lpasquali/yage-tofu"
	tofuCacheDir  = "tofu-cache"
)

// Fetcher clones or updates the yage-tofu repository and provides the
// local path to a named module within it.
type Fetcher struct {
	cfg *config.Config
}

// NewFetcher returns a Fetcher backed by the given config.
func NewFetcher(cfg *config.Config) *Fetcher {
	return &Fetcher{cfg: cfg}
}

// cacheRoot returns ~/.yage/tofu-cache/
func cacheRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".yage", tofuCacheDir)
}

// Fetch clones yage-tofu at cfg.TofuRef if the cache directory does not exist,
// or fetches + checks out cfg.TofuRef if it does. Returns the local path to
// the cache root (i.e. the repository root).
func (f *Fetcher) Fetch() (string, error) {
	root := cacheRoot()
	ref := f.cfg.TofuRef
	if ref == "" {
		ref = "main"
	}

	if _, err := os.Stat(filepath.Join(root, ".git")); os.IsNotExist(err) {
		// First time: clone the repository.
		logx.Log("Fetcher: cloning %s at ref %s to %s ...", tofuRepoURL, ref, root)
		if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
			return "", fmt.Errorf("fetcher: mkdir %s: %w", filepath.Dir(root), err)
		}
		if _, _, err := shell.Capture("git", "clone", "--branch", ref, "--single-branch", "--depth=1", tofuRepoURL, root); err != nil {
			// Fall back to cloning the default branch and checking out the ref.
			logx.Warn("Fetcher: shallow clone at ref %q failed; trying default branch then checkout.", ref)
			if _, _, cloneErr := shell.Capture("git", "clone", "--depth=1", tofuRepoURL, root); cloneErr != nil {
				return "", fmt.Errorf("fetcher: git clone: %w", cloneErr)
			}
			if _, _, checkErr := shell.CaptureIn(root, "git", "fetch", "--depth=1", "origin", ref); checkErr != nil {
				return "", fmt.Errorf("fetcher: git fetch ref %q: %w", ref, checkErr)
			}
			if _, _, checkErr := shell.CaptureIn(root, "git", "checkout", "FETCH_HEAD"); checkErr != nil {
				return "", fmt.Errorf("fetcher: git checkout FETCH_HEAD: %w", checkErr)
			}
		}
		return root, nil
	}

	// Cache exists: fetch latest and checkout the desired ref.
	logx.Log("Fetcher: updating yage-tofu cache at %s (ref %s) ...", root, ref)
	if _, _, err := shell.CaptureIn(root, "git", "fetch", "origin"); err != nil {
		logx.Warn("Fetcher: git fetch failed (%v); continuing with cached content.", err)
		return root, nil
	}
	if _, _, err := shell.CaptureIn(root, "git", "checkout", ref); err != nil {
		// Try as a remote branch.
		if _, _, err2 := shell.CaptureIn(root, "git", "checkout", "origin/"+ref); err2 != nil {
			logx.Warn("Fetcher: could not checkout ref %q (%v); continuing with cached content.", ref, err)
		}
	}
	return root, nil
}

// ModulePath returns the local filesystem path to a module within the cached
// yage-tofu repository. The caller must have called Fetch() first (or supply
// a cacheRoot returned by a previous Fetch call).
func ModulePath(cacheRootPath, module string) string {
	return filepath.Join(cacheRootPath, module)
}
