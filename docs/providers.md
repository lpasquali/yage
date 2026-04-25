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
}
```

Any phase that doesn't apply to a given provider returns
`provider.ErrNotApplicable` — the orchestrator skips silently.

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

type Provider struct{}

func (p *Provider) Name() string              { return "myprovider" }
func (p *Provider) InfraProviderName() string { return "myprovider" }
// ... rest of the interface
```

Then add a blank import to `cmd/bootstrap-capi/main.go`:

```go
import (
    _ "github.com/lpasquali/bootstrap-capi/internal/provider/myprovider"
)
```

That's the entire wiring. `provider.For(cfg)` looks up the
implementation by `cfg.InfraProvider`; the orchestrator calls
methods on the returned interface for every per-provider phase.

## Example minimum implementation: CAPD (Docker)

`internal/provider/capd/capd.go` is the smallest working second
provider — useful for smoke tests without a real hypervisor:

- `EnsureIdentity` / `Capacity` / `EnsureGroup` / `EnsureCSISecret`
  return `ErrNotApplicable` (Docker has no identity layer, no
  capacity API, no pool concept, no CSI ship-by-default).
- `ClusterctlInitArgs` returns `["--infrastructure", "docker"]`.
- `K3sTemplate` is an inline string with `DockerCluster` +
  `DockerMachineTemplate`.
- `PatchManifest` is a no-op (DockerMachineTemplate has no per-role
  sizing fields).

About 100 lines total; serves as the template for any minimum-viable
provider.

## Reference: Proxmox

`internal/provider/proxmox/proxmox.go` is the production-shape
implementation. It's intentionally a THIN WRAPPER over existing
helpers under `internal/proxmox`, `internal/opentofux`,
`internal/capacity`, `internal/csix`, and `internal/capimanifest`.
The plugin point is established without rewriting working code.

## Adding a new provider — checklist

1. Create `internal/provider/<name>/`.
2. Implement `Provider` (use `ErrNotApplicable` liberally for parts
   the provider doesn't have).
3. Register in `init()`.
4. Add the blank import to `cmd/bootstrap-capi/main.go`.
5. Make sure `--infrastructure-provider <name>` resolves —
   `cfg.InfraProvider` already exists; setting it via env / CLI
   picks your provider via `provider.For(cfg)`.
6. If you ship a CSI integration: model your CSI Secret apply on
   `internal/csix.ApplyConfigSecretToWorkload`.
7. If your provider has an identity-bootstrap concept: model your
   `EnsureIdentity` on `internal/opentofux.ApplyIdentity`.
8. If your provider has a capacity API: implement `Capacity`
   returning the same `HostCapacity` shape; the orchestrator
   compares it against the user's plan automatically.

## Currently registered

```bash
$ bootstrap-capi --list-providers   # planned future flag
docker        clusterctl --infrastructure docker
proxmox       clusterctl --infrastructure proxmox
```

(Today this list is hardcoded by the blank imports in
`cmd/bootstrap-capi/main.go`. Adding a provider = one new package
+ one new blank import = it shows up in the registry.)

## What about full coverage of the CAPI ecosystem?

CAPI has 40+ infrastructure providers. The plugin pattern above
makes adding any of them a self-contained ~200-line PR (more for
providers with rich identity-bootstrap; vSphere is closer to
Proxmox in shape, AWS is heavier because IAM is its own world). The
main bootstrap-capi binary doesn't need to ship them all — users
can vendor their own provider package and rebuild, or contribute
upstream.

Suggested priority for the next implementations:

- **vSphere (CAPV)** — the closest in shape to Proxmox. Identity
  is a pre-existing service account → near-trivial. Capacity via
  govmomi inventory.
- **AWS (CAPA)** — high demand. Identity = IAM role + access key.
  Capacity = EC2 quotas API. CSI = ebs-csi-driver via Helm.
- **OpenStack (CAPO)** — handy for sovereign clouds. Identity =
  application credentials; capacity = `nova quota-show`.
- **vCluster / KubeVirt** — for nested or VM-on-K8s scenarios.
- **Hetzner (CAPHV)** — popular EU bare-metal.
