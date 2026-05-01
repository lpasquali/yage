// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package azuredisk implements the Azure Disk CSI driver
// registration for yage's CSI add-on registry (internal/csi).
//
// Auth model branches on cfg.Providers.Azure.IdentityModel:
//
//   - "service-principal" (default): a "azure-cloud-config" Secret
//     in kube-system holds the JSON cloud config (tenantId,
//     subscriptionId, clientId, clientSecret, resourceGroup,
//     location). The chart picks this up via the
//     cloudConfigSecretName value. EnsureSecret applies the Secret
//     server-side via the workload kubeconfig.
//
//   - "workload-identity": no Secret needed; the controller
//     ServiceAccount carries an "azure.workload.identity/client-id"
//     annotation and AAD federates the token. EnsureSecret returns
//     ErrNotApplicable.
//
//   - "managed-identity" / unset: treated as service-principal for
//     this commit (the §13.4 #4 discriminator landing is a separate
//     follow-up); EnsureSecret writes the Secret if creds are
//     populated, no-ops otherwise.
//
// Pinned chart version v1.31.0 — released mid-2024, supports K8s
// 1.27+. Pin updates land alongside `helm-up` runs.
package azuredisk

import (
	"context"
	"encoding/json"
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
	secretName      = "azure-cloud-config"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "azure-disk" }
func (driver) K8sCSIDriverName() string { return "disk.csi.azure.com" }
func (driver) Defaults() []string       { return []string{"azure"} }

func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts",
		"azuredisk-csi-driver",
		"v1.31.0",
		nil
}

// RenderValues emits Helm values that branch on the configured
// IdentityModel. Workload-identity leans on the annotated SA;
// service-principal references the kube-system/azure-cloud-config
// Secret EnsureSecret apply.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/azuredisk.\n")
	b.WriteString("controller:\n")
	b.WriteString("  replicas: 2\n")
	b.WriteString("linux:\n")
	b.WriteString("  enabled: true\n")
	b.WriteString("storageClasses:\n")
	b.WriteString("  - name: azuredisk-standard-ssd\n")
	b.WriteString("    annotations:\n")
	b.WriteString("      storageclass.kubernetes.io/is-default-class: \"true\"\n")
	b.WriteString("    parameters:\n")
	b.WriteString("      skuName: StandardSSD_LRS\n")
	b.WriteString("    volumeBindingMode: WaitForFirstConsumer\n")
	b.WriteString("    reclaimPolicy: Delete\n")

	if usesWorkloadIdentity(cfg) {
		b.WriteString("# Workload Identity: SA annotated for AAD federation.\n")
		b.WriteString("serviceAccount:\n")
		b.WriteString("  controller:\n")
		b.WriteString("    create: true\n")
		b.WriteString("    annotations:\n")
		b.WriteString(fmt.Sprintf("      azure.workload.identity/client-id: %q\n",
			cfg.Providers.Azure.ClientID))
		b.WriteString("    labels:\n")
		b.WriteString("      azure.workload.identity/use: \"true\"\n")
	} else {
		// Service-principal path — chart reads the cloud config
		// from a kube-system Secret. EnsureSecret() places it.
		b.WriteString("# Service-Principal: cloud config Secret in kube-system.\n")
		b.WriteString("cloud: AzurePublicCloud\n")
		b.WriteString("controller:\n")
		b.WriteString("  cloudConfigSecretName: " + secretName + "\n")
		b.WriteString("  cloudConfigSecretNamespace: " + secretNamespace + "\n")
	}
	return b.String(), nil
}

// EnsureSecret writes the kube-system/azure-cloud-config Secret on
// the workload cluster when the Service-Principal identity model is
// in effect. The Secret holds a single cloud-config.json key whose
// JSON body matches the AKS-style cloud config the upstream chart
// expects.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	if usesWorkloadIdentity(cfg) {
		return nil
	}
	az := cfg.Providers.Azure
	// Skip silently when there's nothing to write — keeps dry-runs
	// clean.
	if az.SubscriptionID == "" && az.TenantID == "" && az.ClientID == "" {
		return nil
	}
	body := map[string]any{
		"cloud":               "AzurePublicCloud",
		"tenantId":            az.TenantID,
		"subscriptionId":      az.SubscriptionID,
		"aadClientId":         az.ClientID,
		"resourceGroup":       az.ResourceGroup,
		"location":            az.Location,
		"vnetName":            az.VNetName,
		"subnetName":          az.SubnetName,
		"useManagedIdentityExtension": false,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("azuredisk: marshal cloud config: %w", err)
	}

	cli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("azuredisk: load workload kubeconfig %s: %w", workloadKubeconfigPath, err)
	}
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: secretNamespace,
			Name:      secretName,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"cloud-config.json": jsonBody},
	}
	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("azuredisk: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("azuredisk: yaml→json: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	force := true
	_, err = cli.Typed.CoreV1().Secrets(secretNamespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("azuredisk: apply %s/%s: %w", secretNamespace, secretName, err)
	}
	return nil
}

func (driver) DefaultStorageClass() string { return "azuredisk-standard-ssd" }

// EnsureManagementInstall returns ErrNotApplicable: Azure does not
// pivot to a CAPI management cluster via yage's pivot path.
func (driver) EnsureManagementInstall(_ *config.Config, _ string) error {
	return csi.ErrNotApplicable
}

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("Azure Disk CSI")
	w.Bullet("driver: %s (chart azuredisk-csi-driver pinned v1.31.0)", "disk.csi.azure.com")
	if usesWorkloadIdentity(cfg) {
		w.Bullet("auth: Workload Identity (controller SA annotated with azure.workload.identity/client-id=%s)",
			nonEmpty(cfg.Providers.Azure.ClientID, "<unset>"))
	} else {
		w.Bullet("auth: Service-Principal (Secret %s/%s on workload cluster)", secretNamespace, secretName)
		w.Bullet("subscription: %s, tenant: %s, resourceGroup: %s",
			nonEmpty(cfg.Providers.Azure.SubscriptionID, "<unset>"),
			nonEmpty(cfg.Providers.Azure.TenantID, "<unset>"),
			nonEmpty(cfg.Providers.Azure.ResourceGroup, "<unset>"))
	}
	w.Bullet("default StorageClass: azuredisk-standard-ssd (StandardSSD_LRS, WaitForFirstConsumer)")
}

// usesWorkloadIdentity returns true when the operator picked
// AAD Workload Identity. Anything else falls back to the
// service-principal path for this commit; managed-identity gets
// proper handling once §13.4 #4 lands.
func usesWorkloadIdentity(cfg *config.Config) bool {
	return strings.EqualFold(cfg.Providers.Azure.IdentityModel, "workload-identity")
}

func nonEmpty(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}