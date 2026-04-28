// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard.go — full-screen bubbletea dashboard that replaces the
// charmbracelet/huh wizard when YAGE_XAPIRI_TUI=huh is set.
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
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/pricing"
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
	tiKindName = iota
	tiK8sVer
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
	siCount     = 4
)

// ─── toggle (bool) slot indices ──────────────────────────────────────────────

const (
	toiQueue      = 0
	toiObjStore   = 1
	toiCache      = 2
	toiOvercommit = 3 // on-prem only: allow resource overcommit
	toiCount      = 4
)

// ─── logical focus IDs (tab order) ───────────────────────────────────────────

const (
	focKindName    = iota // 0
	focK8sVer             // 1
	focMode               // 2
	focEnv                // 3
	focResil              // 4
	focApps               // 5
	focDBGB               // 6
	focEgressGB           // 7
	focHasQueue           // 8
	focQueueCPU           // 9
	focQueueMem           // 10
	focQueueVol           // 11
	focHasObjStore        // 12
	focObjCPU             // 13
	focObjMem             // 14
	focObjVol             // 15
	focHasCache           // 16
	focCacheCPU           // 17
	focCacheMem           // 18
	focBootstrap          // 19
	focOvercommit         // 20
	focDCLoc              // 21
	focBudget             // 22
	focHeadroom           // 23
	focCount              // 24 — must be last
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
	// ── Cluster ──────────────────────────────────────────────────────
	{fkText, tiKindName, "kind name", "Cluster", false},
	{fkText, tiK8sVer, "k8s version", "", false},
	// ── Mode ─────────────────────────────────────────────────────────
	{fkSelect, siMode, "mode", "Mode", true},
	// ── Tier (cloud only) ────────────────────────────────────────────
	{fkSelect, siEnv, "environment", "Tier", true},
	{fkSelect, siResil, "resilience", "", true},
	// ── Workload (cloud only) ─────────────────────────────────────────
	{fkText, tiApps, "apps", "Workload", true},
	{fkText, tiDBGB, "db (GB)", "", true},
	{fkText, tiEgressGB, "egress GB/mo", "", true},
	// ── Add-ons (cloud only) ──────────────────────────────────────────
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
	// ── Bootstrap (on-prem only) ──────────────────────────────────────
	{fkSelect, siBootstrap, "bootstrap mode", "Bootstrap", false},
	{fkToggle, toiOvercommit, "allow overcommit", "", false},
	// ── Geo + Budget (cloud only) ─────────────────────────────────────
	{fkText, tiDCLoc, "data-center loc", "Geo", false},
	{fkText, tiBudget, "budget USD/mo", "Budget", false},
	{fkText, tiHeadroom, "headroom %", "", false},
}

// ─── tab IDs ─────────────────────────────────────────────────────────────────

type dashTab int

const (
	tabConfig dashTab = iota // 0 — interactive config form
	tabEditor                // 1 — opens $EDITOR on the YAML config file
	tabCosts                 // 2 — full provider comparison table + bar chart
	tabLogs                  // 3 — scrollable ring buffer
	tabDeploy                // 4 — save-to-kind + start-deploy actions
	tabHelp                  // 5 — keyboard shortcuts reference (always second-to-last)
	tabAbout                 // 6 — version / license / URL (always last)
	tabCount                 // must be last
)

var tabLabels = [tabCount]string{"config", "editor", "costs", "logs", "deploy", "help", "about"}

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

type costMsg struct {
	rows []cost.CloudCost
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
	err      error
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
	costRows       []cost.CloudCost
	costLoading    bool
	costVendor     int // which vendor's detail block is shown (index into costRows)
	refreshPending bool
	lastDirty      time.Time

	// logs tab
	logLines   []string // snapshot from globalLogRing
	logScroll  int      // scroll offset (lines from bottom; 0 = pinned to bottom)
	logSub     <-chan struct{}

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
	termH       int    // total pane height (border+title+content); ctrl+↑/↓ to resize
	termRaw     []byte // raw PTY output ring buffer (last 64 KB)

	width, height int
	errMsg        string
	done          bool // ctrl+s pressed
}

// ─── init ─────────────────────────────────────────────────────────────────────

func newDashModel(cfg *config.Config, s *state) dashModel {
	m := dashModel{
		cfg:         cfg,
		s:           s,
		focus:       focKindName,
		costLoading: cfg.CostCompareEnabled, // show "fetching…" from the first frame
		termH:       termPaneHDefault,
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

	// Toggles.
	m.toggles[toiQueue] = s.workload.HasQueue
	m.toggles[toiObjStore] = s.workload.HasObjStore
	m.toggles[toiCache] = s.workload.HasCache
	m.toggles[toiOvercommit] = cfg.Capacity.AllowOvercommit

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

	// Focus the first visible input.
	cmd := m.textInputs[tiKindName].Focus()
	_ = cmd // will be returned from Init

	return m
}

func (m dashModel) Init() tea.Cmd {
	m.lastDirty = time.Now()
	return tea.Batch(
		textinput.Blink,
		m.textInputs[tiKindName].Focus(),
		m.kickRefreshCmd(),
		m.watchLogsCmd(),
	)
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

func (m *dashModel) isHidden(fid int) bool {
	isCloud := m.isCloud()
	switch fid {
	case focEnv, focResil, focApps, focDBGB, focEgressGB:
		return false // visible on both cloud and on-prem
	case focHasQueue, focHasObjStore, focHasCache,
		focDCLoc, focBudget, focHeadroom:
		return !isCloud
	case focBootstrap, focOvercommit:
		return isCloud // only visible on on-prem
	case focQueueCPU, focQueueMem, focQueueVol:
		return !isCloud || !m.toggles[toiQueue]
	case focObjCPU, focObjMem, focObjVol:
		return !isCloud || !m.toggles[toiObjStore]
	case focCacheCPU, focCacheMem:
		return !isCloud || !m.toggles[toiCache]
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

	case costMsg:
		m.costLoading = false
		m.costRows = msg.rows
		return m, nil

	case tickMsg:
		if time.Since(m.lastDirty) < 380*time.Millisecond {
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
		}
		m.refreshPending = false
		m.costLoading = true
		return m, m.kickRefreshCmd()

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
			m2.activeTab = tabConfig
			m2.costRows = m.costRows
			m2.width = m.width
			m2.height = m.height
			return m2, tea.Batch(textinput.Blink, m2.kickRefreshCmd())
		}
		m.activeTab = tabConfig
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
		if msg.err != nil {
			m.editorErr = msg.err.Error()
			return m, nil
		}
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
					return saveKindMsg{err: kindsync.ApplyBootstrapConfigToManagementCluster(cfg)}
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

		// ── Alt+1..7: universal tab switching — works even inside text fields ──
		switch keyStr {
		case "alt+1":
			m.activeTab = tabConfig
			return m, nil
		case "alt+2":
			m.activeTab = tabEditor
			return m, m.switchToEditorTab()
		case "alt+3":
			m.activeTab = tabCosts
			return m, nil
		case "alt+4":
			m.activeTab = tabLogs
			return m, nil
		case "alt+5":
			m.activeTab = tabDeploy
			return m, nil
		case "alt+6":
			m.activeTab = tabHelp
			return m, nil
		case "alt+7":
			m.activeTab = tabAbout
			return m, nil
		}

		// ── Ctrl+Left/Right: universal tab cycling — works even inside text fields ──
		if key == tea.KeyCtrlLeft {
			m.activeTab = (m.activeTab - 1 + tabCount) % tabCount
			if m.activeTab == tabEditor {
				return m, m.switchToEditorTab()
			}
			return m, nil
		}
		if key == tea.KeyCtrlRight {
			m.activeTab = (m.activeTab + 1) % tabCount
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

		// ── Ctrl+↑/↓: resize terminal pane (works focused or not) ──
		if m.termRunning && (key == tea.KeyCtrlUp || key == tea.KeyCtrlDown) {
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

		// ── tab switching: left/right arrows or number keys 1-7 ──
		// (Only when not in a text input on the config tab.)
		inTextField := m.activeTab == tabConfig && dashFields[m.focus].kind == fkText
		switch {
		case !inTextField && keyStr == "1":
			m.activeTab = tabConfig
			return m, nil
		case !inTextField && keyStr == "2":
			m.activeTab = tabEditor
			return m, m.switchToEditorTab()
		case !inTextField && keyStr == "3":
			m.activeTab = tabCosts
			return m, nil
		case !inTextField && keyStr == "4":
			m.activeTab = tabLogs
			return m, nil
		case !inTextField && keyStr == "5":
			m.activeTab = tabDeploy
			return m, nil
		case !inTextField && keyStr == "6":
			m.activeTab = tabHelp
			return m, nil
		case !inTextField && keyStr == "7":
			m.activeTab = tabAbout
			return m, nil
		case (key == tea.KeyLeft || key == tea.KeyRight) && !inTextField && m.activeTab != tabConfig:
			// Only cycle tabs with arrows when not on config (config uses arrows for fields).
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

		case tabEditor:
			return m.updateEditorTab(msg)

		case tabLogs:
			return m.updateLogsTab(msg)

		case tabCosts:
			return m.updateCostsTab(msg)

		case tabDeploy:
			return m.updateDeployTab(msg)
		}
	}

	return m, nil
}

// updateConfigTab handles key events while the config tab is active.
func (m dashModel) updateConfigTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	keyStr := msg.String()

	switch {
	case key == tea.KeyTab:
		m = m.moveFocus(true)
		return m, textinput.Blink

	case key == tea.KeyShiftTab:
		m = m.moveFocus(false)
		return m, textinput.Blink

	case keyStr == "ctrl+l":
		m.activeTab = tabLogs
		return m, nil

	case key == tea.KeyUp || keyStr == "k":
		m = m.moveFocus(false)
		return m, textinput.Blink

	case key == tea.KeyDown || keyStr == "j":
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
			if meta.costKey {
				return m, m.markDirty()
			}
		case key == tea.KeyLeft || keyStr == "h":
			m.selects[meta.subIdx].prev()
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

// updateLogsTab handles key events on the logs tab (scroll).
func (m dashModel) updateLogsTab(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.Type
	keyStr := msg.String()
	switch {
	case key == tea.KeyUp || keyStr == "k":
		m.logScroll++
	case key == tea.KeyDown || keyStr == "j":
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
				m.saveKindLoading = true
				m.saveKindDone = false
				m.saveKindErr = nil
				cfg := m.cfg
				return m, func() tea.Msg {
					err := kindsync.ApplyBootstrapConfigToManagementCluster(cfg)
					return saveKindMsg{err: err}
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
	case key == tea.KeyUp || keyStr == "k":
		if len(m.costRows) > 0 {
			m.costVendor = (m.costVendor - 1 + len(m.costRows)) % len(m.costRows)
		}
	case key == tea.KeyDown || keyStr == "j":
		if len(m.costRows) > 0 {
			m.costVendor = (m.costVendor + 1) % len(m.costRows)
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
			// Last field: submit.
			return m, m.saveCostCredsCmd()
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
		return m, m.saveCostCredsCmd()

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

		secrets, err := cli.Typed.CoreV1().Secrets(kindsync.BootstrapConfigNamespace).List(bg, metav1.ListOptions{})
		if err != nil {
			return editorResourcesMsg{err: fmt.Errorf("list secrets: %w", err)}
		}
		for _, s := range secrets.Items {
			items = append(items, editorResource{Kind: "Secret", Name: s.Name})
		}

		cms, err := cli.Typed.CoreV1().ConfigMaps(kindsync.BootstrapConfigNamespace).List(bg, metav1.ListOptions{})
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
		switch res.Kind {
		case "Secret":
			sec, err := cli.Typed.CoreV1().Secrets(kindsync.BootstrapConfigNamespace).Get(bg, res.Name, metav1.GetOptions{})
			if err != nil {
				return editorResourcesMsg{err: fmt.Errorf("get secret %s: %w", res.Name, err)}
			}
			body = secretToEditableYAML(sec.Data, res)
		case "ConfigMap":
			cm, err := cli.Typed.CoreV1().ConfigMaps(kindsync.BootstrapConfigNamespace).Get(bg, res.Name, metav1.GetOptions{})
			if err != nil {
				return editorResourcesMsg{err: fmt.Errorf("get configmap %s: %w", res.Name, err)}
			}
			body = configMapToEditableYAML(cm.Data, res)
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
func secretToEditableYAML(data map[string][]byte, res editorResource) string {
	var sb strings.Builder
	sb.WriteString("# ⚠️  🔓  🎥  WARNING: CLEARTEXT SECRETS VISIBLE ON SCREEN  🎥  🔓  ⚠️\n")
	sb.WriteString("# This file contains the decoded contents of Secret: ")
	sb.WriteString(kindsync.BootstrapConfigNamespace + "/" + res.Name + "\n")
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
func configMapToEditableYAML(data map[string]string, res editorResource) string {
	var sb strings.Builder
	sb.WriteString("# ConfigMap: ")
	sb.WriteString(kindsync.BootstrapConfigNamespace + "/" + res.Name + "\n\n")
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
	ns := kindsync.BootstrapConfigNamespace

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
		lines = append(lines, stMuted.Render("  no resources found in "+kindsync.BootstrapConfigNamespace))
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
		return editorFinishedMsg{err: err}
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

const termPaneHDefault = 12 // initial terminal pane height; user can resize with ctrl+↑/↓
const termPaneHMin = 4
const termPaneHMax = 60

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
		lines = append(lines, stAccent.Render("▸ Terminal")+stMuted.Render("  esc=unfocus  ctrl+↑/↓=resize"))
	} else {
		lines = append(lines, sep)
		lines = append(lines, stMuted.Render("  Terminal")+stMuted.Render("  ctrl+t=focus  ctrl+↑/↓=resize"))
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

// kickRefreshCmd builds a cost compare against a snapshot cfg.
// Returns nil when cost estimation is disabled (no credentials configured).
func (m dashModel) kickRefreshCmd() tea.Cmd {
	if !m.cfg.CostCompareEnabled {
		return nil
	}
	snap := m.buildSnapshotCfg()
	// Capture credentials at dispatch time: pricing.SetCredentials is a
	// process-global set by main.go before kind is connected, so it does
	// not include credentials loaded later from the cost-compare-config
	// Secret. Sync here to ensure every fetch uses the current values.
	c := m.cfg.Cost.Credentials
	return func() tea.Msg {
		pricing.SetCredentials(pricing.Credentials{
			AWSAccessKeyID:     c.AWSAccessKeyID,
			AWSSecretAccessKey: c.AWSSecretAccessKey,
			GCPAPIKey:          c.GCPAPIKey,
			HetznerToken:       c.HetznerToken,
			DigitalOceanToken:  c.DigitalOceanToken,
			IBMCloudAPIKey:     c.IBMCloudAPIKey,
		})
		rows := cost.CompareWithFilter(&snap, cost.ScopeCloudOnly, nil)
		log := obs.Global()
		ok := 0
		for _, r := range rows {
			if r.Err != nil {
				log.Error("cost fetch", r.Err, slog.String("provider", r.ProviderName))
			} else {
				ok++
			}
		}
		if len(rows) == 0 {
			log.Error("cost fetch", fmt.Errorf("no providers matched (InfraProvider filter or airgap)"))
		} else {
			log.Info("cost fetch complete", slog.Int("ok", ok), slog.Int("total", len(rows)))
		}
		return costMsg{rows: rows}
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

	mode := m.selects[siMode].value()
	fork := forkCloud
	if mode == "on-prem" {
		fork = forkOnPrem
	}

	env := envTier(m.selects[siEnv].value())
	switch env {
	case envDev:
		snap.ArgoCD.Enabled = false
		snap.ArgoCD.WorkloadEnabled = false
	case envStaging:
		snap.ArgoCD.Enabled = true
		snap.ArgoCD.WorkloadEnabled = false
	case envProd:
		snap.ArgoCD.Enabled = true
		snap.ArgoCD.WorkloadEnabled = true
		snap.CertManagerEnabled = true
	}

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

	// The dashboard always compares all cloud providers — clearing
	// InfraProvider prevents CompareWithFilter from narrowing to a single
	// provider (which would produce zero rows when that provider is on-prem).
	snap.InfraProvider = ""
	snap.InfraProviderDefaulted = true

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
	case tabEditor:
		content = m.renderEditorTab(m.width, usable)
	case tabCosts:
		content = m.renderCostsTab(m.width, usable)
	case tabLogs:
		content = m.renderLogsTab(m.width, usable)
	case tabHelp:
		content = m.renderHelpTab(m.width, usable)
	case tabAbout:
		content = m.renderAboutTab(m.width, usable)
	case tabDeploy:
		content = m.renderDeployTab(m.width, usable)
	}

	return lipgloss.JoinVertical(lipgloss.Left, tabBar, content, termPane, bottomStrip, footer)
}

// renderTabBar renders the tab strip at the top.
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
	title := stBold.Render("yage xapiri") + "  "
	line := title + bar
	return lipgloss.NewStyle().Width(m.width).Render(line) + "\n" +
		stMuted.Render(strings.Repeat("─", m.width))
}

// renderBottomStrip renders the live cost tally always visible below tab content.
func (m dashModel) renderBottomStrip() string {
	line := stMuted.Render(strings.Repeat("─", m.width)) + "\n"
	if !m.cfg.CostCompareEnabled {
		return line + stMuted.Render("  cost estimation: go to [costs] tab (alt+3) to enter API credentials")
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
	var parts []string
	for _, r := range sorted {
		if r.Err != nil {
			parts = append(parts, stMuted.Render(r.ProviderName+" n/a"))
			continue
		}
		total := r.Estimate.TotalUSDMonthly
		var style lipgloss.Style
		cheapest := 0.0
		for _, rr := range sorted {
			if rr.Err == nil && rr.Estimate.TotalUSDMonthly > 0 {
				cheapest = rr.Estimate.TotalUSDMonthly
				break
			}
		}
		budget := m.cfg.BudgetUSDMonth
		switch {
		case budget > 0 && total > budget:
			style = stBad
		case cheapest > 0 && total <= cheapest:
			style = stOK
		case cheapest > 0 && total > cheapest*1.5:
			style = stWarn
		default:
			style = lipgloss.NewStyle()
		}
		parts = append(parts, style.Render(fmt.Sprintf("%s $%.0f", r.ProviderName, total)))
	}
	suffix := ""
	if m.costLoading {
		suffix = stMuted.Render("  (fetching…)")
	}
	if m.termRunning {
		if m.termFocused {
			suffix += stAccent.Render("  [term:active]")
		} else {
			suffix += stMuted.Render("  [term:bg]")
		}
	}
	return line + "  " + strings.Join(parts, stMuted.Render("  ")) + suffix
}

func (m dashModel) renderFooter() string {
	shellHint := stMuted.Render("ctrl+t") + " terminal  "
	tabHint := stMuted.Render("1-7/alt+1-7/ctrl+◄►") + " tabs  "
	var keys string
	switch m.activeTab {
	case tabConfig:
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

// renderConfigTab renders the full-width interactive config form.
func (m dashModel) renderConfigTab(w, h int) string {
	var lines []string
	lastSection := ""

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
// Each candidate (including env-var values) is verified with exec.LookPath
// so we never hand an absent binary to tea.ExecProcess.
// It never returns an empty string.
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

	title := stHdr.Render(" Provider cost comparison ($/mo)")
	if m.costLoading {
		title += stMuted.Render("  refreshing…")
	}
	lines = append(lines, title)
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))

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

		// Table header.
		hdr := fmt.Sprintf("  %-14s %10s  %s", "provider", "$/mo", "bar chart")
		lines = append(lines, stHdr.Render(hdr))

		barW := w - 32 // chars available for bar
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
			lines = append(lines, stMuted.Render(fmt.Sprintf(" ─ %s detail ─", sel.ProviderName)))
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
					lineStr := fmt.Sprintf("  %-*s %8.2f", maxNameW, name, it.SubtotalUSD)
					lines = append(lines, lineStr)
				}
			}
			if budget > 0 && sel.Err == nil {
				lines = append(lines, "")
				total := sel.Estimate.TotalUSDMonthly
				if total <= budget {
					lines = append(lines, stOK.Render(fmt.Sprintf("  ✓ within budget (%.0f / %.0f)", total, budget)))
				} else {
					lines = append(lines, stBad.Render(fmt.Sprintf("  ✗ over budget (%.0f / %.0f)", total, budget)))
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

// renderCostRow renders a single provider row with bar chart.
func (m dashModel) renderCostRow(r cost.CloudCost, selected bool, cheapest, maxTotal, budget float64, barW int) string {
	prefix := "  "
	if selected {
		prefix = stAccent.Render("▌ ")
	}

	if r.Err != nil {
		row := prefix + stMuted.Render(fmt.Sprintf("%-14s  n/a", r.ProviderName))
		return row
	}

	total := r.Estimate.TotalUSDMonthly
	totalStr := fmt.Sprintf("$%8.2f", total)

	var style lipgloss.Style
	switch {
	case budget > 0 && total > budget:
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

	name := fmt.Sprintf("%-14s", r.ProviderName)
	if selected {
		name = stAccent.Render(name)
		totalStr = style.Bold(true).Render(totalStr)
	} else {
		name = style.Render(name)
		totalStr = style.Render(totalStr)
	}

	return prefix + name + " " + totalStr + "  " + bar
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

	// Available content lines (reserve 2 for header).
	contentH := h - 2
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
	if m.logScroll > 0 {
		hdrText += fmt.Sprintf("  scroll↑ %d", m.logScroll)
	} else {
		hdrText += "  (following)"
	}
	out = append(out, stHdr.Render(hdrText))
	out = append(out, stMuted.Render(strings.Repeat("─", w)))

	if total == 0 {
		out = append(out, stMuted.Render("  no log output yet"))
	} else {
		for _, l := range lines[start:end] {
			if len(l) > w-2 {
				l = l[:w-2] + "…"
			}
			out = append(out, "  "+l)
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
		"  " + stAccent.Render("1-7") + "              switch tabs (when not in text field)",
		"  " + stAccent.Render("alt+1-7") + "          switch tabs (works from any context)",
		"  " + stAccent.Render("ctrl+← →") + "         cycle tabs (works from any context)",
		"  " + stAccent.Render("← →") + "              cycle tabs (when not in text field)",
		"",
		stBold.Render("  Config tab"),
		"  " + stAccent.Render("↑ ↓ / j k / tab ⇧tab") + "  move between fields",
		"  " + stAccent.Render("space / enter") + "          toggle booleans",
		"  " + stAccent.Render("← →") + "                    cycle select options",
		"  " + stAccent.Render("ctrl+l") + "                 switch to logs tab",
		"",
		stBold.Render("  Costs tab"),
		"  " + stAccent.Render("j / k") + "            scroll vendor list",
		"  " + stAccent.Render("c") + "                edit API credentials",
		"  " + stAccent.Render("tab / shift+tab") + "  move between credential fields",
		"  " + stAccent.Render("enter") + "            advance / save credentials",
		"",
		stBold.Render("  Deploy tab"),
		"  " + stAccent.Render("tab / ↑↓") + "         cycle between buttons",
		"  " + stAccent.Render("enter") + "            activate focused button",
		"",
		stBold.Render("  Logs tab"),
		"  " + stAccent.Render("j / k") + "            scroll down / up",
		"  " + stAccent.Render("PgUp / PgDn") + "      scroll by 10 lines",
		"  " + stAccent.Render("g / G") + "            jump to top / bottom (follow)",
		"",
		stBold.Render("  Global (any tab)"),
		"  " + stAccent.Render("ctrl+t") + "           open/focus embedded terminal pane (esc or ctrl+t = unfocus)",
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
		return dashResult{saved: true}
	}
	final.flushToCfg()
	return dashResult{saved: true, deployRequested: final.deployRequested}
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
