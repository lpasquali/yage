// Package csix ports Proxmox CSI config helpers: loading from the
// local YAML, and pushing the config Secret into the workload cluster.
//
// Bash source map:
//   - load_csi_vars_from_config                              ~L5822-5843
//   - apply_proxmox_csi_config_secret_to_workload_cluster    ~L6096-6140
package csix

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/lpasquali/bootstrap-capi/internal/config"
	"github.com/lpasquali/bootstrap-capi/internal/logx"
	"github.com/lpasquali/bootstrap-capi/internal/shell"
)

// LoadVarsFromConfig ports load_csi_vars_from_config. Fills empty
// cfg.ProxmoxCSI{URL,TokenID,TokenSecret} and cfg.ProxmoxRegion from the
// on-disk PROXMOX_CSI_CONFIG YAML when that file exists.
func LoadVarsFromConfig(cfg *config.Config) {
	if cfg.ProxmoxCSIConfig == "" {
		return
	}
	raw, err := os.ReadFile(cfg.ProxmoxCSIConfig)
	if err != nil {
		return
	}
	lines := strings.Split(string(raw), "\n")
	find := func(key string) string {
		pat := regexp.MustCompile(`^[^A-Za-z_]*` + regexp.QuoteMeta(key) + `:`)
		strip := regexp.MustCompile(`^[^:]*:\s*"?([^"]*)"?\s*$`)
		for _, ln := range lines {
			if !pat.MatchString(ln) {
				continue
			}
			m := strip.FindStringSubmatch(ln)
			if m == nil {
				return ""
			}
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	if cfg.ProxmoxCSIURL == "" {
		cfg.ProxmoxCSIURL = find("url")
	}
	if cfg.ProxmoxCSITokenID == "" {
		cfg.ProxmoxCSITokenID = find("token_id")
	}
	if cfg.ProxmoxCSITokenSecret == "" {
		cfg.ProxmoxCSITokenSecret = find("token_secret")
	}
	if cfg.ProxmoxRegion == "" {
		cfg.ProxmoxRegion = find("region")
	}
}

// ApplyConfigSecretToWorkload ports
// apply_proxmox_csi_config_secret_to_workload_cluster
// (L6096-L6140). Pushes a Secret named <cluster>-proxmox-csi-config into
// cfg.ProxmoxCSINamespace on the workload, and mirrors the same content
// under the short name proxmox-csi-config.
func ApplyConfigSecretToWorkload(cfg *config.Config, writeWorkloadKubeconfig func() (string, error)) {
	wk, err := writeWorkloadKubeconfig()
	if err != nil || wk == "" {
		logx.Die("Cannot read workload kubeconfig to apply Proxmox CSI config Secret.")
	}
	defer os.Remove(wk)

	nsDoc := fmt.Sprintf(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":%q}}`, cfg.ProxmoxCSINamespace)
	if err := shell.Pipe(nsDoc, "kubectl", "--kubeconfig", wk, "apply", "-f", "-"); err != nil {
		logx.Die("Failed to ensure namespace %s on the workload.", cfg.ProxmoxCSINamespace)
	}

	cfgYAML := fmt.Sprintf(`features:
  provider: %s
clusters:
  - url: "%s"
    insecure: %s
    token_id: "%s"
    token_secret: "%s"
    region: "%s"
`,
		cfg.ProxmoxCSIConfigProvider,
		cfg.ProxmoxCSIURL,
		cfg.ProxmoxCSIInsecure,
		cfg.ProxmoxCSITokenID,
		cfg.ProxmoxCSITokenSecret,
		cfg.ProxmoxRegion,
	)

	secretName := cfg.WorkloadClusterName + "-proxmox-csi-config"
	if err := applyConfigSecret(wk, cfg.ProxmoxCSINamespace, secretName, cfgYAML); err != nil {
		logx.Die("Failed to apply Proxmox CSI config Secret on workload cluster.")
	}
	// Mirror under the short name used by workload-app-of-apps default path.
	if secretName != "proxmox-csi-config" {
		if err := applyConfigSecret(wk, cfg.ProxmoxCSINamespace, "proxmox-csi-config", cfgYAML); err != nil {
			logx.Die("Failed to apply proxmox-csi-config alias Secret on workload cluster.")
		}
	}
	logx.Log("Applied %s (and proxmox-csi-config when names differ) — Proxmox API credentials in %s; Argo Application will not embed them.",
		secretName, cfg.ProxmoxCSINamespace)
}

// applyConfigSecret materializes a generic Secret with a config.yaml
// key via `kubectl create ... --dry-run=client -o yaml | kubectl apply`.
func applyConfigSecret(kubeconfig, namespace, name, body string) error {
	f, err := os.CreateTemp("", "csi-cfg-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return err
	}
	f.Close()
	out, _, err := shell.Capture("kubectl", "--kubeconfig", kubeconfig,
		"-n", namespace,
		"create", "secret", "generic", name,
		"--from-file=config.yaml="+f.Name(),
		"--dry-run=client", "-o", "yaml")
	if err != nil || out == "" {
		return fmt.Errorf("dry-run failed: %v", err)
	}
	return shell.Pipe(out, "kubectl", "--kubeconfig", kubeconfig, "apply", "-f", "-")
}
