package azuredisk

import (
	"strings"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestDriverConstants(t *testing.T) {
	d := driver{}
	if got, want := d.Name(), "azure-disk"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := d.K8sCSIDriverName(), "disk.csi.azure.com"; got != want {
		t.Errorf("K8sCSIDriverName() = %q, want %q", got, want)
	}
	if got, want := d.DefaultStorageClass(), "azuredisk-standard-ssd"; got != want {
		t.Errorf("DefaultStorageClass() = %q, want %q", got, want)
	}
	defs := d.Defaults()
	if len(defs) != 1 || defs[0] != "azure" {
		t.Errorf("Defaults() = %v, want [azure]", defs)
	}
}

func TestRenderValuesIdentityBranch(t *testing.T) {
	d := driver{}

	cfg := &config.Config{}
	cfg.Providers.Azure.IdentityModel = "service-principal"
	out, err := d.RenderValues(cfg)
	if err != nil {
		t.Fatalf("RenderValues SP err: %v", err)
	}
	if !strings.Contains(out, "cloudConfigSecretName: azure-cloud-config") {
		t.Errorf("SP path missing cloudConfigSecretName: %s", out)
	}

	cfg.Providers.Azure.IdentityModel = "workload-identity"
	cfg.Providers.Azure.ClientID = "00000000-0000-0000-0000-000000000000"
	out, err = d.RenderValues(cfg)
	if err != nil {
		t.Fatalf("RenderValues WI err: %v", err)
	}
	if !strings.Contains(out, "azure.workload.identity/client-id") {
		t.Errorf("WI path missing client-id annotation: %s", out)
	}
}

func TestEnsureSecretWorkloadIdentityNoOp(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	cfg.Providers.Azure.IdentityModel = "workload-identity"
	if err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig"); err != nil {
		t.Errorf("EnsureSecret on workload-identity should be no-op, got %v", err)
	}
}

func TestEnsureSecretEmptyConfigNoOp(t *testing.T) {
	d := driver{}
	cfg := &config.Config{}
	// IdentityModel unset → treated as service-principal, but with
	// no creds populated EnsureSecret returns nil silently.
	if err := d.EnsureSecret(cfg, "/nonexistent/kubeconfig"); err != nil {
		t.Errorf("EnsureSecret with empty Azure cfg should be no-op, got %v", err)
	}
}
