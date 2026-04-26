// Package proxmox is the yage Provider implementation for
// the Cluster API Proxmox VE infrastructure provider (CAPMOX).
//
// This package is a THIN WRAPPER over the existing Proxmox-specific
// helpers (internal/proxmox, internal/opentofux, internal/capacity,
// internal/csix, internal/capimanifest). The plugin foundation in
// internal/provider lets future code dispatch through the Provider
// interface; until every call site in internal/bootstrap is moved,
// the existing direct-call paths in bootstrap.Run() continue to work
// unchanged. The two coexist by design: this package adds the
// indirection point, the extraction of bootstrap.Run() onto it is a
// follow-up.
package proxmox

import (
	"github.com/lpasquali/yage/internal/capimanifest"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/csix"
	"github.com/lpasquali/yage/internal/opentofux"
	"github.com/lpasquali/yage/internal/provider"
	pveapi "github.com/lpasquali/yage/internal/proxmox"
)

func init() {
	provider.Register("proxmox", func() provider.Provider { return &Provider{} })
}

// Provider implements provider.Provider for CAPMOX.
type Provider struct{}

func (p *Provider) Name() string              { return "proxmox" }
func (p *Provider) InfraProviderName() string { return "proxmox" }

// EnsureIdentity ports the Proxmox identity bootstrap: OpenTofu
// applies the BPG provider templates that create the CAPI + CSI
// users + tokens + ACL bindings on the PVE cluster. Idempotent —
// `tofu apply` is a no-op when the state matches.
func (p *Provider) EnsureIdentity(cfg *config.Config) error {
	return opentofux.ApplyIdentity(cfg)
}

// Capacity queries `/api2/json/cluster/resources` (filtered by
// AllowedNodes) and returns the aggregated CPU + memory + storage.
// The underlying queries live in inventory.go alongside the
// Phase A.3 Inventory() implementation that supersedes this method.
func (p *Provider) Capacity(cfg *config.Config) (*provider.HostCapacity, error) {
	hc, err := fetchHostCapacity(cfg)
	if err != nil {
		return nil, err
	}
	return &provider.HostCapacity{
		Nodes:     hc.Nodes,
		CPUCores:  hc.CPUCores,
		MemoryMiB: hc.MemoryMiB,
		StorageGB: hc.StorageGB,
		StorageBy: hc.StorageBy,
	}, nil
}

// EnsureGroup creates / verifies a Proxmox VE pool. CAPMOX places
// VMs in the named pool (organizational + ACL only — pools don't
// enforce CPU/memory quotas).
func (p *Provider) EnsureGroup(cfg *config.Config, name string) error {
	return pveapi.EnsurePool(cfg, name)
}

// ClusterctlInitArgs returns "--infrastructure proxmox". Bootstrap
// (kubeadm vs k3s) is added by the orchestrator from cfg.BootstrapMode.
func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
	return []string{"--infrastructure", "proxmox"}
}

// K3sTemplate returns the embedded K3s flavor manifest (with
// ${VAR} placeholders) — defined under internal/capimanifest where
// it's `//go:embed`'d alongside the kubeadm path. Future provider
// implementations ship their own MachineTemplate-kind-specific
// template under their own package (see internal/provider/capd for
// the inline pattern).
func (p *Provider) K3sTemplate(cfg *config.Config, mgmt bool) (string, error) {
	return capimanifest.K3sTemplateText(), nil
}

// PatchManifest runs the four post-generate patches on the rendered
// manifest: role/resource overrides, CSI topology labels, kubeadm
// skip-kube-proxy (no-op for K3s), and ProxmoxMachineTemplate spec
// revisions for templated bumps. The mgmt branch routes to
// pivot.applyMgmtPatches (kept in internal/pivot to avoid a cycle).
func (p *Provider) PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error {
	if mgmt {
		// The mgmt patches live in internal/pivot/manifest.go; that
		// package depends on this one for its provider lookup, so we
		// can't import it here without a cycle. The orchestrator in
		// internal/bootstrap calls the mgmt patches directly when
		// the pivot phase runs; PatchManifest(mgmt=true) is currently
		// a no-op for that reason. Future refactor: move the
		// patches into a leaf package that both provider/proxmox
		// and pivot import.
		return nil
	}
	// Workload patches: ApplyRoleResourceOverrides covers the four
	// patches the kubeadm path currently runs in sequence.
	return capimanifest.ApplyRoleResourceOverrides(cfg)
}

// EnsureCSISecret pushes the Proxmox CSI credentials Secret to the
// workload cluster. Caller supplies the workload kubeconfig path;
// this provider's csix package handles the Secret apply +
// cluster-name aliasing (mirrors under <cluster>-proxmox-csi-config
// and the short proxmox-csi-config name).
func (p *Provider) EnsureCSISecret(cfg *config.Config, workloadKubeconfigPath string) error {
	csix.ApplyConfigSecretToWorkload(cfg, func() (string, error) { return workloadKubeconfigPath, nil })
	return nil
}

// EstimateMonthlyCostUSD — Proxmox is self-hosted, so there's no
// vendor pricing API. The operator opts into a TCO estimate by
// passing --hardware-cost-usd / --hardware-watts / --hardware-kwh-
// rate-usd / --hardware-support-usd-month; without those, returns
// ErrNotApplicable and the orchestrator surfaces "estimate
// unavailable" rather than fabricate.
func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
	return provider.TCOEstimate(cfg, "proxmox")
}
