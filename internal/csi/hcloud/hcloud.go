// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package hcloud implements the Hetzner Cloud CSI driver registration
// for yage's CSI add-on registry (internal/csi). The upstream chart is
// hcloud-csi published by Hetzner at https://charts.hetzner.cloud; auth
// is via a Hetzner Cloud project API token materialized as a Kubernetes
// Secret (kube-system/hcloud-csi, key "token") on the workload cluster.
//
// The chart reads the token from that well-known Secret at controller
// startup — no Helm values are needed to wire the credential, only a
// stable secret reference in the values YAML.
//
// Pinned chart version v2.6.0 — a stable release that supports
// Kubernetes 1.27+. Updating the pin is a follow-up commit.
package hcloud

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/plan"
)

const (
	secretNamespace = "kube-system"
	secretName      = "hcloud-csi"
	tokenKey        = "token"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "hcloud-csi" }
func (driver) K8sCSIDriverName() string { return "csi.hetzner.cloud" }
func (driver) Defaults() []string       { return []string{"hetzner"} }

// HelmChart pins to v2.6.0 of hcloud-csi from the official Hetzner
// Helm chart repository.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://charts.hetzner.cloud",
		"hcloud-csi",
		"v2.6.0",
		nil
}

// RenderValues emits a minimal Helm values document. The API token is
// read by the controller from the kube-system/hcloud-csi Secret applied
// by EnsureSecret — this values block points the chart at that Secret
// rather than inlining the token value.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/hcloud.\n")
	b.WriteString("# Auth: Kubernetes Secret applied by EnsureSecret\n")
	b.WriteString("# (kube-system/hcloud-csi, key \"token\").\n")
	b.WriteString("secret:\n")
	b.WriteString("  name: " + secretName + "\n")
	b.WriteString("storageClasses:\n")
	b.WriteString("  - name: hcloud-volumes\n")
	b.WriteString("    annotations:\n")
	b.WriteString("      storageclass.kubernetes.io/is-default-class: \"true\"\n")
	b.WriteString("    reclaimPolicy: Delete\n")
	b.WriteString("    volumeBindingMode: WaitForFirstConsumer\n")
	return b.String(), nil
}

// EnsureSecret creates (or updates) the kube-system/hcloud-csi Secret
// on the workload cluster with the Hetzner Cloud API token. The token
// is required — an empty token returns an error immediately rather than
// applying a broken Secret.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	if cfg.Providers.Hetzner.Token == "" {
		return fmt.Errorf("hcloud: HCLOUD_TOKEN is required")
	}
	cli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("hcloud: load workload kubeconfig %s: %w", workloadKubeconfigPath, err)
	}
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: secretNamespace,
			Name:      secretName,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{tokenKey: []byte(cfg.Providers.Hetzner.Token)},
	}
	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("hcloud: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("hcloud: yaml→json: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	force := true
	_, err = cli.Typed.CoreV1().Secrets(secretNamespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("hcloud: apply %s/%s: %w", secretNamespace, secretName, err)
	}
	return nil
}

func (driver) DefaultStorageClass() string { return "hcloud-volumes" }

// EnsureManagementInstall returns ErrNotApplicable: Hetzner does not
// pivot to a CAPI management cluster via yage's pivot path.
func (driver) EnsureManagementInstall(_ *config.Config, _ string) error {
	return csi.ErrNotApplicable
}

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("Hetzner Cloud CSI")
	w.Bullet("driver: %s (chart hcloud-csi pinned v2.6.0)", "csi.hetzner.cloud")
	w.Bullet("auth: Kubernetes Secret %s/%s (key %q) applied by EnsureSecret",
		secretNamespace, secretName, tokenKey)
	w.Bullet("token source: HCLOUD_TOKEN / YAGE_HCLOUD_TOKEN → cfg.Providers.Hetzner.Token")
	w.Bullet("default StorageClass: hcloud-volumes (WaitForFirstConsumer)")
}
