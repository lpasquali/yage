// Package capacity computes the aggregate Proxmox host resources
// available to bootstrap-capi (filtered by AllowedNodes / ProxmoxNode)
// and compares them against the resources the planned management +
// workload clusters would consume. Used by the orchestrator to
// pre-flight a real run, and by --dry-run to print a capacity vs plan
// summary.
//
// Default budget threshold is 0.75 — clusters cannot use more than 3/4
// of available host resources. The rest is reserved for the host OS,
// the hypervisor, and overhead.
package capacity

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/proxmox"
)

// DefaultThreshold is the fraction of host resources that may be
// claimed by all clusters combined. The remaining 25 % is reserved.
const DefaultThreshold = 0.75

// HostCapacity is the aggregate of CPU + memory + storage across all
// Proxmox nodes that are eligible for VM placement, after filtering by
// the configured AllowedNodes (or just ProxmoxNode when AllowedNodes is
// empty).
type HostCapacity struct {
	Nodes        []string
	CPUCores     int   // total physical cores
	MemoryMiB    int64 // total memory in MiB
	StorageGB    int64 // total storage capacity in GB across the cluster
	StorageBy    map[string]int64
}

// Plan is the aggregate of CPU + memory + storage that the configured
// workload + (optional) management clusters would consume.
type Plan struct {
	CPUCores  int
	MemoryMiB int64
	StorageGB int64
	Items     []PlanItem
}

// PlanItem is one line in the breakdown — a single role on a single
// cluster contributes one PlanItem.
type PlanItem struct {
	Name      string // "workload control-plane", "mgmt control-plane", ...
	Replicas  int
	CPUCores  int   // per replica
	MemoryMiB int64 // per replica
	DiskGB    int64 // per replica
}

// Total returns the sum of CPU/memory/disk across all replicas of the
// item.
func (p PlanItem) Total() (cpu int, mem, disk int64) {
	return p.CPUCores * p.Replicas, p.MemoryMiB * int64(p.Replicas), p.DiskGB * int64(p.Replicas)
}

// FetchHostCapacity calls Proxmox /api2/json/cluster/resources?type=node
// and aggregates CPU + memory across allowed nodes. Storage is fetched
// from /api2/json/storage and filtered to the configured CSI/cloudinit
// backend names. Caller must have valid admin or clusterctl creds set
// on cfg.
func FetchHostCapacity(cfg *config.Config) (*HostCapacity, error) {
	auth, insecure, base, err := authForCfg(cfg)
	if err != nil {
		return nil, err
	}
	nodesURL := base + "/api2/json/cluster/resources?type=node"

	var nodesEnv struct {
		Data []struct {
			Node     string `json:"node"`
			MaxCPU   int    `json:"maxcpu"`
			CPU      int    `json:"cpu"` // current load (1.0 = full core)
			MaxMem   int64  `json:"maxmem"`
			Mem      int64  `json:"mem"`
			MaxDisk  int64  `json:"maxdisk"`
			Disk     int64  `json:"disk"`
			Status   string `json:"status"`
		} `json:"data"`
	}
	if err := fetchJSON(nodesURL, auth, insecure, &nodesEnv); err != nil {
		return nil, fmt.Errorf("query Proxmox /cluster/resources: %w", err)
	}

	allowed := allowedSet(cfg)
	hc := &HostCapacity{StorageBy: map[string]int64{}}
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
			Disk    int64  `json:"disk"`
			Shared  int    `json:"shared"`
			Status  string `json:"status"`
		} `json:"data"`
	}
	if err := fetchJSON(storageURL, auth, insecure, &storageEnv); err == nil {
		want := strings.TrimSpace(cfg.ProxmoxCSIStorage)
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

// PlanFor builds the resource plan from cfg. Includes the workload
// cluster always, and the management cluster when PivotEnabled is true.
func PlanFor(cfg *config.Config) Plan {
	p := Plan{}
	add := func(name string, replicas int, sockets, cores, memMiB, diskGB string) {
		if replicas <= 0 {
			return
		}
		s := atoiOr(sockets, 1)
		c := atoiOr(cores, 1)
		mem := atoi64Or(memMiB, 0)
		disk := atoi64Or(diskGB, 0)
		item := PlanItem{
			Name: name, Replicas: replicas,
			CPUCores: s * c, MemoryMiB: mem, DiskGB: disk,
		}
		p.Items = append(p.Items, item)
		cpu, m, d := item.Total()
		p.CPUCores += cpu
		p.MemoryMiB += m
		p.StorageGB += d
	}
	wcp := atoiOr(cfg.ControlPlaneMachineCount, 1)
	wwk := atoiOr(cfg.WorkerMachineCount, 0)
	add("workload control-plane", wcp,
		cfg.ControlPlaneNumSockets, cfg.ControlPlaneNumCores,
		cfg.ControlPlaneMemoryMiB, cfg.ControlPlaneBootVolumeSize)
	add("workload worker", wwk,
		cfg.WorkerNumSockets, cfg.WorkerNumCores,
		cfg.WorkerMemoryMiB, cfg.WorkerBootVolumeSize)
	if cfg.PivotEnabled {
		mcp := atoiOr(cfg.MgmtControlPlaneMachineCount, 1)
		mwk := atoiOr(cfg.MgmtWorkerMachineCount, 0)
		add("mgmt control-plane", mcp,
			cfg.MgmtControlPlaneNumSockets, cfg.MgmtControlPlaneNumCores,
			cfg.MgmtControlPlaneMemoryMiB, cfg.MgmtControlPlaneBootVolumeSize)
		add("mgmt worker", mwk,
			cfg.WorkerNumSockets, cfg.WorkerNumCores,
			cfg.WorkerMemoryMiB, cfg.WorkerBootVolumeSize)
	}
	return p
}

// Check returns nil when plan fits inside threshold * capacity, or an
// error describing the overflow. Threshold defaults to DefaultThreshold
// when 0.
func Check(plan Plan, host *HostCapacity, threshold float64) error {
	if threshold <= 0 || threshold > 1 {
		threshold = DefaultThreshold
	}
	maxCPU := int(float64(host.CPUCores) * threshold)
	maxMem := int64(float64(host.MemoryMiB) * threshold)
	maxDisk := int64(float64(host.StorageGB) * threshold)
	var msgs []string
	if plan.CPUCores > maxCPU {
		msgs = append(msgs, fmt.Sprintf(
			"CPU: requested %d cores, capacity %d × %.0f%% = %d",
			plan.CPUCores, host.CPUCores, threshold*100, maxCPU))
	}
	if plan.MemoryMiB > maxMem {
		msgs = append(msgs, fmt.Sprintf(
			"memory: requested %d MiB, capacity %d × %.0f%% = %d MiB",
			plan.MemoryMiB, host.MemoryMiB, threshold*100, maxMem))
	}
	if host.StorageGB > 0 && plan.StorageGB > maxDisk {
		msgs = append(msgs, fmt.Sprintf(
			"storage: requested %d GB, capacity %d GB × %.0f%% = %d GB",
			plan.StorageGB, host.StorageGB, threshold*100, maxDisk))
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("planned clusters exceed %.0f%% of available Proxmox host resources:\n  %s",
		threshold*100, strings.Join(msgs, "\n  "))
}

// --- helpers ---

func authForCfg(cfg *config.Config) (auth string, insecure bool, base string, err error) {
	switch {
	case cfg.ProxmoxAdminToken != "" && cfg.ProxmoxAdminUsername != "":
		auth = "PVEAPIToken=" + cfg.ProxmoxAdminUsername + "=" + cfg.ProxmoxAdminToken
	case cfg.ProxmoxToken != "" && cfg.ProxmoxSecret != "":
		auth = "PVEAPIToken=" + cfg.ProxmoxToken + "=" + cfg.ProxmoxSecret
	default:
		return "", false, "", fmt.Errorf("no Proxmox credentials available (set --admin-username/--admin-token or --proxmox-token/--proxmox-secret)")
	}
	insecure = cfg.ProxmoxAdminInsecure == "true"
	base = strings.TrimRight(proxmox.HostBaseURL(cfg), "/")
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
		tr.TLSClientConfig = nil
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
	if len(m) == 0 && cfg.ProxmoxNode != "" {
		m[cfg.ProxmoxNode] = struct{}{}
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

func atoiOr(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func atoi64Or(s string, def int64) int64 {
	return int64(atoiOr(s, int(def)))
}
