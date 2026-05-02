// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard_fields.go — enums, field metadata, renderField, and palette.
//
// Trap 1 (ADR 0014): renderField must remain co-located with dashFields.
// The unfocused fkText path branches on meta.secret before emitting any
// value — splitting renderField into per-tab helpers would require copying
// that guard into each helper, where it will drift. Keep both here.
//
// Trap 2 (ADR 0014): tiCount, siCount, toiCount, focCount are each a single
// iota const block. Splitting across files would silently reset iota and
// produce collisions. All four live here, never in tab_*.go files.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ─── palette ─────────────────────────────────────────────────────────────────

var (
	colAccent = lipgloss.AdaptiveColor{Light: "#0891b2", Dark: "#22d3ee"}
	colOK     = lipgloss.AdaptiveColor{Light: "#16a34a", Dark: "#4ade80"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#fbbf24"}
	colBad    = lipgloss.AdaptiveColor{Light: "#dc2626", Dark: "#f87171"}
	colMuted  = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#9ca3af"}
	colHdr    = lipgloss.AdaptiveColor{Light: "#1e40af", Dark: "#93c5fd"}

	stAccent = lipgloss.NewStyle().Foreground(colAccent)
	stOK     = lipgloss.NewStyle().Foreground(colOK)
	stWarn   = lipgloss.NewStyle().Foreground(colWarn)
	stBad    = lipgloss.NewStyle().Foreground(colBad)
	stMuted  = lipgloss.NewStyle().Foreground(colMuted)
	stHdr    = lipgloss.NewStyle().Foreground(colHdr).Bold(true)
	stBold   = lipgloss.NewStyle().Bold(true)
)

// ─── text-input slot indices ─────────────────────────────────────────────────

const (
	tiKindName     = iota
	tiK8sVer
	tiWorkloadName  // workload cluster name
	tiApps
	tiDBGB
	tiEgressGB
	tiQueueCPU
	tiQueueMem
	tiQueueVol
	tiObjCPU
	tiObjMem
	tiObjVol
	tiCacheCPU
	tiCacheMem
	tiCPEndpointIP     // workload control-plane VIP
	tiNodeIPRanges     // workload node IP range
	tiGateway
	tiIPPrefix
	tiDNSServers
	tiMgmtCPEndpointIP   // mgmt control-plane VIP (on-prem)
	tiMgmtNodeIPRanges   // mgmt node IP ranges (on-prem)
	tiProxmoxDefaultTmpl    // Proxmox default VM template ID
	tiProxmoxWLCPTmpl       // Proxmox workload control-plane template ID
	tiProxmoxWLCPCores      // Proxmox workload CP CPU cores
	tiProxmoxWLCPMemMiB     // Proxmox workload CP memory MiB
	tiProxmoxWLCPDiskGB     // Proxmox workload CP boot disk GB
	tiProxmoxWLWorkerTmpl   // Proxmox workload worker template ID
	tiProxmoxWLWorkerCores  // Proxmox workload worker CPU cores
	tiProxmoxWLWorkerMemMiB // Proxmox workload worker memory MiB
	tiProxmoxWLWorkerDiskGB // Proxmox workload worker boot disk GB
	tiProxmoxMgmtCPTmpl     // Proxmox mgmt control-plane template ID
	tiProxmoxMgmtCPCores    // Proxmox mgmt CP CPU cores
	tiProxmoxMgmtCPMemMiB   // Proxmox mgmt CP memory MiB
	tiProxmoxMgmtCPDiskGB   // Proxmox mgmt CP boot disk GB
	tiProxmoxMgmtWorkerTmpl    // Proxmox mgmt worker template ID
	tiProxmoxMgmtWorkerCores   // Proxmox mgmt worker CPU cores
	tiProxmoxMgmtWorkerMemMiB  // Proxmox mgmt worker memory MiB
	tiProxmoxMgmtWorkerDiskGB  // Proxmox mgmt worker boot disk GB
	tiProxmoxPool           // Proxmox workload cluster pool
	tiProxmoxMgmtPool       // Proxmox mgmt cluster pool
	tiProxmoxCPCount        // workload CP replica count
	tiProxmoxWorkerCount    // workload worker replica count
	tiProxmoxMgmtCPCount    // mgmt CP replica count
	tiProxmoxMgmtWorkerCount // mgmt worker replica count
	tiProxmoxURL            // Proxmox admin API URL
	tiProxmoxAdminUsername  // Proxmox admin token ID / username
	tiProxmoxAdminInsecure  // Proxmox TLS insecure ("true"/"false")
	tiProxmoxAdminToken     // Proxmox admin token secret (memory-only, never saved)
	tiArgoURL               // AppOfApps git URL
	tiArgoPath      // AppOfApps git path
	tiArgoRef       // AppOfApps git ref
	tiImgMirror     // image registry mirror
	tiCABundle      // internal CA bundle path
	tiHelmMirror    // helm repo mirror URL
	tiHWCost        // TCO: hardware cost USD
	tiHWWatts       // TCO: hardware watts
	tiHWKWH         // TCO: electricity rate USD/kWh
	tiHWSupport     // TCO: support USD/month
	tiDCLoc
	tiBudget
	tiHeadroom
	// Phase H — Platform Services
	tiRegistryNode    // YAGE_REGISTRY_NODE
	tiRegistryVMFlav  // YAGE_REGISTRY_VM_FLAVOR
	tiRegistryNetwork // YAGE_REGISTRY_NETWORK
	tiRegistryStorage // YAGE_REGISTRY_STORAGE
	tiIssuingCACert   // YAGE_ISSUING_CA_ROOT_CERT (PEM)
	tiIssuingCAKey    // YAGE_ISSUING_CA_ROOT_KEY (PEM)
	tiCount // must be last
)

// ─── select slot indices ─────────────────────────────────────────────────────

const (
	siMode         = 0 // cloud | on-prem
	siEnv          = 1 // dev | staging | prod
	siResil        = 2 // single-az | ha | ha-multi-region
	siBootstrap    = 3 // kubeadm | k3s  (on-prem only)
	siProvider     = 4 // infra provider (auto | aws | gcp | …)
	siRegistryFlav = 5 // Phase H registry flavor: harbor | zot
	siCount        = 6
)

// ─── toggle (bool) slot indices ──────────────────────────────────────────────

const (
	toiQueue      = 0
	toiObjStore   = 1
	toiCache      = 2
	toiOvercommit = 3  // on-prem only: allow resource overcommit
	toiAirgapped  = 4
	toiKyverno    = 5
	toiCertMgr    = 6
	toiCNPG       = 7
	toiCrossplane = 8
	toiExtSecrets = 9
	toiOTEL       = 10
	toiGrafana    = 11
	toiVictoria   = 12
	toiMetrics    = 13
	toiSPIRE      = 14
	toiTCO        = 15 // on-prem TCO fields enabled
	toiCount      = 16
)

// ─── logical focus IDs (tab order) ───────────────────────────────────────────

const (
	focMode                = iota // 0
	focProvider                   // 1
	focKindName            // 2
	focK8sVer              // 3
	focWorkloadName        // 4
	focEnv                 // 5
	focResil               // 6
	focApps                // 7
	focDBGB                // 8
	focEgressGB            // 9
	focHasQueue            // 10
	focQueueCPU            // 11
	focQueueMem            // 12
	focQueueVol            // 13
	focHasObjStore         // 14
	focObjCPU              // 15
	focObjMem              // 16
	focObjVol              // 17
	focHasCache            // 18
	focCacheCPU            // 19
	focCacheMem            // 20
	focBootstrap              // 21
	focOvercommit             // 22
	focProxmoxURL            // 23 — Proxmox admin API URL
	focProxmoxAdminUsername  // 24 — Proxmox admin token ID / username
	focProxmoxAdminInsecure  // 25 — Proxmox TLS insecure flag
	focProxmoxAdminToken     // 26 — Proxmox admin token secret (memory-only)
	focProxmoxDefaultTmpl    // 27 — Proxmox default VM template ID
	focProxmoxWLCPTmpl       // 28 — Proxmox workload CP template ID
	focProxmoxWLCPCores      // 29
	focProxmoxWLCPMemMiB     // 30
	focProxmoxWLCPDiskGB     // 31
	focProxmoxWLWorkerTmpl   // 32 — Proxmox workload worker template ID
	focProxmoxWLWorkerCores  // 33
	focProxmoxWLWorkerMemMiB // 34
	focProxmoxWLWorkerDiskGB // 35
	focProxmoxMgmtCPTmpl     // 36 — Proxmox mgmt CP template ID
	focProxmoxMgmtCPCores    // 37
	focProxmoxMgmtCPMemMiB   // 38
	focProxmoxMgmtCPDiskGB   // 39
	focProxmoxMgmtWorkerTmpl    // 40 — Proxmox mgmt worker template ID
	focProxmoxMgmtWorkerCores   // 41
	focProxmoxMgmtWorkerMemMiB  // 42
	focProxmoxMgmtWorkerDiskGB  // 43
	focProxmoxPool              // 44 — Proxmox workload pool name
	focProxmoxMgmtPool          // 45 — Proxmox mgmt pool name
	focProxmoxCPCount           // 46 — workload CP replica count
	focProxmoxWorkerCount       // 47 — workload worker replica count
	focProxmoxMgmtCPCount       // 48 — mgmt CP replica count
	focProxmoxMgmtWorkerCount   // 49 — mgmt worker replica count
	focCPEndpointIP             // 46
	focNodeIPRanges             // 41
	focGateway                  // 42
	focIPPrefix                 // 43
	focDNSServers               // 44
	focMgmtCPEndpointIP         // 45 — on-prem mgmt cluster VIP
	focMgmtNodeIPRanges         // 46 — on-prem mgmt node IP ranges
	focArgoURL                  // 47
	focArgoPath                 // 48
	focArgoRef                  // 49
	focAirgapped                // 50
	focImgMirror                // 51
	focCABundle                 // 52
	focHelmMirror               // 53
	focKyverno                  // 54
	focCertMgr                  // 55
	focCNPG                     // 56
	focCrossplane               // 57
	focExtSecrets               // 58
	focOTEL                     // 59
	focGrafana                  // 60
	focVictoria                 // 61
	focMetrics                  // 62
	focSPIRE                    // 63
	focDCLoc                    // 64
	focBudget                   // 65
	focHeadroom                 // 66
	focTCO                      // 67 — TCO toggle (on-prem only)
	focHWCost                   // 68
	focHWWatts                  // 69
	focHWKWH                    // 70
	focHWSupport                // 71
	// Phase H — Platform Services (on-prem only)
	focRegistryNode    // 72 — YAGE_REGISTRY_NODE (proxmox only)
	focRegistryVMFlav  // 73
	focRegistryNetwork // 74
	focRegistryStorage // 75
	focRegistryFlavor  // 76 — harbor | zot
	focIssuingCACert   // 77 — YAGE_ISSUING_CA_ROOT_CERT
	focIssuingCAKey    // 78 — YAGE_ISSUING_CA_ROOT_KEY
	focCount           // 79 — must be last
)

// ─── per-field metadata ───────────────────────────────────────────────────────

type fkind int

const (
	fkText   fkind = iota
	fkSelect       // left/right arrows cycle through options
	fkToggle       // space flips the bool
)

type fieldMeta struct {
	kind    fkind
	subIdx  int    // index into textInputs / selects / toggles
	label   string // displayed label (padded to labelW)
	section string // section header text; "" = same section as previous
	costKey bool   // triggers a cost-refresh when changed
	secret  bool   // value is a credential — never rendered in cleartext
}

var dashFields = []fieldMeta{
	// ── Mode ─────────────────────────────────────────────────────────────── fid 0
	{fkSelect, siMode, "mode", "Mode", false, false},
	// ── Provider ─────────────────────────────────────────────────────────── fid 1
	{fkSelect, siProvider, "provider", "Provider", false, false},
	// ── Cluster ──────────────────────────────────────────────────────────── fid 2-4
	{fkText, tiKindName, "kind name", "Cluster", false, false},
	{fkText, tiK8sVer, "k8s version", "", false, false},
	{fkText, tiWorkloadName, "workload name", "", false, false},
	// ── Tier ─────────────────────────────────────────────────────────────── fid 5-6
	{fkSelect, siEnv, "environment", "Tier", true, false},
	{fkSelect, siResil, "resilience", "", true, false},
	// ── Workload ─────────────────────────────────────────────────────────── fid 7-9
	{fkText, tiApps, "apps", "Workload", true, false},
	{fkText, tiDBGB, "db (GB)", "", true, false},
	{fkText, tiEgressGB, "egress GB/mo", "", true, false},
	// ── Add-ons (cloud sizing) ───────────────────────────────────────────── fid 10-20
	{fkToggle, toiQueue, "message queue", "Add-ons", true, false},
	{fkText, tiQueueCPU, "  queue CPU (m)", "", true, false},
	{fkText, tiQueueMem, "  queue mem (Mi)", "", true, false},
	{fkText, tiQueueVol, "  queue vol (GB)", "", true, false},
	{fkToggle, toiObjStore, "object storage", "", true, false},
	{fkText, tiObjCPU, "  obj CPU (m)", "", true, false},
	{fkText, tiObjMem, "  obj mem (Mi)", "", true, false},
	{fkText, tiObjVol, "  obj vol (GB)", "", true, false},
	{fkToggle, toiCache, "in-mem cache", "", true, false},
	{fkText, tiCacheCPU, "  cache CPU (m)", "", true, false},
	{fkText, tiCacheMem, "  cache mem (Mi)", "", true, false},
	// ── Bootstrap (on-prem only) ─────────────────────────────────────────── fid 21-22
	{fkSelect, siBootstrap, "bootstrap mode", "Bootstrap", false, false},
	{fkToggle, toiOvercommit, "allow overcommit", "", false, false},
	// ── Proxmox Connection (proxmox only) ───────────────────────────────── fid 23-26
	{fkText, tiProxmoxURL, "pve url", "Proxmox Connection", false, false},
	{fkText, tiProxmoxAdminUsername, "admin username", "", false, false},
	{fkText, tiProxmoxAdminInsecure, "tls insecure", "", false, false},
	{kind: fkText, subIdx: tiProxmoxAdminToken, label: "admin token", secret: true},
	// ── Proxmox Config (proxmox only) ────────────────────────────────────── fid 27-49
	{fkText, tiProxmoxDefaultTmpl, "default tmpl ID", "Proxmox Config", false, false},
	{fkText, tiProxmoxWLCPTmpl, "wl CP tmpl ID", "", false, false},
	{fkText, tiProxmoxWLCPCores, "  cores", "", false, false},
	{fkText, tiProxmoxWLCPMemMiB, "  mem MiB", "", false, false},
	{fkText, tiProxmoxWLCPDiskGB, "  disk GB", "", false, false},
	{fkText, tiProxmoxWLWorkerTmpl, "wl worker tmpl ID", "", false, false},
	{fkText, tiProxmoxWLWorkerCores, "  cores", "", false, false},
	{fkText, tiProxmoxWLWorkerMemMiB, "  mem MiB", "", false, false},
	{fkText, tiProxmoxWLWorkerDiskGB, "  disk GB", "", false, false},
	{fkText, tiProxmoxMgmtCPTmpl, "mgmt CP tmpl ID", "", false, false},
	{fkText, tiProxmoxMgmtCPCores, "  cores", "", false, false},
	{fkText, tiProxmoxMgmtCPMemMiB, "  mem MiB", "", false, false},
	{fkText, tiProxmoxMgmtCPDiskGB, "  disk GB", "", false, false},
	{fkText, tiProxmoxMgmtWorkerTmpl, "mgmt worker tmpl ID", "", false, false},
	{fkText, tiProxmoxMgmtWorkerCores, "  cores", "", false, false},
	{fkText, tiProxmoxMgmtWorkerMemMiB, "  mem MiB", "", false, false},
	{fkText, tiProxmoxMgmtWorkerDiskGB, "  disk GB", "", false, false},
	{fkText, tiProxmoxPool, "wl pool name", "", false, false},
	{fkText, tiProxmoxMgmtPool, "mgmt pool name", "", false, false},
	{fkText, tiProxmoxCPCount, "wl CP replicas", "", false, false},
	{fkText, tiProxmoxWorkerCount, "wl worker replicas", "", false, false},
	{fkText, tiProxmoxMgmtCPCount, "mgmt CP replicas", "", false, false},
	{fkText, tiProxmoxMgmtWorkerCount, "mgmt worker replicas", "", false, false},
	// ── Workload Network (on-prem only) ──────────────────────────────────── fid 40-44
	{fkText, tiCPEndpointIP, "CP endpoint IP", "Workload Network", false, false},
	{fkText, tiNodeIPRanges, "node IP ranges", "", false, false},
	{fkText, tiGateway, "gateway", "", false, false},
	{fkText, tiIPPrefix, "IP prefix", "", false, false},
	{fkText, tiDNSServers, "DNS servers", "", false, false},
	// ── Mgmt Network (on-prem only) ───────────────────────────────────────── fid 28-29
	{fkText, tiMgmtCPEndpointIP, "CP endpoint IP", "Mgmt Network", false, false},
	{fkText, tiMgmtNodeIPRanges, "node IP ranges", "", false, false},
	// ── ArgoCD ───────────────────────────────────────────────────────────── fid 30-32
	{fkText, tiArgoURL, "app-of-apps URL", "ArgoCD", false, false},
	{fkText, tiArgoPath, "app-of-apps path", "", false, false},
	{fkText, tiArgoRef, "app-of-apps ref", "", false, false},
	// ── Airgap ───────────────────────────────────────────────────────────── fid 31-34
	{fkToggle, toiAirgapped, "airgapped", "Airgap", false, false},
	{fkText, tiImgMirror, "image mirror", "", false, false},
	{fkText, tiCABundle, "CA bundle path", "", false, false},
	{fkText, tiHelmMirror, "helm mirror", "", false, false},
	// ── Add-ons installed ────────────────────────────────────────────────── fid 35-44
	{fkToggle, toiKyverno, "kyverno", "Add-ons installed", false, false},
	{fkToggle, toiCertMgr, "cert-manager", "", false, false},
	{fkToggle, toiCNPG, "CNPG", "", false, false},
	{fkToggle, toiCrossplane, "crossplane", "", false, false},
	{fkToggle, toiExtSecrets, "ext-secrets", "", false, false},
	{fkToggle, toiOTEL, "otel", "", false, false},
	{fkToggle, toiGrafana, "grafana", "", false, false},
	{fkToggle, toiVictoria, "victoriametrics", "", false, false},
	{fkToggle, toiMetrics, "metrics-server", "", false, false},
	{fkToggle, toiSPIRE, "spire", "", false, false},
	// ── Geo + Budget (cloud only) ────────────────────────────────────────── fid 45-47
	{fkText, tiDCLoc, "data-center loc", "Geo", false, false},
	{fkText, tiBudget, "budget USD/mo", "Budget", false, false},
	{fkText, tiHeadroom, "headroom %", "", false, false},
	// ── TCO (on-prem only) ───────────────────────────────────────────────── fid 50-54
	{fkToggle, toiTCO, "TCO enabled", "TCO", false, false},
	{fkText, tiHWCost, "  HW cost USD", "", false, false},
	{fkText, tiHWWatts, "  HW watts", "", false, false},
	{fkText, tiHWKWH, "  kWh rate USD", "", false, false},
	{fkText, tiHWSupport, "  support USD/mo", "", false, false},
	// ── Registry (proxmox only) ───────────────────────────────────────────
	{fkText, tiRegistryNode, "registry node", "Registry", false, false},
	{fkText, tiRegistryVMFlav, "  VM flavor", "", false, false},
	{fkText, tiRegistryNetwork, "  network", "", false, false},
	{fkText, tiRegistryStorage, "  storage", "", false, false},
	{fkSelect, siRegistryFlav, "  flavor", "", false, false},
	// ── Issuing CA (on-prem only) ─────────────────────────────────────────
	{kind: fkText, subIdx: tiIssuingCACert, label: "issuing CA cert", section: "Issuing CA", secret: true},
	{kind: fkText, subIdx: tiIssuingCAKey, label: "issuing CA key", secret: true},
}

// ─── select state ────────────────────────────────────────────────────────────

type selectState struct {
	options []string
	cur     int
}

func (s *selectState) value() string { return s.options[s.cur] }

func (s *selectState) next() { s.cur = (s.cur + 1) % len(s.options) }
func (s *selectState) prev() {
	s.cur = (s.cur - 1 + len(s.options)) % len(s.options)
}

// ─── field renderer ───────────────────────────────────────────────────────────

const labelW = 18
const inputW = 13

// renderField renders a single dashboard field row.
//
// Security invariant (ADR 0013 / Trap 1): when focused is false and
// meta.secret is true, the value is NEVER rendered in cleartext — only
// "[✓] set" or "[ ] not set". This function is the single gate; it must
// not be copied into per-tab helpers.
func (m dashModel) renderField(fid int, focused bool, w int) string {
	meta := dashFields[fid]
	focGlyph := "  "
	if focused {
		focGlyph = stAccent.Render("▸ ")
	}
	lbl := fmt.Sprintf("%-*s", labelW, meta.label)
	if !focused {
		lbl = stMuted.Render(lbl)
	}

	var valStr string
	switch meta.kind {
	case fkText:
		ti := m.textInputs[meta.subIdx]
		if focused {
			valStr = "[" + ti.View() + "]"
		} else if meta.secret {
			if ti.Value() == "" {
				valStr = stMuted.Render("[ ] not set")
			} else {
				valStr = stOK.Render("[✓] set")
			}
		} else {
			v := ti.Value()
			if v == "" {
				v = stMuted.Render("─")
			}
			valStr = "[" + fmt.Sprintf("%-*s", inputW, v) + "]"
		}

	case fkSelect:
		sel := m.selects[meta.subIdx]
		var parts []string
		for i, opt := range sel.options {
			if i == sel.cur {
				if focused {
					parts = append(parts, stAccent.Render("◆"+opt))
				} else {
					parts = append(parts, stBold.Render(opt))
				}
			} else {
				parts = append(parts, stMuted.Render(opt))
			}
		}
		valStr = strings.Join(parts, stMuted.Render(" │ "))

	case fkToggle:
		v := m.toggles[meta.subIdx]
		if v {
			valStr = stOK.Render("[Y]")
		} else {
			valStr = stMuted.Render("[N]")
		}
	}

	return focGlyph + lbl + " " + valStr
}
