// Package provider is yage's plugin point for CAPI
// infrastructure providers. The orchestrator drives the
// CAPI-standard flow (kind boots → clusterctl init → workload Cluster
// → CAAPH → Argo CD → optional pivot); a Provider implementation
// supplies the parts that differ per cloud:
//
//   - Pre-flight identity bootstrap (Proxmox: OpenTofu + BPG; AWS:
//     IAM role + access keys; vSphere: typically a pre-existing
//     service account → no-op).
//   - Capacity query (Proxmox: /api2/json/cluster/resources; AWS:
//     EC2 quotas; vSphere: govmomi inventory; CAPD: docker info).
//   - Group / pool creation (Proxmox pool, vSphere folder, AWS IAM
//     group, OpenStack project tag, …).
//   - clusterctl init flags (--infrastructure <name>, optional
//     --core-provider / --bootstrap / --control-plane overrides).
//   - K3s template body (each provider has its own MachineTemplate
//     kind: ProxmoxMachineTemplate, AWSMachineTemplate, …).
//   - Manifest patches that depend on the provider's MachineTemplate
//     shape (e.g. sizing keys, topology labels, sourceNode field).
//   - CSI credentials Secret on the workload, when the provider has
//     a CSI integration we ship.
//
// Implementations register via init() so adding a new provider is a
// self-contained commit: drop a new package under internal/provider/
// that imports this one and calls Register(name, factory).
package provider

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/lpasquali/yage/internal/config"
)

// ErrNotApplicable signals a phase that doesn't apply to this
// provider — caller skips silently. Distinct from real errors so we
// can keep the orchestrator's main loop terse.
var ErrNotApplicable = errors.New("phase not applicable to this provider")

// HostCapacity is the same shape as internal/capacity.HostCapacity;
// duplicated here to avoid an import cycle. The capacity package
// converts between the two when surfacing budget reports.
type HostCapacity struct {
	Nodes        []string
	CPUCores     int
	MemoryMiB    int64
	StorageGB    int64
	StorageBy    map[string]int64
}

// Role is the per-machine-type discriminator that providers use when
// rendering K3s templates and applying patches. Mirrors the strings
// already in patches.go ("control-plane", "worker") so the existing
// patch logic can stay unchanged.
type Role string

const (
	RoleControlPlane Role = "control-plane"
	RoleWorker       Role = "worker"
)

// Provider is the interface every CAPI infrastructure provider
// implementation must satisfy. Every method takes the same *Config
// and returns provider-specific data; the orchestrator plumbs the
// rest. ErrNotApplicable is a valid return for any phase a provider
// doesn't participate in.
type Provider interface {
	// Name is a stable internal id ("proxmox", "aws", "vsphere",
	// "capd", "openstack", …). Used as the registry key.
	Name() string

	// InfraProviderName is the value passed to clusterctl as
	// --infrastructure <name>. For most providers this matches Name();
	// the indirection lets a single binary support clusterctl-side
	// aliases (e.g. "capx" vs "nutanix") without renaming the
	// internal id.
	InfraProviderName() string

	// EnsureIdentity runs any pre-clusterctl identity bootstrap
	// (creating CAPI / CSI users + tokens, IAM roles, service
	// accounts). Idempotent. Return ErrNotApplicable when the
	// provider has nothing to do here.
	EnsureIdentity(cfg *config.Config) error

	// Capacity queries the underlying cloud / hypervisor for the
	// CPU / memory / storage available to yage (filtered
	// by AllowedNodes / region / tag — provider-defined). Used by
	// the capacity preflight + dry-run plan. May return
	// ErrNotApplicable when the provider has no single capacity
	// endpoint we can hit; the orchestrator falls back to "skip
	// preflight, trust the user".
	Capacity(cfg *config.Config) (*HostCapacity, error)

	// EnsureGroup creates an organizational grouping object on the
	// provider — Proxmox pool, vSphere folder, AWS IAM group,
	// OpenStack project — keyed by name. Idempotent. Skipped when
	// name is empty. Return ErrNotApplicable when the provider has
	// no equivalent.
	EnsureGroup(cfg *config.Config, name string) error

	// ClusterctlInitArgs returns extra flags to append to
	// `clusterctl init`. Always includes "--infrastructure <X>";
	// providers can also set core / bootstrap / control-plane
	// overrides.
	ClusterctlInitArgs(cfg *config.Config) []string

	// K3sTemplate returns the embedded K3s manifest template (with
	// ${VAR} placeholders) appropriate to this provider's
	// MachineTemplate kind. Filled by the renderer in
	// internal/capimanifest/k3s.go (which builds the value map).
	// Pass mgmt=true to render the management-cluster variant.
	K3sTemplate(cfg *config.Config, mgmt bool) (string, error)

	// PatchManifest runs provider-specific post-generate patches on
	// the on-disk manifest (sizing, topology labels, anything the
	// provider's MachineTemplate needs that clusterctl generate
	// doesn't emit by default).
	PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error

	// EnsureCSISecret pushes the CSI credentials Secret to the
	// workload kubeconfig when the provider has a CSI integration
	// we ship. Return ErrNotApplicable when the provider's CSI is
	// install-as-helm-only (or doesn't exist).
	EnsureCSISecret(cfg *config.Config, workloadKubeconfigPath string) error

	// EstimateMonthlyCostUSD returns a monthly billing estimate in
	// USD for the planned cluster. Implementations fetch every price
	// LIVE from the vendor's FinOps / billing API via internal/pricing
	// — there are no hardcoded money numbers. Tier-based component
	// counts (NAT GW, LB, egress GB, etc.) describe shape and stay
	// constant across price changes. Return ErrNotApplicable when the
	// provider is self-hosted (Proxmox) or pricing is too variable
	// (private OpenStack, on-prem vSphere). Wrap ErrNotApplicable
	// when a live API is unreachable so callers can surface "estimate
	// unavailable" instead of fabricating a number.
	EstimateMonthlyCostUSD(cfg *config.Config) (CostEstimate, error)
}

// CostEstimate is the breakdown returned by EstimateMonthlyCostUSD.
type CostEstimate struct {
	TotalUSDMonthly float64
	Items           []CostItem
	// Note is a short caveat the orchestrator surfaces alongside
	// the total ("on-demand us-east-1 prices, gp3 EBS, no spot").
	Note string
}

// CostItem is one line in the cost breakdown.
type CostItem struct {
	Name            string  // "workload control-plane (3× t3.medium)"
	UnitUSDMonthly  float64 // per-replica per-month cost
	Qty             int
	SubtotalUSD     float64
}

// --- registry ---

var (
	mu        sync.RWMutex
	providers = map[string]func() Provider{}
)

// Register adds a provider factory under name. Implementations call
// this from init() so the orchestrator picks them up by importing
// the package (and yage imports every provider package
// it ships). Panics on duplicate registration — that's a programmer
// error, fail at start-up not at runtime.
func Register(name string, factory func() Provider) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := providers[name]; ok {
		panic("provider: duplicate registration for " + name)
	}
	providers[name] = factory
}

// Get returns a fresh Provider instance by name, or an error
// listing the registered alternatives. Returned implementations are
// stateless; allocating a fresh one per call is cheap.
func Get(name string) (Provider, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not registered (have: %v)", name, registered())
	}
	return f(), nil
}

// Registered lists the names of every registered provider, sorted.
// Used by the dry-run plan and `--help` to surface what's available
// without re-registering.
func Registered() []string {
	mu.RLock()
	defer mu.RUnlock()
	return registered()
}

func registered() []string {
	out := make([]string, 0, len(providers))
	for k := range providers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// For is a convenience: reads cfg.InfraProvider, falls back to
// "proxmox" when empty (current default), and returns the matching
// implementation. Errors when the named provider isn't registered.
func For(cfg *config.Config) (Provider, error) {
	name := cfg.InfraProvider
	if name == "" {
		name = "proxmox"
	}
	return Get(name)
}
