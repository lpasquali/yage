package gcppd

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestDriverConstants(t *testing.T) {
	d := driver{}
	if got, want := d.Name(), "gcp-pd"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := d.K8sCSIDriverName(), "pd.csi.storage.gke.io"; got != want {
		t.Errorf("K8sCSIDriverName() = %q, want %q", got, want)
	}
	if got, want := d.DefaultStorageClass(), "pd-balanced"; got != want {
		t.Errorf("DefaultStorageClass() = %q, want %q", got, want)
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "gcp" {
		t.Errorf("Defaults() = %v, want [gcp]", defs)
	}
}

func TestRenderValuesProjectInjection(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	cfg.Providers.GCP.Project = "my-test-project"
	out, err := d.RenderValues(cfg)
	if err != nil {
		t.Fatalf("RenderValues err: %v", err)
	}
	if !strings.Contains(out, "project: my-test-project") {
		t.Errorf("missing project line: %s", out)
	}
	if !strings.Contains(out, "pd-balanced") {
		t.Errorf("missing pd-balanced storage class: %s", out)
	}
}

func TestRenderValuesWorkloadIdentity(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	cfg.Providers.GCP.Project = "p"
	cfg.Providers.GCP.IdentityModel = "workload-identity"
	out, err := d.RenderValues(cfg)
	if err != nil {
		t.Fatalf("RenderValues err: %v", err)
	}
	if !strings.Contains(out, "iam.gke.io/gcp-service-account") {
		t.Errorf("WI path missing iam.gke.io/gcp-service-account: %s", out)
	}
}

func TestEnsureSecretWorkloadIdentityNoOp(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	cfg.Providers.GCP.IdentityModel = "workload-identity"
	if err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig"); err != nil {
		t.Errorf("EnsureSecret on workload-identity should be no-op, got %v", err)
	}
}
