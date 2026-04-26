// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

// Phase D / §16 commit 2: the new yage-system/bootstrap-config
// Secret schema. WriteBootstrapConfigSecret emits the canonical
// state at end-of-run (or after xapiri commits a config);
// MergeBootstrapConfigFromKind populates cfg.Cost.Credentials +
// (eventually) per-provider state from that Secret on subsequent
// runs.
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
// Today's Proxmox bootstrap-config Secret (proxmox-bootstrap-config
// with config.yaml payload) is NOT replaced by this — the two
// schemas coexist during the migration window. The legacy schema
// is what live yage clusters already have on disk; the new schema
// is what xapiri / new installs write.

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/provider"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// BootstrapConfigNamespace is the kind-side namespace yage owns
// for its bootstrap state Secret. Renamed from
// proxmox-bootstrap-system to yage-system in commit 5c128ec.
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
	add("cost.eur_usd_override", cfg.Cost.Currency.EURUSDOverride)

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
// Today's coverage:
//   - cost.* → cfg.Cost.Credentials + cfg.Cost.Currency (full)
//   - provider.<key> → routed to Provider.AbsorbConfigYAML, which
//     today only has a Proxmox uppercase-key absorber; lowercase
//     bare keys land as a no-op until per-provider absorbers ship.
//     Net effect: Proxmox state from this NEW Secret schema is
//     visible to the absorber as PROXMOX_<KEY> only when the
//     legacy proxmox-bootstrap-config/config.yaml Secret is also
//     present (which it is in production today).
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
		return nil // not present yet → first-run case
	}

	assign := func(cur *string, v string) {
		if *cur == "" && v != "" {
			*cur = v
		}
	}

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
		case k == "cost.eur_usd_override":
			assign(&cfg.Cost.Currency.EURUSDOverride, v)
		default:
			// Provider-prefixed keys (proxmox.url, aws.region, …)
			// are forwarded to the active provider's absorber as
			// a unified map. Today only Proxmox absorbs, and only
			// the legacy uppercase-key shape — lowercase bare
			// keys land as a no-op for now.
			//
			// TODO: per-provider lowercase-bare-key absorbers
			// (one per provider that ships KindSyncFields).
			//
			// no-op intentional — keep loop going.
			_ = k
		}
	}
	return nil
}