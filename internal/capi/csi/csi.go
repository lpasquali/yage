// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package csix ports Proxmox CSI config helpers: loading from the
// local YAML, and pushing the config Secret into the workload cluster.
//
// Bash source map:
//   - load_csi_vars_from_config                              ~L5822-5843
//   - apply_proxmox_csi_config_secret_to_workload_cluster    ~L6096-6140
package csi

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// LoadVarsFromConfig ports load_csi_vars_from_config. Fills empty
// cfg.ProxmoxCSI{URL,TokenID,TokenSecret} and cfg.Providers.Proxmox.Region from the
// on-disk PROXMOX_CSI_CONFIG YAML when that file exists.
func LoadVarsFromConfig(cfg *config.Config) {
	if cfg.Providers.Proxmox.CSIConfig == "" {
		return
	}
	raw, err := os.ReadFile(cfg.Providers.Proxmox.CSIConfig)
	if err != nil {
		return
	}
	lines := strings.Split(string(raw), "\n")
	find := func(key string) string {
		pat := regexp.MustCompile(`^[^A-Za-z_]*` + regexp.QuoteMeta(key) + `:`)
		strip := regexp.MustCompile(`^[^:]*:\s*"?([^"]*)"?\s*$`)
		for _, ln := range lines {
			if !pat.MatchString(ln) {
				continue
			}
			m := strip.FindStringSubmatch(ln)
			if m == nil {
				return ""
			}
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	if cfg.Providers.Proxmox.CSIURL == "" {
		cfg.Providers.Proxmox.CSIURL = find("url")
	}
	if cfg.Providers.Proxmox.CSITokenID == "" {
		cfg.Providers.Proxmox.CSITokenID = find("token_id")
	}
	if cfg.Providers.Proxmox.CSITokenSecret == "" {
		cfg.Providers.Proxmox.CSITokenSecret = find("token_secret")
	}
	if cfg.Providers.Proxmox.Region == "" {
		cfg.Providers.Proxmox.Region = find("region")
	}
}

// ApplyConfigSecretToWorkload ports
// apply_proxmox_csi_config_secret_to_workload_cluster
// (L6096-L6140). Pushes a Secret named <cluster>-proxmox-csi-config into
// cfg.Providers.Proxmox.CSINamespace on the workload, and mirrors the same content
// under the short name proxmox-csi-config.
func ApplyConfigSecretToWorkload(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	wk, err := writeWorkloadKubeconfig()
	if err != nil || wk == "" {
		logx.Die("Cannot read workload kubeconfig to apply Proxmox CSI config Secret.")
	}
	defer os.Remove(wk)

	cli, err := k8sclient.ForKubeconfigFile(wk)
	if err != nil {
		logx.Die("Cannot connect to workload cluster for Proxmox CSI config Secret: %v", err)
	}
	bg := context.Background()

	if err := cli.EnsureNamespace(bg, cfg.Providers.Proxmox.CSINamespace); err != nil {
		logx.Die("Failed to ensure namespace %s on the workload.", cfg.Providers.Proxmox.CSINamespace)
	}

	cfgYAML := fmt.Sprintf(`features:
  provider: %s
clusters:
  - url: "%s"
    insecure: %s
    token_id: "%s"
    token_secret: "%s"
    region: "%s"
`,
		cfg.Providers.Proxmox.CSIConfigProvider,
		cfg.Providers.Proxmox.CSIURL,
		cfg.Providers.Proxmox.CSIInsecure,
		cfg.Providers.Proxmox.CSITokenID,
		cfg.Providers.Proxmox.CSITokenSecret,
		cfg.Providers.Proxmox.Region,
	)

	secretName := cfg.WorkloadClusterName + "-proxmox-csi-config"
	if err := applyConfigSecret(bg, cli, cfg.Providers.Proxmox.CSINamespace, secretName, cfgYAML); err != nil {
		logx.Die("Failed to apply Proxmox CSI config Secret on workload cluster: %v", err)
	}
	// Mirror under the short name used by workload-app-of-apps default path.
	if secretName != "proxmox-csi-config" {
		if err := applyConfigSecret(bg, cli, cfg.Providers.Proxmox.CSINamespace, "proxmox-csi-config", cfgYAML); err != nil {
			logx.Die("Failed to apply proxmox-csi-config alias Secret on workload cluster: %v", err)
		}
	}
	logx.Log("Applied %s (and proxmox-csi-config when names differ) — Proxmox API credentials in %s; Argo Application will not embed them.",
		secretName, cfg.Providers.Proxmox.CSINamespace)
}

// applyConfigSecret server-side-applies a generic Secret holding a
// single config.yaml key in the named namespace. Replaces the previous
// `kubectl create secret generic ... | kubectl apply -f -` pipeline.
func applyConfigSecret(ctx context.Context, cli *k8sclient.Client, namespace, name, body string) error {
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"config.yaml": []byte(body),
		},
	}
	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("marshal secret: %w", err)
	}
	jsonBody, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("yaml→json: %w", err)
	}
	_, err = cli.Typed.CoreV1().Secrets(namespace).Patch(
		ctx, name, types.ApplyPatchType, jsonBody,
		metav1.PatchOptions{
			FieldManager: k8sclient.FieldManager,
			Force:        boolPtr(true),
		},
	)
	if err != nil {
		return fmt.Errorf("apply secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }