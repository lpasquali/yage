# Forking CAPMOX to add a K3s flavor

`bootstrap-capi --bootstrap-mode k3s` initializes the K3s providers
(KCP-K3s + CABK3s) via `clusterctl init`, then asks clusterctl to
generate the workload + management manifests with `--flavor k3s`. The
infrastructure side of that template — `ProxmoxMachineTemplate` plus
the wiring that points `KThreesControlPlane` and `KThreesConfigTemplate`
at it — has to come from the CAPMOX provider's release artifacts.

CAPMOX upstream (`ionos-cloud/cluster-api-provider-proxmox`) does
**not** ship a K3s flavor today. Until it does, we host the K3s
template in a fork.

## Prerequisites

- Push access to a GitHub fork (e.g. `lpasquali/cluster-api-provider-proxmox`).
- The Go + container build toolchain CAPMOX needs (see CAPMOX `Makefile`).
- An OCI registry to push the controller image, OR you can keep using
  the upstream image and only override the templates.

## Step 1 — fork CAPMOX

```bash
gh repo fork ionos-cloud/cluster-api-provider-proxmox \
  --clone --remote --org lpasquali
cd cluster-api-provider-proxmox
git checkout -b k3s-flavor
```

## Step 2 — drop in the K3s template

Copy `docs/capmox-k3s-fork/cluster-template-k3s.yaml` from this repo
into the fork at `templates/cluster-template-k3s.yaml`:

```bash
cp ~/Devel/bootstrap-capi/docs/capmox-k3s-fork/cluster-template-k3s.yaml \
   ~/cluster-api-provider-proxmox/templates/cluster-template-k3s.yaml
```

The template wires:
- `KThreesControlPlane` → `ProxmoxMachineTemplate` (control-plane)
- `MachineDeployment` → `KThreesConfigTemplate` + `ProxmoxMachineTemplate` (workers)
- `serverConfig.disableComponents`: `[servicelb, traefik]` (Cilium replaces both)
- `agentConfig.kubeletArgs`: `--cloud-provider=external` (CAPMOX is the cloud provider)

CAPMOX's release pipeline auto-publishes every
`templates/cluster-template-*.yaml` as a `--flavor <suffix>` artifact;
no Makefile change required.

## Step 3 — cut a release

```bash
git add templates/cluster-template-k3s.yaml
git commit -m "feat(templates): add K3s flavor"
git push -u origin k3s-flavor
gh pr create --title "Add K3s flavor template" --body "..."     # optional, for tracking
git tag v0.8.1-k3s.1                                            # any semver-ish tag
git push origin v0.8.1-k3s.1
gh release create v0.8.1-k3s.1 \
  --title "v0.8.1-k3s.1 — K3s flavor" \
  --generate-notes \
  templates/cluster-template-k3s.yaml \
  templates/cluster-template.yaml \
  metadata.yaml \
  infrastructure-components.yaml
```

The release must include at minimum:
- `infrastructure-components.yaml` (the full provider deployment)
- `metadata.yaml` (CAPI version compatibility)
- `cluster-template.yaml` (default kubeadm flavor — keeps the
  non-k3s flow working)
- `cluster-template-k3s.yaml` (the file you just added)

If you want to keep the upstream controller image, the
`infrastructure-components.yaml` from the upstream release works
unchanged — copy it across. If you build your own image, push it to
your registry and adjust `metadata.yaml` / `infrastructure-components.yaml`
to reference it.

## Step 4 — point bootstrap-capi at the fork

Set these env vars (or use the matching CLI flags):

```bash
export CAPMOX_REPO="https://github.com/lpasquali/cluster-api-provider-proxmox.git"
export CAPMOX_VERSION="v0.8.1-k3s.1"
export CAPMOX_K3S_PROVIDER_URL="https://github.com/lpasquali/cluster-api-provider-proxmox/releases/download/v0.8.1-k3s.1/infrastructure-components.yaml"
```

When `BOOTSTRAP_MODE=k3s` and `CAPMOX_K3S_PROVIDER_URL` is set,
bootstrap-capi writes a `providers:` override to the clusterctl config
so `clusterctl init --infrastructure proxmox` and
`clusterctl generate cluster --flavor k3s` both pull from your fork's
release URL.

When `BOOTSTRAP_MODE=kubeadm` (the default), `CAPMOX_K3S_PROVIDER_URL`
is ignored and clusterctl uses the upstream defaults.

## Step 5 — bootstrap

```bash
bootstrap-capi --bootstrap-mode k3s \
  --proxmox-url https://pve.example:8006 \
  --admin-username root@pam!capi-bootstrap \
  --admin-token <UUID> \
  --region datacenter --node pve
```

`--dry-run` will reflect the K3s mode in the plan.

## Long-term — submit upstream

Once your K3s template is stable, open an upstream PR to
`ionos-cloud/cluster-api-provider-proxmox` adding it. After it merges
and a release ships, drop `CAPMOX_K3S_PROVIDER_URL` and let
bootstrap-capi use the upstream URL.

## Troubleshooting

**`clusterctl generate cluster --flavor k3s` returns "flavor not found":**
The release artifact name must be exactly `cluster-template-k3s.yaml`.
Check the release page; rename if needed and recreate the release.

**KCP-K3s rejects `serverConfig.disableComponents`:**
Field name shifted between KCP-K3s versions. Check the installed
version's CRD schema:

```bash
kubectl get crd kthreescontrolplanes.controlplane.cluster.x-k8s.io \
  -o jsonpath='{.spec.versions[?(@.served)].name}'
kubectl explain kthreescontrolplane.spec.kthreesConfigSpec.serverConfig
```

Adjust the template if the field path differs.

**Workers stuck in `Provisioning`:**
Check `kubectl describe machine <md-0-...>`; common cause is the
template VM not having the `cloud-init` package installed. CAPMOX
relies on a cloud-init-enabled Proxmox template (any
`cloud-init=enabled` flag in the template VM's hardware tab).
