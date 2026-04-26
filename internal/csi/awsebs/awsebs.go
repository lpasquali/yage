// Package awsebs implements the AWS EBS CSI driver registration
// for yage's CSI add-on registry (internal/csi). The upstream chart
// is aws-ebs-csi-driver published by kubernetes-sigs; auth is via
// IRSA (IAM Roles for Service Accounts) so no Kubernetes Secret is
// required — the controller pod's ServiceAccount carries an
// "eks.amazonaws.com/role-arn" annotation and EKS / EKS-on-EC2 with
// the IAM OIDC provider takes care of credential exchange.
//
// Pinned chart version v2.32.0 — a stable release as of late 2024
// that supports K8s 1.27+. Updating the pin is a follow-up commit
// (chart bumps land alongside `helm-up` runs, not silently).
package awsebs

import (
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csi"
	"github.com/lpasquali/yage/internal/ui/plan"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "aws-ebs" }
func (driver) K8sCSIDriverName() string { return "ebs.csi.aws.com" }
func (driver) Defaults() []string       { return []string{"aws"} }

// HelmChart pins to v2.32.0 of aws-ebs-csi-driver from the
// kubernetes-sigs OCI-style HTTPS chart repo.
func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://kubernetes-sigs.github.io/aws-ebs-csi-driver",
		"aws-ebs-csi-driver",
		"v2.32.0",
		nil
}

// RenderValues emits a minimal Helm values document. The IRSA role
// ARN is left empty — operators set it via the AWS-side IAM role
// they pre-create (EKS module / OpenTofu / hand-rolled). The
// orchestrator can later patch the rendered values in-process; for
// today, an empty annotation is the safest default that still
// produces a valid YAML document.
//
// Storage class:
//   - name "ebs-gp3" — the gp3 volume type is the modern AWS
//     default (faster than gp2, identical price, configurable IOPS
//     up to 16k). yage labels it default so PVCs without an
//     explicit storageClassName land here.
//   - volumeBindingMode WaitForFirstConsumer — required for multi-
//     AZ clusters so the volume is created in the same AZ as the
//     pod's node, not the controller's AZ.
func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/awsebs.\n")
	b.WriteString("# IRSA: operator pre-creates the IAM role and sets\n")
	b.WriteString("# controller.serviceAccount.annotations[eks.amazonaws.com/role-arn]\n")
	b.WriteString("# either via Helm CLI override or out-of-band kubectl annotate.\n")
	b.WriteString("controller:\n")
	b.WriteString("  serviceAccount:\n")
	b.WriteString("    create: true\n")
	b.WriteString("    annotations:\n")
	b.WriteString("      eks.amazonaws.com/role-arn: \"\"\n")
	b.WriteString("storageClasses:\n")
	b.WriteString("  - name: ebs-gp3\n")
	b.WriteString("    annotations:\n")
	b.WriteString("      storageclass.kubernetes.io/is-default-class: \"true\"\n")
	b.WriteString("    parameters:\n")
	b.WriteString("      type: gp3\n")
	b.WriteString("    volumeBindingMode: WaitForFirstConsumer\n")
	b.WriteString("    reclaimPolicy: Delete\n")
	return b.String(), nil
}

// EnsureSecret returns ErrNotApplicable: AWS EBS CSI uses IRSA, not
// a Kubernetes Secret. The orchestrator treats this as a clean skip.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	return csi.ErrNotApplicable
}

func (driver) DefaultStorageClass() string { return "ebs-gp3" }

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("AWS EBS CSI")
	w.Bullet("driver: %s (chart aws-ebs-csi-driver pinned v2.32.0)", "ebs.csi.aws.com")
	w.Bullet("auth: IRSA (no Kubernetes Secret) — operator sets eks.amazonaws.com/role-arn on the controller SA")
	w.Bullet("default StorageClass: ebs-gp3 (volumeBindingMode WaitForFirstConsumer, gp3 volumes)")
	w.Bullet("region: %s", firstNonEmpty(cfg.Providers.AWS.Region, "(unset — Helm chart will read from EC2 metadata)"))
}

// firstNonEmpty is a tiny local helper — duplicated rather than
// introducing a util import that the driver doesn't otherwise need.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
