// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// tab_editor.go — cfgReady, loadCfgListCmd, loadCfgEntryCmd,
// loadEditorResourcesCmd, switchToEditorTab, updateEditorTab,
// renderEditorTab, openEditorCmd, resolveEditor, renderEditorPlaceholder.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/obs"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
)

// ─── editor tab ───────────────────────────────────────────────────────────────

// cfgReady reports whether a config entry has been chosen (cfgSelected). When
// false, tab switching is locked — only tabConfig is reachable.
func (m dashModel) cfgReady() bool { return m.cfgSelected }

// loadCfgListCmd fetches all bootstrap-config Secrets from the kind cluster.
func (m dashModel) loadCfgListCmd() tea.Cmd {
	kindName := m.cfg.KindClusterName
	return func() tea.Msg {
		candidates, err := kindsync.ListBootstrapCandidates(kindName)
		if err != nil {
			return cfgListMsg{err: fmt.Errorf("⚠ Could not reach kind cluster: %w", err)}
		}
		return cfgListMsg{candidates: candidates}
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

