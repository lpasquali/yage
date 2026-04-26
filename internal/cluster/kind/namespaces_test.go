package kind

import (
	"reflect"
	"testing"

	"github.com/lpasquali/yage/internal/config"
)

func TestBackupNamespaces(t *testing.T) {
	t.Run("explicit override, comma+space+dup", func(t *testing.T) {
		cfg := &config.Config{
			BootstrapKindBackupNamespaces: "yage-system, default default capi-system",
		}
		got := BackupNamespaces(cfg)
		want := []string{"capi-system", "default", "yage-system"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v want %v", got, want)
		}
	})
	t.Run("default: union of bootstrap + workload namespace", func(t *testing.T) {
		cfg := &config.Config{
			Providers:                config.Providers{Proxmox: config.ProxmoxConfig{BootstrapSecretNamespace: "yage-system"}},
			WorkloadClusterNamespace: "default",
		}
		got := BackupNamespaces(cfg)
		want := []string{"default", "yage-system"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v want %v", got, want)
		}
	})
	t.Run("default: one of the two empty", func(t *testing.T) {
		cfg := &config.Config{Providers: config.Providers{Proxmox: config.ProxmoxConfig{BootstrapSecretNamespace: "only"}}}
		got := BackupNamespaces(cfg)
		want := []string{"only"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v want %v", got, want)
		}
	})
	t.Run("both defaults empty", func(t *testing.T) {
		got := BackupNamespaces(&config.Config{})
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", got)
		}
	})
}
