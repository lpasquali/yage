// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// dashboard_msgs.go — tea.Msg type definitions and waitForCostRowCmd.

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
	"github.com/lpasquali/yage/internal/platform/installer"
	"github.com/lpasquali/yage/internal/platform/sysinfo"
)

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
