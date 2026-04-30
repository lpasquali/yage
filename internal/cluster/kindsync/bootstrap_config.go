// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

// bootstrap_config.go manages per-config orchestrator-state Secrets.
// Each config lives in its own Kubernetes namespace named
// "yage-<sanitized cfg.ConfigName>" (e.g. "yage-dev", "yage-prod-eu").
// Inside that namespace the Secret is always called "bootstrap-config".
//
// Three coexistence modes (cfg.ConfigName is the discriminator):
//
//  1. N workload-cluster states: ConfigName defaults to WorkloadClusterName
//     (case 1 needs no new flag — naming a workload names its config).
//  2. N profiles for one workload: same WorkloadClusterName, distinct
//     ConfigName values (e.g. "prod-eu-low-cost", "prod-aws-failover").
//  3. N draft scenarios: ConfigName set to a scenario label, no realized
//     workload yet.
//
// Namespace labels (updated on every save):
//
//	infra.yage-deployment.bucaniere.us:  "true"
//	infra.capi-provider.bucaniere.us:    <cfg.InfraProvider>   (omitted when empty)
//
// Secret labels (written inside the per-config namespace):
//
//	app.kubernetes.io/managed-by:  yage
//	app.kubernetes.io/component:   bootstrap-config
//	yage.io/config-name:           <cfg.ConfigName>
//	yage.io/workload-cluster:      <cfg.WorkloadClusterName>
//	yage.io/config-status:         draft  (promoted to "realized" on success)
//	yage.io/provider:              <cfg.InfraProvider>           (when set)
//	yage.io/region:                <provider.Region or "">       (when set)
//
// Discovery: list namespaces with infra.yage-deployment.bucaniere.us=true,
// then fetch "bootstrap-config" from each.
//
// Shared resources (cost credentials) remain in YageSystemNamespace.
// The Proxmox provider-snapshot Secret (proxmox-bootstrap-config, written
// by applyBootstrapConfigToManagementCluster) is a separate concept and is
// not affected by this file.

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/charmbracelet/huh"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// YageSystemNamespace is the shared kind-side namespace for non-per-config
// yage resources (cost credentials, operator state).
const YageSystemNamespace = "yage-system"

// BootstrapConfigNamespace returns the per-config namespace for cfg.
// Named "yage-<sanitized ConfigName>". Falls back to "yage-default" when
// ConfigName is empty (callers that write data should guard against this
// separately via an error return).
func BootstrapConfigNamespace(cfg *config.Config) string {
	if cfg == nil || cfg.ConfigName == "" {
		return "yage-default"
	}
	return "yage-" + sanitizeLabelValue(cfg.ConfigName)
}

// BootstrapConfigSecretName returns the name of the bootstrap-config Secret
// inside the per-config namespace. Always "bootstrap-config" — the namespace
// is the discriminator so no config-name prefix is needed.
func BootstrapConfigSecretName(_ *config.Config) string {
	return "bootstrap-config"
}

// configNamespaceLabels returns the labels applied to the per-config namespace
// on every save so callers can discover all yage configs via label-selector.
func configNamespaceLabels(cfg *config.Config) map[string]string {
	lbl := map[string]string{
		"infra.yage-deployment.bucaniere.us": "true",
	}
	if cfg != nil && cfg.InfraProvider != "" {
		lbl["infra.capi-provider.bucaniere.us"] = sanitizeLabelValue(cfg.InfraProvider)
	}
	return lbl
}

// sanitizeLabelValue maps s to a valid Kubernetes label value
// ([a-z0-9]([-a-z0-9]*[a-z0-9])?, ≤63 chars). Returns "" when
// the result is empty so callers can skip the label entirely.
func sanitizeLabelValue(s string) string {
	s = strings.ToLower(s)
	result := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, s)
	result = strings.Trim(result, "-")
	if len(result) > 63 {
		result = strings.TrimRight(result[:63], "-")
	}
	return result
}

// bestEffortProviderRegion returns the Region or Location field of the
// active provider's config sub-struct. Returns "" when not available.
func bestEffortProviderRegion(cfg *config.Config) string {
	if cfg.InfraProvider == "" {
		return ""
	}
	structNames := map[string]string{
		"aws": "AWS", "azure": "Azure", "gcp": "GCP",
		"hetzner": "Hetzner", "digitalocean": "DigitalOcean",
		"linode": "Linode", "oci": "OCI", "ibmcloud": "IBMCloud",
		"proxmox": "Proxmox", "openstack": "OpenStack", "vsphere": "Vsphere",
	}
	sn, ok := structNames[strings.ToLower(cfg.InfraProvider)]
	if !ok {
		return ""
	}
	pv := reflect.ValueOf(cfg.Providers)
	sub := pv.FieldByName(sn)
	if !sub.IsValid() {
		return ""
	}
	for _, fname := range []string{"Region", "Location"} {
		fv := sub.FieldByName(fname)
		if fv.IsValid() && fv.Kind() == reflect.String && fv.String() != "" {
			return fv.String()
		}
	}
	return ""
}

// bootstrapLabels builds the full label set for a bootstrap-config Secret.
// Labels that sanitize to "" are omitted.
func bootstrapLabels(cfg *config.Config, status string) map[string]string {
	lbl := map[string]string{
		"app.kubernetes.io/managed-by": "yage",
		"app.kubernetes.io/component":  "bootstrap-config",
	}
	if v := sanitizeLabelValue(cfg.ConfigName); v != "" {
		lbl["yage.io/config-name"] = v
	}
	if v := sanitizeLabelValue(cfg.WorkloadClusterName); v != "" {
		lbl["yage.io/workload-cluster"] = v
	}
	if status != "" {
		lbl["yage.io/config-status"] = status
	}
	if v := sanitizeLabelValue(cfg.InfraProvider); v != "" {
		lbl["yage.io/provider"] = v
	}
	if v := sanitizeLabelValue(bestEffortProviderRegion(cfg)); v != "" {
		lbl["yage.io/region"] = v
	}
	return lbl
}

// isTTY reports whether stdin is an interactive terminal.
func isTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// WriteBootstrapConfigSecret emits cfg into the per-config namespace
// (yage-<ConfigName>) as Secret "bootstrap-config". The namespace is
// created on first write and its labels are updated on every save so the
// label-based discovery in ListBootstrapCandidates stays current.
// Returns nil best-effort: when the kind cluster isn't reachable (e.g.
// xapiri running before any cluster exists), logs and returns nil so the
// caller can fall through to a local-file fallback.
func WriteBootstrapConfigSecret(cfg *config.Config) error {
	if cfg.ConfigName == "" {
		return fmt.Errorf("kindsync: ConfigName is required to write bootstrap config; set --config-name or YAGE_CONFIG_NAME")
	}
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		// Kind cluster not reachable — best-effort skip.
		logx.Warn("kindsync: kind context %s not reachable; skipping %s/%s write (%v)", kctx, BootstrapConfigNamespace(cfg), BootstrapConfigSecretName(cfg), err)
		return nil
	}
	bg := context.Background()
	// Always apply namespace with labels — creates on first run, updates
	// provider label on subsequent saves.
	if err := cli.EnsureNamespaceWithLabels(bg, BootstrapConfigNamespace(cfg), configNamespaceLabels(cfg)); err != nil {
		return err
	}

	data := map[string][]byte{}
	add := func(k, v string) {
		if v != "" {
			data[k] = []byte(v)
		}
	}

	// Universal fields (orchestrator-owned, no prefix).
	add("provider", cfg.InfraProvider)
	add("cluster_name", cfg.WorkloadClusterName)
	add("cluster_id", cfg.ClusterSetID)
	add("kubernetes_version", cfg.WorkloadKubernetesVersion)

	// Per-provider state via KindSyncer.KindSyncFields.
	if prov, perr := provider.For(cfg); perr == nil {
		for k, v := range prov.KindSyncFields(cfg) {
			add(strings.ToLower(cfg.InfraProvider)+"."+k, v)
		}
	}

	// Cost credentials + currency preferences (§16 cross-cutting).
	add("cost.gcp_api_key", cfg.Cost.Credentials.GCPAPIKey)
	add("cost.hetzner_token", cfg.Cost.Credentials.HetznerToken)
	add("cost.digitalocean_token", cfg.Cost.Credentials.DigitalOceanToken)
	add("cost.ibmcloud_api_key", cfg.Cost.Credentials.IBMCloudAPIKey)
	add("cost.display_currency", cfg.Cost.Currency.DisplayCurrency)
	add("cost.data_center_location", cfg.Cost.Currency.DataCenterLocation)

	// Full snapshot: network fields, PROXMOX_*, WORKLOAD_CLUSTER_NAME, etc.
	// Read back by MergeBootstrapConfigFromKind via ApplySnapshotKV so that
	// a second xapiri run restores everything (not just the prefix-keyed subset).
	if yaml := cfg.SnapshotYAML(); yaml != "" {
		data["config.yaml"] = []byte(yaml)
	}

	return applySecret(bg, cli, BootstrapConfigNamespace(cfg), BootstrapConfigSecretName(cfg), data, bootstrapLabels(cfg, "draft"))
}

// MergeBootstrapConfigFromKind reads Secret/yage-system/
// bootstrap-config from the active kind cluster and populates
// EMPTY cfg fields (§16 c2 contract: env-set values win; Secret
// fills the gaps). Returns nil silently when the Secret doesn't
// exist or the kind cluster isn't reachable — the caller continues
// with whatever cfg already has.
//
// Coverage:
//   - cost.* → cfg.Cost.Credentials + cfg.Cost.Currency (full)
//   - provider.<key> → routed to Provider.AbsorbConfigYAML.
//     Proxmox accepts uppercase keys (PROXMOX_*) from
//     proxmox-bootstrap-config/config.yaml; lowercase bare keys are
//     a no-op until per-provider absorbers ship.
//   - universal keys (cluster_name, cluster_id, kubernetes_version)
//     are filled when empty.
func MergeBootstrapConfigFromKind(cfg *config.Config) error {
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return nil // best-effort; caller continues
	}
	if cfg.ConfigName == "" {
		return nil // no discriminator; caller must populate ConfigName first
	}
	bg := context.Background()
	sec, err := cli.Typed.CoreV1().Secrets(BootstrapConfigNamespace(cfg)).Get(bg, BootstrapConfigSecretName(cfg), metav1.GetOptions{})
	if err != nil {
		return nil // not present yet → first-run case
	}

	assign := func(cur *string, v string) {
		if *cur == "" && v != "" {
			*cur = v
		}
	}

	// Collect provider-prefixed keys for batch absorption.
	providerKV := map[string]string{}

	for k, raw := range sec.Data {
		v := string(raw)
		switch {
		case k == "provider":
			assign(&cfg.InfraProvider, v)
		case k == "cluster_name":
			assign(&cfg.WorkloadClusterName, v)
		case k == "cluster_id":
			assign(&cfg.ClusterSetID, v)
		case k == "kubernetes_version":
			assign(&cfg.WorkloadKubernetesVersion, v)
		case k == "cost.gcp_api_key":
			assign(&cfg.Cost.Credentials.GCPAPIKey, v)
		case k == "cost.hetzner_token":
			assign(&cfg.Cost.Credentials.HetznerToken, v)
			// Cross-fill the provider-side mirror per §16's
			// "same secret, two consumers" pattern.
			assign(&cfg.Providers.Hetzner.Token, v)
		case k == "cost.digitalocean_token":
			assign(&cfg.Cost.Credentials.DigitalOceanToken, v)
		case k == "cost.ibmcloud_api_key":
			assign(&cfg.Cost.Credentials.IBMCloudAPIKey, v)
		case k == "cost.display_currency":
			assign(&cfg.Cost.Currency.DisplayCurrency, v)
		case k == "cost.data_center_location":
			assign(&cfg.Cost.Currency.DataCenterLocation, v)
		default:
			// provider-prefixed keys (e.g. "proxmox.admin_token"):
			// convert "provider.snake_key" → "PROVIDER_SNAKE_KEY" and
			// batch for Provider.AbsorbConfigYAML absorption.
			if dot := strings.Index(k, "."); dot > 0 {
				prefix := strings.ToUpper(k[:dot])
				suffix := strings.ToUpper(strings.ReplaceAll(k[dot+1:], ".", "_"))
				providerKV[prefix+"_"+suffix] = v
			}
		}
	}
	if len(providerKV) > 0 {
		if prov, err := provider.For(cfg); err == nil {
			prov.AbsorbConfigYAML(cfg, providerKV)
		}
	}
	// Full snapshot: restores network fields, PROXMOX_*, WORKLOAD_CLUSTER_NAME,
	// etc. that the prefix-keyed loop above does not cover. ApplySnapshotKV
	// honours *_EXPLICIT guards so CLI-supplied values always win.
	if raw, ok := sec.Data["config.yaml"]; ok {
		kv := parseFlatYAMLOrJSON(string(raw))
		migrateLegacyKeys(kv)
		cfg.ApplySnapshotKV(kv)
	}
	return nil
}

// PromoteBootstrapConfigToRealized patches the yage.io/config-status
// label on the bootstrap-config Secret to "realized". Idempotent;
// best-effort (logs on failure, never returns error to caller).
// Called by the orchestrator success path after workload kubeconfig
// is verified.
func PromoteBootstrapConfigToRealized(cfg *config.Config) {
	if cfg == nil || cfg.KindClusterName == "" || cfg.ConfigName == "" {
		return
	}
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		logx.Warn("kindsync: PromoteBootstrapConfigToRealized: kind context %s not reachable: %v", kctx, err)
		return
	}
	patch := []byte(`{"metadata":{"labels":{"yage.io/config-status":"realized"}}}`)
	_, err = cli.Typed.CoreV1().Secrets(BootstrapConfigNamespace(cfg)).Patch(
		context.Background(), BootstrapConfigSecretName(cfg),
		types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		logx.Warn("kindsync: PromoteBootstrapConfigToRealized: patch %s/%s: %v",
			BootstrapConfigNamespace(cfg), BootstrapConfigSecretName(cfg), err)
	}
}

// BootstrapCandidate is a row returned by the label-selector scan.
type BootstrapCandidate struct {
	KindCluster string
	ConfigName  string
	Workload    string
	Status      string
	Provider    string
	Region      string
}

func (c BootstrapCandidate) Label() string {
	st := c.Status
	if st == "" {
		st = "unknown"
	}
	return fmt.Sprintf("%-30s [%-8s]  workload=%-15s  provider=%-10s  region=%s",
		c.ConfigName, st, c.Workload, c.Provider, c.Region)
}

// ListBootstrapCandidates discovers all per-config namespaces on the given
// kind cluster by looking for namespaces labeled
// infra.yage-deployment.bucaniere.us=true, then fetches the
// "bootstrap-config" Secret from each to read the rich label metadata.
func ListBootstrapCandidates(kindClusterName string) []BootstrapCandidate {
	kctx := "kind-" + kindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return nil
	}
	bg := context.Background()
	nsList, err := cli.Typed.CoreV1().Namespaces().List(bg, metav1.ListOptions{
		LabelSelector: "infra.yage-deployment.bucaniere.us=true",
	})
	if err != nil {
		return nil
	}
	out := make([]BootstrapCandidate, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		sec, err := cli.Typed.CoreV1().Secrets(ns.Name).Get(bg, "bootstrap-config", metav1.GetOptions{})
		if err != nil {
			// Namespace exists but secret not yet written — skip.
			continue
		}
		lbl := sec.Labels
		out = append(out, BootstrapCandidate{
			KindCluster: kindClusterName,
			ConfigName:  lbl["yage.io/config-name"],
			Workload:    lbl["yage.io/workload-cluster"],
			Status:      lbl["yage.io/config-status"],
			Provider:    lbl["yage.io/provider"],
			Region:      lbl["yage.io/region"],
		})
	}
	return out
}

// pickBootstrapConfig presents a huh.Select form to pick one candidate.
// In non-TTY environments the first candidate is auto-picked with a
// warning to stderr.
func pickBootstrapConfig(candidates []BootstrapCandidate, title string) *BootstrapCandidate {
	if len(candidates) == 0 {
		return nil
	}
	if !isTTY() {
		fmt.Fprintf(os.Stderr, "kindsync: non-interactive: using config %q (kind cluster %q)\n",
			candidates[0].ConfigName, candidates[0].KindCluster)
		return &candidates[0]
	}
	options := make([]huh.Option[int], len(candidates))
	for i, c := range candidates {
		options[i] = huh.NewOption(c.Label(), i)
	}
	km := huh.NewDefaultKeyMap()
	km.Select.Up.SetKeys("up")
	km.Select.Down.SetKeys("down")

	var chosen int
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[int]().
				Title(title).
				Options(options...).
				Value(&chosen),
		),
	).WithKeyMap(km)
	if err := form.Run(); err != nil {
		// User cancelled (Ctrl+C) or non-TTY fallback.
		fmt.Fprintf(os.Stderr, "kindsync: selection cancelled: %v\n", err)
		return nil
	}
	return &candidates[chosen]
}

// applyCandidate sets KindClusterName, ConfigName, and (unless explicit)
// WorkloadClusterName from a picked candidate, then merges its Secret.
func applyCandidate(cfg *config.Config, c *BootstrapCandidate) {
	cfg.KindClusterName = c.KindCluster
	cfg.ConfigName = c.ConfigName
	if !cfg.WorkloadClusterNameExplicit && c.Workload != "" {
		cfg.WorkloadClusterName = c.Workload
	}
	_ = MergeBootstrapConfigFromKind(cfg)
}

// MergeBootstrapConfigFromFirstKindCluster is the zero-argument variant of
// MergeBootstrapConfigFromKind for when cfg.KindClusterName is empty. It
// scans every running kind cluster for bootstrap-config Secrets (via label
// selector), then:
//
//   - 0 found → no-op (cfg.KindClusterName stays empty)
//   - 1 found → merge silently
//   - N found → huh.Select picker; non-TTY auto-picks first with a warning
func MergeBootstrapConfigFromFirstKindCluster(cfg *config.Config) {
	out, _, _ := shell.Capture("kind", "get", "clusters")
	var candidates []BootstrapCandidate
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		name := strings.TrimSpace(ln)
		if name == "" {
			continue
		}
		candidates = append(candidates, ListBootstrapCandidates(name)...)
	}
	switch len(candidates) {
	case 0:
		// No kind cluster has yage data — leave cfg.KindClusterName empty.
	case 1:
		applyCandidate(cfg, &candidates[0])
	default:
		c := pickBootstrapConfig(candidates, "Select a bootstrap config to load")
		if c != nil {
			applyCandidate(cfg, c)
		}
	}
}

// MergeBootstrapConfigFromKindCluster is the single-cluster variant for
// when cfg.KindClusterName is set but cfg.ConfigNameExplicit is false.
// It lists all bootstrap-config Secrets on that kind cluster and applies
// the same 0/1/N huh-picker logic as MergeBootstrapConfigFromFirstKindCluster.
func MergeBootstrapConfigFromKindCluster(cfg *config.Config) {
	candidates := ListBootstrapCandidates(cfg.KindClusterName)
	switch len(candidates) {
	case 0:
		// No configs on this cluster yet — leave ConfigName at its default.
	case 1:
		applyCandidate(cfg, &candidates[0])
	default:
		c := pickBootstrapConfig(candidates,
			fmt.Sprintf("Select a bootstrap config on kind cluster %q", cfg.KindClusterName))
		if c != nil {
			applyCandidate(cfg, c)
		}
	}
}

