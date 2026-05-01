// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package linodebs implements the Linode Block Storage CSI driver
// registration for yage's CSI add-on registry (internal/csi).
//
// Chart coordinates: the task spec referenced
// https://linode.github.io/linode-cloud-controller-manager with chart
// "linode-csi-driver" — that repo hosts only ccm-linode (the Cloud
// Controller Manager), not the CSI driver. The canonical CSI chart is
// published at https://linode.github.io/linode-blockstorage-csi-driver
// with chart name "linode-blockstorage-csi-driver". The coordinates
// below were verified against the live Helm index.
//
// Auth model: a Kubernetes Secret named "linode" in kube-system holds
// the Linode API token under the key "token". The CSI driver reads this
// Secret to authenticate against the Linode Block Storage API. There is
// no Workload Identity equivalent for Linode, so EnsureSecret returns an
// error when the token is empty — the operator must supply
// YAGE_LINODE_TOKEN or LINODE_TOKEN before running the orchestrator.
//
// Pinned chart version v1.1.2 — the latest stable release as of 2026.
package linodebs

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
	secretName      = "linode"
	tokenKey        = "token"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "linode-block-storage" }
func (driver) K8sCSIDriverName() string { return "linodebs.csi.linode.com" }
func (driver) Defaults() []string       { return []string{"linode"} }

// HelmChart pins to v1.1.2 of linode-blockstorage-csi-driver from the
// Linode-published Helm repo. Note: the task spec referenced the CCM
// repo (linode-cloud-controller-manager); the actual CSI chart lives at
// linode-blockstorage-csi-driver — see package doc for details.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://linode.github.io/linode-blockstorage-csi-driver",
		"linode-blockstorage-csi-driver",
		"v1.1.2",
		nil
}

// RenderValues emits a minimal Helm values document. The chart expects
// a pre-existing Secret named "linode" in kube-system with a "token"
// key (ensured by EnsureSecret before Helm install).
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/linodebs.\n")
	b.WriteString("# Requires kube-system/linode Secret with key 'token' (see EnsureSecret).\n")
	b.WriteString("secret:\n")
	b.WriteString("  name: " + secretName + "\n")
	b.WriteString("  namespace: " + secretNamespace + "\n")
	b.WriteString("storageClasses:\n")
	b.WriteString("  - name: linode-block-storage\n")
	b.WriteString("    annotations:\n")
	b.WriteString("      storageclass.kubernetes.io/is-default-class: \"true\"\n")
	b.WriteString("    parameters:\n")
	b.WriteString("      type: namedtype\n")
	b.WriteString("    volumeBindingMode: WaitForFirstConsumer\n")
	b.WriteString("    reclaimPolicy: Delete\n")
	return b.String(), nil
}

// EnsureSecret applies the kube-system/linode Secret to the workload
// cluster. The token comes from cfg.Cost.Credentials.LinodeToken which
// is populated from YAGE_LINODE_TOKEN or LINODE_TOKEN. Returns an error
// if the token is empty — unlike cloud-native identity (IRSA, Workload
// Identity), Linode has no equivalent and the Secret is always required.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	token := cfg.Cost.Credentials.LinodeToken
	if token == "" {
		return fmt.Errorf("linodebs: Linode API token is empty; set YAGE_LINODE_TOKEN or LINODE_TOKEN")
	}
	cli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("linodebs: load workload kubeconfig %s: %w", workloadKubeconfigPath, err)
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
		return fmt.Errorf("linodebs: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("linodebs: yaml→json: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	force := true
	_, err = cli.Typed.CoreV1().Secrets(secretNamespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("linodebs: apply %s/%s: %w", secretNamespace, secretName, err)
	}
	return nil
}

func (driver) DefaultStorageClass() string { return "linode-block-storage" }

// EnsureManagementInstall returns ErrNotApplicable: Linode does not
// pivot to a CAPI management cluster via yage's pivot path.
func (driver) EnsureManagementInstall(_ *config.Config, _ string) error {
	return csi.ErrNotApplicable
}

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("Linode Block Storage CSI")
	w.Bullet("driver: %s (chart linode-blockstorage-csi-driver pinned v1.1.2)", "linodebs.csi.linode.com")
	w.Bullet("auth: API token Secret %s/%s (key: %s)", secretNamespace, secretName, tokenKey)
	if cfg.Cost.Credentials.LinodeToken == "" {
		w.Bullet("note: YAGE_LINODE_TOKEN / LINODE_TOKEN is unset — EnsureSecret will fail at install time")
	}
	w.Bullet("default StorageClass: linode-block-storage (volumeBindingMode WaitForFirstConsumer)")
}
