// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

// cost_creds.go manages the yage-system/cost-compare-config Secret that
// holds pricing API credentials. The secret's presence (not a flag) is the
// gate for enabling live cost estimation on subsequent xapiri runs.

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
)

// costCredsSecretName is the Secret that stores pricing API credentials.
const costCredsSecretName = "cost-compare-config"

// costCredKeyMap maps Secret data keys to pointers into
// cfg.Cost.Credentials. Populated per-call to avoid a stale pointer table.
func costCredKeyMap(cfg *config.Config) map[string]*string {
	return map[string]*string{
		"aws-access-key-id":     &cfg.Cost.Credentials.AWSAccessKeyID,
		"aws-secret-access-key": &cfg.Cost.Credentials.AWSSecretAccessKey,
		"gcp-api-key":           &cfg.Cost.Credentials.GCPAPIKey,
		"hetzner-token":         &cfg.Cost.Credentials.HetznerToken,
		"digitalocean-token":    &cfg.Cost.Credentials.DigitalOceanToken,
		"ibmcloud-api-key":      &cfg.Cost.Credentials.IBMCloudAPIKey,
	}
}

// CostCompareSecretExists reports whether the cost-compare-config Secret is
// present in yage-system on the kind cluster named in cfg.KindClusterName.
// Returns false on any error (cluster unreachable, secret absent).
func CostCompareSecretExists(cfg *config.Config) bool {
	if cfg.KindClusterName == "" {
		return false
	}
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return false
	}
	_, err = cli.Typed.CoreV1().Secrets(BootstrapConfigNamespace).Get(
		context.Background(), costCredsSecretName, metav1.GetOptions{})
	return err == nil
}

// ReadCostCompareSecret reads the cost-compare-config Secret from yage-system
// and populates cfg.Cost.Credentials.* for each non-empty key. Sets
// cfg.CostCompareEnabled = true when the Secret is found. A missing Secret is
// not an error — the function is a no-op in that case.
func ReadCostCompareSecret(cfg *config.Config) error {
	if cfg.KindClusterName == "" {
		return nil
	}
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return nil // cluster unreachable — not an error
	}
	bg := context.Background()
	sec, err := cli.Typed.CoreV1().Secrets(BootstrapConfigNamespace).Get(bg, costCredsSecretName, metav1.GetOptions{})
	if err != nil {
		return nil // secret absent — first-run case, not an error
	}
	km := costCredKeyMap(cfg)
	found := false
	for k, raw := range sec.Data {
		if ptr, ok := km[k]; ok && len(raw) > 0 {
			*ptr = string(raw)
			found = true
		}
	}
	if found {
		cfg.CostCompareEnabled = true
	}
	return nil
}

// WriteCostCompareSecret upserts the cost-compare-config Secret in yage-system
// with the provided credentials map (keys matching costCredKeyMap). Existing
// keys not present in creds are left untouched (SSA patch semantics).
func WriteCostCompareSecret(cfg *config.Config, creds map[string]string) error {
	if cfg.KindClusterName == "" {
		return nil
	}
	kctx := "kind-" + cfg.KindClusterName
	cli, err := k8sclient.ForContext(kctx)
	if err != nil {
		return err
	}
	data := make(map[string][]byte, len(creds))
	for k, v := range creds {
		if v != "" {
			data[k] = []byte(v)
		}
	}
	if len(data) == 0 {
		return nil
	}
	return applySecret(context.Background(), cli, BootstrapConfigNamespace, costCredsSecretName, data, map[string]string{
		"app.kubernetes.io/managed-by": "yage",
		"yage.io/secret-type":          "cost-credentials",
	})
}
