# Provider abstraction — analysis + plan

bootstrap-capi was originally a Proxmox-only Go port of a bash
script. The pluggable `provider.Provider` interface (12 providers
registered today) covers the *peripheral* CAPI bits — clusterctl
init args, K3s template, identity bootstrap, CSI Secret, cost
estimator. The *core orchestration* still hardcodes Proxmox in
several places. This document maps the bindings and proposes a
phased extraction.

## 1. Where Proxmox is currently bound

Numbers below are reference counts as of HEAD. They're indicative,
not authoritative — the analysis is what matters.

### 1a. Hard bindings outside the proxmox provider package

| File                                      | Mentions | What it does                                                                 |
|-------------------------------------------|---------:|------------------------------------------------------------------------------|
| `internal/bootstrap/bootstrap.go`         | 129      | `Run()` orchestrator: identity, kind→mgmt sync, manifest patching, pivot     |
| `internal/bootstrap/plan.go`              |  34      | Dry-run plan body: phase 2.0 / 2.9 / 2.95 are written for Proxmox            |
| `internal/bootstrap/admin.go`             |  35      | Proxmox admin-API helpers (pool create, etc.)                                |
| `internal/bootstrap/purge.go`             |  12      | `--purge` flow (Terraform state, BPG provider tree, CSI configs)             |
| `internal/bootstrap/workloadapps.go`      | (some)   | App-of-apps wiring                                                           |
| `internal/pivot/{pivot,wait,move,manifest}.go` |  44 | clusterctl move: kind → Proxmox mgmt cluster                                 |
| `internal/kindsync/*`                     | 174      | The `proxmox-bootstrap-config/config.yaml` Secret, BPG cred handoff          |
| `internal/capacity/capacity.go`           | (whole)  | Hits Proxmox `/api2/json/cluster/resources` directly                         |
| `internal/capimanifest/{k3s,patches}.go`  | (some)   | ProxmoxMachineTemplate-shaped patches; K3s template embeds Proxmox CRDs      |
| `internal/wlargocd/render.go`             |  some    | App-of-apps overlay names                                                    |
| `internal/yamlx/yamlx.go`                 |  some    | YAML extraction helpers used by Proxmox patches                              |
| `internal/caaph/caaph.go`                 |  some    | Helm values rendering for Proxmox CSI                                        |

### 1b. Config bindings

`internal/config/config.go`: **83 fields** prefixed `Proxmox*`
(URL, Token, Secret, AdminUsername, AdminToken, Node, Pool,
TemplateID, CSIStorage, CSIChartName, CAPIUserID, IdentitySuffix,
…). All read from `PROXMOX_*` env or `--proxmox-*` flags. Treated
as first-class top-level config rather than namespaced under a
provider.

### 1c. CLI bindings

`internal/cli/parse.go` + `usage.txt`: dozens of `--proxmox-*` /
`--admin-*` flags presented as if they were universal program
flags. Other-cloud flags exist (`--aws-mode`, `--azure-location`,
…) but live alongside as peers. There is no `--infra-provider` /
`--infrastructure-provider` flag — the active provider is set only
via `INFRA_PROVIDER` env, which is itself surprising.

### 1d. Capacity bindings

`internal/capacity/capacity.go` directly calls Proxmox endpoints:
- `/api2/json/cluster/resources?type=node` — host totals
- `/api2/json/cluster/resources?type=storage` — storage backends
- `/api2/json/cluster/resources?type=vm` — existing-VM census

`bootstrap.go` and `plan.go` call `capacity.FetchHostCapacity` and
`capacity.FetchExistingUsage` directly — bypassing the
`Provider.Capacity` method that already exists on the interface.

### 1e. Identity bindings

`internal/opentofux/` is purely Proxmox: it generates the BPG
Terraform tree, applies/recreates the CAPI + CSI users. The
`Provider.EnsureIdentity` interface method exists and the proxmox
provider's implementation correctly delegates here. AWS / Azure /
GCP / etc. would each need their own identity-bootstrap layer.

### 1f. Pivot bindings

`internal/pivot/` runs `clusterctl move` from kind → a Proxmox-
hosted management cluster. The shape generalizes (kind → any
managed cluster) but the text and many details are Proxmox-flavored.

## 2. What's already abstracted

The `Provider` interface (`internal/provider/provider.go`) covers:

- `Name()`, `InfraProviderName()`
- `EnsureIdentity(cfg) error`
- `Capacity(cfg) (*HostCapacity, error)`
- `EnsureGroup(cfg, name) error`
- `ClusterctlInitArgs(cfg) []string`
- `K3sTemplate(cfg, mgmt) (string, error)`
- `PatchManifest(cfg, manifestPath, mgmt) error`
- `EnsureCSISecret(cfg, kubeconfigPath) error`
- `EstimateMonthlyCostUSD(cfg) (CostEstimate, error)`

The `provider.MinStub` helper short-circuits the boring bits for
cost-only providers.

What's **missing** from this interface relative to what `Run()`
actually does:

1. **Existing-resource census** (corollary to `Capacity`). Today
   `capacity.FetchExistingUsage` is Proxmox-only. Needs a
   `Provider.ExistingUsage(cfg) (*ExistingUsage, error)` — or fold
   into `Capacity`.
2. **Plan-section content**. The dry-run prints provider-specific
   text like "Proxmox templates: control-plane=104, worker=104" or
   "Cilium HCP …" or "Proxmox CSI on workload …". Should be
   delegated to `Provider.DescribePlan(w, cfg)` — each provider
   prints its own relevant sections.
3. **Kind→mgmt config persistence** (`kindsync`). Today the kind
   Secret is keyed `proxmox-bootstrap-config` and contains
   Proxmox-specific fields. Needs to be neutral (`bootstrap-config`
   namespace, generic schema) with a per-provider Secret extension.
4. **Purge** (`--purge`). Today purges Terraform state + Proxmox
   admin tree. Needs `Provider.Purge(cfg) error` for cloud-specific
   cleanups (e.g. AWS = delete the IAM stack we created in
   `EnsureIdentity`; Hetzner = no-op).
5. **Manifest generation**. `internal/capimanifest` currently
   emits CAPI manifests assuming Proxmox-flavored variables. The
   per-provider `K3sTemplate` + `PatchManifest` already cover most
   of this; what remains is the *value substitution* (which env
   vars get plugged in) — generalize via `Provider.TemplateVars(cfg)
   map[string]string`.
6. **Admin/auth bootstrap helpers** (`bootstrap/admin.go`):
   pool/group/folder creation that runs against the cloud-control
   API. `Provider.EnsureGroup` already exists; the orchestrator
   just needs to delegate consistently.

## 3. Plan — phased extraction

Don't do this in one mega-PR. Five phases, each shippable:

### Phase A — Capacity behind the interface

**Goal:** kill the direct `capacity.Fetch*` calls in
`bootstrap.go` / `plan.go`. Provider becomes the single point of
truth for "host capacity + existing usage on this cloud".

Steps:

1. Extend the `Provider` interface:
   ```go
   ExistingUsage(cfg *config.Config) (*ExistingUsage, error)
   ```
   Move `ExistingUsage` struct from `internal/capacity` into
   `internal/provider` (or a new shared `internal/capacity/types`
   package to avoid an import cycle).
2. Move `FetchHostCapacity` + `FetchExistingUsage` out of
   `internal/capacity` and into `internal/provider/proxmox/` as
   private package-level helpers. The Proxmox `Capacity()` and new
   `ExistingUsage()` call them.
3. Replace direct calls in `bootstrap.go` / `plan.go` with
   `provider.For(cfg).Capacity(...)` and `.ExistingUsage(...)`.
4. Other providers keep `ErrNotApplicable` until each implements
   their own (AWS Service Quotas API, etc.).

**Risk:** low. Mechanical refactor; existing tests still cover the
math.

**Estimated size:** ~400 LOC moved, ~50 LOC of new wiring.

### Phase B — Plan body delegation

**Goal:** kill the Proxmox-specific bullets in
`bootstrap/plan.go`. Each provider prints its own sections.

Steps:

1. Add to `Provider`:
   ```go
   DescribePlan(w PlanWriter, cfg *config.Config)
   ```
   `PlanWriter` is a small interface (just `Section(title string)` +
   `Bullet(format string, args ...any)` + `Skip(reason string)`)
   defined in `internal/bootstrap` or a sibling. Keeps the visual
   style consistent across providers.
2. `bootstrap.planForProvider(w, cfg)` calls
   `provider.For(cfg).DescribePlan(w, cfg)`.
3. Move the Proxmox-specific bullets into
   `internal/provider/proxmox/plan.go`. AWS gets its own
   `internal/provider/aws/plan.go` (currently it has none — and
   that's why `--dry-run` on AWS shows Proxmox phases today).
4. Cross-cutting sections that are provider-agnostic stay in
   `bootstrap/plan.go`: capacity verdict, allocations, cost,
   cost-compare, retention, taller note.

**Risk:** medium. Each provider needs to know what its phases
look like. AWS today has none — this phase is also where AWS
finally gets a real dry-run.

**Estimated size:** ~600 LOC redistributed (Proxmox-specific text
moves from `plan.go` into `provider/proxmox/plan.go`); ~250 LOC new
content per other provider that wants a real dry-run.

### Phase C — Config namespacing

**Goal:** `cfg.Proxmox*` becomes `cfg.Providers.Proxmox.*` (or
similar). Doesn't break env-var compatibility (`PROXMOX_*` still
works), but the in-process structure mirrors the abstraction.

Steps:

1. Move all 83 `Proxmox*` fields into a new struct
   `internal/config/proxmox.go`:
   ```go
   type ProxmoxConfig struct {
       URL string; AdminUsername string; … // 83 fields
   }
   ```
   exposed as `Config.Providers.Proxmox`.
2. Same for `cfg.AWSMode` / `cfg.AzureLocation` / etc. — each
   cloud gets a per-provider sub-config.
3. The ENV → field wiring in `Load()` keeps the same env var names
   (back-compat); only the field paths change.
4. Update every reference: `cfg.ProxmoxURL` → `cfg.Providers.Proxmox.URL`.

**Risk:** medium-high — touches every file that reads a Proxmox
field. Mechanical but big diff.

**Estimated size:** ~1500 LOC churn across many files, mostly
field access rewrites. One commit per provider sub-config keeps
diffs reviewable.

### Phase D — Generic kindsync + Purge

**Goal:** `kindsync` becomes provider-aware. The kind Secret
namespace becomes `bootstrap-config-system`; per-provider Secret
data inside has a `provider:` discriminator.

Steps:

1. Rename Secret namespace `proxmox-bootstrap-system` →
   `bootstrap-config-system` (with back-compat read of the old name
   for one release).
2. Generic Secret schema: `provider`, `cluster_name`, `cluster_id`,
   `kubernetes_version`, plus a per-provider blob.
3. Provider exposes `KindSyncFields(cfg) map[string]string` for
   what it wants persisted in the Secret.
4. `--purge` extends with `Provider.Purge(cfg) error`. Proxmox =
   what's currently in `bootstrap/purge.go`. Others = no-op until
   they have something to delete.

**Risk:** medium. State migration + namespace rename — needs a
deprecation cycle.

**Estimated size:** ~600 LOC.

### Phase E — Pivot generalization

**Goal:** kind → any-cloud-mgmt-cluster, not just kind → Proxmox.

Steps:

1. `Provider.PivotTarget(cfg) (PivotTarget, error)` returns the
   target's kubeconfig path + namespaces to move.
2. `internal/pivot` becomes provider-agnostic: it executes
   `clusterctl move` between two arbitrary kubeconfigs.
3. The "Proxmox-hosted mgmt cluster" Secret-handoff logic stays
   in the proxmox provider package (it's BPG-specific).

**Risk:** medium. Pivot is the most operationally-sensitive flow;
needs careful testing with real Proxmox + at least one non-Proxmox
target (CAPD makes a cheap test target).

**Estimated size:** ~400 LOC.

## 4. Sequencing recommendation

```
                     can be done in any order
                                ↓
  Phase A (capacity)    Phase C (config namespace)
       │                          │
       ▼                          ▼
  Phase B (plan body)  ────────  Phase D (kindsync + purge)
       │                          │
       └──────────┬───────────────┘
                  ▼
          Phase E (pivot)
```

- A is the simplest (mechanical refactor, no UX change).
- B + D depend on A's interface additions — do A first.
- C can run in parallel with A/B/D — it's just a field rename.
- E depends on D (kindsync needs to be neutral first).

**Recommended PR-by-PR order**: A → C → B → D → E. Each is
self-contained, ships independently, can be reverted independently.

## 5. What stays Proxmox-specific (and that's fine)

Some packages don't need abstracting because they *are* Proxmox:

- `internal/proxmox/` — the Proxmox API client. Stays as-is; it's
  the implementation detail behind `provider/proxmox/`.
- `internal/opentofux/` — the BPG identity stack. Same — lives
  inside `provider/proxmox/identity.go` after refactor.
- `internal/capimanifest/k3s_template.yaml` — could split into
  per-provider templates under `internal/provider/<name>/`. Already
  some providers have inline templates (Hetzner, AWS).

## 6. What this *doesn't* fix

The provider abstraction makes the orchestrator multi-cloud-clean
but doesn't address:

- **Identity bootstrap parity**. AWS/Azure/GCP/IBM each need a
  real `EnsureIdentity` (IAM role / Service Principal / Service
  Account / Service ID). Today only Proxmox has one.
- **CSI parity**. Proxmox CSI is wired; other clouds use their
  vendor-supplied CSI via Helm and bootstrap-capi doesn't ship
  the values. Easy to add per-provider.
- **K3s template parity**. Proxmox and Hetzner have working K3s
  templates; the other 10 providers return `ErrNotApplicable`. A
  K3s template per provider is straightforward but tedious.

These are downstream features that the abstraction *enables* but
doesn't deliver itself.

## 7. Estimated total effort

| Phase | LOC churn | Calendar (1 dev) |
|-------|----------:|------------------|
| A     |    ~450   | 1–2 days         |
| B     |  ~600+250×N | 3–5 days       |
| C     |   ~1500   | 2–3 days         |
| D     |    ~600   | 2 days           |
| E     |    ~400   | 1–2 days         |
| **Total** | **~3500–4500** | **~2 weeks of focused work** |

Most of the churn is mechanical (field renames, function-call
indirection). The real design risk is in Phase B (per-provider
plan content) and Phase E (pivot semantics across clouds).
