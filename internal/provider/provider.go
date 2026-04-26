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
	// Per the Phase A spec (§13.4 #1): Proxmox and OpenStack-by-
	// project populate Inventory cleanly. AWS, GCP, Azure, Hetzner,
	// vSphere return ErrNotApplicable because their quota model
	// can't be expressed as flat ResourceTotals. The orchestrator
	// then skips capacity preflight for that provider and relies on
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

	// PlanDescriber emits the provider-specific dry-run plan
	// sections via plan.Writer (Phase B / §8). Three hooks because
	// the orchestrator inserts other phases between them; one big
	// DescribePlan would force every provider to know the
	// orchestrator's section ordering.
	PlanDescriber

	// KindSyncer returns the provider-specific fields persisted in
	// the kind-side handoff Secret (Phase D / §11). Empty map = no
	// state to persist.
	KindSyncer

	// Purger reverses provider-managed state outside the workload
	// cluster (Phase D / §11). Idempotent. nil return = nothing to
	// clean up; ErrNotApplicable when the provider has no cleanup
	// concept.
	Purger

	// TemplateVars returns the provider-specific env-style values
	// substituted into the clusterctl manifest template at render
	// time (Phase D / §11). Universal vars (CLUSTER_NAME,
	// KUBERNETES_VERSION, etc.) come from the orchestrator and are
	// NOT in this map.
	TemplateVars(cfg *config.Config) map[string]string

	// Pivoter returns the destination kubeconfig + namespaces for
	// the kind → managed-mgmt cluster move (Phase E / §12). Only
	// providers that ship a working management-cluster bootstrap
	// (today: Proxmox; future: anything with a K3s template + a
	// strategy for hosting the mgmt cluster) return a real target.
	// Everyone else returns ErrNotApplicable.
	Pivoter
}

// Pivoter is composed into Provider so the pivot path can depend
// on just the pivot capability. See §12.
type Pivoter interface {
	// PivotTarget returns the destination kubeconfig path +
	// namespace list + readiness timeout for clusterctl move.
	// Returns ErrNotApplicable when this provider has no managed
	// mgmt cluster story (kind stays as the mgmt cluster).
	//
	// The KubeconfigPath field reads cfg.MgmtKubeconfigPath when
	// the orchestrator has populated it (after
	// EnsureManagementCluster). Per §12 + §13.4, the orchestrator
	// is responsible for setting that field; the provider is
	// stateless and only packages what it sees.
	PivotTarget(cfg *config.Config) (PivotTarget, error)
}

// PivotTarget is the destination for clusterctl move (Phase E / §12).
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
}

// KindSyncer is composed into Provider for callers that only need
// the kind-Secret handoff capability (e.g. the kindsync rewrite
// that iterates over the provider's returned map). See §11.
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
	// Today's Proxmox impl handles the legacy PROXMOX_* uppercase
	// key set written by historical config.yaml / creds.json /
	// csi.json / admin.json envelopes. Providers added since
	// Phase D follow the new lowercase bare-key convention
	// (matches KindSyncFields output) once the orchestrator's
	// generic Secret-as-source-of-truth path lands in §16
	// commit 2.
	AbsorbConfigYAML(cfg *config.Config, kv map[string]string) bool
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
//
// All three return nothing: rendering errors are not actionable
// from inside a Describe* hook.
type PlanDescriber interface {
	DescribeIdentity(w PlanWriter, cfg *config.Config)
	DescribeWorkload(w PlanWriter, cfg *config.Config)
	DescribePivot(w PlanWriter, cfg *config.Config)
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
