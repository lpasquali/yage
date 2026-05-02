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
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/creack/pty"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/obs"
	"github.com/lpasquali/yage/internal/platform/installer"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
	"github.com/lpasquali/yage/internal/pricing"
)


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

	// token re-prompt overlay (shown after profile load when token is absent)
	tokenPromptActive bool
	tokenPromptInput  textinput.Model

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
	// Proxmox connection fields.
	m.textInputs[tiProxmoxURL].SetValue(cfg.Providers.Proxmox.URL)
	m.textInputs[tiProxmoxAdminUsername].SetValue(cfg.Providers.Proxmox.AdminUsername)
	m.textInputs[tiProxmoxAdminInsecure].SetValue(cfg.Providers.Proxmox.AdminInsecure)
	// AdminToken: masked, memory-only — never seeded from kind Secret.
	m.textInputs[tiProxmoxAdminToken].SetValue(cfg.Providers.Proxmox.AdminToken)
	m.textInputs[tiProxmoxAdminToken].EchoMode = textinput.EchoPassword
	m.textInputs[tiProxmoxAdminToken].EchoCharacter = '·'
	m.textInputs[tiProxmoxAdminToken].Placeholder = "(not saved — enter each session)"

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

	// Phase H — Platform Services.
	m.textInputs[tiRegistryNode].SetValue(cfg.RegistryNode)
	m.textInputs[tiRegistryVMFlav].SetValue(cfg.RegistryVMFlavor)
	m.textInputs[tiRegistryNetwork].SetValue(cfg.RegistryNetwork)
	m.textInputs[tiRegistryStorage].SetValue(cfg.RegistryStorage)
	m.textInputs[tiIssuingCACert].EchoMode = textinput.EchoPassword
	m.textInputs[tiIssuingCACert].EchoCharacter = '·'
	m.textInputs[tiIssuingCACert].SetValue(cfg.IssuingCARootCert)
	m.textInputs[tiIssuingCAKey].EchoMode = textinput.EchoPassword
	m.textInputs[tiIssuingCAKey].EchoCharacter = '·'
	m.textInputs[tiIssuingCAKey].SetValue(cfg.IssuingCARootKey)

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

	// Registry flavor: harbor (default) or zot.
	m.selects[siRegistryFlav] = selectState{options: []string{"harbor", "zot"}, cur: 0}
	if cfg.RegistryFlavor == "zot" {
		m.selects[siRegistryFlav].cur = 1
	}

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
		// If the non-secret fields are populated but the token is missing,
		// prompt the user to re-enter it for this session.
		if m.cfg.Providers.Proxmox.AdminUsername != "" && m.cfg.Providers.Proxmox.AdminToken == "" {
			ti := textinput.New()
			ti.Placeholder = "token secret (not saved)"
			ti.Prompt = "> "
			ti.Width = 40
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '·'
			newM.tokenPromptInput = ti
			newM.tokenPromptActive = true
			cmd := newM.tokenPromptInput.Focus()
			return newM, tea.Batch(textinput.Blink, cmd, newM.kickRefreshCmd(), newM.watchLogsCmd())
		}
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

		// ── Token re-prompt overlay: intercept all keys when active ──
		if m.tokenPromptActive {
			switch key {
			case tea.KeyEnter:
				// Store the token in memory only; clear the prompt.
				if v := strings.TrimSpace(m.tokenPromptInput.Value()); v != "" {
					m.cfg.Providers.Proxmox.AdminToken = v
					m.textInputs[tiProxmoxAdminToken].SetValue(v)
				}
				m.tokenPromptActive = false
				return m, nil
			case tea.KeyEsc:
				// User skipped token entry — leave AdminToken empty.
				m.tokenPromptActive = false
				return m, nil
			case tea.KeyUp, tea.KeyDown:
				// The overlay is a single-line prompt; arrow keys have no
				// meaning here and must not be consumed silently.
				return m, nil
			default:
				ti, cmd := m.tokenPromptInput.Update(msg)
				m.tokenPromptInput = ti
				return m, cmd
			}
		}

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
	// Discard keystrokes while a config entry is loading to prevent input
	// from being silently lost when cfgEntryLoadMsg rebuilds all inputs.
	if m.cfgLoading {
		return m, nil
	}
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

	// Token re-prompt overlay: draw on top of the normal content.
	if m.tokenPromptActive {
		overlay := m.renderTokenPromptOverlay(m.width)
		return lipgloss.JoinVertical(lipgloss.Left, tabBar, overlay, termPane, bottomStrip, footer)
	}

	return lipgloss.JoinVertical(lipgloss.Left, tabBar, content, termPane, bottomStrip, footer)
}

// ─── config tab ──────────────────────────────────────────────────────────────


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
