// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package cindercsi implements the OpenStack Cinder CSI driver
// registration for yage's CSI add-on registry (internal/csi).
//
// Upstream chart: openstack-cinder-csi published by the Kubernetes
// community at https://kubernetes.github.io/cloud-provider-openstack.
// Auth is driven by a gcfg/INI payload stored in a kube-system Secret
// named "cinder-csi-cloud-config" under the key "cloud.conf" — the
// contract the openstack-cinder-csi chart mounts as /etc/kubernetes/
// cloud.conf and passes to the cinder-csi-plugin binary.
//
// The gcfg format (not YAML/clouds.yaml) is what --cloud-config
// expects: a [Global] section with auth-url, username, password,
// tenant-name, domain-name, and region-name keys. EnsureSecret builds
// this payload from the OS_* environment variables that the OpenStack
// provider uses throughout the orchestrator plus
// cfg.Providers.OpenStack.* for non-secret fields (region, project).
//
// Essential fields: OS_AUTH_URL must be set in the environment and
// cfg.Providers.OpenStack.Cloud must be non-empty (used only for
// guard logic — the INI format doesn't reference the cloud name).
// When either is absent EnsureSecret returns csi.ErrNotApplicable so
// dry-runs without credentials stay quiet.
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
	// secretName is the chart's default secret name when secret.create=false
	// and secret.enabled=true; operators set secret.name to this value in
	// RenderValues. Chart ref: values.yaml #  name: cinder-csi-cloud-config
	secretName    = "cinder-csi-cloud-config"
	secretDataKey = "cloud.conf"
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

// RenderValues emits a minimal Helm values document. The chart defaults
// to hostMount=true for credentials; we flip to secret-based auth by
// setting secret.enabled=true, secret.hostMount=false, and pointing
// secret.name at the Secret EnsureSecret applies. storageClass.delete
// is set as default to match DefaultStorageClass().
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/cindercsi.\n")
	b.WriteString("# Credentials: " + secretName + " Secret in " + secretNamespace + "\n")
	b.WriteString("# (applied by EnsureSecret from OS_* env / cfg.Providers.OpenStack).\n")
	b.WriteString("secret:\n")
	b.WriteString("  enabled: true\n")
	b.WriteString("  hostMount: false\n")
	b.WriteString("  create: false\n")
	b.WriteString("  name: " + secretName + "\n")
	b.WriteString("storageClass:\n")
	b.WriteString("  enabled: true\n")
	b.WriteString("  delete:\n")
	b.WriteString("    isDefault: true\n")
	return b.String(), nil
}

// EnsureSecret writes the kube-system/cinder-csi-cloud-config Secret on
// the workload cluster. The Secret carries an INI/gcfg payload (the
// format cinder-csi-plugin reads via --cloud-config) built from the OS_*
// environment variables and cfg.Providers.OpenStack.* for non-secret
// fields (region, project name when not in env).
//
// Returns csi.ErrNotApplicable when essential fields (OS_AUTH_URL env or
// cfg.Providers.OpenStack.Cloud) are absent — the orchestrator treats
// this as a clean skip so dry-runs without OpenStack credentials stay
// quiet.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	cloud := cfg.Providers.OpenStack.Cloud
	authURL := os.Getenv("OS_AUTH_URL")
	if cloud == "" || authURL == "" {
		return csi.ErrNotApplicable
	}

	cloudConf := buildCloudConf(authURL, cfg)

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
		Data: map[string][]byte{secretDataKey: []byte(cloudConf)},
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
	w.Bullet("auth: gcfg/INI in Secret %s/%s key %s (built from OS_* env + cfg.Providers.OpenStack)",
		secretNamespace, secretName, secretDataKey)
	w.Bullet("cloud: %s, region: %s",
		nonEmpty(cfg.Providers.OpenStack.Cloud, "<unset>"),
		nonEmpty(cfg.Providers.OpenStack.Region, "<unset>"))
	w.Bullet("default StorageClass: csi-cinder-sc-delete (volumeBindingMode WaitForFirstConsumer)")
	if os.Getenv("OS_AUTH_URL") == "" {
		w.Bullet("note: OS_AUTH_URL is unset — EnsureSecret will return ErrNotApplicable until credentials are provided")
	}
}

// buildCloudConf constructs the INI/gcfg payload that cinder-csi-plugin
// reads via --cloud-config. Only non-empty keys are emitted. The [Global]
// section holds all connection parameters; see
// https://github.com/kubernetes/cloud-provider-openstack for the full
// key reference.
//
// Key sources:
//   - auth-url:     OS_AUTH_URL env (required — guard in EnsureSecret)
//   - username:     OS_USERNAME env
//   - password:     OS_PASSWORD env
//   - tenant-name:  OS_PROJECT_NAME env, fallback cfg.Providers.OpenStack.ProjectName
//   - domain-name:  OS_USER_DOMAIN_NAME env, fallback OS_DOMAIN_NAME env
//   - region:       cfg.Providers.OpenStack.Region (non-secret runtime config)
func buildCloudConf(authURL string, cfg *config.Config) string {
	var b strings.Builder
	b.WriteString("[Global]\n")
	b.WriteString("auth-url=" + authURL + "\n")
	if v := os.Getenv("OS_USERNAME"); v != "" {
		b.WriteString("username=" + v + "\n")
	}
	if v := os.Getenv("OS_PASSWORD"); v != "" {
		b.WriteString("password=" + v + "\n")
	}
	if v := nonEmpty(os.Getenv("OS_PROJECT_NAME"), cfg.Providers.OpenStack.ProjectName); v != "" {
		b.WriteString("tenant-name=" + v + "\n")
	}
	if v := os.Getenv("OS_USER_DOMAIN_NAME"); v != "" {
		b.WriteString("domain-name=" + v + "\n")
	} else if v := os.Getenv("OS_DOMAIN_NAME"); v != "" {
		b.WriteString("domain-name=" + v + "\n")
	}
	if v := cfg.Providers.OpenStack.Region; v != "" {
		b.WriteString("region=" + v + "\n")
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
