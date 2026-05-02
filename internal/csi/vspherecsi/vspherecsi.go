// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package vspherecsi implements the vSphere CSI driver registration
// for yage's CSI add-on registry (internal/csi). The upstream chart
// is vsphere-csi-driver published by kubernetes.github.io/cloud-provider-vsphere.
//
// Auth model: an INI-format Secret named "vsphere-config-secret" in
// kube-system holds the vCenter connection configuration. The chart
// references this Secret via config.existingSecretName. All three of
// Server, Username, and Password must be set; EnsureSecret returns an
// error if any are missing.
//
// The Server field is used as-is (plain host or host:port, no scheme).
// This matches how the vsphere provider (govmomi) consumes the same
// field in internal/provider/vsphere/connect.go.
//
// Pinned chart version 3.3.1 — a recent stable release of the
// kubernetes/cloud-provider-vsphere project. Pin updates land
// alongside helm-up runs, not silently.
package vspherecsi

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
	"github.com/lpasquali/yage/internal/capi/templates"
	"github.com/lpasquali/yage/internal/platform/manifests"
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/plan"
)

const (
	secretNamespace = "kube-system"
	secretName      = "vsphere-config-secret"
	secretKey       = "csi-vsphere.conf"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "vsphere-csi" }
func (driver) K8sCSIDriverName() string { return "csi.vsphere.volume" }
func (driver) Defaults() []string       { return []string{"vsphere"} }

// HelmChart pins to v3.3.1 of vsphere-csi-driver from the
// kubernetes/cloud-provider-vsphere Helm chart repository.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://kubernetes.github.io/cloud-provider-vsphere",
		"vsphere-csi-driver",
		"3.3.1",
		nil
}

func (driver) Render(f *manifests.Fetcher, cfg *config.Config) (string, error) {
	return f.Render("csi/vsphere-csi/values.yaml.tmpl", templates.HelmValuesData{Cfg: cfg})
}

// EnsureSecret writes kube-system/vsphere-config-secret on the workload
// cluster. The Secret contains a csi-vsphere.conf INI file built from
// cfg.Providers.Vsphere. Returns an error if Server, Username, or
// Password are not set — the driver cannot function without them.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	vs := cfg.Providers.Vsphere

	// Validate required fields before touching the kubeconfig.
	if vs.Server == "" {
		return fmt.Errorf("vspherecsi: Providers.Vsphere.Server must be set")
	}
	if vs.Username == "" {
		return fmt.Errorf("vspherecsi: Providers.Vsphere.Username must be set")
	}
	if vs.Password == "" {
		return fmt.Errorf("vspherecsi: Providers.Vsphere.Password must be set")
	}

	iniBody := buildINI(cfg)

	cli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("vspherecsi: load workload kubeconfig %s: %w", workloadKubeconfigPath, err)
	}

	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: secretNamespace,
			Name:      secretName,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{secretKey: []byte(iniBody)},
	}
	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("vspherecsi: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("vspherecsi: yaml→json: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	force := true
	_, err = cli.Typed.CoreV1().Secrets(secretNamespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("vspherecsi: apply %s/%s: %w", secretNamespace, secretName, err)
	}
	return nil
}

func (driver) DefaultStorageClass() string { return "vsphere-sc" }

// EnsureManagementInstall returns ErrNotApplicable: vSphere does not
// pivot to a CAPI management cluster via yage's pivot path.
func (driver) EnsureManagementInstall(_ *config.Config, _ string) error {
	return csi.ErrNotApplicable
}

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	vs := cfg.Providers.Vsphere
	w.Section("vSphere CSI")
	w.Bullet("driver: %s (chart vsphere-csi-driver pinned 3.3.1)", "csi.vsphere.volume")
	w.Bullet("auth: INI config Secret %s/%s on workload cluster (host/user/password/thumbprint)",
		secretNamespace, secretName)
	w.Bullet("vCenter: %s, datacenter: %s",
		nonEmpty(vs.Server, "<unset>"),
		nonEmpty(vs.Datacenter, "<unset>"))
	if vs.TLSThumbprint != "" {
		w.Bullet("TLS: thumbprint pinned (%s)", vs.TLSThumbprint)
	} else {
		w.Bullet("TLS: thumbprint not set — insecure-flag will be true (dev/lab only)")
	}
	w.Bullet("default StorageClass: vsphere-sc (WaitForFirstConsumer)")
}

// buildINI constructs the csi-vsphere.conf INI file content from cfg.
// The VirtualCenter section header uses the Server value exactly as
// provided (consistent with internal/provider/vsphere/connect.go which
// treats Server as host or host:port, no scheme).
func buildINI(cfg *config.Config) string {
	vs := cfg.Providers.Vsphere
	var b strings.Builder

	b.WriteString("[Global]\n")
	if cfg.WorkloadClusterName != "" {
		b.WriteString(fmt.Sprintf("cluster-id = %q\n", cfg.WorkloadClusterName))
	}
	b.WriteString("\n")

	b.WriteString(fmt.Sprintf("[VirtualCenter %q]\n", vs.Server))
	b.WriteString(fmt.Sprintf("user = %q\n", vs.Username))
	b.WriteString(fmt.Sprintf("password = %q\n", vs.Password))
	if vs.Datacenter != "" {
		b.WriteString(fmt.Sprintf("datacenters = %q\n", vs.Datacenter))
	}
	if vs.TLSThumbprint != "" {
		b.WriteString(fmt.Sprintf("thumbprint = %q\n", vs.TLSThumbprint))
		b.WriteString("insecure-flag = \"false\"\n")
	} else {
		b.WriteString("insecure-flag = \"true\"\n")
	}
	return b.String()
}

// nonEmpty returns a when non-empty, fallback otherwise.
func nonEmpty(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}
