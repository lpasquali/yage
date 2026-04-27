# Per-architecture diagrams

One diagram per registered provider. Every diagram follows the same
shape so cross-provider differences read at a glance.

## The three-cluster model

yage always involves three clusters at different points in the run:

1. **transient kind cluster on the operator host** — a launcher.
   Hosts CAPI core + the chosen CAPI infrastructure provider + CAAPH
   long enough for `clusterctl init` and the management-cluster
   provision step.
2. **management cluster on the target provider** — the long-lived
   CAPI control plane. After `clusterctl move` pivots state from
   kind, kind is torn down and the management cluster takes over.
   Day-2 operations (machine reconciles, manifest apply, addon
   rollouts) hit this cluster.
3. **workload cluster on the target provider** — what user
   workloads land on. Always provisioned BY the management cluster,
   never directly by kind.

`--no-pivot` keeps the single-cluster shape (kind stays as the
management plane forever); `--stop-before-workload` exits after the
pivot, before the workload manifest is applied — useful for
integration tests against a clean managed CAPI plane.

```
LEGEND
  ┌──┐  on the operator host              ─→  control flow / clusterctl
  ╔══╗  on the target provider            ⇢  GitOps reconcile (Argo CD)
  •     phase the diagram emphasises
```

---

## Cloud architectures

### AWS (CAPA)

```
operator host                           AWS account
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │            ║ VPC + subnets                     ║
│   • CAPI · CAPA · CAAPH  │ clusterctl ║                                   ║
│   • bootstrap-only       │ init       ║   ╔══════════════════════╗        ║
│                          │ ─────────→ ║   ║ management cluster   ║        ║
│ yage drives: clusterctl, │            ║   ║   CAPI · CAPA · CAAPH║        ║
│ kubectl, helm, argocd    │  pivot     ║   ║   (post-pivot)       ║        ║
└──────────────────────────┘ ─────────→ ║   ╚════════╤═════════════╝        ║
       (kind torn down)                 ║            │ provisions           ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster     ║        ║
                                        ║   ║   N × EC2 (kubeadm)  ║        ║
                                        ║   ║   or EKS managed CP  ║        ║
                                        ║   ║   M × EC2 workers    ║        ║
                                        ║   ║   Cilium · aws-ebs   ║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   NAT GW · ALB/NLB · Route53     ║
                                        ║   CloudWatch · Internet egress   ║
                                        ╙──────────────────────────────────╜
        Modes: unmanaged (EC2) | eks (managed CP) | eks-fargate (CP+pods)
        Cost overhead tiers: dev | prod | enterprise (NAT, ALB, egress, CW logs)
```

### Azure (CAPZ)

```
operator host                           Azure subscription
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │ clusterctl ║ VNet + subnets                    ║
│   CAPI · CAPZ · CAAPH    │ init       ║                                   ║
└──────────────────────────┘ ─────────→ ║   ╔══════════════════════╗        ║
       (kind torn down)      pivot      ║   ║ management cluster   ║        ║
                             ─────────→ ║   ║   CAPI · CAPZ · CAAPH║        ║
                                        ║   ╚════════╤═════════════╝        ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster     ║        ║
                                        ║   ║   N × VM (kubeadm)   ║        ║
                                        ║   ║   or AKS managed CP  ║        ║
                                        ║   ║   workers VMSS       ║        ║
                                        ║   ║   Cilium · azure-disk║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   NAT · LB · CDN · DNS            ║
                                        ╙──────────────────────────────────╜
        Modes: unmanaged | aks
        Currency: Retail Prices API exposes 24 ISO codes via ?currencyCode=
```

### GCP (CAPG)

```
operator host                           GCP project
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │ clusterctl ║ VPC + subnets                     ║
│   CAPI · CAPG · CAAPH    │ init       ║                                   ║
└──────────────────────────┘ ─────────→ ║   ╔══════════════════════╗        ║
       (kind torn down)      pivot      ║   ║ management cluster   ║        ║
                             ─────────→ ║   ║   CAPI · CAPG · CAAPH║        ║
                                        ║   ╚════════╤═════════════╝        ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster     ║        ║
                                        ║   ║   GCE VMs (kubeadm)  ║        ║
                                        ║   ║   or GKE managed CP  ║        ║
                                        ║   ║   workers (MIG)      ║        ║
                                        ║   ║   Cilium · pd-csi    ║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   Cloud NAT · Cloud LB · DNS     ║
                                        ║   Internet egress                ║
                                        ╙──────────────────────────────────╜
        Modes: unmanaged | gke
```

### Hetzner Cloud (CAPHV)

```
operator host                           Hetzner project
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │ clusterctl ║                                   ║
│   CAPI · CAPHV · CAAPH   │ init       ║   ╔══════════════════════╗        ║
└──────────────────────────┘ ─────────→ ║   ║ management cluster   ║        ║
       (kind torn down)      pivot      ║   ║   CAPI · CAPHV · CAAPH        ║
                             ─────────→ ║   ╚════════╤═════════════╝        ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster     ║        ║
                                        ║   ║   cx-/cpx- VMs       ║        ║
                                        ║   ║   (kubeadm)          ║        ║
                                        ║   ║   Cilium · hcloud-csi║        ║
                                        ║   ║   Hetzner Volumes    ║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   Hetzner Load Balancer           ║
                                        ║   Floating IP                     ║
                                        ╙──────────────────────────────────╜
        Datacenter currency: EUR — yage shows the published EUR price
        directly when taller=EUR, FX-converts otherwise.
        Modes: unmanaged only.
```

### DigitalOcean (CAPDO)

```
operator host                           DO team
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │ clusterctl ║                                   ║
│   CAPI · CAPDO · CAAPH   │ init       ║   ╔══════════════════════╗        ║
└──────────────────────────┘ ─────────→ ║   ║ management cluster   ║        ║
       (kind torn down)      pivot      ║   ║   CAPI · CAPDO · CAAPH        ║
                             ─────────→ ║   ╚════════╤═════════════╝        ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster     ║        ║
                                        ║   ║   Droplets           ║        ║
                                        ║   ║   Cilium · do-csi    ║        ║
                                        ║   ║   DO Volumes / Spaces║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   DO Load Balancer                ║
                                        ╙──────────────────────────────────╜
```

### Linode / Akamai (CAPL)

```
operator host                           Akamai/Linode account
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │ clusterctl ║                                   ║
│   CAPI · CAPL · CAAPH    │ init       ║   ╔══════════════════════╗        ║
└──────────────────────────┘ ─────────→ ║   ║ management cluster   ║        ║
       (kind torn down)      pivot      ║   ║   CAPI · CAPL · CAAPH║        ║
                             ─────────→ ║   ╚════════╤═════════════╝        ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster     ║        ║
                                        ║   ║   Linode VMs         ║        ║
                                        ║   ║   Cilium · linode-csi║        ║
                                        ║   ║   Block / Object Sto.║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   NodeBalancer · DNS              ║
                                        ╙──────────────────────────────────╜
```

### Oracle Cloud (CAPOCI)

```
operator host                           OCI tenancy
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │ clusterctl ║ VCN + subnets                     ║
│   CAPI · CAPOCI · CAAPH  │ init       ║                                   ║
└──────────────────────────┘ ─────────→ ║   ╔══════════════════════╗        ║
       (kind torn down)      pivot      ║   ║ management cluster   ║        ║
                             ─────────→ ║   ║   CAPI · CAPOCI · CAAPH       ║
                                        ║   ╚════════╤═════════════╝        ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster     ║        ║
                                        ║   ║   Compute VMs        ║        ║
                                        ║   ║   E4.Flex (OCPU+mem) ║        ║
                                        ║   ║   Cilium · oci-bv-csi║        ║
                                        ║   ║   Block Volumes      ║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   OCI Load Balancer               ║
                                        ╙──────────────────────────────────╜
        Currency: cetools API supports ?currencyCode= for ~15 ISO codes;
        yage asks in active taller when supported, falls back to USD.
```

### IBM Cloud (CAPIBM)

```
operator host                           IBM Cloud account
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │ clusterctl ║ VPC Gen2                          ║
│   CAPI · CAPIBM · CAAPH  │ init       ║                                   ║
└──────────────────────────┘ ─────────→ ║   ╔══════════════════════╗        ║
       (kind torn down)      pivot      ║   ║ management cluster   ║        ║
                             ─────────→ ║   ║   CAPI · CAPIBM · CAAPH       ║
                                        ║   ╚════════╤═════════════╝        ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster     ║        ║
                                        ║   ║   bx2-/cx2- VSI      ║        ║
                                        ║   ║   Cilium · ibm-vpc-csi        ║
                                        ║   ║   Block · COS        ║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   ALB / NLB · DNS Svc             ║
                                        ╙──────────────────────────────────╜
        Auth: IAM API key → bearer token (pricing.ibmExchangeAPIKey).
        Currency: amounts[] rows per ISO code; yage reads active taller's row.
```

---

## On-prem architectures

### Proxmox VE (CAPMOX) — the most-mature path

```
operator host                           Proxmox cluster (LAN)
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │            ║ Datacenter / nodes                ║
│   CAPI · CAPMOX · CAAPH  │            ║                                   ║
│   in-cluster IPAM        │ identity   ║   pveapi: HA / HW info            ║
│   OpenTofu (BPG)         │ flow:      ║                                   ║
│     · CAPI user/token    │ create     ║   ╔══════════════════════╗        ║
│     · CSI user/token     │ users +    ║   ║ management cluster VM║        ║
│                          │ tokens     ║   ║   CAPI · CAPMOX      ║        ║
│   yage drives: clusterctl, kubectl,   ║   ║   CAAPH              ║        ║
│   helm, argocd, OpenTofu              ║   ╚════════╤═════════════╝        ║
└──────────────────────────┘            ║            ▼                      ║
       (kind torn down post-pivot)      ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster VMs ║        ║
                                        ║   ║   N × CP (cloud-init)║        ║
                                        ║   ║   M × workers        ║        ║
                                        ║   ║   Cilium (kpr+L2 LB) ║        ║
                                        ║   ║   Proxmox CSI →      ║        ║
                                        ║   ║   PVE storage        ║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   bridge net (vmbr0)              ║
                                        ║   IPAM CIDR · gateway             ║
                                        ╙──────────────────────────────────╜
        TCO: --hardware-cost-usd / --hardware-watts /
             --hardware-kwh-rate-usd / --hardware-support-usd-month —
             capex amortized + power + flat support.
```

### vSphere (CAPV)

```
operator host                           vCenter
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │ clusterctl ║ Datacenter / cluster              ║
│   CAPI · CAPV · CAAPH    │ init       ║                                   ║
└──────────────────────────┘ ─────────→ ║   ╔══════════════════════╗        ║
       (kind torn down)      pivot      ║   ║ management cluster VM║        ║
                             ─────────→ ║   ║   CAPI · CAPV · CAAPH║        ║
                                        ║   ╚════════╤═════════════╝        ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster VMs ║        ║
                                        ║   ║   from VM template   ║        ║
                                        ║   ║   Cilium · vsphere-csi        ║
                                        ║   ║   vSAN / VMFS        ║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   NSX / portgroups                ║
                                        ╙──────────────────────────────────╜
        TCO: same flags as Proxmox; surface vSphere licensing via
             --hardware-support-usd-month.
```

### OpenStack (CAPO)

```
operator host                           OpenStack tenant
┌──────────────────────────┐            ╓──────────────────────────────────╖
│ transient kind launcher  │ clusterctl ║ Project / network                 ║
│   CAPI · CAPO · CAAPH    │ init       ║                                   ║
└──────────────────────────┘ ─────────→ ║   ╔══════════════════════╗        ║
       (kind torn down)      pivot      ║   ║ management cluster   ║        ║
                             ─────────→ ║   ║   Nova VM            ║        ║
                                        ║   ║   CAPI · CAPO · CAAPH║        ║
                                        ║   ╚════════╤═════════════╝        ║
                                        ║            ▼                      ║
                                        ║   ╔══════════════════════╗        ║
                                        ║   ║ workload cluster     ║        ║
                                        ║   ║   Nova VMs           ║        ║
                                        ║   ║   Cilium · cinder-csi║        ║
                                        ║   ║   Argo CD            ║ ⇢ apps ║
                                        ║   ╚══════════════════════╝        ║
                                        ║   Octavia LB · Neutron            ║
                                        ╙──────────────────────────────────╜
        Pricing: operator-dependent. Each public OpenStack publishes its
        own catalog; the cost path returns ErrNotApplicable until users
        wire a per-operator fetcher under internal/pricing/os-<operator>.go.
```

### Docker (CAPD) — ephemeral test path

CAPD is the one provider that does NOT pivot. The whole point is an
ephemeral, in-memory test cluster on the operator host — there's no
target provider to move state to. The kind cluster is the management
cluster forever; the "workload" is a sibling kindest/node container
on the same Docker daemon.

```
operator host
┌────────────────────────────────────────────────────────────────┐
│ kind (mgmt) ──────────────────→ ╔══════════════════════════╗   │
│   CAPI · CAPD · CAAPH           ║ workload cluster          ║   │
│                                 ║   N × kindest/node        ║   │
│                                 ║   containers (CP+wk)      ║   │
│                                 ║   Cilium                  ║   │
│                                 ║   no real CSI             ║   │
│                                 ║   Argo CD (in-memory)     ║   │
│                                 ╚══════════════════════════╝   │
└────────────────────────────────────────────────────────────────┘
        Free; ErrNotApplicable on the cost path. Excluded from
        cost-compare. Useful for demos and CI where Docker is the
        only available substrate.
```
