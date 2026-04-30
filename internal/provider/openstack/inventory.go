// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package openstack

// gophercloud-backed PatchManifest + Inventory for the OpenStack provider.
//
// Auth strategy: try OS_* environment variables via openstack.AuthOptionsFromEnv()
// first. clouds.yaml support (via gophercloud/utils/v2 clientconfig) is NOT
// included — that package is a separate dependency not yet in go.mod. When
// cfg.Providers.OpenStack.Cloud is set but OS_AUTH_URL is absent the user must
// export OS_AUTH_URL/OS_USERNAME/OS_PASSWORD/OS_PROJECT_NAME etc. themselves
// (the same values clouds.yaml would supply). A follow-up PR can wire
// gophercloud/utils for full clouds.yaml resolution.
//
// Failure modes: if neither env-var nor any future clouds.yaml path provides
// valid credentials, openstackClients returns an error. Callers in
// PatchManifest and Inventory handle this gracefully — PatchManifest warns +
// returns nil, Inventory returns ErrNotApplicable.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/limits"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/logx"
)

const openstackAPITimeout = 30 * time.Second

// openstackClients authenticates and returns a Nova compute client, a Cinder
// block-storage client, and the active project ID. Auth is attempted via
// OS_* environment variables (openstack.AuthOptionsFromEnv). Returns an error
// when neither source has enough credentials to authenticate.
func openstackClients(ctx context.Context, cfg *config.Config) (compute, block *gophercloud.ServiceClient, projectID string, err error) {
	ao, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		return nil, nil, "", fmt.Errorf("openstack auth: %w (set OS_AUTH_URL, OS_USERNAME, OS_PASSWORD, OS_PROJECT_NAME, OS_DOMAIN_NAME)", err)
	}

	// Promote the project name from cfg when not already set via env.
	if ao.TenantName == "" && cfg.Providers.OpenStack.ProjectName != "" {
		ao.TenantName = cfg.Providers.OpenStack.ProjectName
	}

	provider, err := openstack.AuthenticatedClient(ctx, ao)
	if err != nil {
		return nil, nil, "", fmt.Errorf("openstack authenticate: %w", err)
	}

	eopts := gophercloud.EndpointOpts{
		Region: cfg.Providers.OpenStack.Region,
	}
	compute, err = openstack.NewComputeV2(provider, eopts)
	if err != nil {
		return nil, nil, "", fmt.Errorf("openstack NewComputeV2: %w", err)
	}
	block, err = openstack.NewBlockStorageV3(provider, eopts)
	if err != nil {
		return nil, nil, "", fmt.Errorf("openstack NewBlockStorageV3: %w", err)
	}

	// Derive project ID from the tenant/project fields. OS_PROJECT_ID /
	// OS_TENANT_ID are the canonical sources; the AuthOptions.TenantID is
	// set from those by AuthOptionsFromEnv. Leave empty when not available
	// — the Cinder caller already guards on projectID != "".
	switch {
	case ao.TenantID != "":
		projectID = ao.TenantID
	case os.Getenv("OS_PROJECT_ID") != "":
		projectID = os.Getenv("OS_PROJECT_ID")
	case os.Getenv("OS_TENANT_ID") != "":
		projectID = os.Getenv("OS_TENANT_ID")
	}

	return compute, block, projectID, nil
}

// PatchManifest implements provider.Provider.PatchManifest for CAPO.
//
// When both ControlPlaneFlavor and WorkerFlavor are already explicit names
// (set via env or config), the template vars already carry them and no
// manifest rewrite is needed — return nil immediately.
//
// Otherwise, authenticate against Nova, list all flavors, and find the
// smallest flavor where vcpus >= requested cores AND ram >= requested memory
// for each role. The resolved names are written to cfg (so TemplateVars picks
// them up for this run) AND patched directly into the on-disk manifest
// (because TemplateVars substitution already ran before PatchManifest is
// called). Sizing fields come from cfg.Providers.Proxmox.{ControlPlane,Worker}
// {NumCores,MemoryMiB} — those are the canonical per-role sizing fields shared
// across on-prem providers today.
//
// If the Nova API is unreachable the function warns and returns nil (non-fatal)
// so the bootstrap continues with whatever flavor the caller already set.
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	// If both flavors are already set, nothing to resolve.
	if cfg.Providers.OpenStack.ControlPlaneFlavor != "" &&
		cfg.Providers.OpenStack.WorkerFlavor != "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), openstackAPITimeout)
	defer cancel()

	computeClient, _, _, err := openstackClients(ctx, cfg)
	if err != nil {
		logx.Warn("openstack PatchManifest: skipping flavor resolution — %v", err)
		return nil
	}

	allPages, err := flavors.ListDetail(computeClient, flavors.ListOpts{}).AllPages(ctx)
	if err != nil {
		logx.Warn("openstack PatchManifest: failed to list flavors — %v; skipping", err)
		return nil
	}
	allFlavors, err := flavors.ExtractFlavors(allPages)
	if err != nil {
		logx.Warn("openstack PatchManifest: failed to extract flavors — %v; skipping", err)
		return nil
	}

	// Sort ascending by (vcpus, ram) so we always find the smallest fit.
	sort.Slice(allFlavors, func(i, j int) bool {
		if allFlavors[i].VCPUs != allFlavors[j].VCPUs {
			return allFlavors[i].VCPUs < allFlavors[j].VCPUs
		}
		return allFlavors[i].RAM < allFlavors[j].RAM
	})

	if cfg.Providers.OpenStack.ControlPlaneFlavor == "" {
		cores := parseIntField(cfg.Providers.Proxmox.ControlPlaneNumCores, 2)
		memMiB := parseIntField(cfg.Providers.Proxmox.ControlPlaneMemoryMiB, 4096)
		name := bestFitFlavor(allFlavors, cores, memMiB)
		if name == "" {
			logx.Warn("openstack PatchManifest: no flavor with >= %d vCPUs and >= %d MiB RAM for control-plane; leaving placeholder", cores, memMiB)
		} else {
			logx.Log("openstack: resolved control-plane flavor %q (>= %d vCPUs, >= %d MiB RAM)", name, cores, memMiB)
			cfg.Providers.OpenStack.ControlPlaneFlavor = name
		}
	}

	if cfg.Providers.OpenStack.WorkerFlavor == "" {
		cores := parseIntField(cfg.Providers.Proxmox.WorkerNumCores, 2)
		memMiB := parseIntField(cfg.Providers.Proxmox.WorkerMemoryMiB, 2048)
		name := bestFitFlavor(allFlavors, cores, memMiB)
		if name == "" {
			logx.Warn("openstack PatchManifest: no flavor with >= %d vCPUs and >= %d MiB RAM for worker; leaving placeholder", cores, memMiB)
		} else {
			logx.Log("openstack: resolved worker flavor %q (>= %d vCPUs, >= %d MiB RAM)", name, cores, memMiB)
			cfg.Providers.OpenStack.WorkerFlavor = name
		}
	}

	// Patch the rendered manifest on disk. At this point TemplateVars has
	// already been substituted (the placeholders are gone), so we must
	// rewrite the flavor: field directly in each OpenStackMachineTemplate
	// document. Two documents exist: the control-plane one (name contains
	// "control-plane") and the worker one (name contains "md-0").
	if cfg.Providers.OpenStack.ControlPlaneFlavor != "" || cfg.Providers.OpenStack.WorkerFlavor != "" {
		if err := patchFlavorInManifest(manifestPath,
			cfg.Providers.OpenStack.ControlPlaneFlavor,
			cfg.Providers.OpenStack.WorkerFlavor); err != nil {
			logx.Warn("openstack PatchManifest: failed to rewrite manifest — %v", err)
		}
	}

	return nil
}

// patchFlavorInManifest rewrites the spec.template.spec.flavor field in every
// OpenStackMachineTemplate document. Two documents are expected:
//   - metadata.name containing "control-plane" → cpFlavor
//   - metadata.name containing "md-0"          → workerFlavor
//
// Documents not matching OpenStackMachineTemplate are left unchanged.
// An empty flavor string means "leave this document's flavor as-is".
func patchFlavorInManifest(manifestPath, cpFlavor, workerFlavor string) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}
	text := string(raw)

	// Line-anchored kind match — never strings.Contains (infrastructureRef
	// embeds the same kind string nested).
	kindRE := regexp.MustCompile(`(?m)^kind:\s*OpenStackMachineTemplate\s*$`)
	nameRE := regexp.MustCompile(`(?m)^  name:\s*(\S+)\s*$`)
	flavorRE := regexp.MustCompile(`(?m)^      flavor:\s*\S+\s*$`)

	docs := strings.Split(text, "\n---\n")
	for i, doc := range docs {
		if !kindRE.MatchString(doc) {
			continue
		}
		m := nameRE.FindStringSubmatch(doc)
		if m == nil {
			continue
		}
		docName := m[1]
		var targetFlavor string
		switch {
		case strings.Contains(docName, "control-plane") && cpFlavor != "":
			targetFlavor = cpFlavor
		case strings.Contains(docName, "md-") && workerFlavor != "":
			targetFlavor = workerFlavor
		default:
			continue
		}
		docs[i] = flavorRE.ReplaceAllString(doc, "      flavor: "+targetFlavor)
	}

	return os.WriteFile(manifestPath, []byte(strings.Join(docs, "\n---\n")), 0o644)
}

// Inventory returns the OpenStack per-project quota totals and usage via Nova
// limits and Cinder quota APIs. The Available field is Total − Used, valid for
// the flat per-project OpenStack quota model (per §13.4 #1).
//
// Returns ErrNotApplicable when:
//   - credentials are unavailable (no OS_* env vars configured), or
//   - Nova reports unlimited quota (MaxTotalCores == -1 indicates "no limit").
//
// The Cinder quota fetch is best-effort — a Cinder failure adds a Note but
// does not prevent the Inventory from returning the Nova result.
func (p *Provider) Inventory(cfg *config.Config) (*provider.Inventory, error) {
	ctx, cancel := context.WithTimeout(context.Background(), openstackAPITimeout)
	defer cancel()

	computeClient, blockClient, projectID, err := openstackClients(ctx, cfg)
	if err != nil {
		// No credentials configured — skip capacity preflight.
		return nil, provider.ErrNotApplicable
	}

	result := limits.Get(ctx, computeClient, limits.GetOpts{})
	lims, err := result.Extract()
	if err != nil {
		return nil, fmt.Errorf("openstack Nova limits: %w", err)
	}

	// -1 means unlimited quota — can't express as flat ResourceTotals.
	if lims.Absolute.MaxTotalCores == -1 {
		return nil, provider.ErrNotApplicable
	}

	total := provider.ResourceTotals{
		Cores:     lims.Absolute.MaxTotalCores,
		MemoryMiB: int64(lims.Absolute.MaxTotalRAMSize),
	}
	used := provider.ResourceTotals{
		Cores:     lims.Absolute.TotalCoresUsed,
		MemoryMiB: int64(lims.Absolute.TotalRAMUsed),
	}
	avail := provider.ResourceTotals{
		Cores:     total.Cores - used.Cores,
		MemoryMiB: total.MemoryMiB - used.MemoryMiB,
	}

	var notes []string
	notes = append(notes, "quota-based (not hardware totals)")

	// Cinder block-storage quota — best-effort.
	if projectID != "" && blockClient != nil {
		qs, cerr := quotasets.Get(ctx, blockClient, projectID).Extract()
		if cerr != nil {
			notes = append(notes, "cinder quota unavailable: "+cerr.Error())
		} else if qs != nil {
			total.StorageGiB = int64(qs.Gigabytes)
			avail.StorageGiB = int64(qs.Gigabytes)
			// QuotaSet.Get doesn't return usage; for the detailed form use
			// GetUsage. Attempt it but treat failures as non-fatal.
			qsu, uerr := quotasets.GetUsage(ctx, blockClient, projectID).Extract()
			if uerr == nil {
				used.StorageGiB = int64(qsu.Gigabytes.InUse)
				avail.StorageGiB = int64(qs.Gigabytes) - int64(qsu.Gigabytes.InUse)
			}
		}
	}

	return &provider.Inventory{
		Total:     total,
		Used:      used,
		Available: avail,
		Notes:     notes,
	}, nil
}

// --- helpers ---

// bestFitFlavor returns the name of the smallest flavor (fewest vCPUs, then
// least RAM) where vcpus >= minCores and ram >= minMemMiB. Returns "" when no
// flavor meets the criteria. allFlavors must be pre-sorted ascending by
// (vcpus, ram).
func bestFitFlavor(allFlavors []flavors.Flavor, minCores, minMemMiB int) string {
	for _, f := range allFlavors {
		if f.VCPUs >= minCores && f.RAM >= minMemMiB {
			return f.Name
		}
	}
	return ""
}

// parseIntField parses a string integer field (as used in config), returning
// fallback when the string is empty or unparseable.
func parseIntField(s string, fallback int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}
