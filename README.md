# yage

> **yage** — say it **"yah-HEY"** (rhymes with *olé*).
>
> Named after *Banisteriopsis caapi* (B. caapi → bcapi → yage),
> the Amazonian vine brewed into the psychoactive *yagé*.
> As in the famous south american visionary concotion, yage Go program

A Go tool that bootstraps a Cluster API (CAPI) management
plane in a local kind cluster and provisions a workload cluster on
any registered infrastructure provider — with CNI (Cilium), CSI
(per-provider via the §20 driver registry), and an Argo CD
app-of-apps GitOps surface. Twelve providers are registered
(Proxmox is the most-wired today; AWS / Azure / GCP / Hetzner /
OpenStack / vSphere ship the CSI + dry-run + cost-compare surface;
DigitalOcean / Linode / OCI / IBM Cloud / CAPD round out the
matrix). Pick one with `--infra-provider` or `INFRA_PROVIDER`.

## Quick start

```sh
make deps && make           # build → bin/yage
bin/yage --help             # full flag reference
bin/yage --dry-run          # plan without applying
```

## Documentation

Everything lives under [`docs/`](docs/):

- **[ARCHITECTURE.md](docs/ARCHITECTURE.md)** — the Go
  implementation, package-by-package.
- **[providers.md](docs/providers.md)** — how to add a new CAPI
  infrastructure provider behind the `Provider` interface.
- **[capacity-preflight.md](docs/capacity-preflight.md)** — the
  soft-budget + overcommit ceiling check that runs before
  provisioning.
- **[cost-and-pricing.md](docs/cost-and-pricing.md)** — monthly cost
  estimation and cross-cloud cost comparison at planning time.
- **[abstraction-plan.md](docs/abstraction-plan.md)** — design plan
  for the multi-cloud provider abstraction: how Proxmox-specific
  code is kept out of the orchestrator's hot path.
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

## License

Apache License 2.0 — see [`LICENSE`](LICENSE).

Copyright 2026 Luca Pasquali. yage is free for any use, commercial
or otherwise, under the terms of Apache 2.0 (no warranty, patent
grant included, contributions licensed under the same terms).
