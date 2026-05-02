// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package doblock implements the DigitalOcean Block Storage (DOBS) CSI
// driver registration for yage's CSI add-on registry (internal/csi).
//
// The upstream Helm chart is do-csi-driver published by DigitalOcean at
// https://charts.digitalocean.com. Auth requires a DigitalOcean personal
// access token stored in a kube-system/digitalocean Secret under the key
// "access-token". The token comes from cfg.Cost.Credentials.DigitalOceanToken
// (env: YAGE_DO_TOKEN / DIGITALOCEAN_TOKEN).
//
// Pinned chart version v4.14.0 — a stable release that supports K8s 1.24+.
// Updating the pin is a follow-up commit (chart bumps land alongside
// `helm-up` runs, not silently).
package doblock

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/capi/templates"
	"github.com/lpasquali/yage/internal/platform/manifests"
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/plan"
)

const (
	secretNamespace = "kube-system"
	secretName      = "digitalocean"
	tokenKey        = "access-token"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "do-block-storage" }
func (driver) K8sCSIDriverName() string { return "dobs.csi.digitalocean.com" }
func (driver) Defaults() []string       { return []string{"digitalocean"} }

// HelmChart pins to v4.14.0 of do-csi-driver from the DigitalOcean chart repo.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://charts.digitalocean.com",
		"do-csi-driver",
		"v4.14.0",
		nil
}

func (driver) Render(f *manifests.Fetcher, cfg *config.Config) (string, error) {
	return f.Render("csi/do-block-storage/values.yaml.tmpl", templates.HelmValuesData{Cfg: cfg})
}

// EnsureSecret writes the kube-system/digitalocean Secret on the workload
// cluster. The DO CSI driver requires this Secret to be present before
// the Helm chart is installed; it reads the access-token at controller
// start-up. Returns an error if the token is empty — the driver cannot
// authenticate without it (there is no ambient-identity fallback for DO).
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	token := cfg.Cost.Credentials.DigitalOceanToken
	if token == "" {
		return fmt.Errorf("doblock: DigitalOcean access token is empty; set YAGE_DO_TOKEN or DIGITALOCEAN_TOKEN")
	}
	cli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("doblock: load workload kubeconfig %s: %w", workloadKubeconfigPath, err)
	}
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: secretNamespace,
			Name:      secretName,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{tokenKey: []byte(token)},
	}
	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("doblock: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("doblock: yaml→json: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	force := true
	_, err = cli.Typed.CoreV1().Secrets(secretNamespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("doblock: apply %s/%s: %w", secretNamespace, secretName, err)
	}
	return nil
}

func (driver) DefaultStorageClass() string { return "do-block-storage" }

// EnsureManagementInstall returns ErrNotApplicable: DigitalOcean does
// not pivot to a CAPI management cluster via yage's pivot path.
func (driver) EnsureManagementInstall(_ *config.Config, _ string) error {
	return csi.ErrNotApplicable
}

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("DigitalOcean Block Storage CSI")
	w.Bullet("driver: %s (chart do-csi-driver pinned v4.14.0)", "dobs.csi.digitalocean.com")
	w.Bullet("auth: DigitalOcean access-token (Secret %s/%s key %s)", secretNamespace, secretName, tokenKey)
	if cfg.Cost.Credentials.DigitalOceanToken == "" {
		w.Bullet("note: YAGE_DO_TOKEN / DIGITALOCEAN_TOKEN is unset — EnsureSecret will fail until a token is provided")
	}
	w.Bullet("default StorageClass: do-block-storage (volumeBindingMode WaitForFirstConsumer, pd-ssd volumes)")
}
