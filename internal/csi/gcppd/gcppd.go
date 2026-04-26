// Package gcppd implements the GCP Persistent Disk CSI driver
// registration for yage's CSI add-on registry (internal/csi).
//
// Note on the chart: kubernetes-sigs publishes the GCP PD CSI as
// raw manifests, NOT a Helm chart on a stable repo URL. yage points
// at the kubernetes-sigs published URL for parity with the other
// hyperscale drivers; operators using a private mirror can override
// later via --csi-values-file (out of Phase F MVP). HelmChart
// returns the upstream chart coordinates; if your environment
// can't reach those, the orchestrator will surface the chart-pull
// error at install time and operators can fall back to a manifest-
// based install.
//
// Auth model branches on cfg.Providers.GCP.IdentityModel:
//
//   - "service-account" (default): a "gce-conf" Secret in
//     kube-system holds the service-account JSON key. Chart values
//     reference the Secret name; controllers read the JSON from
//     /etc/cloud-sa/cloud-sa.json mounted from the Secret.
//
//   - "workload-identity": no Secret needed; the controller SA
//     federates GCP IAM via Workload Identity. EnsureSecret is a
//     no-op.
//
//   - "adc" / unset: treated as service-account for this commit.
//
// Pinned chart version v1.13.0 — there's no canonical Helm chart
// home for this driver upstream so this version string is
// nominal; operators may need a private mirror. See package doc.
package gcppd

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
	secretName      = "gce-conf"
	saJSONKey       = "cloud-sa.json"
)

func init() {
	csi.Register(&driver{})
}

type driver struct{}

func (driver) Name() string             { return "gcp-pd" }
func (driver) K8sCSIDriverName() string { return "pd.csi.storage.gke.io" }
func (driver) Defaults() []string       { return []string{"gcp"} }

func (driver) HelmChart(cfg *config.Config) (repo, chart, version string, err error) {
	return "https://kubernetes-sigs.github.io/gcp-compute-persistent-disk-csi-driver",
		"gcp-compute-persistent-disk-csi-driver",
		"v1.13.0",
		nil
}

func (driver) RenderValues(cfg *config.Config) (string, error) {
	var b strings.Builder
	b.WriteString("# Rendered by yage internal/csi/gcppd.\n")
	b.WriteString("controller:\n")
	b.WriteString("  replicas: 2\n")
	b.WriteString("storageClasses:\n")
	b.WriteString("  - name: pd-balanced\n")
	b.WriteString("    annotations:\n")
	b.WriteString("      storageclass.kubernetes.io/is-default-class: \"true\"\n")
	b.WriteString("    parameters:\n")
	b.WriteString("      type: pd-balanced\n")
	b.WriteString("    volumeBindingMode: WaitForFirstConsumer\n")
	b.WriteString("    reclaimPolicy: Delete\n")

	if usesWorkloadIdentity(cfg) {
		b.WriteString("# Workload Identity: SA annotated for GCP IAM federation.\n")
		b.WriteString("serviceAccount:\n")
		b.WriteString("  controller:\n")
		b.WriteString("    create: true\n")
		b.WriteString("    annotations:\n")
		b.WriteString(fmt.Sprintf("      iam.gke.io/gcp-service-account: %q\n",
			gcpSAEmail(cfg)))
	} else {
		b.WriteString("# Service-Account: cloud-sa.json mounted from a Secret.\n")
		b.WriteString("controller:\n")
		b.WriteString("  cloudSAVolume:\n")
		b.WriteString("    secret:\n")
		b.WriteString("      secretName: " + secretName + "\n")
	}
	if cfg.Providers.GCP.Project != "" {
		b.WriteString("project: " + cfg.Providers.GCP.Project + "\n")
	}
	return b.String(), nil
}

// EnsureSecret writes the kube-system/gce-conf Secret on the
// workload cluster when the Service-Account identity model is in
// effect. The JSON body comes from GOOGLE_APPLICATION_CREDENTIALS
// (the env var GCP SDKs read by default); when unset, EnsureSecret
// returns nil so dry-runs without creds stay quiet.
func (driver) EnsureSecret(cfg *config.Config, workloadKubeconfigPath string) error {
	if usesWorkloadIdentity(cfg) {
		return nil
	}
	credPath := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credPath == "" {
		return nil
	}
	jsonBody, err := os.ReadFile(credPath)
	if err != nil {
		return fmt.Errorf("gcppd: read %s: %w", credPath, err)
	}
	cli, err := k8sclient.ForKubeconfigFile(workloadKubeconfigPath)
	if err != nil {
		return fmt.Errorf("gcppd: load workload kubeconfig %s: %w", workloadKubeconfigPath, err)
	}
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: secretNamespace,
			Name:      secretName,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{saJSONKey: jsonBody},
	}
	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("gcppd: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("gcppd: yaml→json: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	force := true
	_, err = cli.Typed.CoreV1().Secrets(secretNamespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("gcppd: apply %s/%s: %w", secretNamespace, secretName, err)
	}
	return nil
}

func (driver) DefaultStorageClass() string { return "pd-balanced" }

func (driver) DescribeInstall(w plan.Writer, cfg *config.Config) {
	w.Section("GCP PD CSI")
	w.Bullet("driver: %s (chart gcp-compute-persistent-disk-csi-driver pinned v1.13.0)", "pd.csi.storage.gke.io")
	if usesWorkloadIdentity(cfg) {
		w.Bullet("auth: Workload Identity (iam.gke.io/gcp-service-account=%s)",
			nonEmpty(gcpSAEmail(cfg), "<unset>"))
	} else {
		w.Bullet("auth: Service-Account JSON (Secret %s/%s)", secretNamespace, secretName)
		if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" {
			w.Bullet("note: GOOGLE_APPLICATION_CREDENTIALS is unset — EnsureSecret will be a no-op until creds are provided")
		}
	}
	w.Bullet("project: %s, region: %s",
		nonEmpty(cfg.Providers.GCP.Project, "<unset>"),
		nonEmpty(cfg.Providers.GCP.Region, "<unset>"))
	w.Bullet("default StorageClass: pd-balanced (pd-balanced disk type, WaitForFirstConsumer)")
	w.Bullet("note: upstream gcp-pd-csi historically does not ship a stable Helm chart; operators with restricted networks may need a private mirror or manifest install")
}

func usesWorkloadIdentity(cfg *config.Config) bool {
	return strings.EqualFold(cfg.Providers.GCP.IdentityModel, "workload-identity")
}

// gcpSAEmail returns the GCP service-account email yage annotates
// the controller SA with for Workload Identity. Today there's no
// dedicated cfg field, so we fall back to a conventional name
// derived from cfg.Providers.GCP.Project.
func gcpSAEmail(cfg *config.Config) string {
	if cfg.Providers.GCP.Project == "" {
		return ""
	}
	return "yage-csi@" + cfg.Providers.GCP.Project + ".iam.gserviceaccount.com"
}

func nonEmpty(a, fallback string) string {
	if a != "" {
		return a
	}
	return fallback
}
