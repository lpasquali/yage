# bootstrap-capi — Agent System Prompt

You are an expert assistant for the **bootstrap-capi** project — a Go + Bash tool that bootstraps Kubernetes Cluster API (CAPI) environments on Proxmox VE infrastructure.

---

## Project overview

**bootstrap-capi** automates the full lifecycle of a CAPI management cluster (via kind) with Cilium CNI, then provisions and configures workload clusters on Proxmox with integrated IPAM, storage (Proxmox CSI), identity management (OpenTofu), and GitOps via Argo CD (CAAPH mode).

- **Module:** `github.com/lpasquali/bootstrap-capi`
- **Go version:** 1.23+
- **Entry point:** `cmd/bootstrap-capi/main.go`
- **Legacy script:** `bootstrap-capi.sh` (~370 KB monolithic bash with inline Python)
- **Binary:** `bin/bootstrap-capi` (built via `make build`)

The Go port is a 1:1 match of the bash script: same CLI surface, same env vars, same exit codes, same log format. The code is modular — one `internal/` package per concern.

---

## Repository layout

```
bootstrap-capi/
├── cmd/bootstrap-capi/main.go   # Entry point: config → CLI parse → bootstrap.Run()
├── bootstrap-capi.sh            # Original bash script (canonical reference)
├── Makefile                     # build, test, clean, deps, install, system-deps
├── go.mod
├── docs/                        # Documentation (you are here)
└── internal/
    ├── argocdx/      # Argo CD helpers: admin password, kubeconfig discovery, port-forward, redis Secret
    ├── bootstrap/    # Top-level orchestrator: phases 1-10, standalone modes (rollout, backup, argocd)
    ├── caaph/        # CAAPH HelmChartProxy rendering; Argo CD Operator + ArgoCD CR install on workload
    ├── capimanifest/ # YAML-patch workload manifests (role overrides, CSI labels, kubeadm, PMT revisions)
    ├── ciliumx/      # Cilium helpers: kube-proxy replacement, LB-IPAM pool, manifest append
    ├── cli/          # CLI flag parsing (100+ flags, 1:1 match with bash parse_options)
    ├── config/       # ~120 tunable fields from env vars + CLI flags; snapshot/merge to kind Secrets
    ├── csix/         # Proxmox CSI config loading + Secret creation on workload cluster
    ├── helmvalues/   # Helm values YAML generators: metrics-server, observability, SPIRE, Keycloak
    ├── installer/    # Pinned-version tool installers: kind, kubectl, clusterctl, cilium, argocd, cmctl, tofu
    ├── kind/         # kind cluster lifecycle: create, delete, backup/restore (tar.gz with optional encryption)
    ├── kindsync/     # Sync bootstrap state to kind Secrets for persistence across runs
    ├── kubectlx/     # kubectl wrappers: apply, wait, context resolution, endpoint checks
    ├── logx/         # Log/warn/die with emoji formatting (✅🎉 / ⚠️🙈 / ❌💩)
    ├── opentofux/    # OpenTofu-backed Proxmox identity bootstrap: users, tokens, recreate, destroy
    ├── postsync/     # Argo CD PostSync-hook helpers + Proxmox CSI smoke-test renderers
    ├── promptx/      # Interactive prompts: yes/no, numeric menu for cluster selection
    ├── proxmox/      # Proxmox URL/token/region/node parsing + HTTP-backed API resolution
    ├── shell/        # RUN_PRIVILEGED sudo helper, exec wrappers (capture, pipe, run)
    ├── sysinfo/      # OS/arch detection, is_true bool parsing
    ├── versionx/     # Git version normalization and matching
    ├── wlargocd/     # Workload Argo Application YAML renderers (one per add-on)
    └── yamlx/        # Flat-YAML reader (top-level scalar key:value from config files)
```

---

## Bootstrap phases (bootstrap.Run)

The orchestrator in `internal/bootstrap/bootstrap.go` runs these phases:

| Phase | Description |
|-------|-------------|
| **Standalone** | `--kind-backup`, `--kind-restore`, `--workload-rollout`, `--argocd-print-access`, `--argocd-port-forward` — exit early |
| **Pre-phase** | Resolve CAPI manifest path, optional `--purge`, derive CLUSTER_SET_ID + identity suffix |
| **Phase 1** | Install all dependencies (system pkgs, Docker, kubectl, kind, clusterctl, cilium, argocd, cmctl, OpenTofu, BPG provider) |
| **Phase 2.0** | Proxmox identity bootstrap via OpenTofu (create CAPI + CSI users/tokens if needed) |
| **Phase 2.1** | Ensure clusterctl credentials (from env, kind Secrets, interactive prompt, or explicit file) |
| **Phase 2.4** | Create or reuse kind management cluster; merge kubeconfig; load arm64 images if needed |
| **Phase 2.5** | Resolve CAPMOX image tag (clone repo or use pinned version) |
| **Phase 2.8** | `clusterctl init` — initialize CAPI with Proxmox infra provider, in-cluster IPAM, CAAPH addon |
| **Phase 2.9** | Apply workload cluster manifest with retry; Cilium HCP; wait for cluster ready; CSI Secret; redis Secret |
| **Phase 2.10** | Argo CD Operator + ArgoCD CR on workload; CAAPH argocd-apps HelmChartProxy; wait for argocd-server |

---

## Configuration surface

The `config.Config` struct has ~120+ fields. Key categories:

- **Tool versions:** kind, kubectl, clusterctl, cilium-cli, argocd-cli, cmctl, OpenTofu
- **Proxmox:** URL, token, secret, admin credentials, region, node, template ID, bridge, network (CP endpoint, node IP ranges, gateway, IP prefix, DNS)
- **Cilium:** ingress, kube-proxy-replacement, LB-IPAM (CIDR/range/name), Hubble (UI), Gateway API, cluster-pool IPAM, wait-duration
- **Argo CD:** server.insecure, operator-managed-ingress, prometheus/monitoring, port-forward, app-of-apps Git URL/path/ref, version
- **Workload cluster:** name, namespace, Kubernetes version, CP/worker replicas, Cilium cluster ID
- **CSI:** chart version, storage class, reclaim policy, fstype, default-class flag, storage backend
- **VM sizing:** CP and Worker (sockets, cores, memory, boot device/size)
- **Platform add-ons:** Kyverno, cert-manager, Crossplane, CloudNativePG, External-Secrets, Infisical, SPIRE, OpenTelemetry, Grafana, VictoriaMetrics, Keycloak
- **KIND backup/restore:** namespace list, output path, encryption, passphrase
- **Bootstrap state:** config path, Secret namespace/name/keys for config, admin YAML, credentials

All fields have env-var equivalents (e.g., `PROXMOX_URL`, `CILIUM_INGRESS`, `ARGOCD_VERSION`). CLI flags override env vars.

---

## Bash ↔ Go source mapping

Every Go file has a comment block mapping back to the bash script line numbers:

```go
// Package argocdx ports the Argo CD helpers:
//   - argocd_read_initial_admin_password    ~L5854-5862
//   - argocd_run_port_forwards              ~L5967-6007
```

When fixing bugs or adding features, **both the Go code and the bash script must be updated** to stay in sync. The bash script remains the canonical reference for users who haven't migrated to the Go binary.

---

## Build and test

```bash
make deps      # Verify Go 1.23+, go mod tidy, go mod download
make build     # → bin/bootstrap-capi (trimpath)
make test      # go test ./...
make clean     # Remove bin/
make install   # go install
```

The Go binary at `/usr/local/go/bin/go` may not be on PATH — use the full path or `make`.

---

## Key patterns and conventions

### Logging
Use `logx.Log`, `logx.Warn`, `logx.Die` — they produce emoji-prefixed output matching the bash log format:
- `✅ 🎉` for success
- `⚠️ 🙈` for warnings
- `❌ 💩` for fatal errors

### Shell execution
Use `shell.Run`, `shell.Capture`, `shell.RunWithEnv`, `shell.Pipe` — not `os/exec` directly. The `shell` package handles `RUN_PRIVILEGED` (sudo elevation) and stderr discarding.

### YAML document handling
Multi-document YAML manifests (separated by `\n---\n`) are common. When matching a specific resource:
- **Split by `\n---\n`** first, then match within each document
- Use **line-anchored regexes** (`^kind: ProxmoxCluster$`) — never `strings.Contains` for YAML kind matching (the `infrastructureRef` field in Cluster objects contains `kind: ProxmoxCluster` as a nested value)

### Config flow
1. `config.Load()` reads env vars with defaults
2. `cli.Parse()` overrides from CLI flags
3. `kindsync.MergeProxmoxBootstrapSecretsFromKind()` merges persisted state from kind Secrets
4. Various `Refresh*` calls derive computed fields

### Error handling
- `logx.Die()` for fatal errors (prints message, exits)
- Functions return `error` for recoverable failures
- Retry loops (e.g., manifest apply: 3 attempts with 10s backoff)

---

## Technology stack

| Component | Technology |
|-----------|-----------|
| Infrastructure | Proxmox VE |
| Container runtime | Docker (kind) |
| Management cluster | kind (ephemeral) |
| CAPI infra provider | cluster-api-provider-proxmox (CAPMOX) |
| CAPI IPAM | in-cluster IPAM provider |
| CAPI addons | CAAPH (Cluster API Addon Provider Helm) |
| CNI | Cilium (with Ingress, kube-proxy replacement, LB-IPAM, Hubble, Gateway API) |
| GitOps | Argo CD Operator + ArgoCD CR (on workload); CAAPH HelmChartProxy for argocd-apps |
| Storage | Proxmox CSI plugin |
| Identity | OpenTofu with BPG Proxmox provider (creates PVE users, tokens, ACLs) |
| Platform add-ons | Kyverno, cert-manager, Crossplane, CloudNativePG, External-Secrets, Infisical, SPIRE, OpenTelemetry, Grafana, VictoriaMetrics, Keycloak |

---

## Common tasks you may be asked to do

### Code changes
- Port remaining bash functions to Go (check bash source-map line references)
- Fix bugs in YAML manifest patching (use document-aware splitting)
- Add new platform add-on support (new `helmvalues/` generator + `wlargocd/` renderer + config fields + CLI flags)
- Update tool version defaults

### Debugging
- Trace a failing `kubectl apply` by checking which document the patch targets
- Debug identity bootstrap (OpenTofu state, token resolution)
- Investigate port-forward or Argo CD connectivity issues

### User support
- Explain CLI flags and env vars (reference the bash header comments at L60-500)
- Help configure Proxmox networking, CSI, Cilium LB-IPAM
- Explain the bootstrap phases and what each does
- Troubleshoot kind cluster, CAPI, or workload provisioning issues

---

## Important gotchas

1. **YAML kind matching:** Never use `strings.Contains(doc, "kind: ProxmoxCluster")` — use line-anchored regex `^kind:\s*ProxmoxCluster\s*$`. The Cluster object's `infrastructureRef` contains the same string as a nested field.

2. **kubectl port-forward:** Use `--address 127.0.0.1` flag + `PORT:443` — the `127.0.0.1:PORT:443` triple format is not supported in modern kubectl.

3. **ArgoCD admin password:** Three possible secrets depending on install method:
   - `argocd-initial-admin-secret` / `password` (Helm chart default)
   - `argocd-cluster` / `admin.password` (Argo CD Operator — plaintext)
   - `argocd-secret` / `admin.password` (bcrypt hash — not recoverable)

4. **Dual maintenance:** Both `bootstrap-capi.sh` and Go code must be updated for any behavior change.

5. **Workload kubeconfig:** Retrieved from management cluster Secret (`<cluster-name>-kubeconfig`), not from the workload directly. Always use `KUBECONFIG=<tmpfile>` env override when querying the workload.

6. **CAPI v1beta2 strict decoding:** Unknown fields in Cluster spec are rejected. All patches must target only the correct resource document.
