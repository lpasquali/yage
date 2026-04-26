// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package csi is yage's add-on registry for Container Storage
// Interface drivers, mirroring the shape of internal/provider/. It
// promotes CSI from "Proxmox-only hardcoded" to "registered add-on
// driver, picked per cluster, with per-provider defaults" per
// docs/abstraction-plan.md §20.
//
// The Provider interface (internal/provider) ships ONE
// EnsureCSISecret hook, which assumes one CSI per provider and a
// Secret-driven install. Both assumptions break in the multi-cloud
// era: AWS commonly runs EBS + EFS together, Helm is the dominant
// install path for hyperscale CSIs, and cross-provider drivers
// (Rook, Longhorn, OpenEBS) don't belong to any single Provider
// plugin. So CSI gets its own registry.
//
// A Driver is any package that implements this Driver interface and
// registers itself in init() via Register(). The orchestrator
// resolves the active driver list via Selector(cfg) — either an
// explicit cfg.CSI.Drivers list or the per-provider defaults table
// in defaults.go. Each driver describes its Helm chart, renders its
// values YAML, and (where applicable) ensures a per-driver Secret on
// the workload cluster.
//
// Phase F (this commit, scoped) ships AWS-EBS, Azure-Disk, and
// GCP-PD. The remaining drivers in §20.1 land in follow-ups.
package csi

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/ui/plan"
)

// ErrNotApplicable signals a driver hook that doesn't apply to the
// active configuration — caller skips silently. Mirrors
// provider.ErrNotApplicable so call sites can treat the two seams
// identically.
var ErrNotApplicable = errors.New("csi: hook not applicable to this driver")

// Driver is the interface every CSI add-on implementation must
// satisfy. The set of methods mirrors the slice of Provider that
// CSI needs (Helm chart location, values rendering, optional
// per-driver Secret, plan-output bullets) without coupling to the
// Provider interface itself — drivers are not providers.
type Driver interface {
	// Name is the stable internal id ("aws-ebs", "azure-disk",
	// "gcp-pd", "longhorn", "rook-ceph", …). Used as the registry
	// key. Lowercase + hyphenated by convention.
	Name() string

	// K8sCSIDriverName is the value that appears in the Kubernetes
	// CSIDriver object's spec, e.g. "ebs.csi.aws.com". Operators
	// recognize this from `kubectl describe csidriver`. Useful for
	// dry-run output and for verifying an install completed.
	K8sCSIDriverName() string

	// Defaults returns the list of provider names this driver is the
	// default choice for. Empty slice = cross-provider opt-in (Rook,
	// Longhorn, OpenEBS). The DefaultsFor() helper inverts this map
	// at lookup time so adding a driver requires no central edit.
	Defaults() []string

	// HelmChart returns the chart's repo URL, name, and pinned
	// version. Drivers MUST pin a known-good version so re-running
	// yage on the same config produces the same workload state. A
	// non-nil error here means "this driver isn't installable as a
	// Helm chart at all" — the caller should fall back to a
	// manifest-based install or surface the gap.
	HelmChart(cfg *config.Config) (repo, chart, version string, err error)

	// RenderValues produces the Helm values YAML for this driver's
	// chart, taking the active config (region, instance metadata,
	// secrets, identity model, etc.). Returned string is fed
	// verbatim to `helm install -f -` (or the CAAPH equivalent).
	RenderValues(cfg *config.Config) (string, error)

	// EnsureSecret pushes any per-driver Secret to the workload
	// cluster. Drivers that authenticate via cloud-native identity
	// (IRSA, Workload Identity, Managed Identity, Instance
	// Principal) return nil OR ErrNotApplicable — both are
	// equivalent to the orchestrator. Drivers that need a
	// JSON/key/token Secret apply it via the workload kubeconfig
	// path provided.
	EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error

	// DefaultStorageClass returns the StorageClass name yage
	// creates / labels as default for this driver. "" = the driver
	// doesn't ship a default class (operator picks via Helm values
	// or out-of-band). When multiple drivers install on the same
	// cluster, cfg.CSI.DefaultClass arbitrates which one wins —
	// otherwise the first driver in cfg.CSI.Drivers wins.
	DefaultStorageClass() string

	// DescribeInstall emits the dry-run plan-output bullets that
	// describe what this driver will install. Mirrors
	// provider.PlanDescriber but at the CSI-add-on level — the
	// orchestrator interleaves these into the workload section.
	DescribeInstall(w plan.Writer, cfg *config.Config)
}

// --- registry ---

var (
	mu      sync.RWMutex
	drivers = map[string]Driver{}
)

// Register adds a Driver instance under its Name(). Idempotent:
// re-registering the same Driver value (pointer-equal) is a no-op;
// registering a DIFFERENT driver under the same name panics — that's
// a programmer error, fail at start-up not at runtime.
//
// Drivers call this from init() so importing the driver package is
// enough to make it visible to the orchestrator. cmd/yage's blank
// import block lists every driver yage ships.
func Register(d Driver) {
	if d == nil {
		panic("csi: Register called with nil Driver")
	}
	mu.Lock()
	defer mu.Unlock()
	name := d.Name()
	if existing, ok := drivers[name]; ok {
		if existing == d {
			return // idempotent: same instance re-registered
		}
		panic("csi: duplicate registration for " + name)
	}
	drivers[name] = d
}

// Get returns a registered Driver by name, or an error listing the
// available alternatives. Returned Driver values are stateless;
// callers may share a single instance across phases.
func Get(name string) (Driver, error) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := drivers[name]
	if !ok {
		return nil, fmt.Errorf("csi: driver %q not registered (have: %v)", name, registered())
	}
	return d, nil
}

// Registered lists the names of every registered Driver, sorted.
// Used by `--help` / dry-run plan output to surface what's
// available.
func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	return registered()
}

func registered() []string {
	out := make([]string, 0, len(drivers))
	for k := range drivers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}