// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package ociblock implements the Oracle Cloud Infrastructure Block Volume
// CSI driver registration for yage's CSI add-on registry (internal/csi).
//
// The upstream chart is oci-csi-node published by Oracle at
// https://oracle.github.io/helm-charts. Auth requires a config file
// containing OCI API key credentials; yage writes this as a Kubernetes
// Secret named oci-cloud-controller-manager in kube-system. If no OCI
// credentials are present in cfg, EnsureSecret returns ErrNotApplicable
// and the operator must supply the Secret out-of-band.
//
// Pinned chart version 1.28.0 — a stable release from the Oracle Helm
// chart repository. Updating the pin is a follow-up commit alongside
// helm-up runs.
package ociblock

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
	secretName      = "oci-cloud-controller-manager"
	configKey       = "cloud-provider.yaml"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "oci-block-storage" }
func (driver) K8sCSIDriverName() string { return "blockvolume.csi.oraclecloud.com" }
func (driver) Defaults() []string       { return []string{"oci"} }

// HelmChart pins to version 1.28.0 of oci-csi-node from Oracle's
// official Helm chart repository.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://oracle.github.io/helm-charts",
		"oci-csi-node",
		"1.28.0",
		nil
}

// RenderValues emits a minimal Helm values document. The OCI CSI node
// driver reads cloud credentials from the oci-cloud-controller-manager
// Secret in kube-system (written by EnsureSecret). The default
// StorageClass uses the oci-bv provisioner with WaitForFirstConsumer
// binding so volumes are created in the same AD as the pod's node.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/ociblock.\n")
	b.WriteString("# OCI credentials are read from the oci-cloud-controller-manager\n")
	b.WriteString("# Secret in kube-system (written by EnsureSecret).\n")
	b.WriteString("cloudConfig:\n")
	b.WriteString("  secretName: " + secretName + "\n")
	b.WriteString("  secretNamespace: " + secretNamespace + "\n")
	b.WriteString("storageClasses:\n")
	b.WriteString("  - name: oci-bv\n")
	b.WriteString("    annotations:\n")
	b.WriteString("      storageclass.kubernetes.io/is-default-class: \"true\"\n")
	b.WriteString("    provisioner: blockvolume.csi.oraclecloud.com\n")
	b.WriteString("    volumeBindingMode: WaitForFirstConsumer\n")
	b.WriteString("    reclaimPolicy: Delete\n")
	return b.String(), nil
}

// EnsureSecret writes the kube-system/oci-cloud-controller-manager Secret
// on the workload cluster. The Secret contains a minimal OCI config file
// built from cfg.Providers.OCI fields. If the required credential fields
// (TenancyOCID, UserOCID, Fingerprint) are not set in cfg, EnsureSecret
// returns ErrNotApplicable — the operator must supply the Secret out-of-band.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	oci := cfg.Providers.OCI
	if oci.TenancyOCID == "" || oci.UserOCID == "" || oci.Fingerprint == "" {
		return csi.ErrNotApplicable
	}

	configBody := buildOCIConfig(oci)

	cli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("ociblock: load workload kubeconfig %s: %w", workloadKubeconfigPath, err)
	}

	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: secretNamespace,
			Name:      secretName,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{configKey: []byte(configBody)},
	}
	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("ociblock: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("ociblock: yaml→json: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	force := true
	_, err = cli.Typed.CoreV1().Secrets(secretNamespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("ociblock: apply %s/%s: %w", secretNamespace, secretName, err)
	}
	return nil
}

func (driver) DefaultStorageClass() string { return "oci-bv" }

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("OCI Block Volume CSI")
	w.Bullet("driver: %s (chart oci-csi-node pinned 1.28.0)", "blockvolume.csi.oraclecloud.com")
	oci := cfg.Providers.OCI
	if oci.TenancyOCID == "" || oci.UserOCID == "" || oci.Fingerprint == "" {
		w.Bullet("auth: OCI credentials not set — EnsureSecret will return ErrNotApplicable; supply Secret %s/%s out-of-band", secretNamespace, secretName)
	} else {
		w.Bullet("auth: OCI API key (Secret %s/%s built from cfg.Providers.OCI)", secretNamespace, secretName)
		w.Bullet("tenancy: %s, user: %s", oci.TenancyOCID, oci.UserOCID)
	}
	w.Bullet("region: %s", nonEmpty(oci.Region, "(unset)"))
	w.Bullet("compartment: %s", nonEmpty(oci.CompartmentOCID, "(unset)"))
	w.Bullet("default StorageClass: oci-bv (volumeBindingMode WaitForFirstConsumer)")
}

// buildOCIConfig produces a minimal OCI cloud-provider YAML config
// for the CSI driver. The private key file reference uses
// cfg.PrivateKeyPath; operators must ensure the key is available at
// that path on the node or mounted via a separate volume.
func buildOCIConfig(oci config.OCIConfig) string {
	var b strings.Builder
	b.WriteString("auth:\n")
	b.WriteString("  region: " + oci.Region + "\n")
	b.WriteString("  tenancy: " + oci.TenancyOCID + "\n")
	b.WriteString("  user: " + oci.UserOCID + "\n")
	b.WriteString("  key_file: " + nonEmpty(oci.PrivateKeyPath, "/etc/oci/oci_api_key.pem") + "\n")
	b.WriteString("  fingerprint: " + oci.Fingerprint + "\n")
	if oci.CompartmentOCID != "" {
		b.WriteString("compartment: " + oci.CompartmentOCID + "\n")
	}
	return b.String()
}

func nonEmpty(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}
