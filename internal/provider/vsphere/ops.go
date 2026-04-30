// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package vsphere

import (
	"fmt"
	"path"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// EnsureGroup creates (or verifies the existence of) the vSphere VM
// folder identified by name. Idempotent: if the folder already exists
// the call is a no-op.
//
// Returns ErrNotApplicable when name is empty — the operator has not
// configured a target folder, so yage skips the step silently.
func (p *Provider) EnsureGroup(cfg *config.Config, name string) error {
	if name == "" {
		return provider.ErrNotApplicable
	}

	sess, err := vsphereConnect(cfg)
	if err != nil {
		return fmt.Errorf("vsphere EnsureGroup: %w", err)
	}
	defer sess.Close()

	finder := find.NewFinder(sess.client.Client, true)
	dc, err := finder.DefaultDatacenter(sess.ctx)
	if err != nil {
		return fmt.Errorf("vsphere EnsureGroup: find datacenter: %w", err)
	}
	finder.SetDatacenter(dc)

	// Idempotency check: if the folder already exists, nothing to do.
	if _, err := finder.Folder(sess.ctx, name); err == nil {
		logx.Log("vSphere folder %q already exists.", name)
		return nil
	}

	// Create the folder as a child of the datacenter's VM folder.
	dcFolders, err := dc.Folders(sess.ctx)
	if err != nil {
		return fmt.Errorf("vsphere EnsureGroup: get datacenter folders: %w", err)
	}
	if _, err := dcFolders.VmFolder.CreateFolder(sess.ctx, path.Base(name)); err != nil {
		return fmt.Errorf("vsphere EnsureGroup: create folder %q: %w", name, err)
	}
	logx.Log("vSphere folder %q created.", name)
	return nil
}

// Inventory returns the ResourcePool capacity for the pool configured
// in cfg.Providers.Vsphere.ResourcePool, expressed as
// provider.Inventory (Total / Used / Available). Storage is
// aggregated across all datastores visible to the finder.
//
// CPU: MaxUsage / OverallUsage are in MHz; the conversion from MHz to
// cores requires host CPU speed (a separate govmomi query). Instead,
// the raw MHz figures are surfaced in Notes and Cores is left as zero
// per the task spec comment in §13.4 #1.
//
// Memory: MaxUsage / OverallUsage are in bytes (not MB despite some
// older govmomi docs). Divided by 1 MiB to produce MiB values.
//
// Returns ErrNotApplicable when ResourcePool is not configured or
// when the pool has unlimited CPU or memory (MaxUsage == -1).
func (p *Provider) Inventory(cfg *config.Config) (*provider.Inventory, error) {
	if cfg.Providers.Vsphere.ResourcePool == "" {
		return nil, provider.ErrNotApplicable
	}

	sess, err := vsphereConnect(cfg)
	if err != nil {
		return nil, fmt.Errorf("vsphere Inventory: %w", err)
	}
	defer sess.Close()

	finder := find.NewFinder(sess.client.Client, true)
	dc, err := finder.DefaultDatacenter(sess.ctx)
	if err != nil {
		return nil, fmt.Errorf("vsphere Inventory: find datacenter: %w", err)
	}
	finder.SetDatacenter(dc)

	// Resolve the ResourcePool managed object reference.
	rp, err := finder.ResourcePool(sess.ctx, cfg.Providers.Vsphere.ResourcePool)
	if err != nil {
		return nil, fmt.Errorf("vsphere Inventory: find ResourcePool %q: %w",
			cfg.Providers.Vsphere.ResourcePool, err)
	}

	// Retrieve the pool summary (runtime CPU + memory usage + limits).
	pc := property.DefaultCollector(sess.client.Client)
	var moRP mo.ResourcePool
	if err := pc.RetrieveOne(sess.ctx, rp.Reference(), []string{"summary"}, &moRP); err != nil {
		return nil, fmt.Errorf("vsphere Inventory: retrieve ResourcePool properties: %w", err)
	}

	s := moRP.Summary.GetResourcePoolSummary()
	cpuMax := s.Runtime.Cpu.MaxUsage       // MHz
	cpuUsed := s.Runtime.Cpu.OverallUsage  // MHz
	memMax := s.Runtime.Memory.MaxUsage    // bytes
	memUsed := s.Runtime.Memory.OverallUsage // bytes

	if cpuMax == -1 || memMax == -1 {
		// Unlimited pool — can't express as flat ResourceTotals.
		return nil, provider.ErrNotApplicable
	}

	const mib = int64(1024 * 1024)

	inv := &provider.Inventory{
		Total: provider.ResourceTotals{
			MemoryMiB: memMax / mib,
		},
		Used: provider.ResourceTotals{
			MemoryMiB: memUsed / mib,
		},
		Available: provider.ResourceTotals{
			MemoryMiB: (memMax - memUsed) / mib,
		},
		Notes: []string{
			fmt.Sprintf("CPU: %d/%d MHz available", cpuMax-cpuUsed, cpuMax),
		},
	}

	// Storage: enumerate all datastores and aggregate free / total.
	dsList, err := finder.DatastoreList(sess.ctx, "*")
	if err == nil {
		storageByClass := make(map[string]int64)
		var totalBytes, freeBytes int64
		for _, ds := range dsList {
			var moDS mo.Datastore
			if err := pc.RetrieveOne(sess.ctx, ds.Reference(), []string{"summary"}, &moDS); err != nil {
				continue
			}
			if !moDS.Summary.Accessible {
				continue
			}
			name := moDS.Summary.Name
			totalBytes += moDS.Summary.Capacity
			freeBytes += moDS.Summary.FreeSpace
			storageByClass[name] = moDS.Summary.FreeSpace / (1024 * 1024 * 1024)
		}
		const gib = int64(1024 * 1024 * 1024)
		inv.Total.StorageGiB = totalBytes / gib
		inv.Used.StorageGiB = (totalBytes - freeBytes) / gib
		inv.Available.StorageGiB = freeBytes / gib
		if len(storageByClass) > 0 {
			inv.Available.StorageByClass = storageByClass
		}
	}

	return inv, nil
}
