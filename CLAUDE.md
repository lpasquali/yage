# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and test

```bash
make deps      # go mod tidy + download; requires Go 1.23+
make build     # → bin/yage (trimpath)
make test      # go test ./...
make install   # go install
make clean     # remove bin/
```

Go binary may not be on PATH — use `make` or the full path `/usr/local/go/bin/go`. Run a single test:

```bash
go test ./internal/cluster/capacity/... -run TestCheckCombined -v
```

## Architecture

`cmd/yage/main.go` → `internal/orchestrator.Run(*config.Config)`. All config comes from `internal/config/config.go` (env vars → CLI flags → kind Secret merge).

### Provider plugin system

`internal/provider/provider.go` defines the `Provider` interface (18 methods). Each provider self-registers:

```go
func init() { provider.Register("proxmox", func() provider.Provider { return &Provider{} }) }
```

`provider.MinStub` is the embed helper: defaults `EnsureIdentity`, `Inventory`, `EnsureGroup`, `K3sTemplate`, `PivotTarget` to `ErrNotApplicable`; `PatchManifest` and `Purge` to no-ops. Use `ErrNotApplicable` (not errors or panics) for phases that don't apply to a provider.

Reference implementation: `internal/provider/capd/capd.go` — deliberately minimal.

### Orchestrator phases

`internal/orchestrator/bootstrap.go` drives the full bootstrap. Key phases in order: standalone early-exit → dependency install → OpenTofu identity → kind cluster → clusterctl init → workload manifest apply → pivot (optional) → Argo CD on workload.

`internal/orchestrator/plan.go` implements `--dry-run` (prints what would run without doing it).

### YAML manifest handling

Multi-document YAML (`\n---\n` separated) is the main workload manifest format. **Always split by `\n---\n` first**, then match within each document. Use line-anchored regexes (`^kind:\s*ProxmoxCluster\s*$`) — never `strings.Contains` for kind matching because `infrastructureRef` embeds the same string nested.

### Shell execution and logging

- Shell: `shell.Run`, `shell.Capture`, `shell.RunWithEnv`, `shell.Pipe` — never raw `os/exec`.
- Logging: `logx.Log` (✅🎉), `logx.Warn` (⚠️🙈), `logx.Die` (❌💩 + exit). `logx.Die` is for fatal errors only.

### State persistence

State lives in kind Secrets (`proxmox-bootstrap-config/config.yaml` in `yage-system` namespace), not local disk. `internal/cluster/kindsync` owns this round-trip. `config.Snapshot()` serialises fields; `kindsync.MergeProxmoxBootstrapSecretsFromKind()` reads them back (skipping fields marked `*_EXPLICIT`).

### Kubernetes clients

All cluster interactions go through `internal/platform/k8sclient` (typed + dynamic clients keyed by kubecontext/kubeconfig). The handful of intentional `kubectl` shell-outs (kustomize apply, port-forward, backup/restore streaming) are documented with rationale comments at each call site.

### Cost / pricing / capacity subsystems

- `internal/pricing/` — per-vendor live catalog fetchers with 24h disk cache at `~/.cache/yage/pricing/`. No hardcoded dollar amounts; return `ErrUnavailable` when the API is unreachable.
- `internal/cost/` — `--cost-compare` calls every provider's `EstimateMonthlyCostUSD` and prints a side-by-side table.
- `internal/cluster/capacity/` — Proxmox `/cluster/resources` preflight with trichotomy verdict (fits / tight / abort). Called directly from bootstrap today; abstraction is planned.

## Key gotchas

1. **Workload kubeconfig** comes from the mgmt cluster Secret `<cluster-name>-kubeconfig`, not from the workload node directly. Always set `KUBECONFIG=<tmpfile>` when querying workload.
2. **kubectl port-forward**: use `--address 127.0.0.1` + `PORT:443` format; the `127.0.0.1:PORT:443` triple is not supported in modern kubectl.
3. **CAPI v1beta2 strict decoding**: unknown fields in Cluster spec are rejected — patches must target only the correct resource document.
4. **ArgoCD admin password**: three possible Secrets depending on install method (`argocd-initial-admin-secret/password`, `argocd-cluster/admin.password` plaintext, `argocd-secret/admin.password` bcrypt).

## Abstraction plan

The abstraction plan ADR in [yage-docs](https://lpasquali.github.io/yage-docs/architecture/adrs/abstraction-plan/) tracks the five-phase plan to decouple Proxmox from the core orchestrator. Phases and their primary targets:

| Phase | Goal | Key files |
|-------|------|-----------|
| C | Config namespacing (`cfg.Proxmox*` → `cfg.Providers.Proxmox.*`) | `internal/config/config.go`, all consumers |
| E | Pivot generalization (kind → any-cloud mgmt) | `internal/capi/pivot/` |
| B | Plan body delegation per provider | `internal/orchestrator/plan.go`, provider DescribePlan methods |
| A | Inventory behind `Provider.Inventory()` | `internal/cluster/capacity/`, orchestrator capacity calls |
| D | Generic kindsync + Purge | `internal/cluster/kindsync/`, `internal/orchestrator/purge.go` |
