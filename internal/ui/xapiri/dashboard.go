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
//   - Left panel (60%): all config fields always visible, tab to move.
//   - Right panel (40%): live cost bill, color-coded, j/k scrolls.
//   - 400 ms debounce → cost.CompareWithFilter goroutine on change.
//   - ctrl+s commits and exits; esc aborts without writing cfg.

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
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

// ─── messages ────────────────────────────────────────────────────────────────

type costMsg struct {
	rows []cost.CloudCost
}

type tickMsg time.Time

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

	// cost preview
	costRows       []cost.CloudCost
	costLoading    bool
	costVendor     int // which vendor's detail block is shown (index into costRows)
	refreshPending bool
	lastDirty      time.Time

	width, height int
	errMsg        string
	done          bool // ctrl+s pressed
}

// ─── init ─────────────────────────────────────────────────────────────────────

func newDashModel(cfg *config.Config, s *state) dashModel {
	m := dashModel{
		cfg:   cfg,
		s:     s,
		focus: focKindName,
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
	)
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
		return m, nil

	case costMsg:
		m.costLoading = false
		m.costRows = msg.rows
		return m, nil

	case tickMsg:
		if time.Since(m.lastDirty) < 380*time.Millisecond {
			// Still in debounce window — check again soon.
			return m, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
		}
		m.refreshPending = false
		m.costLoading = true
		return m, m.kickRefreshCmd()

	case tea.KeyMsg:
		key := msg.Type
		keyStr := msg.String()

		// ── global navigation ──
		switch {
		case key == tea.KeyCtrlS || keyStr == "ctrl+s":
			// Save and exit.
			m.done = true
			return m, tea.Quit

		case key == tea.KeyEsc || keyStr == "q":
			// Abort.
			m.done = false
			return m, tea.Quit

		case key == tea.KeyTab:
			m = m.moveFocus(true)
			return m, textinput.Blink

		case key == tea.KeyShiftTab:
			m = m.moveFocus(false)
			return m, textinput.Blink

		case key == tea.KeyUp || keyStr == "k":
			// Scroll vendor list up if not in a text input.
			curMeta := dashFields[m.focus]
			if curMeta.kind != fkText && len(m.costRows) > 0 {
				m.costVendor = (m.costVendor - 1 + len(m.costRows)) % len(m.costRows)
				return m, nil
			}

		case key == tea.KeyDown || keyStr == "j":
			curMeta := dashFields[m.focus]
			if curMeta.kind != fkText && len(m.costRows) > 0 {
				m.costVendor = (m.costVendor + 1) % len(m.costRows)
				return m, nil
			}
		}

		// ── per-field-kind handling ──
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
			// Forward to the focused text input; suppress tab so we handle it above.
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
	}

	return m, nil
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

// kickRefreshCmd builds a cost compare against a snapshot cfg.
// Returns nil when cost estimation is disabled (no credentials configured).
func (m dashModel) kickRefreshCmd() tea.Cmd {
	if !m.cfg.CostCompareEnabled {
		return nil
	}
	snap := m.buildSnapshotCfg()
	return func() tea.Msg {
		rows := cost.CompareWithFilter(&snap, cost.ScopeCloudOnly, nil)
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

	header := m.renderHeader()
	footer := m.renderFooter()

	// Panel widths: left 60%, divider "|", right fills rest.
	usable := m.height - lipgloss.Height(header) - lipgloss.Height(footer) - 1
	if usable < 1 {
		usable = 1
	}
	leftW := m.width * 60 / 100
	rightW := m.width - leftW - 1

	if m.width < 100 {
		// Narrow: stack vertically.
		left := m.renderLeft(m.width, usable*2/3)
		right := m.renderRight(m.width, usable/3)
		return lipgloss.JoinVertical(lipgloss.Left, header, left, right, footer)
	}

	left := m.renderLeft(leftW, usable)
	right := m.renderRight(rightW, usable)

	divStyle := stMuted.Copy()
	div := divStyle.Render(strings.Repeat("│\n", usable))

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, div, right)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m dashModel) renderHeader() string {
	mode := m.selects[siMode].value()
	env := m.selects[siEnv].value()
	resil := m.selects[siResil].value()
	title := stBold.Render("yage xapiri") + "  " + stMuted.Render("dashboard")
	var crumbs string
	if mode == "cloud" {
		crumbs = stMuted.Render(fmt.Sprintf(" │ mode: %s │ env: %s │ resil: %s",
			stAccent.Render(mode), stAccent.Render(env), stAccent.Render(resil)))
	} else {
		crumbs = stMuted.Render(" │ mode: ") + stAccent.Render(mode)
	}
	return lipgloss.NewStyle().Width(m.width).Render(title+crumbs) + "\n" +
		stMuted.Render(strings.Repeat("─", m.width))
}

func (m dashModel) renderFooter() string {
	keys := stMuted.Render("tab/⇧tab") + " navigate  " +
		stMuted.Render("space") + " toggle  " +
		stMuted.Render("◄ ►") + " select  " +
		stMuted.Render("j/k") + " vendor  " +
		stAccent.Render("ctrl+s") + " save & continue  " +
		stMuted.Render("esc/q") + " abort"
	line := stMuted.Render(strings.Repeat("─", m.width))
	if m.errMsg != "" {
		return line + "\n" + stBad.Render("  "+m.errMsg) + "\n" + keys
	}
	return line + "\n" + keys
}

// ─── left panel ──────────────────────────────────────────────────────────────

const labelW = 18
const inputW = 13

func (m dashModel) renderLeft(w, h int) string {
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

// ─── right panel ─────────────────────────────────────────────────────────────

func (m dashModel) renderRight(w, h int) string {
	var lines []string

	title := stHdr.Render(" Live cost / mo")
	if m.costLoading {
		title += stMuted.Render("  (refreshing…)")
	}
	lines = append(lines, title)
	lines = append(lines, stMuted.Render(strings.Repeat("─", w)))

	if !m.cfg.CostCompareEnabled {
		lines = append(lines, stMuted.Render("  cost estimation disabled"))
		lines = append(lines, "")
		lines = append(lines, stMuted.Render("  pass --cost-compare-config to enable"))
		return strings.Join(lines, "\n")
	}

	if len(m.costRows) == 0 {
		lines = append(lines, stMuted.Render("  computing…"))
	} else {
		// Sort rows by total (ascending), errors last.
		sorted := m.sortedCostRows()

		// Cheapest non-error total for color thresholds.
		cheapest := 0.0
		for _, r := range sorted {
			if r.Err == nil && r.Estimate.TotalUSDMonthly > 0 {
				cheapest = r.Estimate.TotalUSDMonthly
				break
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

		// Clamp selected vendor to valid range.
		if m.costVendor >= len(sorted) {
			m.costVendor = len(sorted) - 1
		}
		if m.costVendor < 0 {
			m.costVendor = 0
		}

		for i, r := range sorted {
			selected := i == m.costVendor
			lines = append(lines, m.renderVendorRow(r, selected, cheapest, budget, w))
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
					if len(name) > w-14 {
						name = name[:w-14] + "…"
					}
					lineStr := fmt.Sprintf("  %-*s %8.2f", w-14, name, it.SubtotalUSD)
					lines = append(lines, lineStr)
				}
			}
			// Budget check.
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

	// Pad to height.
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:min(len(lines), h)], "\n")
}

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

// runDashboard replaces runHuhForm. Opens a full-screen bubbletea
// dashboard, waits for ctrl+s (commit) or esc (abort), then writes the
// operator's choices back to cfg + s so the rest of the walkthrough runs
// unchanged.
//
// Returns 0 on a clean commit; 1 on abort.
func runDashboard(w io.Writer, cfg *config.Config, s *state) int {
	m := newDashModel(cfg, s)
	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithOutput(w),
	)
	result, err := p.Run()
	if err != nil {
		fmt.Fprintf(w, "xapiri (dashboard): %v\n", err)
		return 1
	}
	final, ok := result.(dashModel)
	if !ok || !final.done {
		s.r.info("nothing written. the spirits rest.")
		return 0
	}
	if final.selects[siMode].value() == "on-prem" {
		s.fork = forkOnPrem
		return s.runOnPremFork()
	}
	final.flushToCfg()
	return 0
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
