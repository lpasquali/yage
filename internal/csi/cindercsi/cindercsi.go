// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package cindercsi implements the OpenStack Cinder CSI driver
// registration for yage's CSI add-on registry (internal/csi).
//
// Upstream chart: openstack-cinder-csi published by the Kubernetes
// community at https://kubernetes.github.io/cloud-provider-openstack.
// Auth is driven by a clouds.yaml payload stored in a kube-system
// Secret named "cloud-config" — the standard secret contract that
// cloud-provider-openstack components expect. EnsureSecret builds
// a minimal clouds.yaml from cfg.Providers.OpenStack.* fields and
// the OS_* environment variables (OS_AUTH_URL, OS_USERNAME,
// OS_PASSWORD, OS_PROJECT_NAME, OS_DOMAIN_NAME) following the same
// credential-from-env strategy the OpenStack provider uses throughout
// the orchestrator.
//
// Pinned chart version 2.31.2 — a recent stable release from the
// cloud-provider-openstack project as of early 2026. Updating the pin
// is a follow-up commit that accompanies a `helm-up` run.
package cindercsi

import (
	"context"
	"fmt"
	"os"
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
	secretName      = "cloud-config"
	secretDataKey   = "cloud.conf"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "openstack-cinder" }
func (driver) K8sCSIDriverName() string { return "cinder.csi.openstack.org" }
func (driver) Defaults() []string       { return []string{"openstack"} }

// HelmChart pins to version 2.31.2 of openstack-cinder-csi from the
// cloud-provider-openstack community chart repository.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://kubernetes.github.io/cloud-provider-openstack",
		"openstack-cinder-csi",
		"2.31.2",
		nil
}

// RenderValues emits a minimal Helm values document. The OpenStack
// credentials flow in via the cloud-config Secret in kube-system
// (applied by EnsureSecret); the chart references it by name.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/cindercsi.\n")
	b.WriteString("# Credentials: cloud-config Secret in kube-system\n")
	b.WriteString("# (applied by EnsureSecret from OS_* env / cfg.Providers.OpenStack).\n")
	b.WriteString("secret:\n")
	b.WriteString("  enabled: true\n")
	b.WriteString("  name: " + secretName + "\n")
	b.WriteString("  namespace: " + secretNamespace + "\n")
	b.WriteString("storageClass:\n")
	b.WriteString("  enabled: true\n")
	b.WriteString("  delete:\n")
	b.WriteString("    isDefault: true\n")
	return b.String(), nil
}

// EnsureSecret writes the kube-system/cloud-config Secret on the
// workload cluster. The Secret carries a clouds.yaml-format payload
// built from cfg.Providers.OpenStack.* fields and the OS_* environment
// variables that the OpenStack provider uses throughout the
// orchestrator.
//
// Returns csi.ErrNotApplicable when essential fields (AuthURL or
// Cloud name) are absent — the orchestrator treats this as a clean
// skip so dry-runs without OpenStack credentials stay quiet.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	cloud := cfg.Providers.OpenStack.Cloud
	authURL := os.Getenv("OS_AUTH_URL")
	if cloud == "" || authURL == "" {
		return csi.ErrNotApplicable
	}

	cloudsYAML := buildCloudsYAML(cloud, authURL, cfg)

	cli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("cindercsi: load workload kubeconfig %s: %w", workloadKubeconfigPath, err)
	}

	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: secretNamespace,
			Name:      secretName,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{secretDataKey: []byte(cloudsYAML)},
	}
	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("cindercsi: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("cindercsi: yaml→json: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	force := true
	_, err = cli.Typed.CoreV1().Secrets(secretNamespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("cindercsi: apply %s/%s: %w", secretNamespace, secretName, err)
	}
	return nil
}

func (driver) DefaultStorageClass() string { return "csi-cinder-sc-delete" }

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("OpenStack Cinder CSI")
	w.Bullet("driver: %s (chart openstack-cinder-csi pinned 2.31.2)", "cinder.csi.openstack.org")
	w.Bullet("auth: clouds.yaml in Secret %s/%s (built from OS_* env + cfg.Providers.OpenStack)", secretNamespace, secretName)
	w.Bullet("cloud: %s, region: %s",
		nonEmpty(cfg.Providers.OpenStack.Cloud, "<unset>"),
		nonEmpty(cfg.Providers.OpenStack.Region, "<unset>"))
	w.Bullet("default StorageClass: csi-cinder-sc-delete")
	if os.Getenv("OS_AUTH_URL") == "" {
		w.Bullet("note: OS_AUTH_URL is unset — EnsureSecret will return ErrNotApplicable until credentials are provided")
	}
}

// buildCloudsYAML constructs a minimal clouds.yaml from the supplied
// cloud name, authURL, and optional cfg fields. Only non-empty values
// are emitted to keep the YAML minimal and avoid overriding OpenStack
// client defaults with empty strings.
func buildCloudsYAML(cloud, authURL string, cfg *config.Config) string {
	var b strings.Builder
	b.WriteString("clouds:\n")
	b.WriteString("  " + cloud + ":\n")
	b.WriteString("    auth:\n")
	b.WriteString("      auth_url: " + authURL + "\n")
	if v := nonEmpty(os.Getenv("OS_PROJECT_NAME"), cfg.Providers.OpenStack.ProjectName); v != "" {
		b.WriteString("      project_name: " + v + "\n")
	}
	if v := os.Getenv("OS_USERNAME"); v != "" {
		b.WriteString("      username: " + v + "\n")
	}
	if v := os.Getenv("OS_PASSWORD"); v != "" {
		b.WriteString("      password: " + v + "\n")
	}
	if v := os.Getenv("OS_USER_DOMAIN_NAME"); v != "" {
		b.WriteString("      user_domain_name: " + v + "\n")
	} else if v := os.Getenv("OS_DOMAIN_NAME"); v != "" {
		b.WriteString("      user_domain_name: " + v + "\n")
	}
	if v := cfg.Providers.OpenStack.Region; v != "" {
		b.WriteString("    region_name: " + v + "\n")
	}
	return b.String()
}

// nonEmpty returns a when non-empty, otherwise fallback.
func nonEmpty(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}
