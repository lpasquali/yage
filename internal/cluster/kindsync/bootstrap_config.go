// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

// The yage-system/bootstrap-config Secret schema (§16 c2):
// WriteBootstrapConfigSecret emits the canonical state at
// end-of-run (or after xapiri commits a config);
// MergeBootstrapConfigFromKind populates cfg.Cost.Credentials +
// per-provider state from that Secret on subsequent runs.
//
// The schema is "flat keys with category prefixes":
//
//	provider                = "proxmox" | "aws" | …
//	cluster_name            = "capi-quickstart"
//	cluster_id              = "capi-aws-prod"
//	kubernetes_version      = "v1.32.0"
//	cost.gcp_api_key        = "<base64 by k8s>"
//	cost.hetzner_token      = "<base64>"
//	cost.digitalocean_token = "<base64>"
//	cost.ibmcloud_api_key   = "<base64>"
//	cost.display_currency   = "EUR"
//	cost.eur_usd_override   = "1.07"
//	<provider>.<key>        = (per-provider state from
//	                           Provider.KindSyncFields)
//
// The Proxmox bootstrap-config Secret (proxmox-bootstrap-config
// with config.yaml payload) coexists with this schema; both are
// recognized.

import (
	"context"
	"fmt"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// BootstrapConfigNamespace is the kind-side namespace yage owns
// for its bootstrap state Secret.
const BootstrapConfigNamespace = "yage-system"

// BootstrapConfigSecretName is the name of the Secret inside
// BootstrapConfigNamespace.
const BootstrapConfigSecretName = "bootstrap-config"

// WriteBootstrapConfigSecret emits cfg into Secret/yage-system/
// bootstrap-config, using the prefix-based schema documented at
// the top of this file. Returns nil best-effort: when the kind
// cluster isn't reachable (e.g. xapiri running before any cluster
// exists), logs and returns nil so the caller can fall through to
// a local-file fallback.
func WriteBootstrapConfigSecret(cfg *config.Config) error {
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		// Kind cluster not reachable — best-effort skip.
		logx.Warn("kindsync: kind context %s not reachable; skipping yage-system/bootstrap-config write (%v)", kctx, err)
		return nil
	}
	bg := context.Background()
	if err := cli.EnsureNamespace(bg, BootstrapConfigNamespace); err != nil {
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

	return applySecret(bg, cli, BootstrapConfigNamespace, BootstrapConfigSecretName, data, map[string]string{
		"app.kubernetes.io/managed-by": "yage",
		"app.kubernetes.io/component":  "bootstrap-config",
	})
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
	bg := context.Background()
	sec, err := cli.Typed.CoreV1().Secrets(BootstrapConfigNamespace).Get(bg, BootstrapConfigSecretName, metav1.GetOptions{})
	if err != nil {
		// back-compat: old name from before Phase D
		sec, err = cli.Typed.CoreV1().Secrets("proxmox-bootstrap-system").Get(bg, BootstrapConfigSecretName, metav1.GetOptions{})
		if err != nil {
			return nil // not present yet → first-run case
		}
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

// MergeBootstrapConfigFromFirstKindCluster is the zero-argument variant of
// MergeBootstrapConfigFromKind for when cfg.KindClusterName is empty. It
// scans every running kind cluster for a yage-system/bootstrap-config Secret:
//
//   - 0 found → no-op (cfg.KindClusterName stays empty)
//   - 1 found → merge silently
//   - N found → print a numbered menu to stderr and ask which one to use;
//     if stdin is not a TTY (CI, pipes) the first entry is used with a warning
func MergeBootstrapConfigFromFirstKindCluster(cfg *config.Config) {
	out, _, _ := shell.Capture("kind", "get", "clusters")
	var candidates []string
	for _, ln := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		name := strings.TrimSpace(ln)
		if name == "" {
			continue
		}
		cfg.KindClusterName = name
		if err := MergeBootstrapConfigFromKind(cfg); err == nil && cfg.InfraProvider != "" {
			candidates = append(candidates, name)
			// Reset so the next iteration starts clean.
			cfg.KindClusterName = ""
			cfg.InfraProvider = ""
		} else {
			cfg.KindClusterName = ""
		}
	}

	switch len(candidates) {
	case 0:
		// No kind cluster has yage data — leave cfg.KindClusterName empty.
	case 1:
		cfg.KindClusterName = candidates[0]
		_ = MergeBootstrapConfigFromKind(cfg)
	default:
		chosen := pickKindCluster(candidates)
		cfg.KindClusterName = chosen
		_ = MergeBootstrapConfigFromKind(cfg)
	}
}

// pickKindCluster prints a numbered menu to stderr and returns the chosen
// cluster name. Falls back to the first entry when stdin is not a TTY.
func pickKindCluster(names []string) string {
	fmt.Fprintln(os.Stderr, "  Multiple kind clusters contain yage data. Which one do you want to use?")
	for i, n := range names {
		fmt.Fprintf(os.Stderr, "    %d) %s\n", i+1, n)
	}
	// Non-interactive fallback.
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintf(os.Stderr, "  (non-interactive: using %s)\n", names[0])
		return names[0]
	}
	for {
		fmt.Fprintf(os.Stderr, "  choice [1-%d]: ", len(names))
		var line string
		fmt.Fscan(os.Stdin, &line)
		line = strings.TrimSpace(line)
		for i, n := range names {
			if line == fmt.Sprintf("%d", i+1) || line == n {
				return n
			}
		}
		fmt.Fprintf(os.Stderr, "  invalid choice — enter a number between 1 and %d\n", len(names))
	}
}