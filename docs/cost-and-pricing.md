# Cost & pricing

yage can answer two questions at planning time, both
without provisioning anything:

1. **What does this cluster cost per month** on the configured cloud?
2. **What does the same logical cluster cost on every other cloud** —
   so you can pick the cheapest viable target?

Every monetary value is fetched **live** from the vendor's billing
or catalog API at the moment of planning. There are no hardcoded
$/hour or $/GB-month numbers anywhere in the binary. When a vendor
API is unreachable, the relevant cell shows "estimate unavailable"
rather than a stale fabricated figure.

## Architecture in one diagram

```
                       cfg.InfraProvider
                              │
                              ▼
          internal/provider/<vendor>/cost.go
                              │
                  pricing.Fetch(vendor, sku, region)
                              │
              internal/pricing/<vendor>.go (Fetcher impl)
                              │
                              ▼
                ┌──────────────────────────────────┐
                │  vendor catalog / billing        │
                │  AWS Bulk JSON • Azure Retail    │
                │  GCP Catalog (key) • Hetzner     │
                │  DO • Linode • OCI • IBM         │
                │  fetcher asks in active taller   │
                │  when the vendor supports it     │
                └──────────────────────────────────┘
                              │
                              ▼
       Item{USDPerHour, USDPerMonth,
            NativeCurrency, NativeAmount, FetchedAt}
                              │
                  pricing.FormatTaller(amount, native)
                              │
              ┌────────────────────────────────┐
              │ FX from open.er-api.com (24h   │
              │ disk cache); active taller     │
              │ from --data-center-location,   │
              │ YAGE_TALLER_CURRENCY, or geo   │
              └────────────────────────────────┘
                              │
                              ▼
                     formatted display
                  (USD fallback on FX failure)
```

## Pricing sources

| Vendor       | API                                              | Auth                              |
|--------------|--------------------------------------------------|-----------------------------------|
| AWS          | Bulk Pricing JSON `/offers/v1.0/aws/<svc>/current/<region>/index.json` | anonymous (S3 read) |
| Azure        | Retail Prices API `prices.azure.com/api/retail/prices?$filter=...`     | anonymous           |
| GCP          | Cloud Billing Catalog `cloudbilling.googleapis.com/v1/services/<id>/skus` | API key (free)   |
| Hetzner      | `api.hetzner.cloud/v1/server_types` + `/v1/pricing` | project API token (read scope)  |
| DigitalOcean | `api.digitalocean.com/v2/sizes`                  | personal API token (read scope)   |
| Linode       | `api.linode.com/v4/linode/types`                 | anonymous catalog                 |
| OCI          | `apexapps.oracle.com/.../products` (Cost Estimator JSON) | anonymous                 |
| IBM Cloud    | `globalcatalog.cloud.ibm.com` via IAM bearer     | API key → IAM /identity/token     |

**All of these calls are free**. None of them appear on your bill.
Don't confuse the AWS Pricing API (free) with AWS Cost Explorer
(`ce:GetCostAndUsage` — $0.01/request). yage never touches
Cost Explorer.

## First-run onboarding hint

When the cost path runs and detects no credentials configured for a
vendor, yage prints the exact CLI snippet you'd run to
create a minimum-permission identity for that vendor's pricing API.
Shown ONCE per vendor per cache (sentinel at
`~/.cache/yage/pricing/.onboarded-<vendor>`); replay any
time:

```bash
yage --print-pricing-setup aws        # one vendor
yage --print-pricing-setup all        # every vendor that needs setup
```

Knobs:

- `YAGE_NO_PRICING_ONBOARDING=1` — silence permanently
- `YAGE_FORCE_PRICING_ONBOARDING=1` — always re-display

Hint content per vendor (excerpts):

- **AWS** — `aws iam create-user / put-user-policy / create-access-key`
  for an IAM user with `pricing:GetProducts` only
- **GCP** — `gcloud projects create … && gcloud services enable
  cloudbilling.googleapis.com && gcloud alpha services api-keys create`
- **DigitalOcean** — control-panel walkthrough for a Read-scoped token
- **IBM Cloud** — `ibmcloud iam service-id-create` with Viewer policy
  on `globalcatalog`
- **Hetzner** — Console walkthrough for read-scoped project token
- **Azure / Linode / OCI** — none needed (anonymous catalogs)

## Taller — display currency abstraction

"Taller" is yage's display currency. Every vendor has a **datacenter
currency** — the currency in which their pricing team publishes the
canonical number (USD for AWS/Azure/GCP/DO/Linode/IBM/OCI, EUR for
Hetzner). yage fetches prices in the taller directly when the vendor's
API supports it (Azure `?currencyCode=`, OCI `?currencyCode=`, IBM
amounts[]); otherwise it converts datacenter-currency → taller via
the FX layer. Either way, the user's display currency is the taller.

### Resolution order for the active taller

1. `YAGE_TALLER_CURRENCY` env or `cfg.Cost.Currency.DisplayCurrency`
   (explicit ISO-4217 override)
2. `--data-center-location <CC>` / `YAGE_DATA_CENTER_LOCATION`:
   ISO-3166 alpha-2 country code → ISO currency
3. Geo-IP via `ip-api.com/json` → ISO country → ISO currency
4. USD fallback (with a notice in the dry-run header)

If any step picks a currency outside `pricing.SupportedCurrencies()`
(yage covers ~50 top global currencies), the resolver falls back to
USD and prints `pricing.UnsupportedCurrencyMessage()` asking the user
to file an issue requesting that currency.

### FX

FX is fetched once per 24h from `open.er-api.com/v6/latest/USD` (free,
auth-free), cached on disk at `~/.cache/yage/pricing/fx-USD.json`.
When the FX rate for the active taller is missing, every monetary
value falls back to USD with a `(FX <CCY> unavailable, USD)` tag —
the UI never goes blank. There is no per-currency override knob;
open.er-api.com supplies every supported code.

### Display sample (taller selected via `--data-center-location IT`)

```
🌍 MULTI-CLOUD COST COMPARISON — same cluster shape, every registered provider
    (every monetary value is live from the vendor's billing API;
     fetched directly in the taller when the vendor supports it,
     FX-converted otherwise)
    taller currency: EUR (--data-center-location IT)
─────────────────────────────────────────────────────────────────────────────
  provider         monthly €    €/GB-mo (live)  max retention if budget ÷ storage
  hetzner             €39.61         €0.044     900 GB / month if all storage
  linode              €61.48         (unavail)  (unpriced)
  aws                      —                 —  (estimator: …)
  …
```

### Override

```bash
YAGE_TALLER_CURRENCY=JPY yage --dry-run --cost-compare
# → monthly ¥, ¥/GB-mo
```

## Cross-cloud comparison

`--cost-compare` runs every registered provider's
`EstimateMonthlyCostUSD` against the same logical cluster shape and
prints a side-by-side table sorted by total. `--budget-usd-month N`
adds a "if you spent that on storage instead, X GB" retention column
per cloud — useful for deciding which target gives the widest
persistence envelope.

```bash
yage --dry-run --cost-compare --budget-usd-month 500
```

When a cloud's estimate fails (no creds, API down, SKU not in
catalog), the row shows the specific failure reason so you know
exactly what's missing — never a fabricated number.

## Self-hosted: TCO via operator-supplied inputs

Proxmox and vSphere have no vendor pricing API; the cost is sunk
hardware + electricity + (for vSphere) licensing. The TCO path lets
the operator supply those numbers and yage computes the
amortized monthly:

```bash
yage --dry-run \
  --hardware-cost-usd 5000 \
  --hardware-useful-life-years 5 \
  --hardware-watts 200 \
  --hardware-kwh-rate-usd 0.20 \
  --hardware-support-usd-month 0
```

Yields:

```
▸ Estimated monthly cost (provider: proxmox)
    • taller currency: EUR (geo: IT)
    • Hardware amortized capex ($5000 over 5.0y) 1 × €71.16 = €71.16
    • Electricity (200W @ $0.200/kWh)            1 × €24.95 = €24.95
    • TOTAL: ~€96.12 / month (EUR)
    • note: Self-hosted TCO (no vendor pricing API): capex amortized
            straight-line over 5.0y, power at operator-supplied watts
            × kWh rate, plus any flat support figure.
```

Math:

```
capex/month   = HardwareCostUSD / (UsefulLifeYears × 12)
power/month   = (Watts / 1000) × 24 × 30.4375 × KWHRateUSD
support/month = HardwareSupportUSDMonth (operator flat figure)
total         = sum
```

Without `--hardware-cost-usd`, both providers return
`ErrNotApplicable` and the cost section politely says "supply the
flag to enable". No defaults — yage never invents the
hardware cost of an on-prem cluster.

## AWS overhead tiers

CAPA-bootstrapped clusters pull in dependencies beyond raw EC2/EBS:
NAT Gateway, ALB/NLB, CloudWatch Logs, Route53, internet egress.
The overhead is parameterised by `--aws-overhead-tier`:

| Tier         | NAT GWs | ALBs | NLBs | Egress (GB/mo) | CloudWatch (GB/mo) | Route53 zones |
|--------------|--------:|-----:|-----:|---------------:|-------------------:|--------------:|
| `dev`        | 0 (public subnets) | 1 | 0 |  20 |  2 | 0 |
| `prod` (def) | 1 | 1 | 0 | 100 | 10 | 1 |
| `enterprise` | 3 (multi-AZ HA) | 2 | 1 | 500 | 50 | 2 |

Per-component overrides:
`--aws-nat-gateway-count N`, `--aws-alb-count N`, `--aws-nlb-count N`,
`--aws-data-transfer-gb N`, `--aws-cloudwatch-logs-gb N`.

Counts are *shape* — they describe the architecture and stay
constant across price changes. The $/unit comes live from
`pricing.AWSNATGatewayPricing` / `AWSApplicationLBPricing` /
`AWSCloudWatchLogsPricing` / etc.

Excluded (workload-shape dependent and out-of-scope at planning
time): EBS snapshots, ECR storage, KMS, AWS Backup, GuardDuty /
Security Hub / WAF / Shield, Inspector, Config, Secrets Manager
items the cluster spawns later. Spot pricing not modeled (typical
60–70% off EC2; ~70% off Fargate Spot).

## AWS managed flavors

`--aws-mode` switches the CAPA flavor and the cost shape:

| Mode          | Provisions                       | CP cost                              | Worker cost                                |
|---------------|----------------------------------|--------------------------------------|--------------------------------------------|
| `unmanaged`   | self-managed K8s on EC2          | `cp_count × instance_price` + EBS    | `worker_count × instance_price` + EBS      |
| `eks`         | EKS managed CP + EC2 Node Group  | live flat hourly fee per cluster     | same as unmanaged worker EC2               |
| `eks-fargate` | EKS managed CP + Fargate workers | live flat hourly fee per cluster     | per-pod-hour: `pods × (vCPU + GiB) × live` |

EKS modes require `--bootstrap-mode kubeadm` (EKS *is* upstream
Kubernetes managed by AWS — K3s isn't compatible).

Fargate sizing is parameterised:

```bash
--aws-mode eks-fargate \
  --aws-fargate-pod-count 50 \
  --aws-fargate-pod-cpu 1 \
  --aws-fargate-pod-memory-gib 2
```

## Azure / GCP / Hetzner managed flavors

Symmetric to AWS:

```bash
--azure-mode unmanaged|aks
--gcp-mode    unmanaged|gke
# Hetzner is unmanaged-only — CAPHV doesn't model managed K8s today
```

`aks` / `gke` flip the CP from N× VMs to a flat hourly fee
(live-priced from each vendor's catalog). Same `--azure-overhead-tier`
/ `--gcp-overhead-tier` knobs as AWS.

## Other providers

- **Proxmox / vSphere** — self-hosted, TCO path above.
- **OpenStack** — operator-dependent. No standard pricing API
  across operators (each public OpenStack vendor publishes their own).
  Estimate returns `ErrNotApplicable`; users wire their own per-operator
  fetcher under `internal/pricing/os-<operator>.go` if they need it.
- **Docker (CAPD)** — ephemeral in-memory test clusters. Free.
  `ErrNotApplicable`.

## Why this approach

The "no hardcoded money numbers" rule comes from a real failure
mode in cost-aware tooling: hardcoded tables drift, nobody refreshes
them, and confidence in the planner erodes. By reading live from
each vendor's catalog, yage's plan is as fresh as the
catalog itself. When a catalog is unreachable, the operator sees
"unavailable" and a hint for fixing it — better than confidently
showing a number that's two years stale.
