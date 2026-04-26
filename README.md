# yage

> **yage** — say it **"yah-HEY"** (rhymes with *olé*).
>
> Named after *Banisteriopsis caapi* (B. caapi → bcapi → yage),
> the Amazonian vine brewed into the psychoactive *yagé*.
> As in the famous south american visionary concotion, yage Go program

A Go tool that bootstraps a Cluster API (CAPI) management
plane in a local kind cluster and provisions a Proxmox VE workload
cluster on top, with CNI (Cilium), CSI (Proxmox CSI), and an
Argo CD app-of-apps GitOps surface. Twelve infrastructure providers
are registered (Proxmox is the fully-wired one; the rest cover
cost-comparison and partial bring-up).

## Quick start

```sh
make deps && make           # build → bin/yage
bin/yage --help             # full flag reference
bin/yage --dry-run          # plan without applying
```

## Documentation

Everything lives under [`docs/`](docs/):

- **[ARCHITECTURE.md](docs/ARCHITECTURE.md)** — the Go
  implementation, package-by-package, with notes on the historical
  bash provenance.
- **[providers.md](docs/providers.md)** — how to add a new CAPI
  infrastructure provider behind the `Provider` interface.
- **[capacity-preflight.md](docs/capacity-preflight.md)** — the
  soft-budget + overcommit ceiling check that runs before
  provisioning.
- **[cost-and-pricing.md](docs/cost-and-pricing.md)** — monthly cost
  estimation and cross-cloud cost comparison at planning time.
- **[abstraction-plan.md](docs/abstraction-plan.md)** — five-phase
  plan to extract Proxmox-specific code from the orchestrator (the
  in-progress refactor toward true multi-cloud).
- **[AGENT_SYSTEM_PROMPT.md](docs/AGENT_SYSTEM_PROMPT.md)** —
  system prompt for AI assistants working on this codebase.

## Layout

```
cmd/yage/         entry point (main.go)
internal/         Go packages — orchestrator, providers, capacity, cost, pivot, …
docs/             documentation (see above)
Makefile          build / test / install
```

## Environment variables

User-facing knobs are namespaced `YAGE_*` (e.g.
`YAGE_PRICING_DISABLED`, `YAGE_CURRENCY`); cloud credentials use
their vendor-native names (`PROXMOX_*`, `AWS_*`, `HCLOUD_TOKEN`,
…). See `bin/yage --help` for the full list.
