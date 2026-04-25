# AWS cost tiers — what does $X/month buy you?

bootstrap-capi's AWS provider ships an in-binary monthly cost
estimator (compute + EBS only, us-east-1 on-demand prices). Surface
it in any run via `--dry-run` — when `--infrastructure-provider aws`
the plan grows an "Estimated monthly cost" section.

The tiers below are smoke-tested against the same estimator
(`internal/provider/aws/cost.go`). All numbers exclude data egress,
NAT Gateway, Application/Network Load Balancer, Route53,
CloudWatch logs, IAM, and KMS — those are workload-shape dependent.
Spot pricing typically lops off another 60–70 %.

## Tiers

| Monthly bill | Config | What it buys |
|---:|---|---|
| **~$15** | 1× t4g.small (k3s, single-node) | Hobby cluster on a Graviton burstable. Workload + control-plane on one node; no HA. K3s required (kubeadm doesn't fit the 2 GiB RAM). |
| **~$31** | 1× t4g.small CP + 1× t4g.small worker | Smallest realistic 2-node K3s. Workload tolerates one VM reboot. No system-apps reserve room — system add-ons would have to share the worker with applications. |
| **~$100** | 1× t3.medium CP + 2× t3.medium workers (kubeadm) | Dev/test cluster. 2 vCPU + 4 GiB per node = workable kubeadm. Default bootstrap-capi system-apps reserve (2 cores / 4 GiB) leaves ~2 cores / 4 GiB for workload — enough to demo the bucket allocations. |
| **~$256** | 1× t3.large CP + 3× t3.large workers (kubeadm) | Team dev / staging. ~24 GiB / 8 vCPU total worker capacity; system reserve takes 4 GiB / 2 vCPU; remaining ~16 GiB / 6 vCPU split into db/obs/product thirds. |
| **~$622** | 3× t3.large CP (HA) + 3× m5.xlarge workers (kubeadm) | Small production. HA control plane (etcd survives 1 CP loss); workers have m5 sustained CPU. Storage: bump `WORKER_BOOT_VOLUME_SIZE` for any stateful workload — CSI handles persistence. |
| **~$2,700** | 3× m5.xlarge CP + 8× m5.2xlarge workers | Medium production. ~64 vCPU / 256 GiB worker capacity; system reserve a rounding error; observability + db each get ~85 GiB / 21 vCPU. |
| **~$7,600** | 3× m5.2xlarge CP + 12× m5.4xlarge workers | Large production. ~192 vCPU / 768 GiB worker capacity. Realistic for a multi-tenant Argo-deployed platform with SPIRE + observability + 30+ apps. |
| **~$114k** | 3× m5.4xlarge CP + 100× m5.8xlarge workers | "Big" tier (illustrative). 3,200 vCPU / 12.8 TiB workers. At this scale you'd want savings plans (50 %+ discount), spot for stateless workers, and probably mixed instance types. The estimator reports the on-demand sticker price. |

## Reading the dry-run output

```
$ bootstrap-capi --dry-run \
    --infrastructure-provider aws \
    --bootstrap-mode kubeadm \
    --control-plane-count 3 --worker-count 3 \
    --aws-control-plane-machine-type t3.large \
    --aws-node-machine-type m5.xlarge

▸ Estimated monthly cost (us-east-1 on-demand)
    • workload control-plane (t3.large)    3 × $60.74 = $182.22
    • CP boot volumes (40 GB gp3 each)     3 ×  $3.20 =   $9.60
    • workload workers (m5.xlarge)         3 × $140.16 = $420.48
    • worker boot volumes (40 GB gp3 each) 3 ×  $3.20 =   $9.60
    TOTAL: ~$621.90/month
    Note: us-east-1 on-demand prices, gp3 EBS only. Excludes NAT
    Gateway, ELB, data transfer, CloudWatch, Route53. Spot pricing
    typically saves 60-70 %.
```

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
