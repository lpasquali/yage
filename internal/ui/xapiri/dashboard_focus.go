// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard_focus.go — focus-visibility logic and navigation helpers.
//
// Trap 2 (ADR 0014): isHidden cross-references toggli and selects across
// ALL tabs. It must stay whole here — splitting per-tab would produce N
// copies that drift. The focCount/tiCount/siCount/toiCount enums it
// references live in dashboard_fields.go (single const block per namespace).

import (
	"github.com/lpasquali/yage/internal/provider"
)

// ─── visibility ──────────────────────────────────────────────────────────────

func (m *dashModel) isCloud() bool { return m.selects[siMode].value() == "cloud" }

// onPremProviders is the set of provider IDs that only make sense for bare-metal / VM deployments.
var onPremProviders = map[string]bool{
	"proxmox":   true,
	"vsphere":   true,
	"openstack": true,
	"docker":    true,
}

// providerListForMode returns ["auto", ...] filtered to the providers that
// match mode ("cloud" or "on-prem"). "auto" is always first.
func providerListForMode(mode string) []string {
	all := provider.Registered()
	out := make([]string, 1, len(all)+1)
	out[0] = "auto"
	onPrem := mode == "on-prem"
	for _, p := range all {
		if onPremProviders[p] == onPrem {
			out = append(out, p)
		}
	}
	return out
}

// rebuildProviderList refreshes m.selects[siProvider].options for the current
// mode. If the previously selected provider is still valid it is preserved;
// otherwise the select resets to "auto". Also clears stale cost rows since the
// affordable provider set may have changed.
func (m dashModel) rebuildProviderList() dashModel {
	newOpts := providerListForMode(m.selects[siMode].value())
	cur := m.selects[siProvider].value()
	newCur := 0
	for i, p := range newOpts {
		if p == cur {
			newCur = i
			break
		}
	}
	m.selects[siProvider] = selectState{options: newOpts, cur: newCur}
	m.costRows = nil // stale rows belong to the old mode
	return m
}

func (m *dashModel) isHidden(fid int) bool {
	isCloud := m.isCloud()
	switch fid {
	// Always visible.
	case focMode, focProvider, focKindName, focK8sVer, focWorkloadName,
		focEnv, focResil, focApps, focDBGB, focEgressGB:
		return false
	// Cloud sizing add-ons.
	case focHasQueue, focHasObjStore, focHasCache:
		return !isCloud
	case focQueueCPU, focQueueMem, focQueueVol:
		return !isCloud || !m.toggles[toiQueue]
	case focObjCPU, focObjMem, focObjVol:
		return !isCloud || !m.toggles[toiObjStore]
	case focCacheCPU, focCacheMem:
		return !isCloud || !m.toggles[toiCache]
	// On-prem only.
	case focBootstrap, focOvercommit:
		return isCloud
	// Proxmox-specific: only when provider=proxmox.
	case focProxmoxURL, focProxmoxAdminUsername, focProxmoxAdminInsecure, focProxmoxAdminToken,
		focProxmoxDefaultTmpl,
		focProxmoxWLCPTmpl, focProxmoxWLCPCores, focProxmoxWLCPMemMiB, focProxmoxWLCPDiskGB,
		focProxmoxWLWorkerTmpl, focProxmoxWLWorkerCores, focProxmoxWLWorkerMemMiB, focProxmoxWLWorkerDiskGB,
		focProxmoxMgmtCPTmpl, focProxmoxMgmtCPCores, focProxmoxMgmtCPMemMiB, focProxmoxMgmtCPDiskGB,
		focProxmoxMgmtWorkerTmpl, focProxmoxMgmtWorkerCores, focProxmoxMgmtWorkerMemMiB, focProxmoxMgmtWorkerDiskGB,
		focProxmoxPool, focProxmoxMgmtPool,
		focProxmoxCPCount, focProxmoxWorkerCount, focProxmoxMgmtCPCount, focProxmoxMgmtWorkerCount:
		return m.selects[siProvider].value() != "proxmox"
	// Workload network: on-prem only (cloud VPCs are fully managed).
	case focCPEndpointIP, focNodeIPRanges, focGateway, focIPPrefix, focDNSServers:
		return isCloud
	// Mgmt cluster network: on-prem only.
	case focMgmtCPEndpointIP, focMgmtNodeIPRanges:
		return isCloud
	case focArgoURL, focArgoPath, focArgoRef:
		return false
	// Airgap.
	case focAirgapped:
		return false
	case focImgMirror, focCABundle, focHelmMirror:
		return !m.toggles[toiAirgapped]
	// Add-ons installed: always visible.
	case focKyverno, focCertMgr, focCNPG, focCrossplane, focExtSecrets,
		focOTEL, focGrafana, focVictoria, focMetrics, focSPIRE:
		return false
	// Geo + Budget: cloud only.
	case focDCLoc, focBudget, focHeadroom:
		return !isCloud
	// TCO: on-prem only; detail fields gated by the toggle.
	case focTCO:
		return isCloud
	case focHWCost, focHWWatts, focHWKWH, focHWSupport:
		return isCloud || !m.toggles[toiTCO]
	// Registry: proxmox-only (bootstrap registry is an on-prem / bare-metal concern).
	case focRegistryNode, focRegistryVMFlav, focRegistryNetwork, focRegistryStorage, focRegistryFlavor:
		return m.selects[siProvider].value() != "proxmox"
	// Issuing CA: on-prem only.
	case focIssuingCACert, focIssuingCAKey:
		return isCloud
	}
	return false
}

func (m *dashModel) visibleFocusList() []int {
	out := make([]int, 0, focCount)
	for i := 0; i < focCount; i++ {
		if !m.isHidden(i) {
			out = append(out, i)
		}
	}
	return out
}

// focusAtConfigRow returns the focus ID for the field at content-area row
// (0-indexed, tab bar excluded). Returns (-1, false) for blank/header rows.
// Mirrors the layout produced by renderConfigTab so clicks map correctly.
func (m dashModel) focusAtConfigRow(row int) (int, bool) {
	cur := 0
	lastSection := ""
	for fid := 0; fid < focCount; fid++ {
		if m.isHidden(fid) {
			continue
		}
		meta := dashFields[fid]
		if meta.section != "" && meta.section != lastSection {
			if cur > 0 { // blank line before section header (mirrors "if len(lines) > 0")
				if cur == row {
					return -1, false
				}
				cur++
			}
			if cur == row { // section header line
				return -1, false
			}
			cur++
			lastSection = meta.section
		}
		if cur == row {
			return fid, true
		}
		cur++
	}
	return -1, false
}

// jumpFocus sets focus directly to fid, blurring/focusing text inputs as needed.
func (m dashModel) jumpFocus(fid int) dashModel {
	if m.isHidden(fid) {
		return m
	}
	if oldMeta := dashFields[m.focus]; oldMeta.kind == fkText {
		m.textInputs[oldMeta.subIdx].Blur()
	}
	m.focus = fid
	if newMeta := dashFields[fid]; newMeta.kind == fkText {
		_ = m.textInputs[newMeta.subIdx].Focus()
	}
	return m
}

// ─── focus navigation ─────────────────────────────────────────────────────────

func (m dashModel) moveFocus(forward bool) dashModel {
	vis := m.visibleFocusList()
	if len(vis) == 0 {
		return m
	}
	cur := -1
	for i, v := range vis {
		if v == m.focus {
			cur = i
			break
		}
	}
	if cur == -1 {
		cur = 0
	} else if forward {
		cur = (cur + 1) % len(vis)
	} else {
		cur = (cur - 1 + len(vis)) % len(vis)
	}

	// Blur old text input if any.
	oldFoc := m.focus
	oldMeta := dashFields[oldFoc]
	if oldMeta.kind == fkText {
		m.textInputs[oldMeta.subIdx].Blur()
	}

	m.focus = vis[cur]

	// Focus new text input if any.
	newMeta := dashFields[m.focus]
	if newMeta.kind == fkText {
		cmd := m.textInputs[newMeta.subIdx].Focus()
		_ = cmd
	}
	return m
}

