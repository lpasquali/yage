// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package proxmox

// Proxmox inventory queries: host hardware totals + currently
// running VM usage. These are Proxmox-specific (they hit
// /api2/json/cluster/resources directly), and the plugin abstraction
// keeps cloud-specific queries inside the cloud's package. The
// orchestrator calls Provider.Inventory which composes both into a
// single provider.Inventory result.

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/provider/proxmox/pveapi"
)

// Inventory returns the cloud-correct picture of "what's there +
// what's free" for this Proxmox cluster: host hardware totals,
// running-VM usage, and the headroom (Total − Used, valid for the
// flat-pool Proxmox model). Callers see one method with the
// cloud-specific arithmetic encapsulated inside the provider.
//
// Per §13.4 #1: Proxmox is a flat-pool cloud, so Available =
// Total − Used is correct. Other providers (AWS, Azure, Hetzner, …)
// return ErrNotApplicable from Inventory because their quota model
// can't be expressed as flat ResourceTotals.
func (p *Provider) Inventory(cfg *config.Config) (*provider.Inventory, error) {
	hc, err := fetchHostCapacity(cfg)
	if err != nil {
		return nil, err
	}
	used, uerr := fetchExistingUsage(cfg)
	// fetchExistingUsage failure is not fatal: an empty host returns
	// a zero-VM result, and a transient API hiccup shouldn't block a
	// preflight that already has the host totals. Surface as a Note.
	var usageNote string
	if uerr != nil {
		used = &existingUsage{ByPool: map[string]int{}}
		usageNote = "existing-VM census skipped: " + uerr.Error()
	}

	total := provider.ResourceTotals{
		Cores:          hc.CPUCores,
		MemoryMiB:      hc.MemoryMiB,
		StorageGiB:     hc.StorageGB,
		StorageByClass: copyStorageMap(hc.StorageBy),
	}
	usedTotals := provider.ResourceTotals{
		Cores:      used.CPUCores,
		MemoryMiB:  used.MemoryMiB,
		StorageGiB: used.StorageGB,
	}
	avail := provider.ResourceTotals{
		Cores:      total.Cores - usedTotals.Cores,
		MemoryMiB:  total.MemoryMiB - usedTotals.MemoryMiB,
		StorageGiB: total.StorageGiB - usedTotals.StorageGiB,
	}

	var notes []string
	if used.VMCount > 0 {
		notes = append(notes, fmt.Sprintf("existing VMs: %d (cores %d, mem %d MiB, disk %d GB)",
			used.VMCount, used.CPUCores, used.MemoryMiB, used.StorageGB))
		if len(used.ByPool) > 1 {
			parts := make([]string, 0, len(used.ByPool))
			for pool, n := range used.ByPool {
				parts = append(parts, fmt.Sprintf("%s=%d", pool, n))
			}
			sort.Strings(parts)
			notes = append(notes, "VMs by pool: "+strings.Join(parts, ", "))
		}
	}
	if usageNote != "" {
		notes = append(notes, usageNote)
	}

	return &provider.Inventory{
		Total:     total,
		Used:      usedTotals,
		Available: avail,
		Hosts:     append([]string(nil), hc.Nodes...), // typed pass-through
		Notes:     notes,
	}, nil
}

func copyStorageMap(in map[string]int64) map[string]int64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// hostCapacity is the aggregate of CPU + memory + storage across
// all Proxmox nodes that are eligible for VM placement, after
// filtering by the configured AllowedNodes (or just ProxmoxNode
// when AllowedNodes is empty). Package-private — the orchestrator
// only sees provider.Inventory; this lives here so the proxmox
// package can build that Inventory in one place.
type hostCapacity struct {
	Nodes     []string
	CPUCores  int   // total physical cores
	MemoryMiB int64 // total memory in MiB
	StorageGB int64 // total storage capacity in GB across the cluster
	StorageBy map[string]int64
}

// existingUsage is what's already provisioned on the Proxmox host
// before the planned bootstrap runs. Aggregated from the VM list
// (`/api2/json/cluster/resources?type=vm`) — every VM on the
// allowed nodes contributes its `maxcpu / maxmem / maxdisk` (the
// VM's *configured* size, not its current load).
type existingUsage struct {
	VMCount   int
	CPUCores  int
	MemoryMiB int64
	StorageGB int64
	// ByPool groups VMs by Proxmox pool so the dry-run can show
	// "5 VMs in 'capi-quickstart', 3 VMs in 'other-cluster'".
	ByPool map[string]int
}

// fetchHostCapacity calls Proxmox /api2/json/cluster/resources?type=node
// and aggregates CPU + memory across allowed nodes. Storage is
// fetched from /api2/json/cluster/resources?type=storage and filtered
// to the configured CSI/cloudinit backend names. Caller must have
// valid admin or clusterctl creds set on cfg.
func fetchHostCapacity(cfg *config.Config) (*hostCapacity, error) {
	auth, insecure, base, err := authForCfg(cfg)
	if err != nil {
		return nil, err
	}
	nodesURL := base + "/api2/json/cluster/resources?type=node"

	var nodesEnv struct {
		Data []struct {
			Node    string `json:"node"`
			MaxCPU  int    `json:"maxcpu"`
			MaxMem  int64  `json:"maxmem"`
			MaxDisk int64  `json:"maxdisk"`
			Status  string `json:"status"`
		} `json:"data"`
	}
	if err := fetchJSON(nodesURL, auth, insecure, &nodesEnv); err != nil {
		return nil, fmt.Errorf("query Proxmox /cluster/resources: %w", err)
	}

	allowed := allowedSet(cfg)
	hc := &hostCapacity{StorageBy: map[string]int64{}}
	for _, n := range nodesEnv.Data {
		if n.Status != "online" {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[n.Node]; !ok {
				continue
			}
		}
		hc.Nodes = append(hc.Nodes, n.Node)
		hc.CPUCores += n.MaxCPU
		hc.MemoryMiB += n.MaxMem / (1024 * 1024)
	}

	// Storage: aggregate the configured Proxmox storage backend (the
	// VM disk store). cloudinit-storage and other shared backends are
	// reported but we only sum the data store the CSI / VMs actually
	// consume.
	storageURL := base + "/api2/json/cluster/resources?type=storage"
	var storageEnv struct {
		Data []struct {
			Storage string `json:"storage"`
			Node    string `json:"node"`
			Type    string `json:"type"`
			MaxDisk int64  `json:"maxdisk"`
			Shared  int    `json:"shared"`
			Status  string `json:"status"`
		} `json:"data"`
	}
	if err := fetchJSON(storageURL, auth, insecure, &storageEnv); err == nil {
		want := strings.TrimSpace(cfg.Providers.Proxmox.CSIStorage)
		// Avoid double-counting shared storage across nodes.
		seen := map[string]bool{}
		for _, s := range storageEnv.Data {
			if s.Status != "available" && s.Status != "" {
				continue
			}
			if want != "" && s.Storage != want {
				continue
			}
			if len(allowed) > 0 {
				if _, ok := allowed[s.Node]; !ok {
					continue
				}
			}
			key := s.Storage
			if s.Shared == 0 {
				key = s.Node + ":" + s.Storage
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			gb := s.MaxDisk / (1024 * 1024 * 1024)
			hc.StorageGB += gb
			hc.StorageBy[s.Storage] += gb
		}
	}

	if len(hc.Nodes) == 0 {
		return nil, fmt.Errorf("no eligible Proxmox nodes (allowed=%v); check PROXMOX_NODE / ALLOWED_NODES + node status", strings.Join(allowedSlice(allowed), ","))
	}
	return hc, nil
}

// fetchExistingUsage queries every VM on the allowed nodes and
// aggregates their declared (max*) resources. Returns an empty
// existingUsage on a fresh host. Caller must have valid creds
// just like fetchHostCapacity.
func fetchExistingUsage(cfg *config.Config) (*existingUsage, error) {
	auth, insecure, base, err := authForCfg(cfg)
	if err != nil {
		return nil, err
	}
	url := base + "/api2/json/cluster/resources?type=vm"
	var resp struct {
		Data []struct {
			Node    string `json:"node"`
			VMID    int    `json:"vmid"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Status  string `json:"status"`
			Pool    string `json:"pool"`
			MaxCPU  int    `json:"maxcpu"`
			MaxMem  int64  `json:"maxmem"`
			MaxDisk int64  `json:"maxdisk"`
		} `json:"data"`
	}
	if err := fetchJSON(url, auth, insecure, &resp); err != nil {
		return nil, fmt.Errorf("query Proxmox /cluster/resources?type=vm: %w", err)
	}
	allowed := allowedSet(cfg)
	out := &existingUsage{ByPool: map[string]int{}}
	for _, v := range resp.Data {
		// Only count actual VMs (containers/lxc are reported here too).
		if v.Type != "qemu" {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[v.Node]; !ok {
				continue
			}
		}
		// Disk is provisioned regardless of power state; CPU and RAM are
		// only consumed by running VMs.
		out.StorageGB += v.MaxDisk / (1024 * 1024 * 1024)
		if v.Status != "running" {
			continue
		}
		out.VMCount++
		out.CPUCores += v.MaxCPU
		out.MemoryMiB += v.MaxMem / (1024 * 1024)
		if v.Pool != "" {
			out.ByPool[v.Pool]++
		} else {
			out.ByPool["(no pool)"]++
		}
	}
	return out, nil
}

// --- low-level helpers ---

func authForCfg(cfg *config.Config) (auth string, insecure bool, base string, err error) {
	switch {
	case cfg.Providers.Proxmox.AdminToken != "" && cfg.Providers.Proxmox.AdminUsername != "":
		auth = "PVEAPIToken=" + cfg.Providers.Proxmox.AdminUsername + "=" + cfg.Providers.Proxmox.AdminToken
	case cfg.Providers.Proxmox.CAPIToken != "" && cfg.Providers.Proxmox.CAPISecret != "":
		auth = "PVEAPIToken=" + cfg.Providers.Proxmox.CAPIToken + "=" + cfg.Providers.Proxmox.CAPISecret
	default:
		return "", false, "", fmt.Errorf("no Proxmox credentials available (set --admin-username/--admin-token or --proxmox-token/--proxmox-secret)")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Providers.Proxmox.AdminInsecure)) {
	case "true", "1", "yes", "y", "on":
		insecure = true
	}
	base = strings.TrimRight(pveapi.HostBaseURL(cfg), "/")
	if base == "" {
		return "", false, "", fmt.Errorf("PROXMOX_URL is empty")
	}
	return
}

func fetchJSON(url, auth string, insecure bool, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	c := &http.Client{Transport: tr}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func allowedSet(cfg *config.Config) map[string]struct{} {
	m := map[string]struct{}{}
	for _, raw := range strings.Split(cfg.AllowedNodes, ",") {
		raw = strings.TrimSpace(raw)
		if raw != "" {
			m[raw] = struct{}{}
		}
	}
	if len(m) == 0 && cfg.Providers.Proxmox.Node != "" {
		m[cfg.Providers.Proxmox.Node] = struct{}{}
	}
	return m
}

func allowedSlice(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}