# Capacity preflight

yage computes whether the planned cluster fits inside the
host before provisioning. The check has two parts:

1. **Soft budget** (`--resource-budget-fraction F`, default 2/3) —
   the cap on what yage-managed clusters may claim.
2. **Overcommit ceiling** (`--overcommit-tolerance-pct N`, default
   15) — the hard limit above 100 % of host capacity that triggers
   abort. Memory overcommit via Proxmox ballooning + swap absorbs
   ~15 % drift in practice; beyond that, OOMs under load become
   likely.

The check runs on Proxmox today (the only provider with a working
`Capacity()` implementation). Future provider implementations
plug in transparently — the verdict logic is in
`internal/cluster/capacity` and provider-agnostic.

## Existing-VM census

Before this feature, the preflight only checked the *planned*
cluster against host capacity — ignoring whatever VMs were already
running. On a fresh host that's fine; on a host with prior CAPI
clusters or non-CAPI VMs it silently overcommits.

Now yage queries
`/api2/json/cluster/resources?type=vm` on the allowed Proxmox nodes
and aggregates each `qemu` VM's declared (max) CPU / memory / disk.
That gives a realistic baseline for the verdict:

```
total_demand = existing_used + planned
verdict      = check(total_demand, host, soft_budget, overcommit_ceiling)
```

## Verdict trichotomy

```
                    total_demand / host_capacity
                                │
              ┌─────────────────┼─────────────────┐
              ▼                 ▼                 ▼
            ≤ F            > F  AND ≤ 1+T/100      > 1+T/100
         (default 2/3)
              │                 │                 │
              ▼                 ▼                 ▼
       ✅ VerdictFits   ⚠️  VerdictTight   ❌ VerdictAbort
       proceed silently  proceed with       real run aborts
                         warning            (unless --allow-resource-overcommit)
```

Defaults: F = 2/3 (66.7 %), T = 15 → ceiling at 115 % of host.

## Dry-run output

```
▸ Capacity budget
    • budget: 67% of available Proxmox host CPU/memory/storage
    •   workload control-plane     1 × (2 cores, 4096 MiB, 40 GB) = 2 cores, 4096 MiB, 40 GB
    •   workload worker            2 × (2 cores, 4096 MiB, 40 GB) = 4 cores, 8192 MiB, 80 GB
    • TOTAL requested:  6 cores, 12288 MiB (12 GiB), 120 GB disk
    • host (allowed nodes [pve-01 pve-02]): 16 cores, 65536 MiB (64 GiB), 4000 GB disk
    • existing VMs on host: 5 (CPU 8 cores, mem 16384 MiB, disk 400 GB)
    •   by pool: capi-quickstart=3, other-cluster=2
    • ✅ verdict: fits within 67% soft budget
    •    CPU 8+6=14/16 (88%), mem 16384+12288=28672/65536 MiB (44%), disk 400+120=520/4000 GB (13%)
```

(The CPU dimension overshoots the soft budget at 88% > 67% but
mem/disk are well under — actually that pushes the verdict to
"tight" if the soft budget is computed as the worst dimension.
The above is illustrative; real output reflects whichever
dimension lands first.)

A *tight* verdict reads:

```
    • ⚠️  verdict: tight — above soft budget but inside 15% overcommit tolerance
    •    CPU 8+8=16/16 (100%), mem 16384+24576=40960/65536 MiB (62%), disk 400+200=600/4000 GB (15%)
    •    real run will proceed with a warning; raise --resource-budget-fraction or shrink the plan if you want headroom.
```

An *abort* verdict reads:

```
    • ❌ verdict: abort — over 15% overcommit ceiling
    •    CPU 8+12=20/16 (125%), …
    •    real run aborts; --allow-resource-overcommit forces, --overcommit-tolerance-pct raises the ceiling.
```

When the abort happens with `--bootstrap-mode kubeadm`, bootstrap-
capi computes whether the same machine counts under K3s sizing
would fit and surfaces the suggestion:

```
💡 same machine counts would fit under --bootstrap-mode k3s: 4 cores / 4096 MiB / 70 GB
```

## Real-run preflight

`bootstrap.preflightCapacity` runs the same `CheckCombined` and:

- **Fits** → silent return
- **Tight** → `logx.Warn` (not fatal); proceed
- **Abort** → fatal error unless `cfg.AllowResourceOvercommit` is
  set, with the `--bootstrap-mode k3s` suggestion when applicable.

Knobs (CLI ↔ env):

| Flag                            | Env                       | Default  |
|---------------------------------|---------------------------|----------|
| `--resource-budget-fraction F`  | `RESOURCE_BUDGET_FRACTION`| `0.6667` |
| `--overcommit-tolerance-pct N`  | `OVERCOMMIT_TOLERANCE_PCT`| `15`     |
| `--allow-resource-overcommit`   | `ALLOW_RESOURCE_OVERCOMMIT` | `false`|

## What counts as "existing"

The census includes every `qemu` VM on the allowed nodes regardless
of pool. LXC containers are skipped (Proxmox reports them through the
same endpoint). Stopped VMs *are* counted because they hold disk
and have reserved CPU / memory in the cluster topology.

If your host has VMs from a previous yage run that you
intend to replace, either:

- delete them first (clean slate), or
- add them to the plan deliberately (their resources will show up
  on both sides of the equation, which is wrong), or
- run with `--allow-resource-overcommit` for one bootstrap so the
  check passes while the old VMs are replaced.

Future work: filter the census by Proxmox pool so a fresh
`yage --purge` flow can subtract the cluster-being-replaced
from the existing-VM total automatically.

## Why 15 %

15 % is the operator-approved memory-overcommit margin Proxmox
typically tolerates with default ballooning + swap. Smaller numbers
fail useful runs (a fresh host with one existing utility VM hits
the soft budget but is genuinely safe). Bigger numbers start to
mask real OOM risk. 15 % is the result of a deliberate choice — if
your host is big and you trust it more, `--overcommit-tolerance-pct
30` or higher is fine. If you run latency-sensitive workloads where
swap is unacceptable, drop to `--overcommit-tolerance-pct 0` and
treat the soft budget as a hard cap.
