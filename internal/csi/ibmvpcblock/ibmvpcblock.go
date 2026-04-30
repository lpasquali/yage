// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package ibmvpcblock implements the IBM VPC Block CSI driver
// registration for yage's CSI add-on registry (internal/csi).
//
// The upstream Helm chart is published at icr.io/helm/ibm-helm under
// the name ibm-vpc-block-csi-driver. Auth is via an IBM Cloud API key
// stored in a Kubernetes Secret (ibm-vpc-block-csi-storageclasses) in
// kube-system — the driver reads the apiKey field at runtime to call
// the IBM VPC API for volume operations.
//
// Pinned chart version 5.2.0 — a recent stable release that supports
// IBM VPC Gen2 on Kubernetes 1.26+. Update the pin alongside a
// helm-up run, not silently.
package ibmvpcblock

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
	secretName      = "ibm-vpc-block-csi-storageclasses"
	apiKeyField     = "apiKey"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "ibm-vpc-block" }
func (driver) K8sCSIDriverName() string { return "vpc.block.csi.ibm.io" }
func (driver) Defaults() []string       { return []string{"ibmcloud"} }

// HelmChart pins to version 5.2.0 of ibm-vpc-block-csi-driver from
// the IBM Container Registry Helm repository.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://icr.io/helm/ibm-helm",
		"ibm-vpc-block-csi-driver",
		"5.2.0",
		nil
}

// RenderValues emits a minimal Helm values document. The IBM VPC Block
// CSI driver requires cluster-level configuration (cluster ID, resource
// group) that operators typically set via the chart's own ConfigMap or
// via Helm value overrides at install time.
//
// Operators must set the following in their Helm values override:
//   - clusterInfo.clusterID: the IKS/VPC cluster ID
//   - clusterInfo.resourceGroupID: the IBM Cloud resource group ID
//   - image.ibmVpcBlockDriver: the driver image if using a private registry
//
// The IBM API key is supplied via the Secret created by EnsureSecret,
// not through Helm values.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/ibmvpcblock.\n")
	b.WriteString("# Operator MUST set the following via --csi-values-file or Helm override:\n")
	b.WriteString("#   clusterInfo.clusterID: <your VPC cluster ID>\n")
	b.WriteString("#   clusterInfo.resourceGroupID: <your IBM Cloud resource group ID>\n")
	b.WriteString("# To use a private registry, also set:\n")
	b.WriteString("#   image.ibmVpcBlockDriver: <registry/ibm-vpc-block-csi-driver:tag>\n")
	b.WriteString("# The IBM Cloud API key is supplied via the kube-system/")
	b.WriteString(secretName + " Secret.\n")
	return b.String(), nil
}

// EnsureSecret creates or updates the kube-system/ibm-vpc-block-csi-storageclasses
// Secret on the workload cluster. The apiKey field is set from
// cfg.Cost.Credentials.IBMCloudAPIKey. Returns an error if the API
// key is empty — the driver cannot authenticate without it.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	if cfg.Cost.Credentials.IBMCloudAPIKey == "" {
		return fmt.Errorf("ibmvpcblock: cfg.Cost.Credentials.IBMCloudAPIKey is empty; set IBMCLOUD_API_KEY or provide it via the bootstrap config Secret")
	}
	cli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("ibmvpcblock: load workload kubeconfig %s: %w", workloadKubeconfigPath, err)
	}
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: secretNamespace,
			Name:      secretName,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			apiKeyField: []byte(cfg.Cost.Credentials.IBMCloudAPIKey),
		},
	}
	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("ibmvpcblock: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("ibmvpcblock: yaml→json: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	force := true
	_, err = cli.Typed.CoreV1().Secrets(secretNamespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("ibmvpcblock: apply %s/%s: %w", secretNamespace, secretName, err)
	}
	return nil
}

func (driver) DefaultStorageClass() string { return "ibmc-vpc-block-10iops-tier" }

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("IBM VPC Block CSI")
	w.Bullet("driver: %s (chart ibm-vpc-block-csi-driver pinned 5.2.0)", "vpc.block.csi.ibm.io")
	w.Bullet("auth: IBM Cloud API key via Secret %s/%s", secretNamespace, secretName)
	if cfg.Cost.Credentials.IBMCloudAPIKey == "" {
		w.Bullet("note: IBMCloudAPIKey is unset — EnsureSecret will fail; set IBMCLOUD_API_KEY")
	}
	w.Bullet("region: %s, resource group: %s",
		nonEmpty(cfg.Providers.IBMCloud.Region, "<unset>"),
		nonEmpty(cfg.Providers.IBMCloud.ResourceGroup, "<unset>"))
	w.Bullet("default StorageClass: ibmc-vpc-block-10iops-tier")
	w.Bullet("note: operator must supply clusterInfo.clusterID and clusterInfo.resourceGroupID via Helm values override")
}

// nonEmpty returns a if non-empty, otherwise fallback.
func nonEmpty(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}
