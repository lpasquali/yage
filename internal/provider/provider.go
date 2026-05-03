// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

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
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lpasquali/yage/internal/config"
)

// ErrNotApplicable signals a phase that doesn't apply to this
// provider — caller skips silently. Distinct from real errors so we
// can keep the orchestrator's main loop terse.
var ErrNotApplicable = errors.New("phase not applicable to this provider")

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
	// "docker" for CAPD, "openstack", …). Used as the registry key.
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

	// Inventory returns the cloud-correct picture of "what's there
	// + what's free" in a single round-trip — Total / Used /
	// Available / Notes (see Inventory + ResourceTotals). Replaces
	// the older split between Capacity() (host totals) and the
	// separate capacity.FetchExistingUsage call: those two
	// quantities were always queried together at every call site,
	// and on non-flat-pool clouds the arithmetic
	// Available = Total − Used is wrong (per-family quotas,
	// count-based limits, multi-level hierarchies don't compose
	// with flat subtraction).
	//
	// Per §13.4 #1: Proxmox and OpenStack-by-project populate
	// Inventory cleanly. AWS, GCP, Azure, Hetzner, vSphere return
	// ErrNotApplicable because their quota model can't be expressed
	// as flat ResourceTotals. The orchestrator then skips capacity
	// preflight for that provider and relies on
	// EstimateMonthlyCostUSD + DescribeWorkload as the only
	// pre-deploy gates.
	Inventory(cfg *config.Config) (*Inventory, error)

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

	// CSI flows through internal/csi as a registered Driver — see
	// internal/csi/driver.go and the per-driver packages under
	// internal/csi/<name>/. Drivers that need a Secret implement
	// Driver.EnsureSecret; those that authenticate via cloud-native
	// identity return ErrNotApplicable. The Provider interface
	// intentionally has no CSI hook — providers register their own
	// CSI driver via internal/csi instead.

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
	//
	// ctx carries the active pricing.Fetcher (per ADR 0016 §"Pricing
	// seam"). Implementations that consult internal/pricing should
	// route through pricing.FetcherFrom(ctx).Fetch(...) so deterministic
	// test harnesses can pin a frozen catalog. Calls that go through
	// the bespoke per-vendor helpers (AWSEKSControlPlaneUSDPerMonth,
	// AzureManagedDiskUSDPerGBMonth, …) continue to hit the package
	// globals; full migration of those helpers onto Fetcher is tracked
	// as follow-up.
	EstimateMonthlyCostUSD(ctx context.Context, cfg *config.Config) (CostEstimate, error)

	// PlanDescriber emits the provider-specific dry-run plan
	// sections via plan.Writer. Three hooks because the orchestrator
	// inserts other phases between them; one big DescribePlan would
	// force every provider to know the orchestrator's section
	// ordering.
	PlanDescriber

	// KindSyncer returns the provider-specific fields persisted in
	// the kind-side handoff Secret. Empty map = no state to persist.
	KindSyncer

	// Purger reverses provider-managed state outside the workload
	// cluster. Idempotent. nil return = nothing to clean up;
	// ErrNotApplicable when the provider has no cleanup concept.
	Purger

	// RolloutHooker patches provider-specific infrastructure-machine
	// objects with reconcile annotations so the CAPI controller
	// re-evaluates them during a triggered rollout. Providers that
	// have no extra infrastructure objects to nudge return nil.
	RolloutHooker

	// TemplateVars returns the provider-specific env-style values
	// substituted into the clusterctl manifest template at render
	// time. Universal vars (CLUSTER_NAME, KUBERNETES_VERSION, etc.)
	// come from the orchestrator and are NOT in this map.
	TemplateVars(cfg *config.Config) map[string]string

	// RenderMgmtManifest generates the CAPI manifest for the management
	// cluster and applies all provider-specific patches (sizing, topology
	// labels, template IDs). Returns the on-disk path of the rendered
	// manifest. clusterctlCfgPath must be non-empty and point to an
	// existing file (the same ephemeral config written by
	// SyncClusterctlConfigFile). Returns ErrNotApplicable when the
	// provider has no management-cluster bootstrap story (i.e. kind
	// remains the permanent management cluster).
	RenderMgmtManifest(cfg *config.Config, clusterctlCfgPath string) (string, error)

	// Pivoter returns the destination kubeconfig + namespaces for
	// the kind → managed-mgmt cluster move. Only providers that ship
	// a working management-cluster bootstrap (Proxmox, plus anything
	// else with a K3s template + a strategy for hosting the mgmt
	// cluster) return a real target. Everyone else returns
	// ErrNotApplicable.
	Pivoter
}

// Pivoter is composed into Provider so the pivot path can depend
// on just the pivot capability.
type Pivoter interface {
	// PivotTarget returns the destination kubeconfig path +
	// namespace list + readiness timeout for clusterctl move.
	// Returns ErrNotApplicable when this provider has no managed
	// mgmt cluster story (kind stays as the mgmt cluster).
	//
	// The KubeconfigPath field reads cfg.MgmtKubeconfigPath when
	// the orchestrator has populated it (after
	// EnsureManagementCluster). The orchestrator is responsible for
	// setting that field; the provider is stateless and only
	// packages what it sees.
	PivotTarget(cfg *config.Config) (PivotTarget, error)
}

// PivotTarget is the destination for clusterctl move.
// Zero-value + ErrNotApplicable means "no pivot target — kind
// remains the management cluster forever."
type PivotTarget struct {
	// KubeconfigPath is the local path to the destination cluster's
	// kubeconfig file. Set by the orchestrator after
	// EnsureManagementCluster() and read back via
	// cfg.MgmtKubeconfigPath.
	KubeconfigPath string
	// Namespaces is the list of CAPI namespaces clusterctl move
	// should transfer. nil = "all CAPI namespaces" (today's behavior;
	// idiomatic Go sentinel for unset).
	Namespaces []string
	// ReadyTimeout is how long to wait for the destination to
	// accept the move. Zero = orchestrator default (typically
	// 10 minutes).
	ReadyTimeout time.Duration
	// VerifySecrets is the provider-specific list of namespace/name
	// pairs that VerifyParity checks on the management cluster after
	// clusterctl move. An empty slice means the provider has no
	// provider-specific Secrets to verify (CAPI object parity is still
	// checked regardless). nil = same as empty.
	VerifySecrets []VerifySecret
}

// VerifySecret is a namespace-qualified Secret name used by
// VerifyParity to confirm that the provider's bootstrap Secrets
// arrived on the management cluster after the handoff step.
type VerifySecret struct {
	Namespace string
	Name      string
}

// BootstrapSecretRef describes one Kubernetes Secret the provider
// wants the generic kindsync layer to read and absorb. The generic
// layer fetches the Secret from the kind cluster, decodes all data
// entries, optionally restricts to KeyFilter, and calls
// AbsorbConfigYAML with the decoded map. OnAbsorbed, when non-nil,
// is called after AbsorbConfigYAML returns true so the provider can
// set additional cfg flags (e.g. "this Secret was the capmox-system
// live copy" markers). See §11.
type BootstrapSecretRef struct {
	// Namespace and Name identify the Secret on the kind cluster.
	// An empty Name is treated as "skip this ref" by the dispatcher.
	Namespace string
	Name      string
	// KeyFilter, when non-nil, restricts the keys passed to
	// AbsorbConfigYAML to only those listed here. nil = pass all
	// decoded keys. Used for admin-only Secrets whose other keys
	// must not bleed into the cfg.
	KeyFilter []string
	// OnAbsorbed, when non-nil, is called after AbsorbConfigYAML
	// returns true for this Secret. Useful for setting provider-
	// specific side-effects (e.g. cfg.Providers.Proxmox.KindCAPMOXActive).
	OnAbsorbed func(cfg *config.Config)
}

// KindSyncer is composed into Provider for callers that only need
// the kind-Secret handoff capability (e.g. the kindsync layer that
// iterates over the provider's returned map). See §11.
type KindSyncer interface {
	// KindSyncFields returns the provider's bare-key map of fields
	// to persist (forward direction: cfg → Secret). The orchestrator
	// wraps them with a "<provider>." prefix when writing to the
	// Secret so multiple providers' fields can coexist. Conventions:
	//   - lowercase snake_case keys
	//   - empty values omitted
	//   - bools stringified as "true" / "false"
	//   - sensitive fields returned same as non-sensitive (k8s
	//     encrypts at rest)
	KindSyncFields(cfg *config.Config) map[string]string

	// AbsorbConfigYAML is the reverse direction (Secret → cfg). The
	// orchestrator's kindsync layer reads the provider's section
	// of the bootstrap Secret + per-credential JSON envelopes and
	// hands the merged key/value map here; the provider populates
	// any empty cfg fields it recognizes. Returns true when at
	// least one assignment happened.
	//
	// The Proxmox impl handles both the uppercase PROXMOX_* key
	// set written by config.yaml / creds.json / csi.json /
	// admin.json envelopes and the lowercase bare-key convention
	// (matches KindSyncFields output) used by the generic
	// Secret-as-source-of-truth path.
	AbsorbConfigYAML(cfg *config.Config, kv map[string]string) bool

	// BootstrapSecrets returns the ordered list of credential
	// Secrets the provider wants the generic kindsync layer to
	// fetch and absorb on each run. The refs are processed in order;
	// the dispatcher calls AbsorbConfigYAML for each Secret that
	// exists on the kind cluster. Returns nil when the provider
	// has no credential Secrets beyond the generic config.yaml
	// snapshot (e.g. cost-only providers via MinStub).
	BootstrapSecrets(cfg *config.Config) []BootstrapSecretRef
}

// Purger is composed into Provider so cleanup paths (--purge) can
// depend on just the cleanup capability. See §11.
type Purger interface {
	// Purge reverses what EnsureIdentity / EnsureScope created plus
	// any other provider-managed state outside the workload
	// cluster. MUST be idempotent: re-running is safe; NotFound
	// errors get swallowed; other errors propagate.
	Purge(cfg *config.Config) error
}

// RolloutHooker is composed into Provider for providers that need to
// patch provider-specific infrastructure-machine objects with reconcile
// annotations so the CAPI controller re-evaluates them when a rollout
// is triggered. Providers with no extra infrastructure objects return nil.
type RolloutHooker interface {
	// RolloutMachineAnnotations patches every provider-specific
	// infrastructure Machine object (e.g. ProxmoxMachine) that belongs
	// to the named workload cluster with a reconcile annotation. Called
	// by WorkloadRolloutCAPITouchRollout after the generic
	// KubeadmControlPlane + MachineDeployment triggers are applied.
	//
	// ctxName is the kubeconfig context of the management cluster
	// (e.g. "kind-<cluster>"). ns and selector are the workload
	// cluster namespace and the CAPI label selector
	// "cluster.x-k8s.io/cluster-name=<name>". now is the RFC 3339
	// timestamp string already computed by the caller.
	RolloutMachineAnnotations(cfg *config.Config, ctxName, ns, selector, now string) error
}

// PlanDescriber is composed into Provider so callers can depend on
// just the plan-output capability when that's all they need (e.g.
// the dry-run plan code path). Per §8 / §10:
//
//   - DescribeIdentity prints the provider's identity-bootstrap
//     section ("Identity bootstrap — <provider>"). Skip when not
//     applicable to this provider.
//   - DescribeWorkload prints the workload-cluster section
//     (cluster shape, sizing, networking, CSI). Always implemented
//     by every provider — this is the headline section.
//   - DescribePivot prints the pivot section. For providers without
//     a pivot story today, call w.Skip("...") with a reason.
//   - DescribeClusterctlInit emits provider-specific bullets inside
//     the "clusterctl init on kind" section — the orchestrator has
//     already written the section header, so only w.Bullet / w.Skip
//     calls are appropriate here (no w.Section). Providers that have
//     nothing extra to say leave this as a no-op.
//
// All four return nothing: rendering errors are not actionable
// from inside a Describe* hook.
type PlanDescriber interface {
	DescribeIdentity(w PlanWriter, cfg *config.Config)
	DescribeWorkload(w PlanWriter, cfg *config.Config)
	DescribePivot(w PlanWriter, cfg *config.Config)
	DescribeClusterctlInit(w PlanWriter, cfg *config.Config)
}

// PlanWriter is provider's view of the plan-output seam. It's the
// same interface as plan.Writer in internal/ui/plan; we declare it
// here to avoid a provider→ui import (the ui package imports config
// through main; provider sits below ui in the dependency graph).
//
// internal/ui/plan.Writer satisfies PlanWriter automatically by
// having the same methods.
type PlanWriter interface {
	Section(title string)
	Bullet(format string, args ...any)
	Skip(format string, args ...any)
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
	Name           string  // "workload control-plane (3× t3.medium)"
	UnitUSDMonthly float64 // per-replica per-month cost
	Qty            int
	SubtotalUSD    float64
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

// For is a convenience: reads cfg.InfraProvider and returns the
// matching implementation. Errors when the named provider isn't
// registered or cfg.InfraProvider is empty. There is no silent
// default — main() rejects an empty InfraProvider before reaching
// the orchestrator (see docs/abstraction-plan.md §18 and
// cmd/yage/main.go), so reaching For() with name == "" indicates
// a programming error in a code path that bypassed main().
//
// When cfg.Airgapped is true and the resolved provider needs the
// internet (any cloud provider — see AirgapCompatible), For()
// returns ErrAirgapped instead of the provider so callers fail
// fast with a clear reason.
func For(cfg *config.Config) (Provider, error) {
	name := cfg.InfraProvider
	if name == "" {
		return nil, fmt.Errorf("provider.For: cfg.InfraProvider is empty (set INFRA_PROVIDER or --infra-provider)")
	}
	return AirgapAwareForName(name, cfg.Airgapped)
}
