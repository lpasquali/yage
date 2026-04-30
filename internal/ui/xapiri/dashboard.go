// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard.go — full-screen bubbletea dashboard, the sole xapiri UI.
//
// Design goals (user's request verbatim):
//   "look more a gui but textual, all values at once, detailed bill
//    with enough colors, clear screen like older ncurses apps"
//
// Implementation:
//   - tea.WithAltScreen() for ncurses-style full-screen entry/exit.
//   - Tab bar: [config] [editor] [costs] [logs] [help] [about]
//   - Bottom strip: live cost tally, always visible.
//   - 400 ms debounce → cost.CompareWithFilter goroutine on change.
//   - ctrl+s commits and exits; esc aborts without writing cfg.
//   - ctrl+t spawns $SHELL via tea.ExecProcess (config tab only).
//   - ctrl+l switches to the logs tab (config tab only).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/obs"
	"github.com/lpasquali/yage/internal/platform/installer"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/provider"
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
	tiCount // must be last
)

// ─── select slot indices ─────────────────────────────────────────────────────

const (
	siMode      = 0 // cloud | on-prem
	siEnv       = 1 // dev | staging | prod
	siResil     = 2 // single-az | ha | ha-multi-region
	siBootstrap = 3 // kubeadm | k3s  (on-prem only)
	siProvider  = 4 // infra provider (auto | aws | gcp | …)
	siCount     = 5
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
	focProxmoxDefaultTmpl    // 23 — Proxmox default VM template ID
	focProxmoxWLCPTmpl       // 24 — Proxmox workload CP template ID
	focProxmoxWLCPCores      // 25
	focProxmoxWLCPMemMiB     // 26
	focProxmoxWLCPDiskGB     // 27
	focProxmoxWLWorkerTmpl   // 28 — Proxmox workload worker template ID
	focProxmoxWLWorkerCores  // 29
	focProxmoxWLWorkerMemMiB // 30
	focProxmoxWLWorkerDiskGB // 31
	focProxmoxMgmtCPTmpl     // 32 — Proxmox mgmt CP template ID
	focProxmoxMgmtCPCores    // 33
	focProxmoxMgmtCPMemMiB   // 34
	focProxmoxMgmtCPDiskGB   // 35
	focProxmoxMgmtWorkerTmpl    // 36 — Proxmox mgmt worker template ID
	focProxmoxMgmtWorkerCores   // 37
	focProxmoxMgmtWorkerMemMiB  // 38
	focProxmoxMgmtWorkerDiskGB  // 39
	focProxmoxPool              // 40 — Proxmox workload pool name
	focProxmoxMgmtPool          // 41 — Proxmox mgmt pool name
	focProxmoxCPCount           // 42 — workload CP replica count
	focProxmoxWorkerCount       // 43 — workload worker replica count
	focProxmoxMgmtCPCount       // 44 — mgmt CP replica count
	focProxmoxMgmtWorkerCount   // 45 — mgmt worker replica count
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
	focCount                    // 72 — must be last
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
}

var dashFields = []fieldMeta{
	// ── Mode ─────────────────────────────────────────────────────────────── fid 0
	{fkSelect, siMode, "mode", "Mode", false},
	// ── Provider ─────────────────────────────────────────────────────────── fid 1
	{fkSelect, siProvider, "provider", "Provider", false},
	// ── Cluster ──────────────────────────────────────────────────────────── fid 2-4
	{fkText, tiKindName, "kind name", "Cluster", false},
	{fkText, tiK8sVer, "k8s version", "", false},
	{fkText, tiWorkloadName, "workload name", "", false},
	// ── Tier ─────────────────────────────────────────────────────────────── fid 5-6
	{fkSelect, siEnv, "environment", "Tier", true},
	{fkSelect, siResil, "resilience", "", true},
	// ── Workload ─────────────────────────────────────────────────────────── fid 7-9
	{fkText, tiApps, "apps", "Workload", true},
	{fkText, tiDBGB, "db (GB)", "", true},
	{fkText, tiEgressGB, "egress GB/mo", "", true},
	// ── Add-ons (cloud sizing) ───────────────────────────────────────────── fid 10-20
	{fkToggle, toiQueue, "message queue", "Add-ons", true},
	{fkText, tiQueueCPU, "  queue CPU (m)", "", true},
	{fkText, tiQueueMem, "  queue mem (Mi)", "", true},
	{fkText, tiQueueVol, "  queue vol (GB)", "", true},
	{fkToggle, toiObjStore, "object storage", "", true},
	{fkText, tiObjCPU, "  obj CPU (m)", "", true},
	{fkText, tiObjMem, "  obj mem (Mi)", "", true},
	{fkText, tiObjVol, "  obj vol (GB)", "", true},
	{fkToggle, toiCache, "in-mem cache", "", true},
	{fkText, tiCacheCPU, "  cache CPU (m)", "", true},
	{fkText, tiCacheMem, "  cache mem (Mi)", "", true},
	// ── Bootstrap (on-prem only) ─────────────────────────────────────────── fid 21-22
	{fkSelect, siBootstrap, "bootstrap mode", "Bootstrap", false},
	{fkToggle, toiOvercommit, "allow overcommit", "", false},
	// ── Proxmox Config (proxmox only) ────────────────────────────────────── fid 23-39
	{fkText, tiProxmoxDefaultTmpl, "default tmpl ID", "Proxmox Config", false},
	{fkText, tiProxmoxWLCPTmpl, "wl CP tmpl ID", "", false},
	{fkText, tiProxmoxWLCPCores, "  cores", "", false},
	{fkText, tiProxmoxWLCPMemMiB, "  mem MiB", "", false},
	{fkText, tiProxmoxWLCPDiskGB, "  disk GB", "", false},
	{fkText, tiProxmoxWLWorkerTmpl, "wl worker tmpl ID", "", false},
	{fkText, tiProxmoxWLWorkerCores, "  cores", "", false},
	{fkText, tiProxmoxWLWorkerMemMiB, "  mem MiB", "", false},
	{fkText, tiProxmoxWLWorkerDiskGB, "  disk GB", "", false},
	{fkText, tiProxmoxMgmtCPTmpl, "mgmt CP tmpl ID", "", false},
	{fkText, tiProxmoxMgmtCPCores, "  cores", "", false},
	{fkText, tiProxmoxMgmtCPMemMiB, "  mem MiB", "", false},
	{fkText, tiProxmoxMgmtCPDiskGB, "  disk GB", "", false},
	{fkText, tiProxmoxMgmtWorkerTmpl, "mgmt worker tmpl ID", "", false},
	{fkText, tiProxmoxMgmtWorkerCores, "  cores", "", false},
	{fkText, tiProxmoxMgmtWorkerMemMiB, "  mem MiB", "", false},
	{fkText, tiProxmoxMgmtWorkerDiskGB, "  disk GB", "", false},
	{fkText, tiProxmoxPool, "wl pool name", "", false},
	{fkText, tiProxmoxMgmtPool, "mgmt pool name", "", false},
	{fkText, tiProxmoxCPCount, "wl CP replicas", "", false},
	{fkText, tiProxmoxWorkerCount, "wl worker replicas", "", false},
	{fkText, tiProxmoxMgmtCPCount, "mgmt CP replicas", "", false},
	{fkText, tiProxmoxMgmtWorkerCount, "mgmt worker replicas", "", false},
	// ── Workload Network (on-prem only) ──────────────────────────────────── fid 40-44
	{fkText, tiCPEndpointIP, "CP endpoint IP", "Workload Network", false},
	{fkText, tiNodeIPRanges, "node IP ranges", "", false},
	{fkText, tiGateway, "gateway", "", false},
	{fkText, tiIPPrefix, "IP prefix", "", false},
	{fkText, tiDNSServers, "DNS servers", "", false},
	// ── Mgmt Network (on-prem only) ───────────────────────────────────────── fid 28-29
	{fkText, tiMgmtCPEndpointIP, "CP endpoint IP", "Mgmt Network", false},
	{fkText, tiMgmtNodeIPRanges, "node IP ranges", "", false},
	// ── ArgoCD ───────────────────────────────────────────────────────────── fid 30-32
	{fkText, tiArgoURL, "app-of-apps URL", "ArgoCD", false},
	{fkText, tiArgoPath, "app-of-apps path", "", false},
	{fkText, tiArgoRef, "app-of-apps ref", "", false},
	// ── Airgap ───────────────────────────────────────────────────────────── fid 31-34
	{fkToggle, toiAirgapped, "airgapped", "Airgap", false},
	{fkText, tiImgMirror, "image mirror", "", false},
	{fkText, tiCABundle, "CA bundle path", "", false},
	{fkText, tiHelmMirror, "helm mirror", "", false},
	// ── Add-ons installed ────────────────────────────────────────────────── fid 35-44
	{fkToggle, toiKyverno, "kyverno", "Add-ons installed", false},
	{fkToggle, toiCertMgr, "cert-manager", "", false},
	{fkToggle, toiCNPG, "CNPG", "", false},
	{fkToggle, toiCrossplane, "crossplane", "", false},
	{fkToggle, toiExtSecrets, "ext-secrets", "", false},
	{fkToggle, toiOTEL, "otel", "", false},
	{fkToggle, toiGrafana, "grafana", "", false},
	{fkToggle, toiVictoria, "victoriametrics", "", false},
	{fkToggle, toiMetrics, "metrics-server", "", false},
	{fkToggle, toiSPIRE, "spire", "", false},
	// ── Geo + Budget (cloud only) ────────────────────────────────────────── fid 45-47
	{fkText, tiDCLoc, "data-center loc", "Geo", false},
	{fkText, tiBudget, "budget USD/mo", "Budget", false},
	{fkText, tiHeadroom, "headroom %", "", false},
	// ── TCO (on-prem only) ───────────────────────────────────────────────── fid 50-54
	{fkToggle, toiTCO, "TCO enabled", "TCO", false},
	{fkText, tiHWCost, "  HW cost USD", "", false},
	{fkText, tiHWWatts, "  HW watts", "", false},
	{fkText, tiHWKWH, "  kWh rate USD", "", false},
	{fkText, tiHWSupport, "  support USD/mo", "", false},
}

// ─── tab IDs ─────────────────────────────────────────────────────────────────

type dashTab int

const (
	tabConfig    dashTab = iota // 0 — config file selection
	tabProvision                // 1 — full interactive provision form
	tabEditor                   // 2 — opens $EDITOR on the YAML config file
	tabCosts                    // 3 — full provider comparison table + bar chart
	tabLogs                     // 4 — scrollable ring buffer
	tabDeploy                   // 5 — save-to-kind + start-deploy actions
	tabDeps                     // 6 — CLI deps check + upgrade; provider image arm64 status
	tabHelp                     // 7 — keyboard shortcuts reference (always second-to-last)
	tabAbout                    // 8 — version / license / URL (always last)
	tabCount                    // must be last
)

var tabLabels = [tabCount]string{"config", "provision", "editor", "costs", "logs", "deploy", "deps", "help", "about"}

// ─── config-tab sub-screen state ─────────────────────────────────────────────

// cfgScreenKind is the sub-state of the config tab.
type cfgScreenKind int

const (
	cfgScreenList    cfgScreenKind = iota // show list of existing configs
	cfgScreenNewName                       // enter name for a new config
)

// ─── cost-tab credential input slots ─────────────────────────────────────────

const (
	ccAWSKeyID   = 0
	ccAWSSecret  = 1
	ccGCPKey     = 2
	ccHetznerTok = 3
	ccDOTok      = 4
	ccIBMKey     = 5
	ccCount      = 6
)

var ccLabels = [ccCount]string{
	"AWS Access Key ID",
	"AWS Secret Access Key",
	"GCP API Key",
	"Hetzner Token",
	"DigitalOcean Token",
	"IBM Cloud API Key",
}

// ─── messages ────────────────────────────────────────────────────────────────

// costRowMsg carries one provider's result as it arrives from the streaming
// cost fetch. ch is the same channel used for subsequent waits so the caller
// can chain without storing state in the model.
type costRowMsg struct {
	row  cost.CloudCost
	ch   <-chan cost.CloudCost
	done bool // true when ch is closed (all providers finished)
}

// waitForCostRowCmd blocks until the next CloudCost arrives on ch, then
// delivers it as a costRowMsg. The channel reference is forwarded so
// Update can schedule the next wait without extra state.
func waitForCostRowCmd(ch <-chan cost.CloudCost) tea.Cmd {
	return func() tea.Msg {
		row, ok := <-ch
		return costRowMsg{row: row, ch: ch, done: !ok}
	}
}

// saveCostCredsMsg is returned when the background cost-credentials Secret write completes.
type saveCostCredsMsg struct{ err error }

type tickMsg time.Time

// logUpdateMsg signals that new lines are available in globalLogRing.
type logUpdateMsg struct{}

// editorFinishedMsg is returned by the ExecProcess callback after the editor exits.
type editorFinishedMsg struct {
	err      error
	resource *editorResource // non-nil when editing a kind resource (not the yage config)
	tempFile string          // path to the temp file to read and apply back
}

// editorResourcesMsg carries the result of listing yage-system resources.
type editorResourcesMsg struct {
	items []editorResource
	err   error
}

// editorSaveMsg is returned after a kind resource has been written back.
type editorSaveMsg struct{ err error }

// kindResourceReadyMsg is returned by openKindResourceEditorCmd after the
// temp file has been written. Update() converts it into a tea.ExecProcess
// command — the only correct way to hand off to an external process from
// inside a goroutine (returning tea.ExecProcess directly from a Cmd goroutine
// gives bubbletea a Cmd-as-Msg which it cannot execute).
type kindResourceReadyMsg struct {
	resource *editorResource
	tempFile string
}

// editorResource describes a Secret or ConfigMap in the yage-system namespace.
type editorResource struct {
	Kind string // "Secret" or "ConfigMap"
	Name string
}

// ptyOutputMsg carries a chunk of raw output from the embedded PTY.
type ptyOutputMsg struct{ data []byte }

// ptyExitMsg signals that the embedded PTY process has exited.
type ptyExitMsg struct{ err error }

// saveKindMsg is returned when the background Save-to-Kind goroutine completes.
type saveKindMsg struct{ err error }

// depsCheckMsg carries the result of a background dependency check.
type depsCheckMsg struct {
	tools  []installer.DepCheck
	images []installer.ImageCheck
}

// depsUpgradeMsg carries the result of a background dependency upgrade.
type depsUpgradeMsg struct{ err error }

// cfgListMsg carries the result of listing bootstrap configs on the kind cluster.
type cfgListMsg struct {
	candidates []kindsync.BootstrapCandidate
	err        error
}

// cfgEntryLoadMsg carries the fully merged config for a selected bootstrap entry.
type cfgEntryLoadMsg struct {
	cfg *config.Config
	err error
}

// sysStatsMsg carries a fresh sysinfo sample.
type sysStatsMsg struct{ s sysinfo.Stats }

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

// ─── cost time-window presets ─────────────────────────────────────────────────

type costWindowPreset struct {
	d     time.Duration
	label string // human-readable full name, shown in the selector
	short string // compact suffix used in tables and bottom bar
}

// costWindows is the ordered list of time-window presets. Index 6 (1 month)
// is the default. The user cycles through them with [ / ] in the costs tab.
var costWindows = []costWindowPreset{
	{time.Second, "1 second", "/sec"},
	{time.Minute, "1 minute", "/min"},
	{time.Hour, "1 hour", "/hr"},
	{8 * time.Hour, "8 hours", "/8h"},
	{24 * time.Hour, "1 day", "/day"},
	{7 * 24 * time.Hour, "1 week", "/wk"},
	{30 * 24 * time.Hour, "1 month", "/mo"},   // default (index 6)
	{365 * 24 * time.Hour, "1 year", "/yr"},
}

const costDefaultPeriodIdx = 6 // 1 month

// costMonthSecs is the number of seconds in the reference month (30 days).
const costMonthSecs = 30 * 24 * 3600.0

// ─── dashboard model ─────────────────────────────────────────────────────────

type dashModel struct {
	cfg *config.Config
	s   *state

	textInputs [tiCount]textinput.Model
	selects    [siCount]selectState
	toggles    [toiCount]bool

	focus int // current logical focus ID (focKindName … focHeadroom)

	// tab state
	activeTab dashTab

	// cost preview
	costRows        []cost.CloudCost
	costLoading     bool
	costVendor      int // which vendor's detail block is shown (index into costRows)
	costPeriodIdx   int // index into costWindows (default 6 = 1 month)
	refreshPending  bool
	lastDirty       time.Time

	// logs tab
	logLines       []string        // snapshot from globalLogRing
	logScroll      int             // scroll offset (lines from bottom; 0 = pinned to bottom)
	logSub         <-chan struct{}
	logFilter      string          // active filter pattern (empty = show all)
	logFiltering   bool            // true = filter input bar is open
	logWrap        bool            // ctrl+w toggles word-wrap vs truncate
	logFilterInput textinput.Model

	// cost tab credential form
	costCredsInputs [ccCount]textinput.Model
	costCredsFocus  int
	costCredsMode   bool   // true = showing credential form instead of comparison table
	costCredsStatus string // last save result ("" | "saved" | error text)

	// deploy tab
	deployFocused   int  // 0=save button, 1=deploy button
	saveKindLoading bool
	saveKindDone    bool
	saveKindErr     error
	deployRequested bool

	// config tab selection state (cfgScreenList / cfgScreenNewName)
	cfgScreen     cfgScreenKind
	cfgSelected   bool // true once a config entry is chosen; gates tabProvision and other tabs
	cfgCandidates []kindsync.BootstrapCandidate
	cfgListCursor int
	cfgLoading    bool
	cfgLoadErr    string
	cfgNewInput   textinput.Model

	// deps tab
	depsTools    []installer.DepCheck
	depsImages   []installer.ImageCheck
	depsRunning  bool   // check or upgrade in flight
	depsFocused  int    // 0=check button, 1=upgrade button
	depsStatus   string // last result summary

	// editor tab — kind resource browser
	editorItems    []editorResource // listed Secrets+ConfigMaps in yage-system
	editorSelected int              // index into editorItems
	editorLoading  bool             // listing in progress
	editorErr      string           // last list/save error
	editorSaving   bool             // save-back in progress

	// embedded terminal pane (Ctrl+T)
	termPTY     *os.File
	termCmd     *exec.Cmd
	termRunning bool
	termFocused bool
	termH       int    // total pane height (border+title+content); ctrl+alt+↑/↓ to resize
	termRaw     []byte // raw PTY output ring buffer (last 64 KB)

	// system stats widget (top-right corner of tab bar)
	sysSampler *sysinfo.Sampler
	sysStats   sysinfo.Stats

	width, height int
	errMsg        string
	done          bool // ctrl+s pressed
}

// ─── init ─────────────────────────────────────────────────────────────────────

func newDashModel(cfg *config.Config, s *state) dashModel {
	sampler := sysinfo.NewSampler()
	sampler.Sample() // prime the counters; first real delta will be on the second call
	m := dashModel{
		cfg:           cfg,
		s:             s,
		focus:         focKindName,
		costLoading:   cfg.CostCompareEnabled, // show "fetching…" from the first frame
		termH:         termPaneHDefault,
		sysSampler:    sampler,
		costPeriodIdx: costDefaultPeriodIdx,
	}

	// Build text inputs.
	for i := 0; i < tiCount; i++ {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Width = 14
		m.textInputs[i] = ti
	}

	// Seed values from cfg + state.
	m.textInputs[tiKindName].SetValue(dashDefault(cfg.KindClusterName, "yage-mgmt"))
	m.textInputs[tiKindName].Validate = validateDNSLabel
	m.textInputs[tiK8sVer].SetValue(dashDefault(cfg.WorkloadKubernetesVersion, "v1.35.0"))

	m.textInputs[tiApps].SetValue(dashDefault(formatAppBuckets(s.workload.Apps), "4 medium"))
	m.textInputs[tiApps].Validate = validateAppBuckets

	m.textInputs[tiDBGB].SetValue(dashIntOrEmpty(s.workload.DBGB))
	m.textInputs[tiDBGB].Validate = validateNonNegativeInt

	m.textInputs[tiEgressGB].SetValue(dashIntOrEmpty(s.workload.EgressGBMo))
	m.textInputs[tiEgressGB].Validate = validateNonNegativeIntOptional

	// Add-on resource sizing.
	m.textInputs[tiQueueCPU].SetValue(intToStrOrEmpty(s.workload.QueueCPUMilli, 1000))
	m.textInputs[tiQueueMem].SetValue(intToStrOrEmpty(s.workload.QueueMemMiB, 2048))
	m.textInputs[tiQueueVol].SetValue(intToStrOrEmpty(s.workload.QueueVolGB, 20))
	m.textInputs[tiObjCPU].SetValue(intToStrOrEmpty(s.workload.ObjStoreCPUMilli, 1000))
	m.textInputs[tiObjMem].SetValue(intToStrOrEmpty(s.workload.ObjStoreMemMiB, 2048))
	m.textInputs[tiObjVol].SetValue(intToStrOrEmpty(s.workload.ObjStoreVolGB, 500))
	m.textInputs[tiCacheCPU].SetValue(intToStrOrEmpty(s.workload.CacheCPUMilli, 500))
	m.textInputs[tiCacheMem].SetValue(intToStrOrEmpty(s.workload.CacheMemMiB, 2048))
	for _, i := range []int{tiQueueCPU, tiQueueMem, tiQueueVol, tiObjCPU, tiObjMem, tiObjVol, tiCacheCPU, tiCacheMem} {
		m.textInputs[i].Validate = validateNonNegativeInt
	}

	m.textInputs[tiDCLoc].SetValue(cfg.Cost.Currency.DataCenterLocation)

	if cfg.BudgetUSDMonth > 0 {
		if v, _, err := pricing.ToTaller(cfg.BudgetUSDMonth, "USD"); err == nil {
			m.textInputs[tiBudget].SetValue(strconv.FormatFloat(v, 'f', 2, 64))
		} else {
			m.textInputs[tiBudget].SetValue(strconv.FormatFloat(cfg.BudgetUSDMonth, 'f', 2, 64))
		}
	}
	m.textInputs[tiBudget].Validate = validatePositiveFloat
	headroomPct := 20.0
	if s.headroomPct > 0 {
		headroomPct = s.headroomPct * 100
	}
	m.textInputs[tiHeadroom].SetValue(strconv.FormatFloat(headroomPct, 'f', 0, 64))
	m.textInputs[tiHeadroom].Validate = validateNonNegativeIntOptional

	// New fields.
	m.textInputs[tiWorkloadName].SetValue(cfg.WorkloadClusterName)
	m.textInputs[tiCPEndpointIP].SetValue(cfg.ControlPlaneEndpointIP)
	m.textInputs[tiNodeIPRanges].SetValue(cfg.NodeIPRanges)
	m.textInputs[tiGateway].SetValue(cfg.Gateway)
	m.textInputs[tiIPPrefix].SetValue(cfg.IPPrefix)
	m.textInputs[tiDNSServers].SetValue(cfg.DNSServers)
	m.textInputs[tiMgmtCPEndpointIP].SetValue(cfg.Mgmt.ControlPlaneEndpointIP)
	m.textInputs[tiMgmtNodeIPRanges].SetValue(cfg.Mgmt.NodeIPRanges)
	m.textInputs[tiProxmoxDefaultTmpl].SetValue(cfg.Providers.Proxmox.TemplateID)
	m.textInputs[tiProxmoxWLCPTmpl].SetValue(cfg.WorkloadControlPlaneTemplateID)
	m.textInputs[tiProxmoxWLCPCores].SetValue(cfg.Providers.Proxmox.ControlPlaneNumCores)
	m.textInputs[tiProxmoxWLCPMemMiB].SetValue(cfg.Providers.Proxmox.ControlPlaneMemoryMiB)
	m.textInputs[tiProxmoxWLCPDiskGB].SetValue(cfg.Providers.Proxmox.ControlPlaneBootVolumeSize)
	m.textInputs[tiProxmoxWLWorkerTmpl].SetValue(cfg.WorkloadWorkerTemplateID)
	m.textInputs[tiProxmoxWLWorkerCores].SetValue(cfg.Providers.Proxmox.WorkerNumCores)
	m.textInputs[tiProxmoxWLWorkerMemMiB].SetValue(cfg.Providers.Proxmox.WorkerMemoryMiB)
	m.textInputs[tiProxmoxWLWorkerDiskGB].SetValue(cfg.Providers.Proxmox.WorkerBootVolumeSize)
	m.textInputs[tiProxmoxMgmtCPTmpl].SetValue(cfg.Providers.Proxmox.Mgmt.ControlPlaneTemplateID)
	m.textInputs[tiProxmoxMgmtCPCores].SetValue(cfg.Providers.Proxmox.Mgmt.ControlPlaneNumCores)
	m.textInputs[tiProxmoxMgmtCPMemMiB].SetValue(cfg.Providers.Proxmox.Mgmt.ControlPlaneMemoryMiB)
	m.textInputs[tiProxmoxMgmtCPDiskGB].SetValue(cfg.Providers.Proxmox.Mgmt.ControlPlaneBootVolumeSize)
	m.textInputs[tiProxmoxMgmtWorkerTmpl].SetValue(cfg.Providers.Proxmox.Mgmt.WorkerTemplateID)
	m.textInputs[tiProxmoxMgmtWorkerCores].SetValue(cfg.Providers.Proxmox.Mgmt.WorkerNumCores)
	m.textInputs[tiProxmoxMgmtWorkerMemMiB].SetValue(cfg.Providers.Proxmox.Mgmt.WorkerMemoryMiB)
	m.textInputs[tiProxmoxMgmtWorkerDiskGB].SetValue(cfg.Providers.Proxmox.Mgmt.WorkerBootVolumeSize)
	m.textInputs[tiProxmoxPool].SetValue(cfg.Providers.Proxmox.Pool)
	m.textInputs[tiProxmoxMgmtPool].SetValue(cfg.Providers.Proxmox.Mgmt.Pool)
	m.textInputs[tiProxmoxCPCount].SetValue(cfg.ControlPlaneMachineCount)
	m.textInputs[tiProxmoxWorkerCount].SetValue(cfg.WorkerMachineCount)
	m.textInputs[tiProxmoxMgmtCPCount].SetValue(cfg.Mgmt.ControlPlaneMachineCount)
	m.textInputs[tiProxmoxMgmtWorkerCount].SetValue(cfg.Mgmt.WorkerMachineCount)
	m.textInputs[tiArgoURL].SetValue(cfg.ArgoCD.AppOfAppsGitURL)
	m.textInputs[tiArgoPath].SetValue(cfg.ArgoCD.AppOfAppsGitPath)
	m.textInputs[tiArgoRef].SetValue(cfg.ArgoCD.AppOfAppsGitRef)
	m.textInputs[tiImgMirror].SetValue(cfg.ImageRegistryMirror)
	m.textInputs[tiCABundle].SetValue(cfg.InternalCABundle)
	m.textInputs[tiHelmMirror].SetValue(cfg.HelmRepoMirror)
	if cfg.HardwareCostUSD != 0 {
		m.textInputs[tiHWCost].SetValue(strconv.FormatFloat(cfg.HardwareCostUSD, 'f', 2, 64))
	}
	if cfg.HardwareWatts != 0 {
		m.textInputs[tiHWWatts].SetValue(strconv.FormatFloat(cfg.HardwareWatts, 'f', 0, 64))
	}
	if cfg.HardwareKWHRateUSD != 0 {
		m.textInputs[tiHWKWH].SetValue(strconv.FormatFloat(cfg.HardwareKWHRateUSD, 'f', 4, 64))
	}
	if cfg.HardwareSupportUSDMonth != 0 {
		m.textInputs[tiHWSupport].SetValue(strconv.FormatFloat(cfg.HardwareSupportUSDMonth, 'f', 2, 64))
	}
	validateNonNegativeFloatOptional := func(v string) error {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("must be a non-negative number")
		}
		if f < 0 {
			return fmt.Errorf("must be >= 0")
		}
		return nil
	}
	m.textInputs[tiHWCost].Validate = validateNonNegativeFloatOptional
	m.textInputs[tiHWWatts].Validate = validateNonNegativeIntOptional
	m.textInputs[tiHWKWH].Validate = validateNonNegativeFloatOptional
	m.textInputs[tiHWSupport].Validate = validateNonNegativeFloatOptional

	// Selects.
	m.selects[siMode] = selectState{options: []string{"cloud", "on-prem"}, cur: 0}
	if s.fork == forkOnPrem {
		m.selects[siMode].cur = 1
	}

	m.selects[siEnv] = selectState{options: []string{"dev", "staging", "prod"}, cur: 1}
	switch s.env {
	case envDev:
		m.selects[siEnv].cur = 0
	case envProd:
		m.selects[siEnv].cur = 2
	}

	m.selects[siResil] = selectState{options: []string{"single-az", "ha", "ha-multi-region"}, cur: 0}
	switch s.resil {
	case resilienceHA:
		m.selects[siResil].cur = 1
	case resilienceHAMulti:
		m.selects[siResil].cur = 2
	}

	m.selects[siBootstrap] = selectState{options: []string{"kubeadm", "k3s"}, cur: 0}
	if cfg.BootstrapMode == "k3s" {
		m.selects[siBootstrap].cur = 1
	}

	// Build the initial provider list filtered to the resolved mode.
	initialMode := "cloud"
	if s.fork == forkOnPrem {
		initialMode = "on-prem"
	}
	provOptions := providerListForMode(initialMode)
	provCur := 0
	for i, p := range provOptions {
		if p == cfg.InfraProvider {
			provCur = i
			break
		}
	}
	m.selects[siProvider] = selectState{options: provOptions, cur: provCur}

	// Toggles.
	m.toggles[toiQueue] = s.workload.HasQueue
	m.toggles[toiObjStore] = s.workload.HasObjStore
	m.toggles[toiCache] = s.workload.HasCache
	m.toggles[toiOvercommit] = cfg.Capacity.AllowOvercommit
	m.toggles[toiAirgapped] = cfg.Airgapped
	m.toggles[toiKyverno] = cfg.KyvernoEnabled
	m.toggles[toiCertMgr] = cfg.CertManagerEnabled
	m.toggles[toiCNPG] = cfg.CNPGEnabled
	m.toggles[toiCrossplane] = cfg.CrossplaneEnabled
	m.toggles[toiExtSecrets] = cfg.ExternalSecretsEnabled
	m.toggles[toiOTEL] = cfg.OTELEnabled
	m.toggles[toiGrafana] = cfg.GrafanaEnabled
	m.toggles[toiVictoria] = cfg.VictoriaMetricsEnabled
	m.toggles[toiMetrics] = cfg.EnableMetricsServer
	m.toggles[toiSPIRE] = cfg.SPIREEnabled
	m.toggles[toiTCO] = cfg.HardwareCostUSD != 0 || cfg.HardwareWatts != 0 || cfg.HardwareKWHRateUSD != 0 || cfg.HardwareSupportUSDMonth != 0

	// Cost-tab credential inputs — seeded from saved credentials.
	credsInit := [ccCount]string{
		cfg.Cost.Credentials.AWSAccessKeyID,
		cfg.Cost.Credentials.AWSSecretAccessKey,
		cfg.Cost.Credentials.GCPAPIKey,
		cfg.Cost.Credentials.HetznerToken,
		cfg.Cost.Credentials.DigitalOceanToken,
		cfg.Cost.Credentials.IBMCloudAPIKey,
	}
	for i := 0; i < ccCount; i++ {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Width = 30
		if i != ccAWSKeyID { // mask all secrets except the non-secret key ID
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '·'
		}
		ti.SetValue(credsInit[i])
		m.costCredsInputs[i] = ti
	}
	m.costCredsMode = !cfg.CostCompareEnabled
	if m.costCredsMode {
		m.costCredsInputs[0].Focus()
	}

	// Subscribe to log ring for the [logs] tab.
	m.logSub = globalLogRing.Subscribe()
	m.logLines = globalLogRing.Lines()

	// Filter input for the logs tab.
	fi := textinput.New()
	fi.Placeholder = "filter pattern…"
	fi.Prompt = "/"
	fi.Width = 40
	m.logFilterInput = fi

	// Config selection: skip the list when --config-name was passed explicitly.
	ni := textinput.New()
	ni.Placeholder = "e.g. prod-eu-low-cost"
	ni.Prompt = "> "
	ni.Width = 40
	m.cfgNewInput = ni
	if cfg.ConfigNameExplicit {
		m.cfgSelected = true
		m.activeTab = tabProvision
	} else {
		m.cfgScreen = cfgScreenList
		m.cfgLoading = true
	}

	// Focus the first visible input.
	cmd := m.textInputs[tiKindName].Focus()
	_ = cmd // will be returned from Init

	return m
}

// preserveTransientState copies display state that survives a config reload.
// Add new persistent display fields here — not in the cfgEntryLoadMsg handler.
func preserveTransientState(old, next dashModel) dashModel {
	next.costRows = old.costRows
	next.costLoading = old.costLoading
	next.costPeriodIdx = old.costPeriodIdx
	next.logLines = old.logLines
	next.logSub = old.logSub
	next.sysSampler = old.sysSampler
	next.sysStats = old.sysStats
	next.width = old.width
	next.height = old.height
	next.cfg = old.cfg
	return next
}

// inTextField reports whether the active tab has a text input focused, so
// global shortcuts (number keys, [ / ] timeframe, etc.) yield to typing.
func (m dashModel) inTextField() bool {
	switch m.activeTab {
	case tabProvision:
		return m.focus < len(dashFields) && dashFields[m.focus].kind == fkText
	case tabConfig:
		return m.cfgScreen == cfgScreenNewName
	case tabLogs:
		return m.logFiltering
	case tabCosts:
		return m.costCredsMode
	default:
		return false
	}
}

func (m dashModel) Init() tea.Cmd {
	m.lastDirty = time.Now()
	cmds := []tea.Cmd{
		textinput.Blink,
		m.textInputs[tiKindName].Focus(),
		m.kickRefreshCmd(),
		m.watchLogsCmd(),
		m.sysStatsTickCmd(),
	}
	if m.cfgScreen == cfgScreenList && m.cfgLoading {
		cmds = append(cmds, m.loadCfgListCmd())
	}
	return tea.Batch(cmds...)
}

// sysStatsTickCmd samples process stats after 2 s and delivers a sysStatsMsg.
func (m dashModel) sysStatsTickCmd() tea.Cmd {
	sampler := m.sysSampler
	return tea.Tick(2*time.Second, func(_ time.Time) tea.Msg {
		return sysStatsMsg{s: sampler.Sample()}
	})
}

// watchLogsCmd returns a command that waits for a log-ring notification and
// fires a logUpdateMsg.  Called from Init and re-scheduled after each msg.
func (m dashModel) watchLogsCmd() tea.Cmd {
	if m.logSub == nil {
		return nil
	}
	sub := m.logSub
	return func() tea.Msg {
		<-sub
		return logUpdateMsg{}
	}
}

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
	case focProxmoxDefaultTmpl,
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

// ─── update ──────────────────────────────────────────────────────────────────

func (m dashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.termRunning && m.termPTY != nil {
			_ = pty.Setsize(m.termPTY, &pty.Winsize{
				Rows: uint16(m.termH - 2),
				Cols: uint16(msg.Width),
			})
		}
		return m, nil

	case tea.MouseMsg:
		// Left-click on the tab bar row (Y==0) switches tabs.
		if msg.Action == tea.MouseActionPress &&
			msg.Button == tea.MouseButtonLeft &&
			msg.Y == 0 {
			if tab, ok := tabAtX(msg.X); ok && tab != tabEditor {
				if tab == tabConfig || m.cfgReady() {
					m.activeTab = tab
					return m, nil
				}
			}
		}
		// Scroll wheel: route to the active tab.
		if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
			up := msg.Button == tea.MouseButtonWheelUp
			switch m.activeTab {
			case tabLogs:
				if up {
					m.logScroll++
				} else if m.logScroll > 0 {
					m.logScroll--
				}
			case tabCosts:
				if !m.costCredsMode && len(m.costRows) > 0 {
					if up {
						m.costVendor = (m.costVendor - 1 + len(m.costRows)) % len(m.costRows)
					} else {
						m.costVendor = (m.costVendor + 1) % len(m.costRows)
					}
				}
			case tabConfig:
				// Scroll the config selection list.
				total := len(m.cfgCandidates) + 1
				if up && m.cfgListCursor > 0 {
					m.cfgListCursor--
				} else if !up && m.cfgListCursor < total-1 {
					m.cfgListCursor++
				}
			case tabProvision:
				m = m.moveFocus(!up) // wheel-up = move backward = up the form
			}
			return m, nil
		}
		// Left-click in provision content area: click-to-focus a field.
		if msg.Action == tea.MouseActionPress &&
			msg.Button == tea.MouseButtonLeft &&
			msg.Y >= 1 &&
			m.activeTab == tabProvision {
			if fid, ok := m.focusAtConfigRow(msg.Y - 1); ok {
				m = m.jumpFocus(fid)
			}
			return m, nil
		}
		return m, nil

	case costRowMsg:
		if msg.row.ProviderName != "" {
			log := obs.Global()
			if msg.row.Err != nil {
				log.Error("cost: "+msg.row.ProviderName, msg.row.Err)
			} else {
				log.Info("cost: "+msg.row.ProviderName,
					slog.String("monthly_usd", fmt.Sprintf("$%.2f", msg.row.Estimate.TotalUSDMonthly)))
			}
			m.costRows = append(m.costRows, msg.row)
		}
		if msg.done {
			m.costLoading = false
			log := obs.Global()
			if len(m.costRows) == 0 {
				log.Error("cost fetch", fmt.Errorf("no providers matched (InfraProvider filter or airgap)"))
			} else {
				ok := 0
				for _, r := range m.costRows {
					if r.Err == nil {
						ok++
					}
				}
				log.Info("cost fetch complete", slog.Int("ok", ok), slog.Int("total", len(m.costRows)))
			}
			return m, nil
		}
		return m, waitForCostRowCmd(msg.ch)

	case tickMsg:
		if time.Since(m.lastDirty) < 380*time.Millisecond {
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
		}
		m.refreshPending = false
		m.costLoading = true
		m.costRows = nil // clear so each provider appears as it lands
		return m, m.kickRefreshCmd()

	case sysStatsMsg:
		m.sysStats = msg.s
		return m, m.sysStatsTickCmd()

	case logUpdateMsg:
		m.logLines = globalLogRing.Lines()
		return m, m.watchLogsCmd()

	case editorFinishedMsg:
		if msg.resource != nil {
			// Kind resource editor: read the edited temp file and apply back.
			m.editorSaving = true
			res := msg.resource
			tmpFile := msg.tempFile
			cfg := m.cfg
			return m, func() tea.Msg {
				err := applyEditedResourceToKind(cfg, res, tmpFile)
				_ = os.Remove(tmpFile)
				return editorSaveMsg{err: err}
			}
		}
		// yage config editor: reload config from YAML if ConfigFile is set.
		if m.cfg.ConfigFile != "" {
			_ = config.ApplyYAMLFile(m.cfg, m.cfg.ConfigFile)
			m2 := newDashModel(m.cfg, m.s)
			m2.activeTab = tabProvision
			m2.width = m.width
			m2.height = m.height
			return m2, tea.Batch(textinput.Blink, m2.kickRefreshCmd())
		}
		m.activeTab = tabProvision
		return m, nil

	case editorResourcesMsg:
		m.editorLoading = false
		if msg.err != nil {
			obs.Global().Error("editor: list resources", msg.err)
			m.editorErr = msg.err.Error()
		} else {
			m.editorItems = msg.items
			m.editorSelected = 0
			m.editorErr = ""
		}
		return m, nil

	case editorSaveMsg:
		m.editorSaving = false
		if msg.err != nil {
			obs.Global().Error("editor: save resource", msg.err)
			m.editorErr = "save failed: " + msg.err.Error()
		} else {
			m.editorErr = ""
			// Reload the resource list to reflect any changes.
			m.editorLoading = true
			return m, m.loadEditorResourcesCmd()
		}
		return m, nil

	case kindResourceReadyMsg:
		res := msg.resource
		tmpFile := msg.tempFile
		return m, tea.ExecProcess(exec.Command(resolveEditor(), tmpFile), func(err error) tea.Msg {
			return editorFinishedMsg{err: err, resource: res, tempFile: tmpFile}
		})

	case ptyOutputMsg:
		if len(msg.data) > 0 {
			(&m).processTermBytes(msg.data)
		}
		if m.termRunning {
			return m, m.watchPTYCmd()
		}
		return m, nil

	case ptyExitMsg:
		m.termRunning = false
		m.termFocused = false
		if m.termPTY != nil {
			_ = m.termPTY.Close()
			m.termPTY = nil
		}
		if m.termCmd != nil {
			cmd := m.termCmd
			m.termCmd = nil
			go func(cmd *exec.Cmd) {
				_ = cmd.Wait()
			}(cmd)
		}
		return m, nil

	case saveKindMsg:
		m.saveKindLoading = false
		m.saveKindDone = true
		m.saveKindErr = msg.err
		if msg.err != nil {
			obs.Global().Error("save to kind", msg.err)
		} else {
			obs.Global().Info("saved config to kind", slog.String("cluster", m.cfg.KindClusterName))
		}
		return m, nil

	case depsCheckMsg:
		m.depsRunning = false
		m.depsTools = msg.tools
		m.depsImages = msg.images
		bad := 0
		for _, t := range msg.tools {
			if !t.OK && !t.Skip {
				bad++
			}
		}
		if bad == 0 {
			m.depsStatus = "all tools OK"
		} else {
			m.depsStatus = fmt.Sprintf("%d tool(s) need upgrade", bad)
		}
		return m, nil

	case depsUpgradeMsg:
		m.depsRunning = false
		if msg.err != nil {
			m.depsStatus = "upgrade failed: " + msg.err.Error()
		} else {
			m.depsStatus = "upgrade complete"
		}
		cfg := m.cfg
		return m, func() tea.Msg {
			return depsCheckMsg{
				tools:  installer.CheckDeps(cfg),
				images: installer.CheckProviderImages(cfg),
			}
		}

	case cfgListMsg:
		m.cfgLoading = false
		if msg.err != nil {
			m.cfgLoadErr = msg.err.Error()
		} else {
			m.cfgCandidates = msg.candidates
			m.cfgListCursor = 0
			m.cfgLoadErr = ""
		}
		return m, nil

	case cfgEntryLoadMsg:
		m.cfgLoading = false
		if msg.err != nil {
			m.cfgLoadErr = msg.err.Error()
			return m, nil
		}
		// Copy loaded fields back into the original cfg pointer so the
		// caller's reference remains valid after the dashboard exits.
		*m.cfg = *msg.cfg
		m.s.initFromConfig(m.cfg)
		// Rebuild all text inputs / selects / toggles from the loaded cfg.
		newM := newDashModel(m.cfg, m.s)
		newM.cfgSelected = true
		newM.activeTab = tabProvision
		newM = preserveTransientState(m, newM)
		return newM, tea.Batch(textinput.Blink, newM.kickRefreshCmd(), newM.watchLogsCmd())

	case saveCostCredsMsg:
		if msg.err != nil {
			obs.Global().Error("save cost credentials to kind", msg.err)
			m.costCredsStatus = "warning: could not save to kind: " + msg.err.Error()
		} else {
			m.costCredsStatus = "saved"
		}
		return m, nil

	case tea.KeyMsg:
		key := msg.Type
		keyStr := msg.String()

		// ── Ctrl+S: save config to kind without quitting ──
		if key == tea.KeyCtrlS && m.activeTab != tabCosts {
			if !m.saveKindLoading {
				m.flushToCfg()
				m.saveKindLoading = true
				m.saveKindDone = false
				m.saveKindErr = nil
				cfg := m.cfg
				return m, func() tea.Msg {
					return saveKindMsg{err: kindsync.WriteBootstrapConfigSecret(cfg)}
				}
			}
			return m, nil
		}

		// ── Esc/q: quit, unless terminal is focused (Esc unfocuses instead) ──
		if key == tea.KeyEsc {
			if m.termFocused {
				m.termFocused = false
				return m, nil
			}
			m.done = false
			return m, tea.Quit
		}
		if keyStr == "q" && !m.termFocused {
			m.done = false
			return m, tea.Quit
		}

		// ── Ctrl+Alt+1..8: universal tab switching — works even inside text fields ──
		// Tab 1 (config) is always reachable; others require cfgSelected.
		switch keyStr {
		case "ctrl+alt+1":
			m.activeTab = tabConfig
			return m, nil
		}
		if m.cfgReady() {
			switch keyStr {
			case "ctrl+alt+2":
				m.activeTab = tabProvision
				return m, nil
			case "ctrl+alt+3":
				m.activeTab = tabEditor
				return m, m.switchToEditorTab()
			case "ctrl+alt+4":
				m.activeTab = tabCosts
				return m, nil
			case "ctrl+alt+5":
				m.activeTab = tabLogs
				return m, nil
			case "ctrl+alt+6":
				m.activeTab = tabDeploy
				return m, nil
			case "ctrl+alt+7":
				m.activeTab = tabDeps
				return m, nil
			case "ctrl+alt+8":
				m.activeTab = tabHelp
				return m, nil
			}
		}

		// ── Ctrl+Left/Right: universal tab cycling — works even inside text fields ──
		// tabConfig is always reachable; other tabs require cfgReady.
		if key == tea.KeyCtrlLeft || key == tea.KeyCtrlRight {
			next := m.activeTab
			if key == tea.KeyCtrlLeft {
				next = (m.activeTab - 1 + tabCount) % tabCount
			} else {
				next = (m.activeTab + 1) % tabCount
			}
			// Skip tabs that require cfgReady when not ready.
			if next != tabConfig && !m.cfgReady() {
				next = tabConfig
			}
			m.activeTab = next
			if m.activeTab == tabEditor {
				return m, m.switchToEditorTab()
			}
			return m, nil
		}

		// ── Ctrl+T: start embedded terminal / toggle focus ──
		if keyStr == "ctrl+t" {
			if m.termRunning {
				m.termFocused = !m.termFocused
				return m, nil
			}
			shell := os.Getenv("SHELL")
			if shell == "" {
				shell = "sh"
			}
			cmd := exec.Command(shell)
			cmd.Env = append(os.Environ(),
				"COLORTERM=truecolor",
				"TERM=xterm-256color",
				`PS1=\[\e[0;32m\]\u@\h\[\e[0m\]:\[\e[0;34m\]\w\[\e[0;33m\]\$\[\e[0m\] `,
			)
			cols := uint16(m.width)
			if cols == 0 {
				cols = 80
			}
			f, err := pty.StartWithSize(cmd, &pty.Winsize{
				Rows: uint16(m.termH - 2),
				Cols: cols,
			})
			if err != nil {
				m.errMsg = "terminal: " + err.Error()
				return m, nil
			}
			m.termPTY = f
			m.termCmd = cmd
			m.termRunning = true
			m.termFocused = true
			return m, tea.Batch(
				m.watchPTYCmd(),
				func() tea.Msg {
					_ = cmd.Wait()
					_ = f.Close()
					return nil
				},
			)
		}

		// ── Ctrl+Alt+↑/↓: resize terminal pane (works focused or not) ──
		// Ctrl+↑/↓ alone conflicts with macOS Mission Control shortcuts.
		if m.termRunning && msg.Alt && (key == tea.KeyCtrlUp || key == tea.KeyCtrlDown) {
			prev := m.termH
			if key == tea.KeyCtrlUp {
				m.termH--
			} else {
				m.termH++
			}
			if m.termH < termPaneHMin {
				m.termH = termPaneHMin
			}
			maxH := m.height / 2
			if maxH < termPaneHMin {
				maxH = termPaneHMin
			}
			if m.termH > maxH {
				m.termH = maxH
			}
			if m.termH != prev && m.termPTY != nil {
				cols := uint16(m.width)
				if cols == 0 {
					cols = 80
				}
				_ = pty.Setsize(m.termPTY, &pty.Winsize{
					Rows: uint16(m.termH - 2),
					Cols: cols,
				})
			}
			return m, nil
		}

		// ── terminal focus: route all keys to PTY ──
		if m.termFocused && m.termRunning {
			if b := keyMsgToBytes(msg); len(b) > 0 {
				f := m.termPTY
				return m, func() tea.Msg {
					_, _ = f.Write(b)
					return nil
				}
			}
			return m, nil
		}

		// ── tab switching: left/right arrows or number keys 1-9 ──
		// (Only when not in a text input on the provision tab, and a config is selected.)
		switch {
		case !m.inTextField() && keyStr == "1":
			m.activeTab = tabConfig
			return m, nil
		case !m.inTextField() && keyStr == "2" && m.cfgReady():
			m.activeTab = tabProvision
			return m, nil
		case !m.inTextField() && keyStr == "3" && m.cfgReady():
			m.activeTab = tabEditor
			return m, m.switchToEditorTab()
		case !m.inTextField() && keyStr == "4" && m.cfgReady():
			m.activeTab = tabCosts
			return m, nil
		case !m.inTextField() && keyStr == "5" && m.cfgReady():
			m.activeTab = tabLogs
			return m, nil
		case !m.inTextField() && keyStr == "6" && m.cfgReady():
			m.activeTab = tabDeploy
			return m, nil
		case !m.inTextField() && keyStr == "7" && m.cfgReady():
			m.activeTab = tabDeps
			return m, nil
		case !m.inTextField() && keyStr == "8" && m.cfgReady():
			m.activeTab = tabHelp
			return m, nil
		case (key == tea.KeyLeft || key == tea.KeyRight) && !m.inTextField() && m.activeTab != tabConfig && m.activeTab != tabProvision && m.cfgReady():
			// Only cycle tabs with arrows when not on config/provision (those use arrows for fields/list).
			if key == tea.KeyLeft {
				m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
			} else {
				m.activeTab = (m.activeTab + 1) % tabCount
			}
			if m.activeTab == tabEditor {
				return m, m.switchToEditorTab()
			}
			return m, nil
		}

		// ── per-tab key handling ──
		switch m.activeTab {

		case tabConfig:
			return m.updateConfigTab(msg)

		case tabProvision:
			if m.cfgReady() {
				return m.updateProvisionTab(msg)
			}

		case tabEditor:
			if m.cfgReady() {
				return m.updateEditorTab(msg)
			}

		case tabLogs:
			if m.cfgReady() {
				return m.updateLogsTab(msg)
			}

		case tabCosts:
			if m.cfgReady() {
				return m.updateCostsTab(msg)
			}

		case tabDeploy:
			if m.cfgReady() {
				return m.updateDeployTab(msg)
			}

		case tabDeps:
			if m.cfgReady() {
				return m.updateDepsTab(msg)
			}
		}
	}

	return m, nil
}

// updateConfigTab dispatches key events to the active config sub-screen.
func (m dashModel) updateConfigTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.cfgScreen {
	case cfgScreenList:
		return m.updateCfgListScreen(msg)
	case cfgScreenNewName:
		return m.updateCfgNewNameScreen(msg)
	}
	return m, nil
}

// updateProvisionTab handles key events on the provision tab (full edit form).
func (m dashModel) updateProvisionTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	return m.updateCfgEditScreen(msg)
}

// updateCfgListScreen handles keys on the config-list screen.
func (m dashModel) updateCfgListScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.cfgLoading {
		return m, nil
	}
	key := msg.Type
	keyStr := msg.String()
	total := len(m.cfgCandidates) + 1 // +1 for the "[ + New config ]" sentinel
	switch {
	case key == tea.KeyUp:
		if m.cfgListCursor > 0 {
			m.cfgListCursor--
		}
	case key == tea.KeyDown:
		if m.cfgListCursor < total-1 {
			m.cfgListCursor++
		}
	case key == tea.KeyEnter:
		if m.cfgListCursor == len(m.cfgCandidates) {
			// New config sentinel selected.
			m.cfgScreen = cfgScreenNewName
			m.cfgNewInput.SetValue("")
			cmd := m.cfgNewInput.Focus()
			return m, cmd
		}
		c := m.cfgCandidates[m.cfgListCursor]
		m.cfgLoading = true
		m.cfgLoadErr = ""
		return m, m.loadCfgEntryCmd(c)
	case keyStr == "n":
		m.cfgScreen = cfgScreenNewName
		m.cfgNewInput.SetValue("")
		cmd := m.cfgNewInput.Focus()
		return m, cmd
	case keyStr == "r":
		m.cfgLoading = true
		m.cfgLoadErr = ""
		m.cfgCandidates = nil
		return m, m.loadCfgListCmd()
	}
	return m, nil
}

// updateCfgNewNameScreen handles keys while entering a new config name.
func (m dashModel) updateCfgNewNameScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	switch {
	case key == tea.KeyEsc:
		m.cfgScreen = cfgScreenList
		m.cfgNewInput.Blur()
		return m, nil
	case key == tea.KeyEnter:
		name := strings.TrimSpace(m.cfgNewInput.Value())
		m.cfgNewInput.Blur()
		if name != "" {
			m.cfg.ConfigName = name
			m.cfg.ConfigNameExplicit = true
		}
		m.cfgSelected = true
		m.activeTab = tabProvision
		return m, textinput.Blink
	default:
		var cmd tea.Cmd
		m.cfgNewInput, cmd = m.cfgNewInput.Update(msg)
		return m, cmd
	}
}

// updateCfgEditScreen handles key events in the full config edit form.
func (m dashModel) updateCfgEditScreen(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	keyStr := msg.String()

	switch {
	case keyStr == "ctrl+l":
		m.activeTab = tabLogs
		return m, nil

	case key == tea.KeyUp:
		m = m.moveFocus(false)
		return m, textinput.Blink

	case key == tea.KeyDown:
		m = m.moveFocus(true)
		return m, textinput.Blink
	}

	// Per-field-kind handling.
	meta := dashFields[m.focus]
	switch meta.kind {
	case fkToggle:
		if key == tea.KeySpace || key == tea.KeyEnter {
			m.toggles[meta.subIdx] = !m.toggles[meta.subIdx]
			if meta.costKey {
				return m, m.markDirty()
			}
		}

	case fkSelect:
		switch {
		case key == tea.KeyRight || key == tea.KeyEnter || keyStr == "l":
			m.selects[meta.subIdx].next()
			if meta.subIdx == siMode {
				m = m.rebuildProviderList()
			}
			if meta.costKey {
				return m, m.markDirty()
			}
		case key == tea.KeyLeft || keyStr == "h":
			m.selects[meta.subIdx].prev()
			if meta.subIdx == siMode {
				m = m.rebuildProviderList()
			}
			if meta.costKey {
				return m, m.markDirty()
			}
		}

	case fkText:
		if key == tea.KeyTab || key == tea.KeyShiftTab {
			return m, nil
		}
		ti, cmd := m.textInputs[meta.subIdx].Update(msg)
		m.textInputs[meta.subIdx] = ti
		if meta.costKey {
			return m, tea.Batch(cmd, m.markDirty())
		}
		return m, cmd
	}

	return m, nil
}

// updateLogsTab handles key events on the logs tab (scroll + filter).
func (m dashModel) updateLogsTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// When filter input is open, route all keys there except Esc/Enter.
	if m.logFiltering {
		switch msg.Type {
		case tea.KeyEsc:
			m.logFiltering = false
			m.logFilter = ""
			m.logFilterInput.SetValue("")
			m.logFilterInput.Blur()
			return m, nil
		case tea.KeyEnter:
			m.logFiltering = false
			m.logFilter = m.logFilterInput.Value()
			m.logFilterInput.Blur()
			return m, nil
		default:
			var cmd tea.Cmd
			m.logFilterInput, cmd = m.logFilterInput.Update(msg)
			m.logFilter = m.logFilterInput.Value()
			return m, cmd
		}
	}

	key := msg.Type
	keyStr := msg.String()
	switch {
	case keyStr == "/":
		// Open filter bar (vim-style).
		m.logFiltering = true
		m.logFilterInput.SetValue("")
		m.logFilter = ""
		cmd := m.logFilterInput.Focus()
		return m, cmd
	case keyStr == "esc" || key == tea.KeyEsc:
		// Clear active filter.
		m.logFilter = ""
		m.logFilterInput.SetValue("")
	case key == tea.KeyUp:
		m.logScroll++
	case key == tea.KeyDown:
		if m.logScroll > 0 {
			m.logScroll--
		}
	case key == tea.KeyPgUp:
		m.logScroll += 10
	case key == tea.KeyPgDown:
		if m.logScroll > 10 {
			m.logScroll -= 10
		} else {
			m.logScroll = 0
		}
	case keyStr == "g":
		// Top.
		m.logScroll = len(m.logLines)
	case keyStr == "G":
		// Bottom (follow).
		m.logScroll = 0
	case key == tea.KeyCtrlW:
		m.logWrap = !m.logWrap
	}
	return m, nil
}

// updateDeployTab handles key events on the deploy tab.
func (m dashModel) updateDeployTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	switch {
	case key == tea.KeyTab || key == tea.KeyDown:
		m.deployFocused = (m.deployFocused + 1) % 2
	case key == tea.KeyShiftTab || key == tea.KeyUp:
		m.deployFocused = (m.deployFocused - 1 + 2) % 2
	case key == tea.KeyEnter || key == tea.KeySpace:
		switch m.deployFocused {
		case 0:
			if !m.saveKindLoading {
				m.flushToCfg()
				m.saveKindLoading = true
				m.saveKindDone = false
				m.saveKindErr = nil
				cfg := m.cfg
				return m, func() tea.Msg {
					return saveKindMsg{err: kindsync.WriteBootstrapConfigSecret(cfg)}
				}
			}
		case 1:
			m.flushToCfg()
			m.deployRequested = true
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// updateCostsTab handles key events on the costs tab.
func (m dashModel) updateCostsTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	keyStr := msg.String()
	if m.costCredsMode {
		return m.updateCostsCredsForm(msg)
	}
	switch {
	case keyStr == "c" || keyStr == "e":
		m.costCredsMode = true
		m.costCredsInputs[m.costCredsFocus].Focus()
		return m, textinput.Blink
	case key == tea.KeyUp:
		if len(m.costRows) > 0 {
			m.costVendor = (m.costVendor - 1 + len(m.costRows)) % len(m.costRows)
		}
	case key == tea.KeyDown:
		if len(m.costRows) > 0 {
			m.costVendor = (m.costVendor + 1) % len(m.costRows)
		}
	case keyStr == "[":
		if m.costPeriodIdx > 0 {
			m.costPeriodIdx--
		}
	case keyStr == "]":
		if m.costPeriodIdx < len(costWindows)-1 {
			m.costPeriodIdx++
		}
	}
	return m, nil
}

// updateCostsCredsForm handles key events inside the credential entry form.
func (m dashModel) updateCostsCredsForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	keyStr := msg.String()

	switch {
	case key == tea.KeyEnter:
		if m.costCredsFocus == ccCount-1 {
			// Last field: submit. Evaluate saveCostCredsCmd first so
			// pointer-receiver mutations (costCredsMode=false) are
			// visible in the returned model.
			cmd := m.saveCostCredsCmd()
			return m, cmd
		}
		// Advance to next field.
		m.costCredsInputs[m.costCredsFocus].Blur()
		m.costCredsFocus++
		m.costCredsInputs[m.costCredsFocus].Focus()
		return m, textinput.Blink

	case key == tea.KeyTab:
		m.costCredsInputs[m.costCredsFocus].Blur()
		m.costCredsFocus = (m.costCredsFocus + 1) % ccCount
		m.costCredsInputs[m.costCredsFocus].Focus()
		return m, textinput.Blink

	case key == tea.KeyShiftTab:
		m.costCredsInputs[m.costCredsFocus].Blur()
		m.costCredsFocus = (m.costCredsFocus - 1 + ccCount) % ccCount
		m.costCredsInputs[m.costCredsFocus].Focus()
		return m, textinput.Blink

	case keyStr == "ctrl+s":
		cmd := m.saveCostCredsCmd()
		return m, cmd

	default:
		ti, cmd := m.costCredsInputs[m.costCredsFocus].Update(msg)
		m.costCredsInputs[m.costCredsFocus] = ti
		return m, cmd
	}
}

// saveCostCredsCmd applies the credential form values to cfg, wires the
// pricing package, and asynchronously persists to the kind Secret.
func (m *dashModel) saveCostCredsCmd() tea.Cmd {
	m.cfg.Cost.Credentials.AWSAccessKeyID = strings.TrimSpace(m.costCredsInputs[ccAWSKeyID].Value())
	m.cfg.Cost.Credentials.AWSSecretAccessKey = strings.TrimSpace(m.costCredsInputs[ccAWSSecret].Value())
	m.cfg.Cost.Credentials.GCPAPIKey = strings.TrimSpace(m.costCredsInputs[ccGCPKey].Value())
	m.cfg.Cost.Credentials.HetznerToken = strings.TrimSpace(m.costCredsInputs[ccHetznerTok].Value())
	m.cfg.Cost.Credentials.DigitalOceanToken = strings.TrimSpace(m.costCredsInputs[ccDOTok].Value())
	m.cfg.Cost.Credentials.IBMCloudAPIKey = strings.TrimSpace(m.costCredsInputs[ccIBMKey].Value())

	pricing.SetCredentials(pricing.Credentials{
		AWSAccessKeyID:     m.cfg.Cost.Credentials.AWSAccessKeyID,
		AWSSecretAccessKey: m.cfg.Cost.Credentials.AWSSecretAccessKey,
		GCPAPIKey:          m.cfg.Cost.Credentials.GCPAPIKey,
		HetznerToken:       m.cfg.Cost.Credentials.HetznerToken,
		DigitalOceanToken:  m.cfg.Cost.Credentials.DigitalOceanToken,
		IBMCloudAPIKey:     m.cfg.Cost.Credentials.IBMCloudAPIKey,
	})
	m.cfg.CostCompareEnabled = true
	m.costCredsMode = false
	m.costCredsInputs[m.costCredsFocus].Blur()

	cfg := m.cfg
	return tea.Batch(
		m.markDirty(),
		func() tea.Msg {
			creds := map[string]string{
				"aws-access-key-id":     cfg.Cost.Credentials.AWSAccessKeyID,
				"aws-secret-access-key": cfg.Cost.Credentials.AWSSecretAccessKey,
				"gcp-api-key":           cfg.Cost.Credentials.GCPAPIKey,
				"hetzner-token":         cfg.Cost.Credentials.HetznerToken,
				"digitalocean-token":    cfg.Cost.Credentials.DigitalOceanToken,
				"ibmcloud-api-key":      cfg.Cost.Credentials.IBMCloudAPIKey,
			}
			return saveCostCredsMsg{err: kindsync.WriteCostCompareSecret(cfg, creds)}
		},
	)
}

// ─── editor tab ───────────────────────────────────────────────────────────────

// cfgReady reports whether a config entry has been chosen (cfgSelected). When
// false, tab switching is locked — only tabConfig is reachable.
func (m dashModel) cfgReady() bool { return m.cfgSelected }

// loadCfgListCmd fetches all bootstrap-config Secrets from the kind cluster.
func (m dashModel) loadCfgListCmd() tea.Cmd {
	kindName := m.cfg.KindClusterName
	return func() tea.Msg {
		return cfgListMsg{candidates: kindsync.ListBootstrapCandidates(kindName)}
	}
}

// loadCfgEntryCmd merges the selected bootstrap entry into a cfg copy and
// returns cfgEntryLoadMsg with the fully-populated config.
func (m dashModel) loadCfgEntryCmd(c kindsync.BootstrapCandidate) tea.Cmd {
	cfgCopy := *m.cfg
	return func() tea.Msg {
		cfgCopy.KindClusterName = c.KindCluster
		cfgCopy.ConfigName = c.ConfigName
		if !cfgCopy.WorkloadClusterNameExplicit && c.Workload != "" {
			cfgCopy.WorkloadClusterName = c.Workload
		}
		_ = kindsync.MergeBootstrapConfigFromKind(&cfgCopy)
		kindsync.MergeBootstrapSecretsFromKind(&cfgCopy)
		_ = kindsync.ReadCostCompareSecret(&cfgCopy)
		disableProvidersMissingCredentials(&cfgCopy)
		return cfgEntryLoadMsg{cfg: &cfgCopy}
	}
}

// switchToEditorTab transitions to the editor tab and kicks a resource list load.
func (m dashModel) switchToEditorTab() tea.Cmd {
	if !m.editorLoading && len(m.editorItems) == 0 {
		return m.loadEditorResourcesCmd()
	}
	return nil
}

// loadEditorResourcesCmd lists Secrets and ConfigMaps in the yage-system
// namespace on the kind management cluster.
func (m dashModel) loadEditorResourcesCmd() tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		kctx := "kind-" + cfg.KindClusterName
		cli, err := k8sclient.ForContext(kctx)
		if err != nil {
			e := fmt.Errorf("connect to %s: %w", kctx, err)
			obs.Global().Error("editor: kind connect", e)
			return editorResourcesMsg{err: e}
		}
		bg := context.Background()
		var items []editorResource

		cfgNS := kindsync.BootstrapConfigNamespace(cfg)
		secrets, err := cli.Typed.CoreV1().Secrets(cfgNS).List(bg, metav1.ListOptions{})
		if err != nil {
			return editorResourcesMsg{err: fmt.Errorf("list secrets: %w", err)}
		}
		for _, s := range secrets.Items {
			items = append(items, editorResource{Kind: "Secret", Name: s.Name})
		}

		cms, err := cli.Typed.CoreV1().ConfigMaps(cfgNS).List(bg, metav1.ListOptions{})
		if err == nil {
			for _, cm := range cms.Items {
				items = append(items, editorResource{Kind: "ConfigMap", Name: cm.Name})
			}
		}

		sort.Slice(items, func(i, j int) bool {
			if items[i].Kind != items[j].Kind {
				return items[i].Kind < items[j].Kind
			}
			return items[i].Name < items[j].Name
		})
		return editorResourcesMsg{items: items}
	}
}

// updateEditorTab handles key events on the editor tab.
func (m dashModel) updateEditorTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	keyStr := msg.String()
	if m.editorLoading || m.editorSaving {
		return m, nil
	}
	switch {
	case key == tea.KeyUp || keyStr == "k":
		if m.editorSelected > 0 {
			m.editorSelected--
		}
	case key == tea.KeyDown || keyStr == "j":
		if m.editorSelected < len(m.editorItems)-1 {
			m.editorSelected++
		}
	case keyStr == "r":
		m.editorLoading = true
		m.editorErr = ""
		return m, m.loadEditorResourcesCmd()
	case key == tea.KeyEnter:
		if len(m.editorItems) == 0 {
			return m, nil
		}
		res := m.editorItems[m.editorSelected]
		return m, m.openKindResourceEditorCmd(res)
	}
	return m, nil
}

// openKindResourceEditorCmd fetches a Secret or ConfigMap from kind, decodes
// its data into a cleartext temp file, and opens $EDITOR on it.
func (m dashModel) openKindResourceEditorCmd(res editorResource) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		kctx := "kind-" + cfg.KindClusterName
		cli, err := k8sclient.ForContext(kctx)
		if err != nil {
			return editorResourcesMsg{err: fmt.Errorf("connect to %s: %w", kctx, err)}
		}
		bg := context.Background()

		var body string
		cfgNS2 := kindsync.BootstrapConfigNamespace(cfg)
		switch res.Kind {
		case "Secret":
			sec, err := cli.Typed.CoreV1().Secrets(cfgNS2).Get(bg, res.Name, metav1.GetOptions{})
			if err != nil {
				return editorResourcesMsg{err: fmt.Errorf("get secret %s: %w", res.Name, err)}
			}
			body = secretToEditableYAML(sec.Data, res, cfgNS2)
		case "ConfigMap":
			cm, err := cli.Typed.CoreV1().ConfigMaps(cfgNS2).Get(bg, res.Name, metav1.GetOptions{})
			if err != nil {
				return editorResourcesMsg{err: fmt.Errorf("get configmap %s: %w", res.Name, err)}
			}
			body = configMapToEditableYAML(cm.Data, res, cfgNS2)
		default:
			return editorResourcesMsg{err: fmt.Errorf("unknown kind %s", res.Kind)}
		}

		tmp, err := os.CreateTemp("", "yage-kind-*.yaml")
		if err != nil {
			return editorResourcesMsg{err: err}
		}
		if _, err := tmp.WriteString(body); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return editorResourcesMsg{err: err}
		}
		_ = tmp.Close()

		resPtr := res
		return kindResourceReadyMsg{resource: &resPtr, tempFile: tmp.Name()}
	}
}

// secretToEditableYAML converts Secret data to a cleartext YAML for editing.
// Values are base64-decoded and JSON-quoted.
func secretToEditableYAML(data map[string][]byte, res editorResource, ns string) string {
	var sb strings.Builder
	sb.WriteString("# ⚠️  🔓  🎥  WARNING: CLEARTEXT SECRETS VISIBLE ON SCREEN  🎥  🔓  ⚠️\n")
	sb.WriteString("# This file contains the decoded contents of Secret: ")
	sb.WriteString(ns + "/" + res.Name + "\n")
	sb.WriteString("# Anyone watching your screen can see these values!\n")
	sb.WriteString("# The temp file is deleted automatically after you close the editor.\n")
	sb.WriteString("#\n# Format: key: \"json-quoted-value\"  (one entry per line)\n\n")
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		q, _ := json.Marshal(string(data[k]))
		sb.WriteString(k + ": " + string(q) + "\n")
	}
	return sb.String()
}

// configMapToEditableYAML converts ConfigMap data to a simple YAML for editing.
func configMapToEditableYAML(data map[string]string, res editorResource, ns string) string {
	var sb strings.Builder
	sb.WriteString("# ConfigMap: ")
	sb.WriteString(ns + "/" + res.Name + "\n\n")
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		q, _ := json.Marshal(data[k])
		sb.WriteString(k + ": " + string(q) + "\n")
	}
	return sb.String()
}

// applyEditedResourceToKind reads the edited temp file and patches the Secret
// or ConfigMap back to kind, re-encoding string values to base64 for Secrets.
func applyEditedResourceToKind(cfg *config.Config, res *editorResource, tmpFile string) error {
	raw, err := os.ReadFile(tmpFile)
	if err != nil {
		return err
	}
	kv := parseEditableYAML(string(raw))
	if len(kv) == 0 {
		return nil
	}
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return err
	}
	bg := context.Background()
	ns := kindsync.BootstrapConfigNamespace(cfg)

	switch res.Kind {
	case "Secret":
		data := make(map[string][]byte, len(kv))
		for k, v := range kv {
			data[k] = []byte(v)
		}
		sec, err := cli.Typed.CoreV1().Secrets(ns).Get(bg, res.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		sec.Data = data
		_, err = cli.Typed.CoreV1().Secrets(ns).Update(bg, sec, metav1.UpdateOptions{})
		return err
	case "ConfigMap":
		cm, err := cli.Typed.CoreV1().ConfigMaps(ns).Get(bg, res.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		cm.Data = kv
		_, err = cli.Typed.CoreV1().ConfigMaps(ns).Update(bg, cm, metav1.UpdateOptions{})
		return err
	}
	return fmt.Errorf("unknown kind %s", res.Kind)
}

// parseEditableYAML parses the editable YAML format (key: "json-quoted-value")
// skipping comment lines. Returns the decoded key-value map.
func parseEditableYAML(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		k := strings.TrimSpace(line[:idx])
		v := strings.TrimSpace(line[idx+1:])
		if k == "" {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(v), &s); err == nil {
			out[k] = s
		} else {
			// Plain unquoted value — try as base64 (for round-tripping raw binary).
			if dec, err2 := base64.StdEncoding.DecodeString(v); err2 == nil {
				out[k] = string(dec)
			} else {
				out[k] = v
			}
		}
	}
	return out
}

// renderEditorTab renders the kind resource browser.
func (m dashModel) renderEditorTab(w, h int) string {
	var lines []string
	lines = append(lines, stHdr.Render(" yage-system resources  (enter=edit, r=refresh)"))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))

	if m.editorLoading {
		lines = append(lines, stMuted.Render("  loading…"))
	} else if m.editorSaving {
		lines = append(lines, stWarn.Render("  saving…"))
	} else if m.editorErr != "" {
		lines = append(lines, stBad.Render("  "+m.editorErr))
		lines = append(lines, stMuted.Render("  r = retry"))
	} else if len(m.editorItems) == 0 {
		lines = append(lines, stMuted.Render("  no resources found in "+kindsync.BootstrapConfigNamespace(m.cfg)))
		lines = append(lines, stMuted.Render("  r = refresh"))
	} else {
		for i, res := range m.editorItems {
			var kindBadge string
			if res.Kind == "Secret" {
				kindBadge = stWarn.Render("🔑 Secret    ")
			} else {
				kindBadge = stMuted.Render("📄 ConfigMap ")
			}
			name := res.Name
			if i == m.editorSelected {
				lines = append(lines, stAccent.Render("▸ ")+kindBadge+stBold.Render(name))
			} else {
				lines = append(lines, "  "+kindBadge+stMuted.Render(name))
			}
		}
		lines = append(lines, "")
		lines = append(lines, stMuted.Render(fmt.Sprintf("  ↑/↓  navigate    enter  edit in %s    r  refresh", resolveEditor())))
		if m.editorSelected < len(m.editorItems) &&
			m.editorItems[m.editorSelected].Kind == "Secret" {
			lines = append(lines, "")
			lines = append(lines, stWarn.Render("  ⚠️  🎥  Editing a Secret writes values in CLEARTEXT to a temp file."))
			lines = append(lines, stWarn.Render("     Anyone who can see your screen will see the secret values."))
		}
	}

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// openEditorCmd launches the resolved editor on cfg.ConfigFile (or a temp file).
func (m dashModel) openEditorCmd() tea.Cmd {
	path := m.cfg.ConfigFile
	if path == "" {
		// No config file set — open a temp file so the user can see/edit the
		// current values in YAML form. (We write the snapshot first.)
		tmp, err := os.CreateTemp("", "yage-config-*.yaml")
		if err != nil {
			return nil
		}
		snap := m.buildSnapshotCfg()
		data, merr := marshalConfigYAML(&snap)
		if merr == nil {
			_, _ = tmp.Write(data)
		}
		tmp.Close()
		path = tmp.Name()
	}
	cmd := exec.Command(resolveEditor(), path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to launch editor %q for %q: %v\n", cmd.Path, path, err)
			return nil
		}
		return editorFinishedMsg{}
	})
}


// markDirty stamps lastDirty and schedules a debounce tick if none is pending.
func (m *dashModel) markDirty() tea.Cmd {
	m.lastDirty = time.Now()
	if m.refreshPending {
		return nil // existing tick chain will fire
	}
	m.refreshPending = true
	return tea.Tick(400*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// ─── embedded terminal helpers ───────────────────────────────────────────────

const termPaneHDefault = 12 // initial terminal pane height; user can resize with ctrl+alt+↑/↓
const termPaneHMin = 4

// watchPTYCmd reads one chunk from the PTY master fd and returns a ptyOutputMsg.
// Re-scheduled after every ptyOutputMsg while termRunning is true.
func (m dashModel) watchPTYCmd() tea.Cmd {
	f := m.termPTY
	return func() tea.Msg {
		buf := make([]byte, 4096)
		n, err := f.Read(buf)
		if n > 0 {
			return ptyOutputMsg{data: buf[:n]}
		}
		if err != nil {
			return ptyExitMsg{err: err}
		}
		return ptyOutputMsg{}
	}
}

// processTermBytes appends raw PTY output to the ring buffer.
func (m *dashModel) processTermBytes(data []byte) {
	const maxRaw = 64 * 1024
	m.termRaw = append(m.termRaw, data...)
	if len(m.termRaw) > maxRaw {
		m.termRaw = m.termRaw[len(m.termRaw)-maxRaw:]
	}
}

// keyMsgToBytes converts a bubbletea key message to the raw bytes sent to the PTY.
func keyMsgToBytes(msg tea.KeyMsg) []byte {
	if msg.Type == tea.KeyRunes {
		if msg.Alt {
			b := []byte{0x1b}
			return append(b, []byte(string(msg.Runes))...)
		}
		return []byte(string(msg.Runes))
	}
	switch msg.Type {
	case tea.KeyEnter:
		return []byte{'\r'}
	case tea.KeyBackspace:
		return []byte{0x7f}
	case tea.KeyDelete:
		return []byte{'\x1b', '[', '3', '~'}
	case tea.KeyUp:
		return []byte{'\x1b', '[', 'A'}
	case tea.KeyDown:
		return []byte{'\x1b', '[', 'B'}
	case tea.KeyRight:
		return []byte{'\x1b', '[', 'C'}
	case tea.KeyLeft:
		return []byte{'\x1b', '[', 'D'}
	case tea.KeyHome:
		return []byte{'\x1b', '[', 'H'}
	case tea.KeyEnd:
		return []byte{'\x1b', '[', 'F'}
	case tea.KeyPgUp:
		return []byte{'\x1b', '[', '5', '~'}
	case tea.KeyPgDown:
		return []byte{'\x1b', '[', '6', '~'}
	case tea.KeySpace:
		return []byte{' '}
	case tea.KeyTab:
		return []byte{'\t'}
	case tea.KeyCtrlA:
		return []byte{1}
	case tea.KeyCtrlB:
		return []byte{2}
	case tea.KeyCtrlC:
		return []byte{3}
	case tea.KeyCtrlD:
		return []byte{4}
	case tea.KeyCtrlE:
		return []byte{5}
	case tea.KeyCtrlF:
		return []byte{6}
	case tea.KeyCtrlK:
		return []byte{11}
	case tea.KeyCtrlL:
		return []byte{12}
	case tea.KeyCtrlN:
		return []byte{14}
	case tea.KeyCtrlP:
		return []byte{16}
	case tea.KeyCtrlR:
		return []byte{18}
	case tea.KeyCtrlU:
		return []byte{21}
	case tea.KeyCtrlW:
		return []byte{23}
	case tea.KeyCtrlZ:
		return []byte{26}
	default:
		return nil
	}
}

// stripNonSGR strips all ANSI escape sequences from data except SGR (color/style,
// ending in 'm'). This makes PTY output safe for lipgloss rendering while preserving
// colours.
func stripNonSGR(data []byte) string {
	out := make([]byte, 0, len(data))
	i := 0
	for i < len(data) {
		b := data[i]
		if b != 0x1b {
			if b >= 0x20 || b == '\t' {
				out = append(out, b)
			}
			i++
			continue
		}
		i++
		if i >= len(data) {
			break
		}
		switch data[i] {
		case '[': // CSI
			i++
			start := i
			for i < len(data) && !(data[i] >= 0x40 && data[i] <= 0x7e) {
				i++
			}
			if i < len(data) {
				final := data[i]
				i++
				if final == 'm' {
					out = append(out, '\x1b', '[')
					out = append(out, data[start:i]...)
				}
			}
		case ']': // OSC — skip until BEL or ST
			i++
			for i < len(data) {
				if data[i] == 0x07 {
					i++
					break
				}
				if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default:
			i++ // skip ESC + one byte
		}
	}
	return string(out)
}

// termRawToLines converts the PTY raw buffer to display-ready lines.
// It splits by \n, strips trailing \r (from \r\n sequences), handles
// progress-bar overwrites (last segment after mid-line \r), and strips
// non-SGR ANSI sequences.
func (m dashModel) termRawToLines(maxLines int) []string {
	if len(m.termRaw) == 0 {
		return nil
	}
	parts := bytes.Split(m.termRaw, []byte{'\n'})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		for len(part) > 0 && part[len(part)-1] == '\r' {
			part = part[:len(part)-1]
		}
		if idx := bytes.LastIndexByte(part, '\r'); idx >= 0 {
			part = part[idx+1:]
		}
		result = append(result, stripNonSGR(part))
	}
	// Trim trailing empty lines.
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	if len(result) > maxLines {
		return result[len(result)-maxLines:]
	}
	return result
}

// renderTermPane renders the embedded terminal pane (always below tab content
// when the terminal is running).  Returns "" when not running.
func (m dashModel) renderTermPane(w int) string {
	if !m.termRunning {
		return ""
	}
	var lines []string
	sep := stMuted.Render(strings.Repeat("─", w))
	if m.termFocused {
		lines = append(lines, sep)
		lines = append(lines, stAccent.Render("▸ Terminal")+stMuted.Render("  esc=unfocus  ctrl+alt+↑/↓=resize"))
	} else {
		lines = append(lines, sep)
		lines = append(lines, stMuted.Render("  Terminal")+stMuted.Render("  ctrl+t=focus  ctrl+alt+↑/↓=resize"))
	}
	contentH := m.termH - 2
	for _, l := range m.termRawToLines(contentH) {
		lines = append(lines, "  "+l)
	}
	for len(lines) < m.termH {
		lines = append(lines, "")
	}
	return strings.Join(lines[:m.termH], "\n")
}

// kickRefreshCmd launches streaming cost fetches and returns a cmd that
// delivers the first result. Subsequent results chain via waitForCostRowCmd.
// Returns nil when cost estimation is disabled.
func (m dashModel) kickRefreshCmd() tea.Cmd {
	if !m.cfg.CostCompareEnabled {
		return nil
	}
	snap := m.buildSnapshotCfg()
	// Cost comparison always queries every credentialled provider, regardless
	// of which provider the user has selected in the config tab. The provider
	// select is a deployment choice, not a cost-filter. Clear InfraProvider
	// so StreamWithRegions does not narrow to a single provider.
	snap.InfraProvider = ""
	snap.InfraProviderDefaulted = true
	// Capture credentials at dispatch time: pricing.SetCredentials is a
	// process-global set before kind is connected, so it may not include
	// credentials loaded later from the cost-compare-config Secret.
	c := m.cfg.Cost.Credentials
	s := m.s
	cfg := m.cfg
	return func() tea.Msg {
		pricing.SetCredentials(pricing.Credentials{
			AWSAccessKeyID:     c.AWSAccessKeyID,
			AWSSecretAccessKey: c.AWSSecretAccessKey,
			GCPAPIKey:          c.GCPAPIKey,
			HetznerToken:       c.HetznerToken,
			DigitalOceanToken:  c.DigitalOceanToken,
			IBMCloudAPIKey:     c.IBMCloudAPIKey,
		})

		// Determine geo lat/lon for nearest-region ranking.
		// Source priority: --geoip outbound IP > DataCenterLocation centroid.
		var geoLat, geoLon float64
		geoOK := false
		if cfg.GeoIPEnabled {
			s.ensureGeoLookup()
			geoLat, geoLon, geoOK = s.geoLat, s.geoLon, s.geoOK
		}
		if !geoOK {
			dc := strings.ToUpper(strings.TrimSpace(snap.Cost.Currency.DataCenterLocation))
			if dc != "" {
				if lat, lon, ok := pricing.CountryCentroid(dc); ok {
					geoLat, geoLon, geoOK = lat, lon, true
				}
			}
		}

		// Build per-provider region list (up to 4 nearest). When geo is
		// unavailable, regionsByProvider stays nil and StreamWithRegions
		// falls back to the region already in snap per provider.
		var regionsByProvider map[string][]string
		if geoOK {
			regionsByProvider = map[string][]string{}
			for _, name := range []string{"aws", "azure", "gcp", "hetzner", "digitalocean", "linode", "oci", "ibmcloud"} {
				ranked := geoRankedRegions(name, geoLat, geoLon, 4)
				if len(ranked) > 0 {
					regionsByProvider[name] = ranked
				}
			}
		}

		ch := make(chan cost.CloudCost, 64)
		cost.StreamWithRegions(&snap, cost.ScopeCloudOnly, regionsByProvider, globalLogRing, ch)
		row, ok := <-ch
		return costRowMsg{row: row, ch: ch, done: !ok}
	}
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

// ─── view ─────────────────────────────────────────────────────────────────────

func (m dashModel) View() string {
	if m.width == 0 {
		return "loading…"
	}

	tabBar := m.renderTabBar()
	termPane := m.renderTermPane(m.width)
	bottomStrip := m.renderBottomStrip()
	footer := m.renderFooter()

	// Compute usable height for tab content.
	usable := m.height - lipgloss.Height(tabBar) - lipgloss.Height(termPane) - lipgloss.Height(bottomStrip) - lipgloss.Height(footer)
	if usable < 1 {
		usable = 1
	}

	var content string
	switch m.activeTab {
	case tabConfig:
		content = m.renderConfigTab(m.width, usable)
	case tabProvision:
		content = m.renderProvisionTab(m.width, usable)
	case tabEditor:
		content = m.renderEditorTab(m.width, usable)
	case tabCosts:
		content = m.renderCostsTab(m.width, usable)
	case tabLogs:
		content = m.renderLogsTab(m.width, usable)
	case tabDeploy:
		content = m.renderDeployTab(m.width, usable)
	case tabDeps:
		content = m.renderDepsTab(m.width, usable)
	case tabHelp:
		content = m.renderHelpTab(m.width, usable)
	case tabAbout:
		content = m.renderAboutTab(m.width, usable)
	}

	return lipgloss.JoinVertical(lipgloss.Left, tabBar, content, termPane, bottomStrip, footer)
}

// renderTabBar renders the tab strip at the top with a right-aligned sysinfo widget.
func (m dashModel) renderTabBar() string {
	var parts []string
	for i := dashTab(0); i < tabCount; i++ {
		label := "[" + tabLabels[i] + "]"
		if i == m.activeTab {
			parts = append(parts, lipgloss.NewStyle().Bold(true).Underline(true).Foreground(colAccent).Render(label))
		} else {
			parts = append(parts, stMuted.Render(label))
		}
	}
	bar := strings.Join(parts, "  ")
	title := stBold.Render("yage") + "  "
	left := title + bar

	// Right-align the sysinfo widget; pad between left and widget.
	widget := m.renderSysWidget()
	// Strip ANSI for width math (widget uses lipgloss styles).
	wPlain := lipgloss.Width(widget)
	lPlain := lipgloss.Width(left)
	pad := m.width - lPlain - wPlain
	if pad < 1 {
		pad = 1
	}
	line := left + strings.Repeat(" ", pad) + widget
	return lipgloss.NewStyle().Width(m.width).Render(line) + "\n" +
		stMuted.Render(strings.Repeat("─", m.width))
}

// renderSysWidget returns a compact right-corner widget:
//
//	cpu 3%  128M  ↓1.2M ↑400B
func (m dashModel) renderSysWidget() string {
	s := m.sysStats
	if s.MemRSSBytes == 0 {
		return stMuted.Render("cpu –  – M  ↓–  ↑–")
	}

	// CPU colour: green < 20%, yellow < 60%, red ≥ 60%.
	cpuStr := fmt.Sprintf("%.0f%%", s.CPUPercent)
	var cpuStyle lipgloss.Style
	switch {
	case s.CPUPercent < 20:
		cpuStyle = stOK
	case s.CPUPercent < 60:
		cpuStyle = stWarn
	default:
		cpuStyle = stBad
	}

	// Memory: colour by absolute size.
	memStr := fmtBytes(s.MemRSSBytes)
	var memStyle lipgloss.Style
	switch {
	case s.MemRSSBytes < 256<<20: // < 256 MB
		memStyle = stOK
	case s.MemRSSBytes < 512<<20: // < 512 MB
		memStyle = stWarn
	default:
		memStyle = stBad
	}

	// Network: per-second rate; muted when idle, accent when data flowing.
	var rxRate, txRate uint64
	if s.DeltaDur > 0 {
		secs := s.DeltaDur.Seconds()
		rxRate = uint64(float64(s.NetRxDelta) / secs)
		txRate = uint64(float64(s.NetTxDelta) / secs)
	}
	netStyle := stMuted
	if rxRate > 1024 || txRate > 1024 {
		netStyle = stAccent
	}

	return stMuted.Render("cpu ") + cpuStyle.Render(cpuStr) +
		stMuted.Render("  ") + memStyle.Render(memStr) +
		stMuted.Render("  ↓") + netStyle.Render(fmtRate(rxRate)) +
		stMuted.Render(" ↑") + netStyle.Render(fmtRate(txRate))
}

// fmtBytes formats a byte count compactly: "128M", "4.2G", "512K", "64B".
func fmtBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0fK", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// fmtRate formats bytes/s compactly: "1.2M/s", "400K/s", "64B/s", "0".
func fmtRate(bps uint64) string {
	if bps == 0 {
		return "0"
	}
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1fM/s", float64(bps)/float64(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0fK/s", float64(bps)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB/s", bps)
	}
}

// tabAtX returns the dashTab at visible column x in the tab bar title row (Y==0).
// The layout is: "yage  " (6 visible chars) then "[label]  " per tab
// separated by "  ". Returns (tab, true) when x falls inside a tab label.
func tabAtX(x int) (dashTab, bool) {
	col := len("yage  ") // 6 visible chars prefix
	for i := dashTab(0); i < tabCount; i++ {
		w := len(tabLabels[i]) + 2 // "[" + label + "]"
		if x >= col && x < col+w {
			return i, true
		}
		col += w + 2 // "  " separator between tabs
	}
	return 0, false
}

// renderBottomStrip renders the live cost tally always visible below tab content.
func (m dashModel) renderBottomStrip() string {
	line := stMuted.Render(strings.Repeat("─", m.width)) + "\n"
	if !m.cfg.CostCompareEnabled {
		return line + stMuted.Render("  cost estimation: go to [costs] tab (4 / ctrl+alt+4) to enter API credentials")
	}
	if len(m.costRows) == 0 {
		var suffix string
		if m.costLoading {
			suffix = stMuted.Render("  fetching…")
		} else {
			suffix = stMuted.Render("  no cost data")
		}
		return line + suffix
	}
	sorted := m.sortedCostRows()

	// Find cheapest once.
	cheapest := 0.0
	for _, rr := range sorted {
		if rr.Err == nil && rr.Estimate.TotalUSDMonthly > 0 {
			cheapest = rr.Estimate.TotalUSDMonthly
			break
		}
	}
	budget := m.cfg.BudgetUSDMonth

	// Build fixed suffix first so we know how much room is left.
	suffix := ""
	if m.costLoading {
		suffix = "  (fetching…)"
	}
	if m.termRunning {
		if m.termFocused {
			suffix += "  [term:active]"
		} else {
			suffix += "  [term:bg]"
		}
	}

	// Greedy-fit: add provider tokens one by one until they no longer fit.
	// Each token is "  provider $NNN". Width budget = terminal width - 2
	// (leading indent) - suffix width. When a token doesn't fit we stop and
	// show "+N more" so the bar is always exactly one line.
	avail := m.width
	if avail <= 0 {
		avail = 120 // safe default before WindowSizeMsg
	}
	avail -= 2 // leading "  "
	avail -= len(suffix)

	var kept []string
	skipped := 0
	sep := "  "
	for _, r := range sorted {
		var token string
		label := r.ProviderName
		if r.Region != "" {
			label = r.ProviderName + "/" + r.Region
		}
		if r.Err != nil {
			token = label + " n/a"
		} else {
			token = label + " " + m.formatCost(r.Estimate.TotalUSDMonthly)
		}
		need := len(token)
		if len(kept) > 0 {
			need += len(sep)
		}
		if need > avail {
			skipped++
			continue
		}
		avail -= need
		kept = append(kept, token)
	}

	// Render kept tokens with colour.
	var renderedParts []string
	for _, tok := range kept {
		name := strings.Fields(tok)[0]
		// Find the original row to apply colour.
		var style lipgloss.Style
		for _, r := range sorted {
			if r.ProviderName != name {
				continue
			}
			if r.Err != nil {
				style = stMuted
			} else {
				total := r.Estimate.TotalUSDMonthly
				scaledTotal := m.costForPeriod(total)
				scaledBudget := m.costForPeriod(budget)
				switch {
				case scaledBudget > 0 && scaledTotal > scaledBudget:
					style = stBad
				case cheapest > 0 && total <= cheapest:
					style = stOK
				case cheapest > 0 && total > cheapest*1.5:
					style = stWarn
				default:
					style = lipgloss.NewStyle()
				}
			}
			break
		}
		renderedParts = append(renderedParts, style.Render(tok))
	}

	content := "  " + strings.Join(renderedParts, stMuted.Render(sep))
	if skipped > 0 {
		content += stMuted.Render(fmt.Sprintf("  +%d more", skipped))
	}
	if suffix != "" {
		content += stMuted.Render(suffix)
	}
	return line + content
}

func (m dashModel) renderFooter() string {
	shellHint := stMuted.Render("ctrl+t") + " terminal  "
	tabHint := stMuted.Render("1-8/ctrl+alt+1-8/ctrl+◄►") + " tabs  "
	var keys string
	switch m.activeTab {
	case tabConfig:
		switch m.cfgScreen {
		case cfgScreenList:
			keys = stMuted.Render("↑/↓") + " navigate  " +
				stMuted.Render("enter") + " select  " +
				stMuted.Render("n") + " new  " +
				stMuted.Render("r") + " refresh  " +
				stMuted.Render("esc/q") + " quit"
		case cfgScreenNewName:
			keys = stMuted.Render("enter") + " confirm  " +
				stMuted.Render("esc") + " back to list  " +
				stMuted.Render("esc/q") + " quit"
		default:
			keys = stMuted.Render("esc/q") + " quit"
		}
	case tabProvision:
		keys = stMuted.Render("tab/⇧tab") + " navigate  " +
			stMuted.Render("space") + " toggle  " +
			stMuted.Render("◄ ►") + " select  " +
			shellHint +
			stMuted.Render("ctrl+l") + " logs  " +
			tabHint +
			stAccent.Render("ctrl+s") + " save  " +
			stMuted.Render("esc/q") + " abort"
	case tabLogs:
		keys = stMuted.Render("j/k") + " scroll  " +
			stMuted.Render("g/G") + " top/bottom  " +
			shellHint + tabHint +
			stAccent.Render("ctrl+s") + " save  " +
			stMuted.Render("esc/q") + " abort"
	case tabCosts:
		if m.costCredsMode {
			keys = stMuted.Render("tab/⇧tab") + " navigate  " +
				stMuted.Render("enter") + " save (last field)  " +
				shellHint + tabHint +
				stAccent.Render("ctrl+s") + " save+exit  " +
				stMuted.Render("esc/q") + " abort"
		} else {
			keys = stMuted.Render("j/k") + " scroll  " +
				stMuted.Render("[/]") + " time window  " +
				stMuted.Render("c") + " edit creds  " +
				shellHint + tabHint +
				stAccent.Render("ctrl+s") + " save  " +
				stMuted.Render("esc/q") + " abort"
		}
	case tabDeploy:
		keys = stMuted.Render("tab") + " focus  " +
			stMuted.Render("enter") + " activate  " +
			shellHint + tabHint +
			stAccent.Render("ctrl+s") + " save  " +
			stMuted.Render("esc/q") + " abort"
	case tabDeps:
		keys = stMuted.Render("tab") + " focus  " +
			stMuted.Render("enter") + " activate  " +
			shellHint + tabHint +
			stMuted.Render("esc/q") + " abort"
	default:
		keys = shellHint + tabHint +
			stAccent.Render("ctrl+s") + " save  " +
			stMuted.Render("esc/q") + " abort"
	}
	sep := stMuted.Render(strings.Repeat("─", m.width))
	if m.errMsg != "" {
		return sep + "\n" + stBad.Render("  "+m.errMsg) + "\n" + keys
	}
	return sep + "\n" + keys
}

// ─── config tab ──────────────────────────────────────────────────────────────

const labelW = 18
const inputW = 13

// renderConfigTab dispatches to the active config sub-screen renderer.
func (m dashModel) renderConfigTab(w, h int) string {
	switch m.cfgScreen {
	case cfgScreenNewName:
		return m.renderCfgNewNameScreen(w, h)
	default:
		return m.renderCfgListScreen(w, h)
	}
}

// renderProvisionTab renders the full interactive provision edit form.
func (m dashModel) renderProvisionTab(w, h int) string {
	return m.renderCfgEditScreen(w, h)
}

// renderCfgListScreen renders the list of existing bootstrap configs.
func (m dashModel) renderCfgListScreen(w, h int) string {
	var lines []string
	title := fmt.Sprintf(" Configurations on kind cluster %q", m.cfg.KindClusterName)
	lines = append(lines, stHdr.Render(title))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))
	lines = append(lines, "")

	if m.cfgLoading {
		lines = append(lines, stMuted.Render("  loading…"))
	} else if m.cfgLoadErr != "" {
		lines = append(lines, stBad.Render("  "+m.cfgLoadErr))
		lines = append(lines, stMuted.Render("  r = retry"))
	} else {
		for i, c := range m.cfgCandidates {
			cursor := "  "
			if i == m.cfgListCursor {
				cursor = stAccent.Render("▸ ")
			}
			lines = append(lines, cursor+c.Label())
		}
		lines = append(lines, "")
		// "[ + New config ]" sentinel.
		sentinelIdx := len(m.cfgCandidates)
		newLabel := "[ + New config ]"
		if m.cfgListCursor == sentinelIdx {
			lines = append(lines, stAccent.Render("▸ ")+stAccent.Render(newLabel))
		} else {
			lines = append(lines, "  "+stMuted.Render(newLabel))
		}
	}

	lines = append(lines, "")
	if m.cfgLoading {
		lines = append(lines, stMuted.Render("  please wait…"))
	} else {
		lines = append(lines, stMuted.Render("  ↑/↓  navigate    enter  select    n  new config    r  refresh    esc/q  quit"))
	}
	lines = append(lines, "")
	if m.cfgSelected {
		lines = append(lines, stOK.Render("  config: "+m.cfg.ConfigName+"  — select another to switch, or press 2 to return to provision"))
	} else {
		lines = append(lines, stWarn.Render("  ⚠ Select a configuration to unlock all tabs"))
	}

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// renderCfgNewNameScreen renders the new config name input.
func (m dashModel) renderCfgNewNameScreen(w, h int) string {
	var lines []string
	lines = append(lines, stHdr.Render(" New configuration name"))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))
	lines = append(lines, "")
	lines = append(lines, stMuted.Render("  Enter a name for the new configuration."))
	lines = append(lines, stMuted.Render("  A dedicated namespace yage-<name> will be created on the kind cluster."))
	lines = append(lines, "  (leave blank to use the workload cluster name as the default)")
	lines = append(lines, "")
	lines = append(lines, "  "+m.cfgNewInput.View())
	lines = append(lines, "")
	lines = append(lines, stMuted.Render("  enter  confirm    esc  back to list"))

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// renderCfgEditScreen renders the full interactive config edit form.
func (m dashModel) renderCfgEditScreen(w, h int) string {
	var lines []string
	lastSection := ""

	// Config name badge at the top.
	if name := m.cfg.ConfigName; name != "" {
		lines = append(lines, stMuted.Render(fmt.Sprintf("  config: %s", name)))
		lines = append(lines, "")
	}

	for fid := 0; fid < focCount; fid++ {
		if m.isHidden(fid) {
			continue
		}
		meta := dashFields[fid]
		// Section header.
		if meta.section != "" && meta.section != lastSection {
			if len(lines) > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, stHdr.Render(" "+meta.section))
			lastSection = meta.section
		}

		focused := m.focus == fid
		lines = append(lines, m.renderField(fid, focused, w))
	}

	// Pad to height.
	for len(lines) < h {
		lines = append(lines, "")
	}

	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// resolveEditor returns the editor binary to use for in-place editing.
// Priority: $VISUAL → $EDITOR → first hit in editorFallbacks (OS-specific).
// Env-var values and fallback candidates are probed with exec.LookPath
// where possible. If none are found, it returns a conventional fallback
// name for exec to report on, so it never returns an empty string.
func resolveEditor() string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			if p, err := exec.LookPath(v); err == nil {
				return p
			}
			// Env var set but binary not in PATH — fall through to probe list.
		}
	}
	for _, candidate := range editorFallbacks {
		if p, err := exec.LookPath(candidate); err == nil {
			return p
		}
	}
	// Last resort: return the final fallback name even if not found —
	// exec will produce a clear error message to the user.
	if len(editorFallbacks) > 0 {
		return editorFallbacks[len(editorFallbacks)-1]
	}
	return "vi"
}

// renderEditorPlaceholder shows while waiting for the editor to launch.
func (m dashModel) renderEditorPlaceholder(w, h int) string {
	editor := resolveEditor()
	msg := stMuted.Render(fmt.Sprintf("  Opening %s…  (press any key after it exits)", editor))
	lines := []string{"", msg}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

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

// ─── costs tab ───────────────────────────────────────────────────────────────

// renderCostsTab renders the full comparison table + ASCII bar chart.
func (m dashModel) renderCostsTab(w, h int) string {
	var lines []string

	win := m.activeCostWindow()
	windowSel := stMuted.Render("[") + win.label + stMuted.Render("]") +
		stMuted.Render("  ◄[ ]►")
	title := stHdr.Render(" Provider cost comparison") + "  " + windowSel
	if m.costLoading {
		title += stMuted.Render("  refreshing…")
	}
	lines = append(lines, title)
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))

	if m.cfg.CostCompareEnabled && !m.cfg.GeoIPEnabled && m.cfg.Cost.Currency.DataCenterLocation == "" {
		lines = append(lines, stMuted.Render("  DataCenter location unset — cost comparison unavailable."))
		lines = append(lines, stMuted.Render("  Set YAGE_DATA_CENTER_LOCATION or enable GeoIP to activate region-aware pricing."))
		lines = append(lines, "")
	}

	if m.costCredsMode {
		lines = append(lines, m.renderCostsCredsForm()...)
	} else if len(m.costRows) == 0 {
		lines = append(lines, stMuted.Render("  computing…"))
	} else {
		sorted := m.sortedCostRows()

		// Find max and cheapest for bar-chart normalization.
		maxTotal := 0.0
		cheapest := 0.0
		for _, r := range sorted {
			if r.Err == nil {
				if cheapest == 0 {
					cheapest = r.Estimate.TotalUSDMonthly
				}
				if r.Estimate.TotalUSDMonthly > maxTotal {
					maxTotal = r.Estimate.TotalUSDMonthly
				}
			}
		}

		budget := m.cfg.BudgetUSDMonth
		if budget == 0 {
			if f, err := strconv.ParseFloat(strings.TrimSpace(m.textInputs[tiBudget].Value()), 64); err == nil && f > 0 {
				if u, ferr := pricing.FromTaller(f); ferr == nil {
					budget = u
				} else {
					budget = f
				}
			}
		}

		// Clamp selected vendor.
		if m.costVendor >= len(sorted) {
			m.costVendor = len(sorted) - 1
		}
		if m.costVendor < 0 {
			m.costVendor = 0
		}

		// Table header. The provider/region column is 22 chars wide to
		// accommodate "digitalocean nyc3" and similar combined labels.
		hdr := fmt.Sprintf("  %-22s %10s  %s", "provider/region", m.activeCostWindow().short, "bar chart")
		lines = append(lines, stHdr.Render(hdr))

		barW := w - 40 // chars available for bar (2+22+1+10+2 = 37 fixed + margin)
		if barW < 10 {
			barW = 10
		}

		for i, r := range sorted {
			selected := i == m.costVendor
			lines = append(lines, m.renderCostRow(r, selected, cheapest, maxTotal, budget, barW))
		}

		// Detail block for selected vendor.
		lines = append(lines, "")
		if m.costVendor < len(sorted) {
			sel := sorted[m.costVendor]
			detailLabel := sel.ProviderName
			if sel.Region != "" {
				detailLabel = sel.ProviderName + " " + sel.Region
			}
			lines = append(lines, stMuted.Render(fmt.Sprintf(" ─ %s detail ─", detailLabel)))
			if sel.Err != nil {
				lines = append(lines, stBad.Render("  "+sel.Err.Error()))
			} else {
				for _, it := range sel.Estimate.Items {
					name := it.Name
					maxNameW := w - 16
					if maxNameW < 10 {
						maxNameW = 10
					}
					if len(name) > maxNameW {
						name = name[:maxNameW] + "…"
					}
					lineStr := fmt.Sprintf("  %-*s %10s", maxNameW, name, m.formatCost(it.SubtotalUSD))
					lines = append(lines, lineStr)
				}
			}
			if budget > 0 && sel.Err == nil {
				lines = append(lines, "")
				scaledTotal := m.costForPeriod(sel.Estimate.TotalUSDMonthly)
				scaledBudget := m.costForPeriod(budget)
				w := m.activeCostWindow()
				if scaledTotal <= scaledBudget {
					lines = append(lines, stOK.Render(fmt.Sprintf("  ✓ within budget (%s / %s%s)",
						m.formatCost(sel.Estimate.TotalUSDMonthly),
						fmt.Sprintf("$%.0f", scaledBudget), w.short)))
				} else {
					lines = append(lines, stBad.Render(fmt.Sprintf("  ✗ over budget (%s / %s%s)",
						m.formatCost(sel.Estimate.TotalUSDMonthly),
						fmt.Sprintf("$%.0f", scaledBudget), w.short)))
				}
			}
		}
	}

	if !m.costCredsMode {
		// Footer: allow re-entering credential form.
		statusLine := stMuted.Render("  c = edit credentials")
		if m.costCredsStatus != "" {
			if m.costCredsStatus == "saved" {
				statusLine = stOK.Render("  ✓ credentials saved")
			} else {
				statusLine = stWarn.Render("  ⚠ " + m.costCredsStatus)
			}
		}
		// Insert footer before padding.
		lines = append(lines, "", statusLine)
	}

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// renderCostsCredsForm renders the API credential entry form for the costs tab.
func (m dashModel) renderCostsCredsForm() []string {
	var lines []string
	lines = append(lines, stHdr.Render("  API Credentials"))
	lines = append(lines, stMuted.Render("  Enter keys for the providers you want priced. Leave blank to skip (Azure, Linode, OCI use public APIs)."))
	lines = append(lines, stMuted.Render("  tab / shift+tab = move  ·  enter on last field = save  ·  ctrl+s = save"))
	lines = append(lines, "")
	for i := 0; i < ccCount; i++ {
		lbl := fmt.Sprintf("  %-22s", ccLabels[i])
		input := m.costCredsInputs[i].View()
		cursor := " "
		if i == m.costCredsFocus {
			cursor = stAccent.Render("▌")
		}
		lines = append(lines, cursor+lbl+" "+input)
	}
	if m.costCredsStatus != "" && m.costCredsStatus != "saved" {
		lines = append(lines, "")
		lines = append(lines, stWarn.Render("  ⚠ "+m.costCredsStatus))
	}
	return lines
}

// renderCostRow renders a single provider+region row with bar chart.
func (m dashModel) renderCostRow(r cost.CloudCost, selected bool, cheapest, maxTotal, budget float64, barW int) string {
	prefix := "  "
	if selected {
		prefix = stAccent.Render("▌ ")
	}

	// Combined provider/region label (max 22 chars).
	label := r.ProviderName
	if r.Region != "" {
		label = r.ProviderName + " " + r.Region
	}
	if len(label) > 22 {
		label = label[:21] + "…"
	}

	if r.Err != nil {
		row := prefix + stMuted.Render(fmt.Sprintf("%-22s  n/a", label))
		return row
	}

	total := r.Estimate.TotalUSDMonthly
	totalStr := fmt.Sprintf("%10s", m.formatCost(total))

	// Budget comparison uses period-scaled values so the budget field
	// (which is always monthly) is also scaled before the check.
	scaledBudget := m.costForPeriod(budget)
	scaledTotal := m.costForPeriod(total)
	var style lipgloss.Style
	switch {
	case scaledBudget > 0 && scaledTotal > scaledBudget:
		style = stBad
	case cheapest > 0 && total <= cheapest:
		style = stOK
	case cheapest > 0 && total > cheapest*1.5:
		style = stWarn
	default:
		style = lipgloss.NewStyle()
	}

	// ASCII bar chart using █.
	barLen := 0
	if maxTotal > 0 {
		barLen = int(float64(barW) * total / maxTotal)
	}
	if barLen < 1 && total > 0 {
		barLen = 1
	}
	bar := strings.Repeat("█", barLen)
	bar = style.Render(bar)

	nameStr := fmt.Sprintf("%-22s", label)
	if selected {
		nameStr = stAccent.Render(nameStr)
		totalStr = style.Bold(true).Render(totalStr)
	} else {
		nameStr = style.Render(nameStr)
		totalStr = style.Render(totalStr)
	}

	return prefix + nameStr + " " + totalStr + "  " + bar
}

// renderVendorRow is kept for backward compatibility (used by bottom strip logic).
func (m dashModel) renderVendorRow(r cost.CloudCost, selected bool, cheapest, budget float64, w int) string {
	prefix := "  "
	if selected {
		prefix = stAccent.Render("▌ ")
	}

	name := fmt.Sprintf("%-12s", r.ProviderName)

	if r.Err != nil {
		row := prefix + stMuted.Render(name) + stMuted.Render(" n/a")
		return row
	}

	total := r.Estimate.TotalUSDMonthly
	totalStr := fmt.Sprintf("$%8.2f", total)

	var badge string
	var style lipgloss.Style
	switch {
	case budget > 0 && total > budget:
		style = stBad
		badge = stBad.Render(" ✗ over")
	case cheapest > 0 && total <= cheapest:
		style = stOK
		badge = stOK.Render(" ✓ low")
	case cheapest > 0 && total > cheapest*1.5:
		style = stWarn
		badge = stWarn.Render(" ▲ hi")
	default:
		style = lipgloss.NewStyle()
		badge = ""
	}

	if selected {
		name = stAccent.Render(fmt.Sprintf("%-12s", r.ProviderName))
		totalStr = style.Bold(true).Render(totalStr)
	} else {
		name = style.Render(fmt.Sprintf("%-12s", r.ProviderName))
		totalStr = style.Render(totalStr)
	}

	return prefix + name + " " + totalStr + badge
}

// ─── logs tab ────────────────────────────────────────────────────────────────

func (m dashModel) renderLogsTab(w, h int) string {
	lines := m.logLines

	// Apply filter (case-insensitive substring).
	if m.logFilter != "" {
		pat := strings.ToLower(m.logFilter)
		var filtered []string
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), pat) {
				filtered = append(filtered, l)
			}
		}
		lines = filtered
	}

	// Header row + optional filter bar + separator.
	showFilterBar := m.logFiltering || m.logFilter != ""
	hdrRows := 2
	if showFilterBar {
		hdrRows = 3
	}
	contentH := h - hdrRows
	if contentH < 1 {
		contentH = 1
	}

	// Apply scroll: logScroll=0 means pinned to bottom.
	total := len(lines)
	end := total - m.logScroll
	if end > total {
		end = total
	}
	if end < 0 {
		end = 0
	}
	start := end - contentH
	if start < 0 {
		start = 0
	}

	var out []string
	hdrText := fmt.Sprintf(" Logs  [%d lines]", total)
	if m.logFilter != "" {
		hdrText += fmt.Sprintf("  filter:%q", m.logFilter)
	}
	if m.logScroll > 0 {
		hdrText += fmt.Sprintf("  scroll↑ %d", m.logScroll)
	} else {
		hdrText += "  (following)"
	}
	if m.logWrap {
		hdrText += "  [wrap]"
	}
	out = append(out, stHdr.Render(hdrText))

	// Filter bar: active input or static hint when a filter is set.
	if showFilterBar {
		if m.logFiltering {
			out = append(out, m.logFilterInput.View())
		} else {
			out = append(out, stMuted.Render(" /"+m.logFilter+"  (/ to edit, esc to clear)"))
		}
	}

	out = append(out, stMuted.Render(strings.Repeat("─", w)))

	colW := w - 2
	if colW < 10 {
		colW = 10
	}
	if total == 0 {
		if m.logFilter != "" {
			out = append(out, stMuted.Render("  no matching lines"))
		} else {
			out = append(out, stMuted.Render("  no log output yet"))
		}
	} else {
		for _, l := range lines[start:end] {
			if m.logWrap && len(l) > colW {
				// Break into colW-wide chunks.
				for len(l) > colW {
					out = append(out, "  "+l[:colW])
					l = l[colW:]
				}
				if len(l) > 0 {
					out = append(out, "  "+l)
				}
			} else {
				if len(l) > colW {
					l = l[:colW] + "…"
				}
				out = append(out, "  "+l)
			}
		}
	}

	for len(out) < h {
		out = append(out, "")
	}
	return strings.Join(out[:min(len(out), h)], "\n")
}

// ─── help tab ────────────────────────────────────────────────────────────────

func (m dashModel) renderHelpTab(w, h int) string {
	lines := []string{
		stHdr.Render(" Keyboard shortcuts"),
		stMuted.Render(strings.Repeat("─", w)),
		"",
		stBold.Render("  Tab switching"),
		"  " + stAccent.Render("1") + "                config (always)  " + stAccent.Render("2-8") + " other tabs (after config selected)",
		"  " + stAccent.Render("ctrl+alt+1") + "         config  " + stAccent.Render("ctrl+alt+2-8") + " other tabs",
		"  " + stAccent.Render("ctrl+← →") + "          cycle tabs (works from any context)",
		"  " + stAccent.Render("← →") + "               cycle tabs (when not in text field)",
		"",
		stBold.Render("  Config tab  (config selection)"),
		"  " + stAccent.Render("↑ ↓") + "           navigate list",
		"  " + stAccent.Render("enter") + "           select config (→ provision tab)",
		"  " + stAccent.Render("n") + "               new config",
		"  " + stAccent.Render("r") + "               refresh list",
		"",
		stBold.Render("  Provision tab  (edit form)"),
		"  " + stAccent.Render("↑ ↓") + "                    move between fields",
		"  " + stAccent.Render("space / enter") + "          toggle booleans",
		"  " + stAccent.Render("← →") + "                    cycle select options",
		"  " + stAccent.Render("ctrl+l") + "                 switch to logs tab",
		"",
		stBold.Render("  Costs tab"),
		"  " + stAccent.Render("↑ ↓") + "              scroll vendor list",
		"  " + stAccent.Render("[ / ]") + "            previous / next time window",
		"  " + stAccent.Render("c") + "                edit API credentials",
		"  " + stAccent.Render("tab / shift+tab") + "  move between credential fields",
		"  " + stAccent.Render("enter") + "            advance / save credentials",
		"",
		stBold.Render("  Deploy tab"),
		"  " + stAccent.Render("tab / ↑↓") + "         cycle between buttons",
		"  " + stAccent.Render("enter") + "            activate focused button",
		"",
		stBold.Render("  Logs tab"),
		"  " + stAccent.Render("↑ ↓") + "              scroll down / up",
		"  " + stAccent.Render("PgUp / PgDn") + "      scroll by 10 lines",
		"  " + stAccent.Render("g / G") + "            jump to top / bottom (follow)",
		"  " + stAccent.Render("/") + "                filter (vim-style, esc to clear)",
		"  " + stAccent.Render("ctrl+w") + "           toggle line-wrap",
		"",
		stBold.Render("  Global (any tab)"),
		"  " + stAccent.Render("ctrl+t") + "           open/focus embedded terminal pane (esc or ctrl+t = unfocus)",
		"  " + stAccent.Render("ctrl+alt+↑/↓") + "     resize terminal pane",
		"  " + stAccent.Render("ctrl+s") + "           save config and continue",
		"  " + stAccent.Render("esc / q") + "          abort (no changes written)",
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// ─── about tab ───────────────────────────────────────────────────────────────

func (m dashModel) renderAboutTab(w, h int) string {
	version := "unknown"
	commit := "unknown"
	if info, ok := debug.ReadBuildInfo(); ok {
		version = info.Main.Version
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				commit = s.Value[:7]
			}
		}
	}

	lines := []string{
		stHdr.Render(" About yage xapiri"),
		stMuted.Render(strings.Repeat("─", w)),
		"",
		"  " + stBold.Render("yage") + "  — Yet Another GitOps Engine",
		"",
		"  " + stMuted.Render("version:") + "  " + stAccent.Render(version),
		"  " + stMuted.Render("commit: ") + "  " + stAccent.Render(commit),
		"",
		"  " + stMuted.Render("license:") + "  Apache-2.0",
		"  " + stMuted.Render("project:") + "  https://github.com/lpasquali/yage",
		"",
		stMuted.Render("  xapiri are sacred spirits in the Yanomami people's cosmology."),
		stMuted.Render("  yage runs xapiri to get help from the spirits to create a"),
		stMuted.Render("  visionary deployment."),
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// ─── deploy tab ──────────────────────────────────────────────────────────────

func (m dashModel) renderDeployTab(w, h int) string {
	var lines []string
	lines = append(lines, stHdr.Render(" Actions"))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))
	lines = append(lines, "")

	btnSave := "[Save to Kind]"
	btnDeploy := "[Start Deploy]"
	if m.deployFocused == 0 {
		btnSave = stAccent.Render("▸ " + btnSave)
		btnDeploy = stMuted.Render("  " + btnDeploy)
	} else {
		btnSave = stMuted.Render("  " + btnSave)
		btnDeploy = stAccent.Render("▸ " + btnDeploy)
	}
	lines = append(lines, btnSave)
	lines = append(lines, "")
	lines = append(lines, btnDeploy)
	lines = append(lines, "")

	var statusLine string
	switch {
	case m.saveKindLoading:
		statusLine = stWarn.Render("  ● saving…")
	case m.saveKindDone && m.saveKindErr != nil:
		statusLine = stBad.Render("  ✗ " + m.saveKindErr.Error())
	case m.saveKindDone:
		statusLine = stOK.Render("  ✓ saved to kind")
	default:
		statusLine = stMuted.Render("  ● Ready")
	}
	lines = append(lines, "Status: "+statusLine)

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// ─── deps tab ────────────────────────────────────────────────────────────────

// updateDepsTab handles key events on the deps tab.
func (m dashModel) updateDepsTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	switch {
	case key == tea.KeyTab || key == tea.KeyDown:
		m.depsFocused = (m.depsFocused + 1) % 2
	case key == tea.KeyShiftTab || key == tea.KeyUp:
		m.depsFocused = (m.depsFocused - 1 + 2) % 2
	case key == tea.KeyEnter || key == tea.KeySpace:
		if m.depsRunning {
			return m, nil
		}
		cfg := m.cfg
		switch m.depsFocused {
		case 0: // check button
			m.depsRunning = true
			m.depsStatus = "checking…"
			return m, func() tea.Msg {
				return depsCheckMsg{
					tools:  installer.CheckDeps(cfg),
					images: installer.CheckProviderImages(cfg),
				}
			}
		case 1: // upgrade button
			m.depsRunning = true
			m.depsStatus = "upgrading…"
			return m, func() tea.Msg {
				return depsUpgradeMsg{err: installer.UpgradeDeps(cfg)}
			}
		}
	}
	return m, nil
}

func (m dashModel) renderDepsTab(w, h int) string {
	var lines []string
	lines = append(lines, stHdr.Render(" Dependencies"))
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))
	lines = append(lines, "")

	// buttons
	btnCheck := "[Check]"
	btnUpgrade := "[Upgrade All]"
	if m.depsFocused == 0 {
		btnCheck = stAccent.Render("▸ " + btnCheck)
		btnUpgrade = stMuted.Render("  " + btnUpgrade)
	} else {
		btnCheck = stMuted.Render("  " + btnCheck)
		btnUpgrade = stAccent.Render("▸ " + btnUpgrade)
	}
	lines = append(lines, btnCheck+"  "+btnUpgrade)
	lines = append(lines, "")

	// status line
	if m.depsStatus != "" {
		lines = append(lines, stMuted.Render("  "+m.depsStatus))
		lines = append(lines, "")
	}

	if len(m.depsTools) > 0 {
		lines = append(lines, stBold.Render("  CLI tools"))
		for _, t := range m.depsTools {
			var badge string
			switch {
			case t.Skip:
				badge = stMuted.Render("  ◦")
			case t.OK:
				badge = stOK.Render("  ✓")
			default:
				badge = stBad.Render("  ✗")
			}
			have := t.Have
			if len(have) > 20 {
				have = have[:20]
			}
			line := fmt.Sprintf("%s %-12s  have: %-20s  want: %s", badge, t.Name, have, t.Want)
			lines = append(lines, line)
		}
		lines = append(lines, "")
	}

	if len(m.depsImages) > 0 {
		provLabel := m.cfg.InfraProvider
		if provLabel == "" {
			provLabel = "provider"
		}
		lines = append(lines, stBold.Render("  "+provLabel+" images"))
		archOK := func(ok bool) string {
			if ok {
				return stOK.Render("✓")
			}
			return stBad.Render("✗")
		}
		for _, img := range m.depsImages {
			ref := img.Image
			if len(ref) > 45 {
				ref = "…" + ref[len(ref)-44:]
			}
			var archStr string
			if img.Err {
				archStr = stWarn.Render("  ? [amd64:?] [arm64:?]")
			} else {
				archStr = fmt.Sprintf("  [amd64:%s] [arm64:%s]", archOK(img.Amd64), archOK(img.Arm64))
			}
			lines = append(lines, fmt.Sprintf("  %-18s %s  %s", img.Name, archStr, stMuted.Render(ref)))
		}
		lines = append(lines, "")
	}

	if len(m.depsTools) == 0 && len(m.depsImages) == 0 && m.depsStatus == "" {
		lines = append(lines, stMuted.Render("  Press [Check] to scan installed CLI versions and provider image availability."))
	}

	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

// sortedCostRows returns the cost rows sorted cheapest-first (errors last).
func (m dashModel) sortedCostRows() []cost.CloudCost {
	sorted := make([]cost.CloudCost, len(m.costRows))
	copy(sorted, m.costRows)
	sort.Slice(sorted, func(i, j int) bool {
		ei := sorted[i].Err != nil
		ej := sorted[j].Err != nil
		if ei != ej {
			return !ei // errors go last
		}
		return sorted[i].Estimate.TotalUSDMonthly < sorted[j].Estimate.TotalUSDMonthly
	})
	return sorted
}

// ─── entry point ─────────────────────────────────────────────────────────────

// dashResult carries the outcome of a dashboard session.
type dashResult struct {
	saved           bool
	deployRequested bool
}

// runDashboard opens the full-screen bubbletea dashboard and waits for the
// user to commit (ctrl+s or Start Deploy) or abort (esc/q).
func runDashboard(w io.Writer, cfg *config.Config, s *state) dashResult {
	m := newDashModel(cfg, s)
	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithOutput(w),
	)
	result, err := p.Run()
	if err != nil {
		fmt.Fprintf(w, "xapiri (dashboard): %v\n", err)
		return dashResult{}
	}
	final, ok := result.(dashModel)
	if !ok || !final.done {
		return dashResult{}
	}
	if final.selects[siMode].value() == "on-prem" {
		s.fork = forkOnPrem
		// Copy loaded cfg back to caller's pointer (handles cfgEntryLoadMsg replacement).
		*cfg = *final.cfg
		return dashResult{saved: true}
	}
	final.flushToCfg()
	// Copy flushed cfg back to caller's pointer.
	*cfg = *final.cfg
	return dashResult{saved: true, deployRequested: final.deployRequested}
}

// ─── cost period helpers ──────────────────────────────────────────────────────

// activeCostWindow returns the currently selected time window preset.
func (m dashModel) activeCostWindow() costWindowPreset {
	if m.costPeriodIdx < 0 || m.costPeriodIdx >= len(costWindows) {
		return costWindows[costDefaultPeriodIdx]
	}
	return costWindows[m.costPeriodIdx]
}

// costForPeriod scales a monthly USD figure to the active window.
func (m dashModel) costForPeriod(monthly float64) float64 {
	w := m.activeCostWindow()
	return monthly * w.d.Seconds() / costMonthSecs
}

// formatCost renders an amount with the active window's short suffix.
func (m dashModel) formatCost(monthly float64) string {
	if monthly == 0 {
		return "n/a"
	}
	amt := m.costForPeriod(monthly)
	w := m.activeCostWindow()
	switch {
	case amt < 0.01:
		return fmt.Sprintf("$%.4f%s", amt, w.short)
	case amt < 1:
		return fmt.Sprintf("$%.3f%s", amt, w.short)
	case amt < 100:
		return fmt.Sprintf("$%.2f%s", amt, w.short)
	default:
		return fmt.Sprintf("$%.0f%s", amt, w.short)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func dashDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func dashIntOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// marshalConfigYAML marshals a config snapshot to YAML bytes for the editor tab.
// Uses the same flat KEY: "value" format that ApplyYAMLFile reads.
func marshalConfigYAML(cfg *config.Config) ([]byte, error) {
	snap := cfg.Snapshot()
	lines := make([]string, 0, len(snap))
	for _, f := range snap {
		v := f.Get()
		lines = append(lines, fmt.Sprintf("%s: %q", f.EnvName, v))
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}
