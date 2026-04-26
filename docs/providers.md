# Adding a new CAPI infrastructure provider

bootstrap-capi's orchestrator drives the CAPI-standard flow (kind →
clusterctl init → workload Cluster → CAAPH → Argo CD → optional
pivot). Provider-specific bits live behind a `Provider` interface in
`internal/provider`. New providers ship as a self-contained package.

## Interface

`internal/provider/provider.go` defines:

```go
type Provider interface {
    Name() string
    InfraProviderName() string

    EnsureIdentity(cfg *config.Config) error                  // pre-clusterctl identity bootstrap
    Capacity(cfg *config.Config) (*HostCapacity, error)        // host CPU/memory/storage query
    EnsureGroup(cfg *config.Config, name string) error         // pool / folder / IAM-group / project
    ClusterctlInitArgs(cfg *config.Config) []string            // adds --infrastructure <name> + overrides
    K3sTemplate(cfg *config.Config, mgmt bool) (string, error) // K3s flavor with provider's MachineTemplate kind
    PatchManifest(cfg *config.Config, manifestPath string, mgmt bool) error
    EnsureCSISecret(cfg *config.Config, workloadKubeconfigPath string) error
    EstimateMonthlyCostUSD(cfg *config.Config) (CostEstimate, error)
}
```

Any phase that doesn't apply to a given provider returns
`provider.ErrNotApplicable` — the orchestrator skips silently.

## `provider.MinStub` — minimal-provider shortcut

For a cost-only provider (just clusterctl-init + a live pricing
fetcher), embed the `MinStub` helper and override only what's
needed. `MinStub` defaults `EnsureIdentity` / `EnsureGroup` /
`EnsureCSISecret` / `Capacity` / `K3sTemplate` to `ErrNotApplicable`
and `PatchManifest` to a no-op. Concrete cloud package becomes <100 LOC:

```go
type Provider struct{ provider.MinStub }

func (p *Provider) Name() string              { return "myprovider" }
func (p *Provider) InfraProviderName() string { return "myprovider" }

func (p *Provider) ClusterctlInitArgs(cfg *config.Config) []string {
    return []string{"--infrastructure", "myprovider"}
}

func (p *Provider) EstimateMonthlyCostUSD(cfg *config.Config) (provider.CostEstimate, error) {
    // call into internal/pricing.Fetch("myprovider", sku, region)
    // and return the breakdown
}
```

The five clouds added in 2026-Q2 (DigitalOcean, Linode, OCI,
IBM Cloud, Equinix Metal) each ship as MinStub-based packages in
~80–110 LOC. Pattern is mechanical: stub registration + a cost.go
that walks `pricing.Fetch` for compute. Overhead modeling (LBs,
egress, etc.) goes in a separate file when needed (see
`internal/provider/aws/overhead.go` for the reference shape).

## Registry

Each provider package self-registers in `init()`:

```go
package myprovider

import (
    "github.com/lpasquali/bootstrap-capi/internal/config"
    "github.com/lpasquali/bootstrap-capi/internal/provider"
)

func init() {
    provider.Register("myprovider", func() provider.Provider { return &Provider{} })
}
```

Then add a blank import to `cmd/bootstrap-capi/main.go`:

```go
import (
    _ "github.com/lpasquali/bootstrap-capi/internal/provider/myprovider"
)
```

`provider.For(cfg)` looks up the implementation by `cfg.InfraProvider`;
the orchestrator calls methods on the returned interface for every
per-provider phase.

## Adding a new provider — checklist

1. Create `internal/provider/<name>/` with `<name>.go`.
2. Embed `provider.MinStub`; override `Name`, `InfraProviderName`,
   `ClusterctlInitArgs`. Keep `K3sTemplate` as `ErrNotApplicable`
   until you've built the per-CRD K3s flavor (or wire it now if you
   have one — see `internal/provider/hetzner/hetzner.go` for shape).
3. Add `cost.go` calling `pricing.Fetch(name, sku, region)`. Wrap
   `provider.ErrNotApplicable` around any live-API failure so the
   orchestrator surfaces "estimate unavailable" rather than fabricate.
4. Add `internal/pricing/<name>.go` implementing the `pricing.Fetcher`
   interface (`Fetch(sku, region) (Item, error)`). Register via
   `init()` calling `pricing.Register("<name>", &fetcher{})`.
5. Extend `internal/pricing/onboarding.go`:
   - Add a case to `PricingCredsConfigured` that returns true when
     the user's API key / token is present, false otherwise.
   - Add an `OnboardingHint` constant with the exact CLI commands
     to set up minimum-permission credentials.
6. Add config fields (region, instance-type defaults) in
   `internal/config/config.go` plus `Load()` defaults.
7. Add a blank import to `cmd/bootstrap-capi/main.go`.
8. If you ship a CSI integration: model your CSI Secret apply on
   `internal/csix.ApplyConfigSecretToWorkload`.
9. If your provider has an identity-bootstrap concept: model your
   `EnsureIdentity` on `internal/opentofux.ApplyIdentity`.
10. If your provider has a capacity API: implement `Capacity`
    returning the same `HostCapacity` shape; the orchestrator
    compares it against the user's plan automatically (and now
    against existing VMs too — see `docs/capacity-preflight.md`).

## Currently registered (13 providers)

```
provider       infrastructure-provider   pricing source                    creds for live pricing
─────────────  ─────────────────────────  ────────────────────────────────  ──────────────────────
aws            --infrastructure aws       AWS Bulk Pricing JSON             public anonymous
azure          --infrastructure azure     Azure Retail Prices API           anonymous
gcp            --infrastructure gcp       GCP Cloud Billing Catalog API     GOOGLE_BILLING_API_KEY
hetzner        --infrastructure hetzner   Hetzner Cloud /v1/pricing+types   HCLOUD_TOKEN
proxmox        --infrastructure proxmox   self-hosted (TCO via flags)       n/a
vsphere        --infrastructure vsphere   self-hosted (TCO via flags)       n/a
openstack      --infrastructure openstack operator-dependent (none today)   n/a
docker (capd)  --infrastructure docker    free                              n/a
digitalocean   --infrastructure digitalocean  /v2/sizes                     DIGITALOCEAN_TOKEN
linode         --infrastructure linode    /v4/linode/types                  anonymous
oci            --infrastructure oci       Oracle Cost Estimator JSON        anonymous
ibmcloud       --infrastructure ibmcloud  IBM Global Catalog (IAM bearer)   IBMCLOUD_API_KEY
equinix        --infrastructure packet    /metal/v1/plans                   METAL_AUTH_TOKEN
```

Replay first-run setup hints any time:

```bash
bootstrap-capi --print-pricing-setup aws         # one vendor
bootstrap-capi --print-pricing-setup all         # every vendor that needs setup
```

See `docs/cost-and-pricing.md` for what each pricing fetcher does
and what setup is needed.

## Reference implementations

| Shape                              | Reference package                              |
|------------------------------------|------------------------------------------------|
| Production-quality, full lifecycle | `internal/provider/proxmox/proxmox.go`         |
| Cost+overhead with managed-mode    | `internal/provider/aws/`                       |
| Cost+overhead, K3s template wired  | `internal/provider/hetzner/`                   |
| MinStub-based, cost-only           | `internal/provider/digitalocean/` (and 4 more) |
| Self-hosted (TCO, no vendor API)   | `internal/provider/proxmox/proxmox.go` cost path |
| In-memory test (no real cloud)     | `internal/provider/capd/capd.go`               |

## What about full coverage of the CAPI ecosystem?

CAPI has 40+ infrastructure providers. The 13 registered cover the
main public clouds + the typical on-prem trio + Docker. The plugin
pattern makes adding any of the rest a self-contained ~150-line PR
when there's a public pricing API:

- **Vultr** — `https://api.vultr.com/v2/plans` (token auth)
- **OVHcloud** — `https://api.ovh.com/v1/pricing` (OAuth)
- **Scaleway** — `https://api.scaleway.com/instance/v1/.../products/servers` (token)
- **Outscale**, **CleverCloud**, **UpCloud** — each has a public catalog

Without a public pricing API the provider can still register (for
clusterctl init + provisioning) and return `ErrNotApplicable` from
`EstimateMonthlyCostUSD`, mirroring how vSphere / OpenStack work.
