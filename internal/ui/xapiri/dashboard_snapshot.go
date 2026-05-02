// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard_snapshot.go — config snapshot serialisers and markDirty.
//
// buildSnapshotCfg and flushToCfg are paired atomic state serialisers.
// Any diff touching state persistence is scoped to this file.

import (
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
)

// markDirty stamps lastDirty and schedules a debounce tick if none is pending.
func (m *dashModel) markDirty() tea.Cmd {
	m.lastDirty = time.Now()
	if m.refreshPending {
		return nil // existing tick chain will fire
	}
	m.refreshPending = true
	return tea.Tick(400*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// buildSnapshotCfg assembles a config.Config from the current dashboard
// field values without mutating m.cfg or m.s.
func (m dashModel) buildSnapshotCfg() config.Config {
	snap := *m.cfg

	snap.KindClusterName = strings.TrimSpace(m.textInputs[tiKindName].Value())
	if snap.KindClusterName == "" {
		snap.KindClusterName = "yage-mgmt"
	}
	snap.WorkloadKubernetesVersion = strings.TrimSpace(m.textInputs[tiK8sVer].Value())
	if wn := strings.TrimSpace(m.textInputs[tiWorkloadName].Value()); wn != "" {
		snap.WorkloadClusterName = wn
	}

	// Provider select: "auto" means keep whatever InfraProvider was loaded from config.
	if prov := m.selects[siProvider].value(); prov != "auto" {
		snap.InfraProvider = prov
		snap.InfraProviderDefaulted = false
	}

	// Network (on-prem only; cloud fields are managed by the cloud provider).
	snap.ControlPlaneEndpointIP = strings.TrimSpace(m.textInputs[tiCPEndpointIP].Value())
	snap.NodeIPRanges = strings.TrimSpace(m.textInputs[tiNodeIPRanges].Value())
	snap.Gateway = strings.TrimSpace(m.textInputs[tiGateway].Value())
	snap.IPPrefix = strings.TrimSpace(m.textInputs[tiIPPrefix].Value())
	snap.DNSServers = strings.TrimSpace(m.textInputs[tiDNSServers].Value())
	snap.Mgmt.ControlPlaneEndpointIP = strings.TrimSpace(m.textInputs[tiMgmtCPEndpointIP].Value())
	snap.Mgmt.NodeIPRanges = strings.TrimSpace(m.textInputs[tiMgmtNodeIPRanges].Value())
	// Proxmox connection fields (non-secret only; AdminToken handled separately).
	if v := strings.TrimSpace(m.textInputs[tiProxmoxURL].Value()); v != "" {
		snap.Providers.Proxmox.URL = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxAdminUsername].Value()); v != "" {
		snap.Providers.Proxmox.AdminUsername = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxAdminInsecure].Value()); v != "" {
		snap.Providers.Proxmox.AdminInsecure = v
	}
	// AdminToken: memory-only — written directly to m.cfg, not to snap,
	// so it can never accidentally flow into snapshot serialisation paths.
	if t := strings.TrimSpace(m.textInputs[tiProxmoxDefaultTmpl].Value()); t != "" {
		snap.Providers.Proxmox.TemplateID = t
	}
	snap.WorkloadControlPlaneTemplateID = strings.TrimSpace(m.textInputs[tiProxmoxWLCPTmpl].Value())
	if v := strings.TrimSpace(m.textInputs[tiProxmoxWLCPCores].Value()); v != "" {
		snap.Providers.Proxmox.ControlPlaneNumCores = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxWLCPMemMiB].Value()); v != "" {
		snap.Providers.Proxmox.ControlPlaneMemoryMiB = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxWLCPDiskGB].Value()); v != "" {
		snap.Providers.Proxmox.ControlPlaneBootVolumeSize = v
	}
	snap.WorkloadWorkerTemplateID = strings.TrimSpace(m.textInputs[tiProxmoxWLWorkerTmpl].Value())
	if v := strings.TrimSpace(m.textInputs[tiProxmoxWLWorkerCores].Value()); v != "" {
		snap.Providers.Proxmox.WorkerNumCores = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxWLWorkerMemMiB].Value()); v != "" {
		snap.Providers.Proxmox.WorkerMemoryMiB = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxWLWorkerDiskGB].Value()); v != "" {
		snap.Providers.Proxmox.WorkerBootVolumeSize = v
	}
	snap.Providers.Proxmox.Mgmt.ControlPlaneTemplateID = strings.TrimSpace(m.textInputs[tiProxmoxMgmtCPTmpl].Value())
	if v := strings.TrimSpace(m.textInputs[tiProxmoxMgmtCPCores].Value()); v != "" {
		snap.Providers.Proxmox.Mgmt.ControlPlaneNumCores = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxMgmtCPMemMiB].Value()); v != "" {
		snap.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxMgmtCPDiskGB].Value()); v != "" {
		snap.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize = v
	}
	snap.Providers.Proxmox.Mgmt.WorkerTemplateID = strings.TrimSpace(m.textInputs[tiProxmoxMgmtWorkerTmpl].Value())
	if v := strings.TrimSpace(m.textInputs[tiProxmoxMgmtWorkerCores].Value()); v != "" {
		snap.Providers.Proxmox.Mgmt.WorkerNumCores = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxMgmtWorkerMemMiB].Value()); v != "" {
		snap.Providers.Proxmox.Mgmt.WorkerMemoryMiB = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxMgmtWorkerDiskGB].Value()); v != "" {
		snap.Providers.Proxmox.Mgmt.WorkerBootVolumeSize = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxPool].Value()); v != "" {
		snap.Providers.Proxmox.Pool = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxMgmtPool].Value()); v != "" {
		snap.Providers.Proxmox.Mgmt.Pool = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxCPCount].Value()); v != "" {
		snap.ControlPlaneMachineCount = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxWorkerCount].Value()); v != "" {
		snap.WorkerMachineCount = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxMgmtCPCount].Value()); v != "" {
		snap.Mgmt.ControlPlaneMachineCount = v
	}
	if v := strings.TrimSpace(m.textInputs[tiProxmoxMgmtWorkerCount].Value()); v != "" {
		snap.Mgmt.WorkerMachineCount = v
	}

	// ArgoCD git coordinates.
	if u := strings.TrimSpace(m.textInputs[tiArgoURL].Value()); u != "" {
		snap.ArgoCD.AppOfAppsGitURL = u
	}
	if p := strings.TrimSpace(m.textInputs[tiArgoPath].Value()); p != "" {
		snap.ArgoCD.AppOfAppsGitPath = p
	}
	if r := strings.TrimSpace(m.textInputs[tiArgoRef].Value()); r != "" {
		snap.ArgoCD.AppOfAppsGitRef = r
	}

	// Airgap.
	snap.Airgapped = m.toggles[toiAirgapped]
	if snap.Airgapped {
		snap.ImageRegistryMirror = strings.TrimSpace(m.textInputs[tiImgMirror].Value())
		snap.InternalCABundle = strings.TrimSpace(m.textInputs[tiCABundle].Value())
		snap.HelmRepoMirror = strings.TrimSpace(m.textInputs[tiHelmMirror].Value())
	}

	// Add-ons installed.
	snap.KyvernoEnabled = m.toggles[toiKyverno]
	snap.CertManagerEnabled = m.toggles[toiCertMgr]
	snap.CNPGEnabled = m.toggles[toiCNPG]
	snap.CrossplaneEnabled = m.toggles[toiCrossplane]
	snap.ExternalSecretsEnabled = m.toggles[toiExtSecrets]
	snap.OTELEnabled = m.toggles[toiOTEL]
	snap.GrafanaEnabled = m.toggles[toiGrafana]
	snap.VictoriaMetricsEnabled = m.toggles[toiVictoria]
	snap.EnableMetricsServer = m.toggles[toiMetrics]
	snap.SPIREEnabled = m.toggles[toiSPIRE]

	// TCO (on-prem).
	if v, err := strconv.ParseFloat(strings.TrimSpace(m.textInputs[tiHWCost].Value()), 64); err == nil {
		snap.HardwareCostUSD = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(m.textInputs[tiHWWatts].Value()), 64); err == nil {
		snap.HardwareWatts = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(m.textInputs[tiHWKWH].Value()), 64); err == nil {
		snap.HardwareKWHRateUSD = v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(m.textInputs[tiHWSupport].Value()), 64); err == nil {
		snap.HardwareSupportUSDMonth = v
	}

	// Phase H — Platform Services.
	snap.RegistryNode = strings.TrimSpace(m.textInputs[tiRegistryNode].Value())
	snap.RegistryVMFlavor = strings.TrimSpace(m.textInputs[tiRegistryVMFlav].Value())
	snap.RegistryNetwork = strings.TrimSpace(m.textInputs[tiRegistryNetwork].Value())
	snap.RegistryStorage = strings.TrimSpace(m.textInputs[tiRegistryStorage].Value())
	snap.RegistryFlavor = m.selects[siRegistryFlav].value()
	snap.IssuingCARootCert = strings.TrimSpace(m.textInputs[tiIssuingCACert].Value())
	snap.IssuingCARootKey = strings.TrimSpace(m.textInputs[tiIssuingCAKey].Value())

	mode := m.selects[siMode].value()
	fork := forkCloud
	if mode == "on-prem" {
		fork = forkOnPrem
	}

	env := envTier(m.selects[siEnv].value())
	snap.ArgoCD.Enabled = true
	snap.ArgoCD.WorkloadEnabled = true

	var resil resilienceTier
	switch m.selects[siResil].value() {
	case "ha":
		resil = resilienceHA
		snap.ControlPlaneMachineCount = "3"
	case "ha-multi-region":
		resil = resilienceHAMulti
		snap.ControlPlaneMachineCount = "3"
	default:
		resil = resilienceSingle
		snap.ControlPlaneMachineCount = "1"
	}

	snap.BootstrapMode = m.selects[siBootstrap].value()
	snap.Capacity.AllowOvercommit = m.toggles[toiOvercommit]

	snap.Cost.Currency.DataCenterLocation = strings.ToUpper(strings.TrimSpace(m.textInputs[tiDCLoc].Value()))

	if f, err := strconv.ParseFloat(strings.TrimSpace(m.textInputs[tiBudget].Value()), 64); err == nil && f > 0 {
		if u, ferr := pricing.FromTaller(f); ferr == nil {
			snap.BudgetUSDMonth = u
		} else {
			snap.BudgetUSDMonth = f
		}
	}

	var wl workloadShape
	wl.HasQueue = m.toggles[toiQueue]
	wl.HasObjStore = m.toggles[toiObjStore]
	wl.HasCache = m.toggles[toiCache]
	snap.Capacity.AllowOvercommit = m.toggles[toiOvercommit]

	if apps := parseAppBuckets(m.textInputs[tiApps].Value()); len(apps) > 0 {
		wl.Apps = apps
	} else {
		wl.Apps = []appBucket{{Count: 4, Template: "medium"}}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(m.textInputs[tiDBGB].Value())); err == nil && n >= 0 {
		wl.DBGB = n
	}
	if e := strings.TrimSpace(m.textInputs[tiEgressGB].Value()); e != "" {
		if n, err := strconv.Atoi(e); err == nil && n >= 0 {
			wl.EgressGBMo = n
		}
	}
	if wl.EgressGBMo == 0 && wl.DBGB > 0 {
		wl.EgressGBMo = wl.DBGB * 2
	}
	if m.toggles[toiQueue] {
		wl.QueueCPUMilli = parseIntOrKeep(m.textInputs[tiQueueCPU].Value(), 1000)
		wl.QueueMemMiB = parseIntOrKeep(m.textInputs[tiQueueMem].Value(), 2048)
		wl.QueueVolGB = parseIntOrKeep(m.textInputs[tiQueueVol].Value(), 20)
	}
	if m.toggles[toiObjStore] {
		wl.ObjStoreCPUMilli = parseIntOrKeep(m.textInputs[tiObjCPU].Value(), 1000)
		wl.ObjStoreMemMiB = parseIntOrKeep(m.textInputs[tiObjMem].Value(), 2048)
		wl.ObjStoreVolGB = parseIntOrKeep(m.textInputs[tiObjVol].Value(), 500)
	}
	if m.toggles[toiCache] {
		wl.CacheCPUMilli = parseIntOrKeep(m.textInputs[tiCacheCPU].Value(), 500)
		wl.CacheMemMiB = parseIntOrKeep(m.textInputs[tiCacheMem].Value(), 2048)
	}

	// Worker heuristic.
	total := 0
	for _, b := range wl.Apps {
		total += b.Count
	}
	w := total / 4
	if w < 1 {
		w = 1
	}
	snap.WorkerMachineCount = strconv.Itoa(w)

	syncWorkloadShapeToCfg(&snap, wl, resil, env, fork)

	// Preserve the user-selected InfraProvider in the snapshot. Any
	// compare-specific normalization (for example, clearing on-prem
	// providers before a cloud-only cost comparison) must happen in the
	// refresh/compare path, not while building the config snapshot that is
	// also used for persistence.

	// Recalculate the credential-based SkipProviders using the current
	// snapshot credentials, but preserve any explicit skip list already
	// configured via config/env/flags. This avoids dropping user-requested
	// skips while still refreshing the auto-disabled providers based on the
	// latest credentials from the kind Secret or the credentials form.
	explicitSkipProviders := snap.SkipProviders
	snap.SkipProviders = explicitSkipProviders
	disableProvidersMissingCredentials(&snap)

	return snap
}

// flushToCfg writes the dashboard state onto m.cfg and m.s permanently
// (called on ctrl+s).
func (m *dashModel) flushToCfg() {
	snap := m.buildSnapshotCfg()
	// Copy all computed fields back.
	m.cfg.KindClusterName = snap.KindClusterName
	m.cfg.WorkloadKubernetesVersion = snap.WorkloadKubernetesVersion
	m.cfg.ControlPlaneMachineCount = snap.ControlPlaneMachineCount
	m.cfg.WorkerMachineCount = snap.WorkerMachineCount
	m.cfg.ArgoCD = snap.ArgoCD
	m.cfg.CertManagerEnabled = snap.CertManagerEnabled
	m.cfg.Cost.Currency.DataCenterLocation = snap.Cost.Currency.DataCenterLocation
	m.cfg.BudgetUSDMonth = snap.BudgetUSDMonth
	m.cfg.Workload = snap.Workload
	m.cfg.MQCPUMillicoresOverride = snap.MQCPUMillicoresOverride
	m.cfg.MQMemoryMiBOverride = snap.MQMemoryMiBOverride
	m.cfg.MQVolumeGBOverride = snap.MQVolumeGBOverride
	m.cfg.ObjStoreCPUMillicoresOverride = snap.ObjStoreCPUMillicoresOverride
	m.cfg.ObjStoreMemoryMiBOverride = snap.ObjStoreMemoryMiBOverride
	m.cfg.ObjStoreVolumeGBOverride = snap.ObjStoreVolumeGBOverride
	m.cfg.CacheCPUMillicoresOverride = snap.CacheCPUMillicoresOverride
	m.cfg.CacheMemoryMiBOverride = snap.CacheMemoryMiBOverride
	m.cfg.BootstrapMode = snap.BootstrapMode
	m.cfg.Capacity.AllowOvercommit = snap.Capacity.AllowOvercommit
	m.cfg.WorkloadClusterName = snap.WorkloadClusterName
	m.cfg.InfraProvider = snap.InfraProvider
	m.cfg.InfraProviderDefaulted = snap.InfraProviderDefaulted
	m.cfg.ControlPlaneEndpointIP = snap.ControlPlaneEndpointIP
	m.cfg.NodeIPRanges = snap.NodeIPRanges
	m.cfg.Gateway = snap.Gateway
	m.cfg.IPPrefix = snap.IPPrefix
	m.cfg.DNSServers = snap.DNSServers
	m.cfg.Mgmt.ControlPlaneEndpointIP = snap.Mgmt.ControlPlaneEndpointIP
	m.cfg.Mgmt.NodeIPRanges = snap.Mgmt.NodeIPRanges
	// Proxmox connection fields.
	m.cfg.Providers.Proxmox.URL = snap.Providers.Proxmox.URL
	m.cfg.Providers.Proxmox.AdminUsername = snap.Providers.Proxmox.AdminUsername
	m.cfg.Providers.Proxmox.AdminInsecure = snap.Providers.Proxmox.AdminInsecure
	// AdminToken: memory-only — read directly from the textinput, bypassing snap
	// so the value never appears in SnapshotYAML() or KindSyncFields().
	if v := strings.TrimSpace(m.textInputs[tiProxmoxAdminToken].Value()); v != "" {
		m.cfg.Providers.Proxmox.AdminToken = v
	}
	m.cfg.Providers.Proxmox.TemplateID = snap.Providers.Proxmox.TemplateID
	m.cfg.WorkloadControlPlaneTemplateID = snap.WorkloadControlPlaneTemplateID
	m.cfg.Providers.Proxmox.ControlPlaneNumCores = snap.Providers.Proxmox.ControlPlaneNumCores
	m.cfg.Providers.Proxmox.ControlPlaneMemoryMiB = snap.Providers.Proxmox.ControlPlaneMemoryMiB
	m.cfg.Providers.Proxmox.ControlPlaneBootVolumeSize = snap.Providers.Proxmox.ControlPlaneBootVolumeSize
	m.cfg.WorkloadWorkerTemplateID = snap.WorkloadWorkerTemplateID
	m.cfg.Providers.Proxmox.WorkerNumCores = snap.Providers.Proxmox.WorkerNumCores
	m.cfg.Providers.Proxmox.WorkerMemoryMiB = snap.Providers.Proxmox.WorkerMemoryMiB
	m.cfg.Providers.Proxmox.WorkerBootVolumeSize = snap.Providers.Proxmox.WorkerBootVolumeSize
	m.cfg.Providers.Proxmox.Mgmt.ControlPlaneTemplateID = snap.Providers.Proxmox.Mgmt.ControlPlaneTemplateID
	m.cfg.Providers.Proxmox.Mgmt.ControlPlaneNumCores = snap.Providers.Proxmox.Mgmt.ControlPlaneNumCores
	m.cfg.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB = snap.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB
	m.cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize = snap.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize
	m.cfg.Providers.Proxmox.Mgmt.WorkerTemplateID = snap.Providers.Proxmox.Mgmt.WorkerTemplateID
	m.cfg.Providers.Proxmox.Mgmt.WorkerNumCores = snap.Providers.Proxmox.Mgmt.WorkerNumCores
	m.cfg.Providers.Proxmox.Mgmt.WorkerMemoryMiB = snap.Providers.Proxmox.Mgmt.WorkerMemoryMiB
	m.cfg.Providers.Proxmox.Mgmt.WorkerBootVolumeSize = snap.Providers.Proxmox.Mgmt.WorkerBootVolumeSize
	m.cfg.Providers.Proxmox.Pool = snap.Providers.Proxmox.Pool
	m.cfg.Providers.Proxmox.Mgmt.Pool = snap.Providers.Proxmox.Mgmt.Pool
	m.cfg.ControlPlaneMachineCount = snap.ControlPlaneMachineCount
	m.cfg.WorkerMachineCount = snap.WorkerMachineCount
	m.cfg.Mgmt.ControlPlaneMachineCount = snap.Mgmt.ControlPlaneMachineCount
	m.cfg.Mgmt.WorkerMachineCount = snap.Mgmt.WorkerMachineCount
	m.cfg.Airgapped = snap.Airgapped
	m.cfg.ImageRegistryMirror = snap.ImageRegistryMirror
	m.cfg.InternalCABundle = snap.InternalCABundle
	m.cfg.HelmRepoMirror = snap.HelmRepoMirror
	m.cfg.KyvernoEnabled = snap.KyvernoEnabled
	m.cfg.CertManagerEnabled = snap.CertManagerEnabled
	m.cfg.CNPGEnabled = snap.CNPGEnabled
	m.cfg.CrossplaneEnabled = snap.CrossplaneEnabled
	m.cfg.ExternalSecretsEnabled = snap.ExternalSecretsEnabled
	m.cfg.OTELEnabled = snap.OTELEnabled
	m.cfg.GrafanaEnabled = snap.GrafanaEnabled
	m.cfg.VictoriaMetricsEnabled = snap.VictoriaMetricsEnabled
	m.cfg.EnableMetricsServer = snap.EnableMetricsServer
	m.cfg.SPIREEnabled = snap.SPIREEnabled
	m.cfg.HardwareCostUSD = snap.HardwareCostUSD
	m.cfg.HardwareWatts = snap.HardwareWatts
	m.cfg.HardwareKWHRateUSD = snap.HardwareKWHRateUSD
	m.cfg.HardwareSupportUSDMonth = snap.HardwareSupportUSDMonth
	m.cfg.RegistryNode = snap.RegistryNode
	m.cfg.RegistryVMFlavor = snap.RegistryVMFlavor
	m.cfg.RegistryNetwork = snap.RegistryNetwork
	m.cfg.RegistryStorage = snap.RegistryStorage
	m.cfg.RegistryFlavor = snap.RegistryFlavor
	m.cfg.IssuingCARootCert = snap.IssuingCARootCert
	m.cfg.IssuingCARootKey = snap.IssuingCARootKey

	// Derive s.fork / s.env / s.resil for the rest of the walkthrough.
	if m.selects[siMode].value() == "on-prem" {
		m.s.fork = forkOnPrem
	} else {
		m.s.fork = forkCloud
	}
	m.s.env = envTier(m.selects[siEnv].value())
	switch m.selects[siResil].value() {
	case "ha":
		m.s.resil = resilienceHA
	case "ha-multi-region":
		m.s.resil = resilienceHAMulti
	default:
		m.s.resil = resilienceSingle
	}
	if f, err := strconv.ParseFloat(strings.TrimSpace(m.textInputs[tiBudget].Value()), 64); err == nil && f > 0 {
		usd := f
		if u, ferr := pricing.FromTaller(f); ferr == nil {
			usd = u
		}
		m.s.budgetUSDMonth = usd
		if hr, e2 := strconv.ParseFloat(strings.TrimSpace(m.textInputs[tiHeadroom].Value()), 64); e2 == nil && hr >= 0 && hr < 100 {
			m.s.headroomPct = hr / 100.0
		}
		m.s.budgetAfterHeadroom = usd * (1 - m.s.headroomPct)
	}
}
