# Adding a new CAPI infrastructure provider

yage's orchestrator drives the CAPI-standard flow (kind →
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

The four clouds added in 2026-Q2 (DigitalOcean, Linode, OCI,
IBM Cloud) each ship as MinStub-based packages in ~80–110 LOC.
Pattern is mechanical: stub registration + a cost.go that walks
`pricing.Fetch` for compute. Overhead modeling (LBs, egress, etc.)
goes in a separate file when needed (see
`internal/provider/aws/overhead.go` for the reference shape).

## Registry

Each provider package self-registers in `init()`:

```go
package myprovider

import (
    "github.com/lpasquali/yage/internal/config"
    "github.com/lpasquali/yage/internal/provider"
)

func init() {
    provider.Register("myprovider", func() provider.Provider { return &Provider{} })
}
```

Then add a blank import to `cmd/yage/main.go`:

```go
import (
    _ "github.com/lpasquali/yage/internal/provider/myprovider"
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
7. Add a blank import to `cmd/yage/main.go`.
8. If you ship a CSI integration: model your CSI Secret apply on
   `internal/capi/csi.ApplyConfigSecretToWorkload`.
9. If your provider has an identity-bootstrap concept: model your
   `EnsureIdentity` on `internal/platform/opentofux.ApplyIdentity`.
10. If your provider has a capacity API: implement `Capacity`
    returning the same `HostCapacity` shape; the orchestrator
    compares it against the user's plan automatically (and now
    against existing VMs too — see `docs/capacity-preflight.md`).

## Currently registered (12 providers)

Cross-checked against the upstream CAPI provider list at
https://cluster-api.sigs.k8s.io/reference/providers — every entry
below corresponds to an actively-maintained `cluster-api-provider-*`
repo.

```
provider       infrastructure-provider     upstream repo                                  pricing source                    creds for live pricing
─────────────  ──────────────────────────  ─────────────────────────────────────────────  ────────────────────────────────  ──────────────────────
aws            --infrastructure aws        kubernetes-sigs/cluster-api-provider-aws       AWS Bulk Pricing JSON             public anonymous
azure          --infrastructure azure      kubernetes-sigs/cluster-api-provider-azure     Azure Retail Prices API           anonymous
gcp            --infrastructure gcp        kubernetes-sigs/cluster-api-provider-gcp       GCP Cloud Billing Catalog API     GOOGLE_BILLING_API_KEY
hetzner        --infrastructure hetzner    syself/cluster-api-provider-hetzner            Hetzner Cloud /v1/pricing+types   HCLOUD_TOKEN
proxmox        --infrastructure proxmox    ionos-cloud/cluster-api-provider-proxmox       self-hosted (TCO via flags)       n/a
vsphere        --infrastructure vsphere    kubernetes-sigs/cluster-api-provider-vsphere   self-hosted (TCO via flags)       n/a
openstack      --infrastructure openstack  kubernetes-sigs/cluster-api-provider-openstack operator-dependent                n/a
docker (capd)  --infrastructure docker     kubernetes-sigs/cluster-api (test/infra)       free                              n/a
digitalocean   --infrastructure digitalocean kubernetes-sigs/cluster-api-provider-digitalocean  /v2/sizes                   DIGITALOCEAN_TOKEN
linode         --infrastructure linode     linode/cluster-api-provider-linode             /v4/linode/types                  anonymous
oci            --infrastructure oci        oracle/cluster-api-provider-oci                Oracle Cost Estimator JSON        anonymous
ibmcloud       --infrastructure ibmcloud   kubernetes-sigs/cluster-api-provider-ibmcloud  IBM Global Catalog (IAM bearer)   IBMCLOUD_API_KEY
```

> **Equinix Metal / Packet was removed** — `kubernetes-sigs/cluster-
> api-provider-packet` was archived as `[EOL]` upstream in 2025-08
> and there is no successor repo. Don't re-add unless the CAPI
> provider list resurrects it.

Replay first-run setup hints any time:

```bash
yage --print-pricing-setup aws         # one vendor
yage --print-pricing-setup all         # every vendor that needs setup
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

The upstream CAPI provider list has ~35 infrastructure providers.
The 12 registered cover the main public clouds + the typical on-prem
trio + Docker. The plugin pattern makes adding any of the rest a
self-contained ~150-line PR when the underlying repo is active.

**Have CAPI provider AND a public pricing API — easy adds:**

| Provider     | CAPI repo                                       | Pricing API                                                |
|--------------|-------------------------------------------------|------------------------------------------------------------|
| Vultr        | `vultr/cluster-api-provider-vultr`              | `api.vultr.com/v2/plans` (token auth)                      |
| Scaleway     | `scaleway/cluster-api-provider-scaleway`        | `api.scaleway.com/instance/v1/.../products/servers` (token) |
| Outscale     | `outscale/cluster-api-provider-outscale`        | OSC pricing API (token)                                    |
| Huawei Cloud | `HuaweiCloudDeveloper/cluster-api-provider-huawei` | Huawei pricing API (AK/SK)                              |
| IONOS Cloud  | `ionos-cloud/cluster-api-provider-ionoscloud`   | IONOS pricing API (token)                                  |

**Have CAPI provider, no canonical public pricing API:**
KubeVirt, Metal3, Sidero, Tinkerbell, Nutanix, MAAS, BYOH, vcluster,
Virtink, Harvester, OpenNebula, metal-stack, microvm, Hivelocity,
CloudStack, KubeKey, VMware Cloud Director, Azure Stack HCI, CoxEdge —
each can register for clusterctl init + provisioning and return
`ErrNotApplicable` from `EstimateMonthlyCostUSD`, mirroring how
vSphere / OpenStack work today.
