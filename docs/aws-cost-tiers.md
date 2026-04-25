# AWS cost tiers — what does $X/month buy you?

bootstrap-capi's AWS provider ships an in-binary monthly cost
estimator that includes both **compute** (EC2/EBS/Fargate/EKS-CP)
**and the AWS service overhead** a CAPA-bootstrapped cluster pulls
in: NAT Gateway, ALB/NLB, CloudWatch Logs, Route53, internet egress.
Surfaced via `--dry-run` when `--infrastructure-provider aws`.

The overhead is parameterised by `--aws-overhead-tier`:

| Tier | NAT GWs | ALBs | NLBs | Egress (GB/mo) | CloudWatch (GB/mo) | Route53 zones |
|---|---:|---:|---:|---:|---:|---:|
| `dev` | 0 (public subnets) | 1 | 0 | 20 | 2 | 0 |
| `prod` (default) | 1 | 1 | 0 | 100 | 10 | 1 |
| `enterprise` | 3 (multi-AZ HA) | 2 | 1 | 500 | 50 | 2 |

Per-component overrides on top of the tier:
`--aws-nat-gateway-count N`, `--aws-alb-count N`, `--aws-nlb-count N`,
`--aws-data-transfer-gb N`, `--aws-cloudwatch-logs-gb N`.

Still excluded (workload-shape dependent and out-of-scope for
bootstrap-time planning): per-volume EBS snapshots, ECR storage,
KMS, AWS Backup, GuardDuty / Security Hub / WAF / Shield, Inspector,
Config, Secrets Manager line items beyond what the cluster spawns.
Spot pricing not modeled (typical 60-70 % off EC2; ~70 % off
Fargate Spot).

## Tiers (compute + dev overhead)

Numbers below pair compute with the matching `--aws-overhead-tier`:
small dev clusters use `dev` (no NAT, 1 ALB only); production tiers
use `prod` (1 NAT GW, 1 ALB, CloudWatch, Route53). Switch tiers on
the same compute to see overhead grow.

| Monthly | Config | Tier | What it buys |
|---:|---|---|---|
| **~$116** | 1× t3.medium CP + 1× t3.medium worker (kubeadm) | dev | Smallest sane dev cluster: 1 ALB ($46/mo) eats most of the bill above the ~$67 EC2+EBS. K3s + 1 t4g.small isn't worth saving compute when 1 ALB still costs $46. |
| **~$716** | 3× t3.large CP HA + 3× m5.xlarge workers | prod | Small production with NAT + ALB + CloudWatch + Route53 (~$94 of overhead on top of the ~$622 compute). |
| **~$2,146** | 3× m5.xlarge CP + 5× m5.2xlarge workers | enterprise | 3 NAT GWs + 2 ALBs + 1 NLB + heavy CloudWatch + 500 GB egress = ~$300 of overhead on top of compute. Multi-AZ HA. |
| **~$8,000** | 3× m5.2xlarge CP + 12× m5.4xlarge workers | enterprise | Large production. Compute dominates (~$7,600); overhead a rounding error at this scale. |
| **~$114k+** | 3× m5.4xlarge CP + 100× m5.8xlarge workers | enterprise | Big. Savings plans + spot mandatory at this scale; on-demand sticker is the worst case. |

## Reading the dry-run output

A real plan now shows compute *and* overhead lines:

```
▸ Estimated monthly cost (provider: aws)
    EKS managed control plane (flat per cluster)        ~$  73.00
    workload workers (EKS Managed Node Group) (m5.xlarge)  ~$ 420.48
    worker boot volumes (40 GB gp3 each)                ~$   9.60
    NAT Gateway (~30 GB processed/mo each)              ~$  34.20
    Application Load Balancer (Argo CD ingress / app)   ~$  45.62
    Internet egress (~100 GB/mo)                        ~$   9.00
    CloudWatch Logs (10 GB ingested/mo)                 ~$   5.30
    Route53 hosted zones                                ~$   0.50
    TOTAL: ~$597.71/month
```

Switching to `--aws-overhead-tier dev` on the same compute drops
the overhead to ~$48 (1 ALB, no NAT, less CloudWatch); switching to
`enterprise` raises it to ~$300+ (3 NATs, 2 ALBs, 1 NLB, 500 GB
egress, 50 GB CloudWatch).

## Sizing for a budget — back-of-envelope

You can flip the math to start from the budget:

```
budget                       suggested config
$10/month   → not feasible on-demand. Try a t4g.nano spot ($1-3) for an exploratory
              single-node K3s; expect crashes and no HA.
$30/month   → 1 CP + 1 worker, t4g.small (k3s), no HA, no system-apps reserve.
$100/month  → 1 CP + 2 workers, t3.medium (kubeadm), single-CP risk acceptable.
$500/month  → 1 CP + 3 workers, t3.large (kubeadm) with default 2-core system reserve.
$1k/month   → 3 CP + 3-5 workers, mix t3.large CP + m5.xlarge workers (HA).
$5k/month   → 3 CP + 8 workers, m5.xlarge CP + m5.2xlarge workers (production-grade).
$10k/month  → 3 CP + 12 workers, m5.2xlarge CP + m5.4xlarge workers.
$100k/month → 3 CP + ~100 m5.8xlarge workers (or fewer .16xlarge boxes for fewer ENIs).
              At this tier savings plans + spot become mandatory; on-demand pricing is
              the worst-case sticker.
```

## Capacity preflight against AWS

The orchestrator's capacity preflight (the `--resource-budget-fraction
0.6667` check) doesn't run against AWS today — `provider.aws.Capacity`
returns `ErrNotApplicable` and the orchestrator falls back to "skip
preflight, trust the user". That's deliberate for the stub: AWS
quotas are per-account + per-region + per-instance-family and the
right capacity backend is the EC2 Service Quotas API. Future work,
documented in the AWS provider package doc.

For now: the cost estimator is the practical "what does this run
cost?" check. Pair it with `--dry-run` before any real run.

## EKS + Fargate (managed flavors)

`--aws-mode` switches the CAPA flavor and the cost shape:

| Mode | What it provisions | CP cost | Worker cost |
|---|---|---|---|
| `unmanaged` (default) | Self-managed Kubernetes on EC2 | `cp_count × instance_price` + EBS | `worker_count × instance_price` + EBS |
| `eks` | EKS managed CP + EC2 Managed Node Group | **flat $73/month** per cluster | same as unmanaged worker EC2 (you still pay for the nodes) |
| `eks-fargate` | EKS managed CP + serverless Fargate workers | **flat $73/month** per cluster | per-pod-hour: `pods × (vCPU × $29.55 + GiB × $3.24)` |

The same logical workload (3-CP HA + 3 m5.xlarge workers) at each tier:

| Mode | Monthly | Notes |
|---:|---:|---|
| unmanaged HA | **~$622** | 3 self-managed CP nodes (t3.large) + 3 m5.xlarge workers |
| EKS + Managed Node Group | **~$503** | $73 CP saves you the 3-CP EC2 cost (~$182) but you pay for it as the EKS flat fee + lose CP customization |
| EKS + Fargate, 10 pods × 0.5 vCPU / 1 GiB | **~$253** | Pay-for-what-runs: cheaper if you really only run 10 small pods |
| EKS + Fargate, 50 pods × 1 vCPU / 2 GiB | **~$1,875** | Crosses over EC2 break-even fast as pod count + size grow |
| EKS + Fargate, 200 pods × 0.5 vCPU / 1 GiB | **~$3,677** | At 200 pods Fargate is ~6× more expensive than equivalent EC2 |

**Break-even rule of thumb**: Fargate is cheaper than EC2 for **low pod count + small pods**; EC2 (with proper bin-packing) wins for **dense, sustained workloads**. The crossover depends on pod size; for 1 vCPU / 2 GiB pods the crossover is ~10–12 pods (above which the equivalent m5.xlarge fleet is cheaper).

**Tiny dev clusters** that fit best on EKS + Fargate:
- `--aws-mode eks-fargate --aws-fargate-pod-count 5 --aws-fargate-pod-cpu 0.25 --aws-fargate-pod-memory-gib 0.5` → **~$118/month** ($73 CP + ~$45 for 5 small pods). Cheaper than running 1 t3.medium worker.

**Why pick EKS at all?** No CP maintenance — AWS patches etcd / apiserver / scheduler, runs HA across 3 AZs by default, gives you a 99.95 % SLA. Worth the $73 for any workload where CP downtime = cost. Combine with `--bootstrap-mode kubeadm` (K3s isn't compatible with EKS — EKS *is* upstream Kubernetes managed by AWS).

**Caveats** the estimator doesn't model:
- Fargate has a 50%-up CPU/memory charge for arm64 vs x86 — we estimate x86.
- Fargate Spot exists for non-critical workloads (~70 % discount).
- EKS auto-mode (newer feature) bundles compute into the managed CP fee — not modeled today.
- ELB Ingress on EKS = $16-25/month per LoadBalancer; not in the estimate.

## Other providers

- **Proxmox**: self-hosted, no monthly cost from the cloud — your
  homelab electricity bill is your bill. Estimator returns
  `ErrNotApplicable`.
- **OpenStack**: depends on whether you're on a public OpenStack
  (where prices vary by operator) or private. `ErrNotApplicable`
  by default; users can wire their own.
- **vSphere**: license + hardware — also self-hosted. `ErrNotApplicable`.
- **CAPD (Docker)**: free locally. `ErrNotApplicable`.
- **GCP / Azure / Hetzner / Linode / DigitalOcean / Equinix**: when
  those provider stubs land, each can implement
  `EstimateMonthlyCostUSD` against the equivalent in-binary price
  table. Pattern follows `internal/provider/aws/cost.go`.
