// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

// EnsureRegistry provisions a bootstrap OCI registry VM on Proxmox via the
// yage-tofu/registry/ OpenTofu module (ADR 0009 §1, Phase H gap 1). After the
// module applies successfully, it reads the tofu outputs and sets
// cfg.ImageRegistryMirror from the registry_url output so that subsequent Job
// pods (JobRunner.tofuImageRef) and workload-cluster image pulls route through
// the internal registry.
//
// ErrNotApplicable is returned when cfg.RegistryNode is empty, so the
// orchestrator can call this function unconditionally and skip gracefully when
// the operator has not configured a registry node.
//
// When cfg.ImageRegistryMirror is already set (operator-supplied), it is
// preserved and not overwritten by the module output.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// RegistryOutputs holds the structured outputs consumed from the
// yage-tofu/registry/ module after a successful apply.
type RegistryOutputs struct {
	IP           string
	Host         string
	URL          string
	Flavor       string
	VMID         string
	TLSCertPEM   string
	CABundlePEM  string
}

// EnsureRegistry applies the yage-tofu/registry/ module via a JobRunner on the
// management cluster (cli), then reads the outputs and auto-populates
// cfg.ImageRegistryMirror when it is not already set.
//
// Prerequisites (caller's responsibility):
//   - yage-system namespace and RBAC are in place (EnsureYageSystemOnCluster).
//   - yage-repos PVC is populated with the yage-tofu checkout (EnsureRepoSync).
//   - cli is a connected client to the current management cluster.
//
// Returns ErrNotApplicable when cfg.RegistryNode == "".
// Returns an error wrapping the tofu or k8s failure on any other problem.
func EnsureRegistry(ctx context.Context, cli *k8sclient.Client, cfg *config.Config) error {
	if cfg.RegistryNode == "" {
		return ErrNotApplicable
	}

	runner := &JobRunner{cfg: cfg, client: cli}

	vars := registryVars(cfg)

	logx.Log("EnsureRegistry: applying yage-tofu/registry/ module (node=%s, hostname=%s) ...",
		cfg.RegistryNode, cfg.RegistryHostname)
	if err := runner.Apply(ctx, "registry", vars); err != nil {
		return fmt.Errorf("EnsureRegistry: tofu apply: %w", err)
	}

	outputs, err := runner.Output(ctx, "registry")
	if err != nil {
		return fmt.Errorf("EnsureRegistry: tofu output: %w", err)
	}

	reg, err := parseRegistryOutputs(outputs)
	if err != nil {
		return fmt.Errorf("EnsureRegistry: parse outputs: %w", err)
	}

	logx.Log("EnsureRegistry: registry VM provisioned (ip=%s host=%s url=%s flavor=%s vmid=%s)",
		reg.IP, reg.Host, reg.URL, reg.Flavor, reg.VMID)

	if cfg.ImageRegistryMirror == "" && reg.URL != "" {
		cfg.ImageRegistryMirror = reg.URL
		logx.Log("EnsureRegistry: cfg.ImageRegistryMirror set to %s", cfg.ImageRegistryMirror)
	} else if cfg.ImageRegistryMirror != "" {
		logx.Log("EnsureRegistry: cfg.ImageRegistryMirror already set to %s; not overwriting with module output %s",
			cfg.ImageRegistryMirror, reg.URL)
	}

	return nil
}

// DestroyRegistry tears down the registry VM by running tofu destroy against
// the yage-tofu/registry/ module. It is called by PurgeGeneratedArtifacts
// before the kind cluster is deleted so the kubernetes-backend state Secret
// is still reachable.
//
// Returns ErrNotApplicable when cfg.RegistryNode == "" (no registry was provisioned).
func DestroyRegistry(ctx context.Context, cli *k8sclient.Client, cfg *config.Config) error {
	if cfg.RegistryNode == "" {
		return ErrNotApplicable
	}
	runner := &JobRunner{cfg: cfg, client: cli}
	logx.Log("DestroyRegistry: running tofu destroy on registry module ...")
	if err := runner.Destroy(ctx, "registry"); err != nil {
		return fmt.Errorf("DestroyRegistry: tofu destroy: %w", err)
	}
	logx.Log("DestroyRegistry: registry VM destroyed.")
	return nil
}

// registryVars builds the var map passed to the registry module. Required
// Proxmox credentials are taken from cfg.Providers.Proxmox so callers do not
// need to duplicate them.
func registryVars(cfg *config.Config) map[string]string {
	vars := map[string]string{
		"proxmox_url":      cfg.Providers.Proxmox.URL,
		"proxmox_username": cfg.Providers.Proxmox.AdminUsername,
		"proxmox_password": cfg.Providers.Proxmox.AdminToken,
		"cluster_name":     cfg.WorkloadClusterName,
		"registry_node":    cfg.RegistryNode,
	}

	// Optional fields — only set when non-empty so module defaults apply.
	if cfg.Providers.Proxmox.AdminInsecure != "" {
		vars["proxmox_insecure"] = cfg.Providers.Proxmox.AdminInsecure
	}
	if cfg.RegistryNetwork != "" {
		vars["registry_network"] = cfg.RegistryNetwork
	}
	if cfg.RegistryStorage != "" {
		vars["registry_storage"] = cfg.RegistryStorage
	}
	if cfg.RegistryTemplateID != "" {
		vars["registry_template_id"] = cfg.RegistryTemplateID
	}
	if cfg.RegistryHostname != "" {
		vars["registry_hostname"] = cfg.RegistryHostname
	}
	if cfg.RegistryFlavor != "" {
		vars["registry_flavor"] = cfg.RegistryFlavor
	}
	if cfg.RegistryTLSCertPEM != "" {
		vars["registry_tls_cert_pem"] = cfg.RegistryTLSCertPEM
	}
	if cfg.RegistryTLSKeyPEM != "" {
		vars["registry_tls_key_pem"] = cfg.RegistryTLSKeyPEM
	}
	if cfg.RegistryCABundlePEM != "" {
		vars["registry_ca_bundle_pem"] = cfg.RegistryCABundlePEM
	}
	if cfg.RegistryAdminPassword != "" {
		vars["registry_admin_password"] = cfg.RegistryAdminPassword
	}

	// Map RegistryVMFlavor → discrete sizing inputs using the canonical
	// flavor table. Unknown flavors are silently skipped so the module
	// defaults (2 cores / 4096 MiB / 100 GiB) apply.
	cores, memMB, diskGB := resolveVMFlavor(cfg.RegistryVMFlavor)
	if cores != "" {
		vars["registry_vm_cores"] = cores
	}
	if memMB != "" {
		vars["registry_vm_memory_mb"] = memMB
	}
	if diskGB != "" {
		vars["registry_vm_disk_gb"] = diskGB
	}

	return vars
}

// resolveVMFlavor maps a human-readable flavor name to (cores, memMB, diskGB).
// Returns ("", "", "") for unknown flavors (module defaults apply).
func resolveVMFlavor(flavor string) (cores, memMB, diskGB string) {
	switch strings.ToLower(flavor) {
	case "small":
		return "2", "4096", "50"
	case "medium", "default", "":
		return "", "", "" // accept module defaults
	case "large":
		return "4", "8192", "200"
	case "xlarge":
		return "8", "16384", "500"
	default:
		return "", "", ""
	}
}

// parseRegistryOutputs decodes the structured tofu output map into RegistryOutputs.
// The map values follow encoding/json.Unmarshal conventions: string scalars are
// wrapped as {"value": <string>, "type": "string"} in some tofu versions; this
// function handles both the wrapped and bare-string forms.
func parseRegistryOutputs(raw map[string]any) (RegistryOutputs, error) {
	get := func(key string) string {
		v, ok := raw[key]
		if !ok {
			return ""
		}
		// Bare string (tofu -json sometimes omits the wrapper for simple types).
		if s, ok := v.(string); ok {
			return s
		}
		// Wrapped: {"value": "...", "type": "string", "sensitive": false}
		if m, ok := v.(map[string]any); ok {
			if s, ok := m["value"].(string); ok {
				return s
			}
		}
		return fmt.Sprintf("%v", v)
	}

	var out RegistryOutputs
	out.IP = get("registry_ip")
	out.Host = get("registry_host")
	out.URL = strings.TrimRight(get("registry_url"), "/")
	out.Flavor = get("registry_flavor")
	out.VMID = get("vm_id")
	out.TLSCertPEM = get("registry_tls_cert_pem")
	out.CABundlePEM = get("registry_ca_bundle_pem")

	if out.URL == "" && out.IP == "" && out.Host == "" {
		return out, errors.New("registry tofu outputs missing expected fields (registry_url, registry_ip, registry_host all empty)")
	}
	return out, nil
}
