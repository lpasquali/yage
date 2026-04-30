// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package proxmoxcsi registers the proxmox-csi-plugin driver with
// yage's CSI add-on registry (internal/csi). The upstream chart is
// proxmox-csi-plugin published by sergelogvinov on GHCR; auth is via
// a Proxmox API token, materialised as a Kubernetes Secret on the
// workload cluster.
package proxmoxcsi

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
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
	"github.com/lpasquali/yage/internal/ui/plan"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "proxmox-csi" }
func (driver) K8sCSIDriverName() string { return "csi.proxmox.sinextra.dev" }
func (driver) Defaults() []string       { return []string{"proxmox"} }

// HelmChart returns the chart coordinates from cfg.Providers.Proxmox
// rather than hardcoding them — Proxmox operators commonly mirror
// the chart internally and yage already exposes the override knobs
// (PROXMOX_CSI_CHART_REPO_URL / _NAME / _VERSION).
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return cfg.Providers.Proxmox.CSIChartRepoURL,
		cfg.Providers.Proxmox.CSIChartName,
		cfg.Providers.Proxmox.CSIChartVersion,
		nil
}

// RenderValues emits a thin Helm values document. The bulk of the
// driver's runtime config (Proxmox URL / token / region / cluster
// alias) flows in via the Secret applied by EnsureSecret below, not
// via Helm values — that's how the upstream chart is wired and why
// yage doesn't need to enumerate fields here.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/proxmoxcsi.\n")
	b.WriteString("# Auth: Kubernetes Secret applied by EnsureSecret\n")
	b.WriteString("storageClass:\n")
	b.WriteString("  - name: ")
	b.WriteString(cfg.Providers.Proxmox.CSIStorageClassName)
	b.WriteString("\n")
	b.WriteString("    storage: data\n")
	b.WriteString("    reclaimPolicy: Delete\n")
	b.WriteString("    fstype: xfs\n")
	return b.String(), nil
}

// EnsureSecret pushes the Proxmox CSI config Secret to the workload
// cluster. The Secret name, namespace, and aliasing
// (<cluster>-proxmox-csi-config plus the short proxmox-csi-config
// mirror) are an upstream-chart contract.
//
// Returns csi.ErrNotApplicable when PROXMOX_CSI_ENABLED is false or
// required credentials (URL / token ID / token secret / region) are
// not populated; the orchestrator loop treats this as a silent skip.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	if !cfg.Providers.Proxmox.CSIEnabled {
		return csi.ErrNotApplicable
	}
	if cfg.Providers.Proxmox.CSIURL == "" ||
		cfg.Providers.Proxmox.CSITokenID == "" ||
		cfg.Providers.Proxmox.CSITokenSecret == "" ||
		cfg.Providers.Proxmox.Region == "" {
		logx.Warn("proxmox-csi: skipping EnsureSecret — one or more required fields (CSIURL, CSITokenID, CSITokenSecret, Region) are empty.")
		return csi.ErrNotApplicable
	}
	return applyConfigSecretToWorkload(cfg, func() (string, error) {
		return workloadKubeconfigPath, nil
	})
}

func (driver) DefaultStorageClass() string {
	return "proxmox-data-xfs"
}

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("Proxmox CSI")
	w.Bullet("driver: %s (chart %s pinned %s)",
		"csi.proxmox.sinextra.dev",
		cfg.Providers.Proxmox.CSIChartName,
		cfg.Providers.Proxmox.CSIChartVersion)
	w.Bullet("auth: Kubernetes Secret <cluster>-proxmox-csi-config in %s",
		cfg.Providers.Proxmox.CSINamespace)
	w.Bullet("default StorageClass: %s",
		cfg.Providers.Proxmox.CSIStorageClassName)
}

// LoadVarsFromConfig fills empty cfg.Providers.Proxmox.{CSIURL,CSITokenID,
// CSITokenSecret} and cfg.Providers.Proxmox.Region from the on-disk
// PROXMOX_CSI_CONFIG YAML when that file exists.
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

// ApplyConfigSecretToWorkload pushes a Secret named
// <cluster>-proxmox-csi-config into cfg.Providers.Proxmox.CSINamespace
// on the workload, and mirrors the same content under the short
// name proxmox-csi-config.
func ApplyConfigSecretToWorkload(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	if err := applyConfigSecretToWorkload(cfg, writeWorkloadKubeconfig); err != nil {
		logx.Die("Failed to apply Proxmox CSI config Secret on workload cluster: %v", err)
	}
}

func applyConfigSecretToWorkload(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) error {
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
	if err := applySecret(bg, cli, cfg.Providers.Proxmox.CSINamespace, secretName, cfgYAML); err != nil {
		return fmt.Errorf("apply secret %s: %w", secretName, err)
	}
	if secretName != "proxmox-csi-config" {
		if err := applySecret(bg, cli, cfg.Providers.Proxmox.CSINamespace, "proxmox-csi-config", cfgYAML); err != nil {
			return fmt.Errorf("apply alias secret: %w", err)
		}
	}
	logx.Log("Applied %s (and proxmox-csi-config when names differ) — Proxmox API credentials in %s; Argo Application will not embed them.",
		secretName, cfg.Providers.Proxmox.CSINamespace)
	return nil
}

func applySecret(ctx context.Context, cli *k8sclient.Client, namespace, name, body string) error {
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
