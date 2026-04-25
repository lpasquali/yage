#!/usr/bin/env bash
#
# bootstrap-capi.sh
#
# Bootstraps a CAPI management cluster on kind with Cilium CNI (default: Ingress, kube-proxy replacement, and Hubble + Relay + UI on the workload),
# initializes Cluster API with the Proxmox infrastructure provider
# and in-cluster IPAM, then applies a workload cluster manifest.
#
# Usage:
#   ./bootstrap-capi.sh [OPTIONS]
#
# Options:
#   -f, --force                    Replace an existing kind cluster (delete workload + kind) without prompting; skips kind cluster selection when other clusters exist
#   --no-delete-kind               Never delete the kind cluster (even with --force); skip heavy host maintenance
#   --persist-local-secrets        Also write local CSI/proxmox-csi path when that path is set. Bootstrap state (env/CLI) is always synced to proxmox-bootstrap-system when the kind cluster is reachable; no clusterctl/proxmox-admin on disk.
#   --kind-cluster-name NAME       Management kind cluster name (default: capi-provisioner; see existing-cluster prompt)
#   --kind-config PATH             kind cluster config file (default: ephemeral minimal config under $TMPDIR)
#   --proxmox-bootstrap-admin-secret NAME  kind Secret for Terraform / admin PVE API (data key proxmox-admin.yaml; default: proxmox-bootstrap-admin-credentials; legacy: set PROXMOX_BOOTSTRAP_SECRET_NAME to combine CAPI+CSI+admin in one Secret)
#   --capi-manifest PATH           Workload Cluster API manifest on disk; default is **no** persistent file (manifest in a kind Secret; override for a local path)
#   --regenerate-capi-manifest      Always re-run clusterctl to refresh workload YAML (after changing bootstrap config; see PROXMOX_BOOTSTRAP_CONFIG_FILE)
#   --bootstrap-config-file PATH   Use this file as the local config.yaml for stale detection (overrides default ./config.yaml)
#   -p, --purge                    Delete generated files and Terraform state before continuing.
#                                 Default manifest (Secret on kind, no CAPI_MANIFEST): also removes the workload
#                                 manifest Secret in proxmox-bootstrap-system.
#   -b, --build-all                Build and kind-load all images even when arm64 images exist online
#   -u, --admin-username USERNAME    Proxmox admin token username (default: root@pam!capi-bootstrap)
#   -t, --admin-token TOKEN          Proxmox admin token secret (UUID)
#   --proxmox-url URL                Proxmox API URL (e.g. https://pve.example:8006)
#   --proxmox-token TOKEN_ID         Proxmox API token ID for clusterctl; auto-derived from CAPI user/prefix if omitted
#   --proxmox-secret TOKEN_SECRET    Proxmox API token secret for clusterctl
#   -r, --region REGION              Proxmox region/datacenter name for workload clusters
#   -n, --node NODE                  Proxmox node name to deploy workload cluster VMs
#   --template-id ID                 PROXMOX template VM ID (default: 104; clusterctl still consumes env TEMPLATE_VMID derived from it)
#   --template-vmid ID               Same as --template-id (deprecated alias; sets PROXMOX_TEMPLATE_ID)
#   --bridge BRIDGE                  Bridge interface name (default: vmbr0)
#   --control-plane-endpoint-ip IP   Control plane endpoint VIP (default: 10.27.192.20)
#   --control-plane-endpoint-port P  Apiserver port on that endpoint (default: 6443; used for Cilium k8sServicePort when kube-proxy is replaced)
#   --node-ip-ranges RANGES          Node IP ranges (default: 10.27.192.21-10.27.192.30)
#   --gateway IP                     Gateway IP (default: 10.27.192.78)
#   --ip-prefix N                    IP prefix (default: 24)
#   --dns-servers LIST               DNS servers list (default: 8.8.8.8,8.8.4.4)
#   --allowed-nodes LIST             Allowed Proxmox nodes (default: same as PROXMOX_NODE)
#   --csi-url URL                    Proxmox CSI API URL (default: <PROXMOX_URL>/api2/json)
#   --csi-token-id TOKEN_ID          Proxmox CSI token ID; auto-derived from CSI user/prefix if omitted
#   --csi-token-secret SECRET        Proxmox CSI token secret
#   --csi-user-id USER_ID            Proxmox CSI user ID for Terraform bootstrap (default: kubernetes-csi-<CLUSTER_SET_ID>@pve)
#   --csi-token-prefix PREFIX        Proxmox CSI token name prefix for Terraform bootstrap (default: csi)
#   --csi-insecure true|false        Proxmox CSI insecure TLS setting (default: PROXMOX_ADMIN_INSECURE)
#   --csi-storage-class NAME         CSI StorageClass name (default: proxmox-data-xfs)
#   --csi-storage NAME               Proxmox storage backend name (default: local-lvm)
#   --cloudinit-storage NAME         Proxmox storage for CloudInit snippets/ISO (default: local)
#   --csi-reclaim-policy POLICY      CSI reclaim policy (default: Delete)
#   --csi-fstype TYPE                CSI filesystem type (default: xfs)
#   --csi-default-class true|false   Set CSI StorageClass as default (default: true)
#   --capi-user-id USER_ID           Proxmox CAPI user ID for Terraform bootstrap (default: capmox-<CLUSTER_SET_ID>@pve)
#   --capi-token-prefix PREFIX       Proxmox CAPI token name prefix for Terraform bootstrap (default: capi)
#   --control-plane-boot-volume-device DEVICE  Control plane boot volume device (default: scsi0)
#   --control-plane-boot-volume-size SIZE      Control plane boot volume size in GB (default: 100)
#   --control-plane-num-sockets N              Control plane sockets (default: 2)
#   --control-plane-num-cores N                Control plane cores (default: 1)
#   --control-plane-memory-mib N               Control plane memory in MiB (default: 8192)
#   --worker-boot-volume-device DEVICE         Worker boot volume device (default: scsi0)
#   --worker-boot-volume-size SIZE             Worker boot volume size in GB (default: 100)
#   --worker-num-sockets N                     Worker sockets (default: 2)
#   --worker-num-cores N                       Worker cores (default: 4)
#   --worker-memory-mib N                      Worker memory in MiB (default: 16384)
#   --workload-cluster-name NAME     CAPI cluster name on the management cluster (default: capi-quickstart; kubeconfig Secret is <name> in the Cluster’s namespace)
#   --workload-cluster-namespace NS  That Cluster’s namespace (default: default; with --name, can be auto-resolved if exactly one match on kind)
#   --workload-cilium-cluster-id ID  Cilium cluster.id for workload cluster (default: derived from CLUSTER_SET_ID)
#   --workload-k8s-version VERSION   Workload Kubernetes version (default: v1.35.0)
#   --control-plane-count N          Control plane replica count (default: 1)
#   --worker-count N                 Worker replica count (default: 2)
#   --capi-proxmox-machine-template-spec-rev-skip  Do not suffix ProxmoxMachineTemplate names with -t<specHash> (env: CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV=false)
#   --cilium-wait-duration DURATION  Cilium wait duration for installs (default: 10m0s)
#   --cilium-ingress V               Cilium Ingress controller + default IngressClass (default: true; requires kube-proxy replacement)
#   --cilium-kube-proxy-replacement V  true|false|auto — auto enables replacement only when --cilium-ingress is true (default: true)
#   --cilium-lb-ipam V               Append default CiliumLoadBalancerIPPool for LB-IPAM (default: true)
#   --cilium-lb-ipam-pool-cidr CIDR  LB-IPAM pool as a CIDR block. If not set, a range can be used, or a default CIDR is derived from the VM network.
#   --cilium-lb-ipam-pool-start IP   Start of the IP range for the LB-IPAM pool.
#   --cilium-lb-ipam-pool-stop IP    End of the IP range for the LB-IPAM pool.
#   --cilium-lb-ipam-pool-name NAME  metadata.name for the pool (default: <WORKLOAD_CLUSTER_NAME>-lb-pool)
#   --cilium-ipam-cluster-pool-ipv4 CIDR  Cilium cluster-pool *pod* IPv4 range (default: 10.244.0.0/16). Avoid 192.168.0.0/24
#                                         or other ranges that overlap your Proxmox/LAN/underlay—broken pod→ClusterIP/DNS and tiny capacity.
#   --cilium-ipam-cluster-pool-ipv4-mask-size N  Per-node IPv4 mask size (default: 24)
#   --cilium-gateway-api V            Enable Cilium Gateway API (Envoy; gateway.networking.k8s.io); default false — set true with app-of-apps Gateway+HTTPRoute
#   --argocd-disable-operator-ingress V  Do not let the Argo CD Operator create cluster Ingress/HTTPRoute for the server (default: false). Disables them on the ArgoCD CR — expose with Gateway API in Git (e.g. examples/gateway-api)
#   --cilium-hubble V                 Hubble (agent flows + Hubble Relay; default: true)
#   --cilium-hubble-ui V             Hubble UI deployment (default: true; only if --cilium-hubble is true)
#   --disable-argocd                 Skip all Argo CD (workload) and all GitOps-delivered add-ons
#   --disable-workload-argocd        Skip Argo on the CAPI cluster (proxmox-csi, kyverno, cert-manager, …) installed via CAAPH
#   --argocd-app-version VERSION     Argo CD *application* image tag (default: v3.3.8; ArgoCD CR spec.version)
#   --argocd-server-insecure V       Argo CD server.insecure (HTTP / gRPC TLS disabled; default: false — lab only)
#   --workload-gitops-mode caaph     must be caaph (only mode; Cilium + argocd-apps via CAAPH; Argo CD Operator + ArgoCD CR in-bootstrap)
#   --workload-app-of-apps-git-url URL   Git repo for the root app-of-apps Application (default: public workload-app-of-apps; overridable)
#   --workload-app-of-apps-git-path PATH Path in repo (default: examples/default)
#   --workload-app-of-apps-git-ref REF  targetRevision (default: main)
#   --argocd-print-access [workload]  Print Argo login hints for the CAPI/Proxmox cluster only (no Argo on kind; no bootstrap)
#   --argocd-print-access-only […]  Alias of --argocd-print-access; combine with --argocd-port-forward
#   --argocd-port-forward [workload]  kubectl port-forward to Argo on the provisioned cluster (default; no management/kind Argo; blocks)
#   --argocd-port-forward-only […]  Alias; optional with --argocd-print-access
#   --workload-rollout [argocd|capi|all]  Skip Phase 1/2: re-touch CAPI resources and/or sync hints (argocd: CAAPH+Git; use argocd app sync on the workload, not in-script YAML)
#   --workload-rollout-no-wait     With --workload-rollout: for non-argocd modes, skip health waits when applicable
#   --kind-backup [path]            Export kind management cluster state (namespaces + kind/); optional output path; env: BOOTSTRAP_KIND_BACKUP_*; not with other standalone modes
#   --kind-restore <archive>        Import prior --kind-backup archive into current kind context; passphrase env if encrypted
#   --disable-proxmox-csi            Skip proxmox-csi Argo Application (default: installed)
#   --proxmox-csi-version VERSION    proxmox-csi Helm chart version (default: 0.5.7, app v1.18.1)
#   --disable-proxmox-csi-smoketest  Skip PostSync 1Gi PVC/rollout hook Jobs for proxmox-csi (default: when CSI and hooks enabled)
#   --disable-argocd-workload-postsync-hooks  Skip co-located PostSync hook git sources (default: PostSync under each in-cluster app; requires Argo CD 2.6+)
#   --argocd-workload-postsync-hooks-git-url URL   Git repo for argo-postsync-hooks/* (default: public workload-smoketests; empty → bootstrap origin; published cluster needs this)
#   --argocd-workload-postsync-hooks-git-path PATH  Optional path prefix in that repo (before argo-postsync-hooks/<component>)
#   --argocd-workload-postsync-hooks-git-ref REF     Argo targetRevision (default: current branch, commit, or main)
#   --recreate-proxmox-identities  Re-run Terraform to replace CAPI+CSI (or subset) Proxmox users/tokens, then refresh kind + workload Secrets (admin API token required; identity from .tfstate or token IDs)
#   --recreate-proxmox-identities-scope capi|csi|both  Which identities to replace (default: both)
#   --recreate-proxmox-identities-state-rm  Drop all addresses from Terraform state then apply (use if PVE was wiped; creates objects from scratch)
#   --cluster-set-id ID            Optional; pin CLUSTER_SET_ID for recreate when not inferrable
#   --disable-kyverno                Skip Kyverno Argo Application (default: installed)
#   --kyverno-version VERSION        Kyverno Helm chart version (default: 3.7.1 = app v1.17.1)
#   --disable-cert-manager           Skip cert-manager Argo Application (default: installed)
#   --cert-manager-version VERSION   cert-manager Helm chart version (default: v1.20.2)
#   --disable-crossplane             Skip Crossplane Argo Application (default: installed)
#   --crossplane-version VERSION     Crossplane Helm chart version (default: 2.2.1)
#   --disable-cnpg                   Skip CloudNativePG operator Argo Application (default: installed)
#   --cnpg-version VERSION           CloudNativePG Helm chart version (default: latest)
#   --disable-victoriametrics         Skip VictoriaMetrics (single) Argo Application (default: installed; Prometheus-compatible TSDB)
#   --victoriametrics-version VERSION  VictoriaMetrics single Helm chart version (default: latest)
#   --exp-cluster-resource-set V     Enable ClusterResourceSet in clusterctl (default: false; Cilium/Argo use CAAPH, not CRS)
#   --cluster-topology V             Enable ClusterTopology feature (default: true)
#   --exp-kubeadm-bootstrap-format-ignition V  Enable kubeadm ignition format (default: true)
#   --disable-metrics-server         Skip metrics-server on the kind management cluster (default: install for kubectl top / HPA)
#   --disable-workload-metrics-server  Skip metrics-server on the CAPI/Proxmox workload cluster (default: install; HPA / kubectl top there)
#   -h, --help                       Show this usage message
#
# Environment variables (alternatives to options):
#   FORCE                           true|false; replace kind cluster without prompting; skip kind cluster picker
#   NO_DELETE_KIND                  true|false; never delete kind; skip Docker upgrade / Terraform provider install when set
#   BOOTSTRAP_PERSIST_LOCAL_SECRETS true|false; if set, also write PROXMOX_CSI_CONFIG to disk when that path is set. Bootstrap config in kind is always updated when the cluster is reachable (default: false).
#   PURGE                           true|false; delete generated files/state before continuing (ConfigMap: same scoping as --purge, see there)
#   BUILD_ALL                       true|false; force local build+kind load for all images
#   ENABLE_METRICS_SERVER           true|false; install metrics-server on kind (kubectl top; default: true)
#   ENABLE_WORKLOAD_METRICS_SERVER  true|false; metrics-server on CAPI via in-cluster Argo (else kubectl; default: true)
#   WORKLOAD_METRICS_SERVER_INSECURE_TLS  true = add --kubelet-insecure-tls (default: true; set false if kubelet certs are trusted)
#   METRICS_SERVER_MANIFEST_URL     Direct kubectl only (Argo off or workload Argo off): components.yaml URL
#   METRICS_SERVER_GIT_CHART_TAG    Tag in kubernetes-sigs/metrics-server for charts/metrics-server (in-cluster Argo; default: v0.7.2)
#   SPIRE_OIDC_INSECURE_HTTP         When Keycloak+SPIRE: use HTTP for oidc-dp so Keycloak can add SPIFFE IdP from env (default: true; set false for TLS)
#   SPIRE_OIDC_BUNDLE_SOURCE         ConfigMap | CSI (default: CSI — trust bundle + JWKS via Workload API / SPIFFE CSI; ConfigMap = file-based bundle, no Workload API)
#   SPIRE_TOLERATE_CONTROL_PLANE     true|false; tolerations for control-plane taints on spire-agent, spiffe-csi-driver, oidc (default: true — required on many CAPI / single-CP nodes for CSI + agent to run on the same node as OIDC)
#   KIND_CLUSTER_NAME               kind management cluster name (default: capi-provisioner; CLUSTER_NAME is an alias)
#   CLUSTER_NAME                    Alias for KIND_CLUSTER_NAME
#   KIND_CONFIG                     kind config path; unset = ephemeral minimal config (nothing in workspace)
#   CAPI_MANIFEST                   Workload manifest; unset = only in-cluster Secret on kind + ephemeral process file (set to a path to use a file on disk)
#   Immutability: many CAPI / infrastructure fields (e.g. cluster network CIDRs, cluster name, InfraRef types, parts of
#   KubeadmControlPlane) cannot be updated in place. If you change those in the manifest, delete the workload Cluster on
#   the management cluster first (kubectl delete cluster -n <ns> <name> and wait for machines to go away), then re-apply.
#   The script warns when it regenerates the manifest with clusterctl while that Cluster already exists.
#   BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING  true: suppress that warning (default: false)
#   CAPI_MANIFEST_SECRET_NAMESPACE  kind namespace for workload YAML Secret (default: proxmox-bootstrap-system)
#   CAPI_MANIFEST_SECRET_NAME       name of the Secret (default: proxmox-bootstrap-capi-manifest)
#   CAPI_MANIFEST_SECRET_KEY        data key in that Secret (default: workload.yaml; same on-disk role as a checked-in workload CAPI file)
#   PROXMOX_BOOTSTRAP_CONFIG_FILE     Optional path to a local config.yaml; when unset, ./config.yaml is used if that file exists (stale check vs. workload)
#   BOOTSTRAP_REGENERATE_CAPI_MANIFEST  true: always clusterctl re-generate workload YAML before re-apply (e.g. only edited the in-cluster config Secret; default: false)
#   WORKLOAD_ARGOCD_ENABLED         true|false; second Argo on the workload + in-cluster platform apps (default: true; see --disable-workload-argocd)
#   WORKLOAD_ARGOCD_NAMESPACE        Namespace for Argo on the CAPI workload cluster (default: argocd)
#   (in-bootstrap ArgoCD CR) spec.notifications + spec.server.grpc.ingress enabled; spec.prometheus/spec.monitoring default false
#   (VictoriaMetrics: scrape Argo with VMServiceScrape/PodMonitor in app-of-apps — not Prometheus Operator ServiceMonitors). Set
#   ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED / ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED true only for kube-prometheus / Prom Operator.
#   PROXMOX_CSI_CONFIG              Optional proxmox-csi values file; unset = CSI settings from env / in-cluster Secret only
#   PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME  Secret for non-secret bootstrap state (data key config.yaml; default: proxmox-bootstrap-config)
#   PROXMOX_BOOTSTRAP_CONFIG_SECRET_KEY    data key in that Secret (default: config.yaml; legacy: config.json still read for migration)
#   (merge) merge_proxmox_bootstrap_secrets_from_kind re-reads that Secret: snapshot keys (network, Cilium, …) come from
#   in-cluster before each sync, so k9s edits are not lost when the script re-writes the Secret. Use CLI flags
#   (--node-ip-ranges, --gateway, …) to override for one run; those set *_EXPLICIT and skip the Secret for that key.
#   After try_fill_workload_manifest_inputs_from_management_cluster the script re-runs merge so NODE_IP_RANGES in
#   proxmox-bootstrap-config wins over the live ProxmoxCluster (otherwise the next clusterctl+sync would copy the
#   cluster’s range back into the config Secret).
#   KubeadmControlPlane / MachineDeployment rolling-update strategy (maxSurge, maxUnavailable) is not modified here — set it
#   in the workload manifest, cluster template, or Git/Helm that feeds clusterctl. Remove stale keys CAPI_PROXMOX_ROLLOUT_*
#   and CONTROL_PLANE/WORKER_ROLLOUT_* from proxmox-bootstrap-config if present.
#   CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV  true: before apply, each ProxmoxMachineTemplate is renamed to
#   <stem>-t<8-hex> where <8-hex> = sha256(manifest .spec)[:8] and KCP/MD infrastructureRef name lines are
#   updated. Avoids "ProxmoxMachineTemplate is immutable" when VM sizing / template (spec) changes; new templates
#   roll in via CAPI. false: keep clusterctl names (in-place spec updates may fail on re-apply). CLI: --capi-proxmox-machine-template-spec-rev-skip.
#   Does not set CAPMox’s own controller workers — for that, scale the capmox-controller-manager on the management cluster.
#   PROXMOX_BOOTSTRAP_SECRET_NAMESPACE  kind namespace (default: proxmox-bootstrap-system)
#   PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME  CAPMOX CAPI / clusterctl token Secret (default: proxmox-bootstrap-capmox-credentials)
#   PROXMOX_BOOTSTRAP_CSI_SECRET_NAME     Proxmox CSI API token Secret (default: proxmox-bootstrap-csi-credentials)
#   PROXMOX_BOOTSTRAP_SECRET_NAME         If set, legacy: one Secret for CAPI+CSI (and admin when same as admin Secret name) instead of capmox+csi split
#   PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME  Terraform / admin PVE API (data key ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY:-proxmox-admin.yaml}; default: proxmox-bootstrap-admin-credentials)
#   PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY
#   On kind, proxmox-bootstrap-config holds config.yaml: management/bootstrap toggles and CSI *defaults* — not API secrets, not admin tokens, and not workload spec (${CAPI_MANIFEST_SECRET_NAME} / workload.yaml). Clusterctl/CAPMOX tokens: ${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME} (or legacy ${PROXMOX_BOOTSTRAP_SECRET_NAME}). CSI tokens: ${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} (or legacy). Admin: ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME} (proxmox-admin.yaml). capmox-system/capmox-manager-credentials is merged when needed. CLI/env override.
#   CLUSTERCTL_CFG                 Optional; path to a local clusterctl Proxmox credentials YAML. Empty = env / kind Secrets + a temp file only for the clusterctl CLI.
#   PROXMOX_ADMIN_CONFIG            Optional; legacy path to a local YAML for Terraform admin API token. Empty = env / kind Secrets only. This script does not create these files by default.
#   PROXMOX_URL                      Proxmox API URL
#   PROXMOX_TOKEN                    Proxmox API token ID used by clusterctl; auto-derived if omitted
#   PROXMOX_SECRET                   Proxmox API token secret used by clusterctl
#   PROXMOX_ADMIN_USERNAME           Proxmox admin token username
#   PROXMOX_ADMIN_TOKEN              Proxmox admin token secret
#   PROXMOX_REGION                   Proxmox region/datacenter name
#   PROXMOX_NODE                     Proxmox node name
#   PROXMOX_TEMPLATE_ID              Proxmox template VM ID for workload nodes (default: 104)
#   PROXMOX_BRIDGE                   Bridge interface name (default: vmbr0)
#   CONTROL_PLANE_ENDPOINT_IP        Control plane endpoint VIP
#   CONTROL_PLANE_ENDPOINT_PORT      Apiserver port (default: 6443; Cilium k8sServicePort when CILIUM_KUBE_PROXY_REPLACEMENT is on)
#   NODE_IP_RANGES                   Node IP ranges
#   GATEWAY                          Gateway IP
#   IP_PREFIX                        Network prefix
#   DNS_SERVERS                      DNS server list
#   ALLOWED_NODES                    Allowed Proxmox nodes
#   VM_SSH_KEYS                      Comma-separated SSH public keys for workload VMs (clusterctl); in config.yaml when set or after generate; if still empty at generate, ~/.ssh/authorized_keys is read once
#   PROXMOX_CSI_URL                  Proxmox CSI API URL
#   PROXMOX_CSI_TOKEN_ID             Proxmox CSI token ID; auto-derived if omitted
#   PROXMOX_CSI_TOKEN_SECRET         Proxmox CSI token secret
#   PROXMOX_CSI_USER_ID              Proxmox CSI user ID for Terraform bootstrap
#   PROXMOX_CSI_TOKEN_PREFIX         Proxmox CSI token name prefix for Terraform bootstrap
#   PROXMOX_CSI_INSECURE             Proxmox CSI insecure TLS setting
#   PROXMOX_CSI_STORAGE_CLASS_NAME   CSI StorageClass name
#   PROXMOX_CSI_STORAGE              Proxmox storage backend name
#   PROXMOX_CLOUDINIT_STORAGE        Proxmox storage for CloudInit snippets/ISO (must support iso,snippets)
#   PROXMOX_CSI_RECLAIM_POLICY       CSI reclaim policy
#   PROXMOX_CSI_FSTYPE               CSI filesystem type
#   PROXMOX_CSI_DEFAULT_CLASS        Set CSI StorageClass as default (true|false)
#   PROXMOX_CSI_CHART_REPO_URL       OCI registry base or full chart URL (default: oci://ghcr.io/sergelogvinov/charts)
#   PROXMOX_CSI_CHART_NAME           Chart name appended to repo URL when URL lacks it (default: proxmox-csi-plugin)
#   PROXMOX_CSI_CHART_VERSION        Helm OCI chart tag (default: 0.5.7; app proxmox-csi v1.18.1)
#   PROXMOX_CSI_SMOKE_ENABLED        1Gi PVC + rollout PostSync jobs for proxmox-csi (default: true; co-located on proxmox-csi app)
#   ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED  Per-platform PostSync hook git Kustomize on each in-cluster app (default: true; needs multi-source, Argo 2.6+)
#   ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL    Git for argo-postsync-hooks/<component> (default: public https://github.com/lpasquali/workload-smoketests.git; empty in env → same default; explicit "" → discover git origin)
#   ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH   Path prefix in that repo (before argo-postsync-hooks/…)
#   ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF   targetRevision (default: current branch, commit, or main)
#   ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE  Override PostSync Job kubectl (default: registry.k8s.io/kubectl from CAPI / WORKLOAD_KUBERNETES_VERSION)
#   WORKLOAD_POSTSYNC_NAMESPACE     Namespace for PostSync Jobs that use the rollout script pattern (default: workload-smoke)
#   RECREATE_PROXMOX_IDENTITIES       true: same as --recreate-proxmox-identities (re-run Terraform, refresh Secrets)
#   PROXMOX_IDENTITY_RECREATE_SCOPE  capi | csi | both (default: both)
#   PROXMOX_IDENTITY_RECREATE_STATE_RM  true: tofu state rm all then apply (empty PVE; full recreate)
#   PROXMOX_CSI_CONFIG_PROVIDER      proxmox-csi config.features.provider (default: proxmox)
#   PROXMOX_CSI_TOPOLOGY_LABELS      Inject kubelet node-labels for CSI topology (default: true)
#   PROXMOX_TOPOLOGY_REGION          topology.kubernetes.io/region (default: PROXMOX_REGION)
#   PROXMOX_TOPOLOGY_ZONE            topology.kubernetes.io/zone, PVE node name (default: PROXMOX_NODE)
#   PROXMOX_CAPI_USER_ID             Proxmox CAPI user ID for Terraform bootstrap
#   PROXMOX_CAPI_TOKEN_PREFIX        Proxmox CAPI token name prefix for Terraform bootstrap
#   CONTROL_PLANE_BOOT_VOLUME_DEVICE Control plane boot volume device
#   CONTROL_PLANE_BOOT_VOLUME_SIZE   Control plane boot volume size in GB
#   CONTROL_PLANE_NUM_SOCKETS        Control plane sockets
#   CONTROL_PLANE_NUM_CORES          Control plane cores
#   CONTROL_PLANE_MEMORY_MIB         Control plane memory in MiB
#   WORKER_BOOT_VOLUME_DEVICE        Worker boot volume device
#   WORKER_BOOT_VOLUME_SIZE          Worker boot volume size in GB
#   WORKER_NUM_SOCKETS               Worker sockets
#   WORKER_NUM_CORES                 Worker cores
#   WORKER_MEMORY_MIB                Worker memory in MiB
#   WORKLOAD_CLUSTER_NAME            CAPI workload cluster name (see --workload-cluster-name)
#   WORKLOAD_CLUSTER_NAMESPACE       Namespace of the Cluster (see --workload-cluster-namespace; default: default)
#   BOOTSTRAP_KIND_BACKUP_NAMESPACES  optional list (comma/space); default: PROXMOX_BOOTSTRAP + WORKLOAD CAPI namespace
#   BOOTSTRAP_KIND_BACKUP_OUT         path prefix for kind_bootstrap_state_backup (default: timestamped .tar in cwd)
#   BOOTSTRAP_KIND_BACKUP_ENCRYPT     auto|age|openssl|none; auto encrypts with age, else openssl, when passphrase is set; else .tar.gz only
#   BOOTSTRAP_KIND_BACKUP_PASSPHRASE, AGE_PASSPHRASE  at-rest passphrases (openssl: former; age: AGE native + fallback to the former for convenience)
#   --kind-backup [path]             Dump namespaces + kind/ into a .tar.gz (or .age/.enc; optional path → BOOTSTRAP_KIND_BACKUP_OUT; env: BOOTSTRAP_KIND_BACKUP_NAMESPACES, …)
#   --kind-restore <archive>         kubectl apply the backup into context kind-$KIND_CLUSTER_NAME (set passphrase if encrypted)
#   (functions) kind_bootstrap_state_backup [outfile]  kind_bootstrap_state_restore <archive> — same as flags; set KIND_CLUSTER_NAME, env as needed
#   WORKLOAD_CILIUM_CLUSTER_ID       Cilium cluster.id for workload cluster (default: derived from CLUSTER_SET_ID)
#   WORKLOAD_KUBERNETES_VERSION      Workload Kubernetes version
#   CONTROL_PLANE_MACHINE_COUNT      Control plane replica count
#   WORKER_MACHINE_COUNT             Worker replica count
#   CILIUM_WAIT_DURATION             Cilium wait duration for installs
#   CILIUM_INGRESS                   true|false; Cilium Ingress + default IngressClass (default: true; requires kube-proxy replacement)
#   CILIUM_KUBE_PROXY_REPLACEMENT    true|false|auto — auto = on only if CILIUM_INGRESS; default: true
#                                    (when true, Cilium is installed with k8sServiceHost/Port = control plane endpoint so pods reach the API before ClusterIP is live — see Cilium kube-proxy-free docs)
#   CILIUM_LB_IPAM                   true|false; apply CiliumLoadBalancerIPPool to workload (after nodes; default: true)
#   CILIUM_LB_IPAM_POOL_CIDR         LB-IPAM pool CIDR; empty = derive from CONTROL_PLANE_ENDPOINT_IP (see --cilium-lb-ipam-pool-cidr)
#   CILIUM_LB_IPAM_POOL_START        Start of the IP range for the LB-IPAM pool.
#   CILIUM_LB_IPAM_POOL_STOP         End of the IP range for the LB-IPAM pool.
#   CILIUM_LB_IPAM_POOL_NAME         Pool object name; empty = <WORKLOAD_CLUSTER_NAME>-lb-pool
#   CILIUM_HUBBLE                    true|false; enable Hubble + Hubble Relay (default: true)
#   CILIUM_HUBBLE_UI                 true|false; enable Hubble UI (default: true; requires CILIUM_HUBBLE)
#   CILIUM_IPAM_CLUSTER_POOL_IPV4     cluster-pool pod CIDR (default: 10.244.0.0/16); set if cilium status shows a bad/small pool (e.g. 192.168.0.0/24)
#   CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE  per-node mask (default: 24)
#   CILIUM_GATEWAY_API_ENABLED       true|false; set Cilium helm gatewayAPI.enabled (default: false; install Gateway API CRDs per Cilium version if missing)
#   ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS  true|false; no operator-managed Kubernetes Ingress for the Argo CD server (default: false). Disables server + gRPC Ingress on the ArgoCD CR — expose only via Gateway API in Git (e.g. `examples/gateway-api`).
#   ARGOCD_SERVER_INSECURE           true|false; ArgoCD CR: server.insecure (default: false)
#   ARGOCD_PRINT_ACCESS_TARGET      workload (only value; for standalone --argocd-print-access)
#   ARGOCD_PORT_FORWARD_TARGET      workload (only value; port-forward to provisioned cluster Argo)
#   ARGOCD_PORT_FORWARD_PORT         local port for Argo on the CAPI/Proxmox cluster (default: 8443; legacy: ARGOCD_PORT_FORWARD_WORKLOAD_PORT)
#   (internal) ARGOCD_PRINT_ACCESS_STANDALONE  set by --argocd-print-access; not needed when using flags
#   (internal) ARGOCD_PORT_FORWARD_STANDALONE  set by --argocd-port-forward; not needed when using flags
#   WORKLOAD_ROLLOUT_MODE            argocd | capi | all (for --workload-rollout; default: argocd)
#   WORKLOAD_ROLLOUT_NO_WAIT         true: skip wait for in-cluster Argo apps healthy
#   ARGOCD_VERSION                   Argo CD app/server version + argocd CLI release tag (default: v3.3.8; CLI and ArgoCD CR are kept in lockstep upstream)
#   KYVERNO_CLI_VERSION              kyverno CLI release tag (default: v1.17.1); Linux amd64/arm64; Argo + Kyverno
#   KYVERNO_TOLERATE_CONTROL_PLANE  true|false; add Helm global.tolerations for control-plane taints (default: true; needed on many CAPI/single-CP nodes)
#   CMCTL_VERSION                    cmctl release tag (default: v2.4.1); Linux amd64/arm64; Argo + cert-manager
#   EXP_CLUSTER_RESOURCE_SET         clusterctl: ClusterResourceSet feature (default: false; unused for Cilium/Argo here)
#   WORKLOAD_GITOPS_MODE             caaph only (CAAPH + app-of-apps Git; see --workload-app-of-apps-*)
#   ARGOCD_OPERATOR_VERSION          Argo CD Operator git ref for kubectl apply -k …/argocd-operator/config/default?ref= (default: v0.16.0)
#   ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED   true: ArgoCD CR spec.prometheus.enabled (ServiceMonitors; kube-prometheus). Default false (VM: use VMServiceScrape in Git).
#   ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED  true: ArgoCD CR spec.monitoring.enabled (PrometheusRules). Default false. Enable with prometheus for full Prom Operator integration.
#   WORKLOAD_APP_OF_APPS_GIT_URL       default https://github.com/lpasquali/workload-app-of-apps.git
#   WORKLOAD_APP_OF_APPS_GIT_PATH      default examples/default (use examples/proxmox-secret-name if CSI Secret is <cluster>-proxmox-csi-config; edit patch MY_CLUSTER)
#   WORKLOAD_APP_OF_APPS_GIT_REF       default main
#   CLUSTER_TOPOLOGY                 Enable ClusterTopology feature
#   EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION  Enable kubeadm ignition format
#
# Prerequisites (must be on PATH):
#   - kind
#   - kubectl
#   - clusterctl
#   - cilium (Cilium CLI)
#   - argocd / kyverno / cmctl — Linux amd64/arm64 only; Phase 1 when Argo is enabled and matching workload apps are enabled
#
# By default this script does not create kind-config.yaml, capi-quickstart.yaml, or proxmox-csi.yaml in the working tree.
# Interactive runs: if kubectl current-context is kind-<name>, you are prompted to use it for updates before picking/creating a cluster.
# After CAPI is installed on kind, existing cluster.cluster.x-k8s.io resources are listed so you can reuse WORKLOAD_CLUSTER_NAME/namespace (unless --force).
# Proxmox CAPI/CSI (and admin, when known) credentials are stored in Secrets on the kind cluster; those Secrets are the source of truth (not env, clusterctl.yaml, or Terraform state) when present.

set -euo pipefail

# --- Software version matrix --------------------------------------------------
#
#  Component                  Version     Notes
#  ─────────────────────────  ──────────  ──────────────────────────────────────
#  kind                       v0.31.0     Management cluster host
#  kubectl                    v1.35.4     Matches workload-cluster k8s version
#  cluster-api (clusterctl)   v1.11.8     CAPMOX v0.8 requires CAPI v1.11.x
#  cilium CLI                 v0.19.2     CLI used to install/manage Cilium CNI
#  cilium CNI                 v1.19.3     Latest stable Cilium CNI
#  argocd CLI                 v3.3.8      Phase 1 when Argo CD is not disabled
#  kyverno CLI                v1.17.1     Phase 1 when Argo CD + Kyverno app enabled
#  cmctl (cert-manager)       v2.4.1      Phase 1 when Argo CD + cert-manager app enabled
#  CAPMOX (provider-proxmox)  v0.8.1      Latest stable; CAPI v1beta2 / v1alpha2
#  IPAM in-cluster            v1.0.3      Used by clusterctl --ipam in-cluster
#
#  Compatibility reference:
#    CAPMOX v0.8 ↔ CAPI v1.11  https://github.com/ionos-cloud/cluster-api-provider-proxmox#compatibility
#    CAPI versions              https://cluster-api.sigs.k8s.io/reference/versions.html
# ------------------------------------------------------------------------------

# --- Tunables -----------------------------------------------------------------
KIND_VERSION="${KIND_VERSION:-v0.31.0}"
KUBECTL_VERSION="${KUBECTL_VERSION:-v1.35.4}"
CLUSTERCTL_VERSION="${CLUSTERCTL_VERSION:-v1.11.8}"
CILIUM_CLI_VERSION="${CILIUM_CLI_VERSION:-v0.19.2}"
CILIUM_VERSION="${CILIUM_VERSION:-1.19.3}"
CILIUM_WAIT_DURATION="${CILIUM_WAIT_DURATION:-10m0s}"
CILIUM_INGRESS="${CILIUM_INGRESS:-true}"
CILIUM_KUBE_PROXY_REPLACEMENT="${CILIUM_KUBE_PROXY_REPLACEMENT:-true}"
CILIUM_LB_IPAM="${CILIUM_LB_IPAM:-true}"
CILIUM_LB_IPAM_POOL_CIDR="${CILIUM_LB_IPAM_POOL_CIDR:-}"
CILIUM_LB_IPAM_POOL_START="${CILIUM_LB_IPAM_POOL_START:-}"
CILIUM_LB_IPAM_POOL_STOP="${CILIUM_LB_IPAM_POOL_STOP:-}"
CILIUM_LB_IPAM_POOL_NAME="${CILIUM_LB_IPAM_POOL_NAME:-}"
CILIUM_HUBBLE="${CILIUM_HUBBLE:-true}"
CILIUM_HUBBLE_UI="${CILIUM_HUBBLE_UI:-true}"
# Cilium IPAM: avoid cluster-pool falling back to a /24 in 192.168.0.0/16 that overlaps Proxmox/LAN (breaks routing/DNS to kube-dns).
CILIUM_IPAM_CLUSTER_POOL_IPV4="${CILIUM_IPAM_CLUSTER_POOL_IPV4:-10.244.0.0/16}"
CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE="${CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE:-24}"
# Cilium Gateway API (HTTPRoute / Gateway) — opt-in; requires gateway.networking.k8s.io CRDs (Cilium docs for your version).
CILIUM_GATEWAY_API_ENABLED="${CILIUM_GATEWAY_API_ENABLED:-false}"
# When true: (1) Argo CD Operator does not create server/gRPC Ingress on the ArgoCD CR. (2) argo-helm (CAAPH) values set server.ingress / ingressGrpc / httproute to disabled — use `examples/gateway-api` or your own HTTPRoute.
ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS="${ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS:-false}"
CAPMOX_VERSION="${CAPMOX_VERSION:-v0.8.1}"   # overrides auto-detection when set
FORCE="${FORCE:-false}"
NO_DELETE_KIND="${NO_DELETE_KIND:-false}"
BOOTSTRAP_PERSIST_LOCAL_SECRETS="${BOOTSTRAP_PERSIST_LOCAL_SECRETS:-false}"
PURGE="${PURGE:-false}"
BUILD_ALL="${BUILD_ALL:-false}"
CLUSTER_ID="${CLUSTER_ID:-1}"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-}"
CLUSTER_NAME="${CLUSTER_NAME:-}"  # optional alias; merged before default is applied
KIND_CONFIG="${KIND_CONFIG:-}"
CAPI_MANIFEST="${CAPI_MANIFEST:-}"
BOOTSTRAP_EPHEMERAL_KIND_CONFIG=""
BOOTSTRAP_KIND_CONFIG_EPHEMERAL=false
BOOTSTRAP_CAPI_MANIFEST_EPHEMERAL=false
BOOTSTRAP_CAPI_MANIFEST_USER_SET=false
# When CAPI_MANIFEST is unset, manifest is stored on kind in a Secret (no ~/.bootstrap-capi file).
BOOTSTRAP_CAPI_USE_SECRET=false
# Local bootstrap snapshot file (if present, its mtime is compared to the last pushed workload to decide whether to re-run clusterctl).
PROXMOX_BOOTSTRAP_CONFIG_FILE="${PROXMOX_BOOTSTRAP_CONFIG_FILE:-}"
BOOTSTRAP_REGENERATE_CAPI_MANIFEST="${BOOTSTRAP_REGENERATE_CAPI_MANIFEST:-false}"
BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING="${BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING:-false}"
# Set true only inside generate_workload_manifest_if_missing after a successful clusterctl generate (not when reusing YAML).
BOOTSTRAP_CLUSTERCTL_REGENERATED_MANIFEST="${BOOTSTRAP_CLUSTERCTL_REGENERATED_MANIFEST:-false}"
# Version ProxmoxMachineTemplate metadata.name from spec hash so re-applies can change VM shape without immutability errors.
CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV="${CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV:-true}"
CAPI_MANIFEST_SECRET_NAMESPACE="${CAPI_MANIFEST_SECRET_NAMESPACE:-proxmox-bootstrap-system}"
CAPI_MANIFEST_SECRET_NAME="${CAPI_MANIFEST_SECRET_NAME:-proxmox-bootstrap-capi-manifest}"
CAPI_MANIFEST_SECRET_KEY="${CAPI_MANIFEST_SECRET_KEY:-workload.yaml}"
PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME="${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME:-proxmox-bootstrap-config}"
PROXMOX_BOOTSTRAP_CONFIG_SECRET_KEY="${PROXMOX_BOOTSTRAP_CONFIG_SECRET_KEY:-config.yaml}"
PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY="${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY:-proxmox-admin.yaml}"
# Kind backup/restore: comma-separated namespaces (empty = proxmox-bootstrap + Argo + workload CAPI namespace).
BOOTSTRAP_KIND_BACKUP_NAMESPACES="${BOOTSTRAP_KIND_BACKUP_NAMESPACES:-}"
# Output file for kind_bootstrap_state_backup (default: pwd + timestamped name).
BOOTSTRAP_KIND_BACKUP_OUT="${BOOTSTRAP_KIND_BACKUP_OUT:-}"
# age (set AGE_PASSPHRASE), openssl (set BOOTSTRAP_KIND_BACKUP_PASSPHRASE), or none.
BOOTSTRAP_KIND_BACKUP_ENCRYPT="${BOOTSTRAP_KIND_BACKUP_ENCRYPT:-auto}"
# Symmetric passphrase for openssl enc; age also accepts AGE_PASSPHRASE from upstream age(1).
BOOTSTRAP_KIND_BACKUP_PASSPHRASE="${BOOTSTRAP_KIND_BACKUP_PASSPHRASE:-}"
BOOTSTRAP_EXIT_TRAP_REGISTERED=false
INFRA_PROVIDER="${INFRA_PROVIDER:-proxmox}"
IPAM_PROVIDER="${IPAM_PROVIDER:-in-cluster}"
CAPMOX_REPO="${CAPMOX_REPO:-https://github.com/ionos-cloud/cluster-api-provider-proxmox.git}"
CAPMOX_IMAGE_REPO="${CAPMOX_IMAGE_REPO:-ghcr.io/ionos-cloud/cluster-api-provider-proxmox}"
CAPMOX_BUILD_DIR="${CAPMOX_BUILD_DIR:-./cluster-api-provider-proxmox}"
CAPI_CORE_IMAGE="${CAPI_CORE_IMAGE:-registry.k8s.io/cluster-api/cluster-api-controller:${CLUSTERCTL_VERSION}}"
CAPI_CORE_REPO="${CAPI_CORE_REPO:-https://github.com/kubernetes-sigs/cluster-api.git}"
CAPI_BOOTSTRAP_IMAGE="${CAPI_BOOTSTRAP_IMAGE:-registry.k8s.io/cluster-api/kubeadm-bootstrap-controller:${CLUSTERCTL_VERSION}}"
CAPI_CONTROLPLANE_IMAGE="${CAPI_CONTROLPLANE_IMAGE:-registry.k8s.io/cluster-api/kubeadm-control-plane-controller:${CLUSTERCTL_VERSION}}"
# After CLUSTERCTL_VERSION is set from env or merge_proxmox_bootstrap_secrets_from_kind, keep CAPI pre-load images in sync.
bootstrap_sync_capi_controller_images_to_clusterctl_version() {
  CAPI_CORE_IMAGE="registry.k8s.io/cluster-api/cluster-api-controller:${CLUSTERCTL_VERSION}"
  CAPI_BOOTSTRAP_IMAGE="registry.k8s.io/cluster-api/kubeadm-bootstrap-controller:${CLUSTERCTL_VERSION}"
  CAPI_CONTROLPLANE_IMAGE="registry.k8s.io/cluster-api/kubeadm-control-plane-controller:${CLUSTERCTL_VERSION}"
}
IPAM_IMAGE="${IPAM_IMAGE:-registry.k8s.io/capi-ipam-ic/cluster-api-ipam-in-cluster-controller:v1.0.3}"
IPAM_REPO="${IPAM_REPO:-https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster.git}"
OPENTOFU_VERSION="${OPENTOFU_VERSION:-1.8.5}"
# Workload GitOps (single path): management runs CAPI + CAPMOX + **caaph-system** (Cluster API add-on provider Helm).
# On the **provisioned** workload: (1) CAAPH **HelmChartProxy** installs **Cilium**. (2) **Argo CD Operator** is installed
# with `kubectl apply -k` to the workload API (argoproj-labs ships kustomize, not a supported Helm chart for HCP).
# (3) Bootstrap applies the **ArgoCD** CR; the operator reconciles Argo. (4) CAAPH **HelmChartProxy** for **argo-helm/argocd-apps**
# creates the **root** `Application` that syncs this app-of-apps repo. There is no alternate (removed: CAAPH `argo-cd` chart, BYO Argo, skip-operator).
# Platform `Application` children (metrics, CSI, …) come only from Git. --disable-workload-argocd skips Argo and argocd-apps.
# VictoriaMetrics/OTel are for metrics logs/traces; they do not replace the Kubernetes
# Resource Metrics API — install metrics-server on the workload (default) for kubectl top / HPA there
# the same way kind uses ENABLE_METRICS_SERVER. For SPIRE+Keycloak, SPIRE chart sets JWKs needed by Keycloak;
# in-cluster Keycloak gets SPIFFE_OIDC_WELL_KNOWN_URL; add a broker IdP in Keycloak if you want SPIFFE JWTs in the realm.
# SPIRE: the spire-crds chart (ClusterSPIFFEID, etc.) is a separate in-cluster Argo app (sync-wave -3, same as metrics-server) before spire (wave 5).
ARGOCD_ENABLED="${ARGOCD_ENABLED:-true}"
# Install Argo CD on the CAPI-provisioned workload and apply in-cluster Applications there
WORKLOAD_ARGOCD_ENABLED="${WORKLOAD_ARGOCD_ENABLED:-true}"
WORKLOAD_ARGOCD_NAMESPACE="${WORKLOAD_ARGOCD_NAMESPACE:-argocd}"
# Resource metrics on the kind management cluster (kubectl top, HPA). Uses kubelet-insecure-tls patch for kind.
ENABLE_METRICS_SERVER="${ENABLE_METRICS_SERVER:-true}"
# Same for the CAPI/Proxmox workload cluster: kubelet resource metrics (kubectl top, HPA, VPA) — separate from VictoriaMetrics/OTel.
# When workload Argo is enabled, metrics-server is delivered via your app-of-apps Git (not kubectl) unless you disable that path.
# VictoriaMetrics/OTel do not replace the Kubernetes Resource Metrics API.
ENABLE_WORKLOAD_METRICS_SERVER="${ENABLE_WORKLOAD_METRICS_SERVER:-true}"
# Default true: CAPI/Proxmox kubelet certs often lack IP SANs; set false if metrics-server can verify kubelet TLS.
WORKLOAD_METRICS_SERVER_INSECURE_TLS="${WORKLOAD_METRICS_SERVER_INSECURE_TLS:-true}"
METRICS_SERVER_MANIFEST_URL="${METRICS_SERVER_MANIFEST_URL:-https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml}"
# Git tag for charts/metrics-server in kubernetes-sigs/metrics-server (in-cluster CAPI/Proxmox Argo App).
METRICS_SERVER_GIT_CHART_TAG="${METRICS_SERVER_GIT_CHART_TAG:-v0.7.2}"
# When true, Argo CD serves the API without TLS (insecure). Default false — use port-forward to :443 and trust or pass TLS to argocd login.
ARGOCD_SERVER_INSECURE="${ARGOCD_SERVER_INSECURE:-false}"
# Pin with upstream releases: https://github.com/argoproj/argo-cd/releases
ARGOCD_VERSION="${ARGOCD_VERSION:-v3.3.8}"
# Pin: https://github.com/argoproj-labs/argocd-operator/blob/main/config/default/kustomization.yaml
ARGOCD_OPERATOR_VERSION="${ARGOCD_OPERATOR_VERSION:-v0.16.0}"
# ArgoCD CR: spec.prometheus / spec.monitoring create Prometheus Operator ServiceMonitors and PrometheusRules — off by default (VictoriaMetrics uses VMServiceScrape from Git; see workload-app-of-apps). Set true with kube-prometheus-stack.
ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED="${ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED:-false}"
ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED="${ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED:-false}"
# Cilium + app-of-apps on the workload: CAAPH (HelmChartProxy); root app-of-apps
# (name = WORKLOAD_CLUSTER_NAME) from the **argocd-apps** chart (argo-helm repo). Ref:
# https://cluster-api.sigs.k8s.io/tasks/workload-bootstrap-gitops
# https://github.com/kubernetes-sigs/cluster-api-addon-provider-helm/blob/main/docs/quick-start.md
# App-of-apps: https://argo-cd.readthedocs.io/en/stable/operator-manual/cluster-bootstrapping/
WORKLOAD_GITOPS_MODE="${WORKLOAD_GITOPS_MODE:-caaph}"
# Public app-of-apps (override for a fork or private mirror).
WORKLOAD_APP_OF_APPS_GIT_URL="${WORKLOAD_APP_OF_APPS_GIT_URL:-https://github.com/lpasquali/workload-app-of-apps.git}"
WORKLOAD_APP_OF_APPS_GIT_PATH="${WORKLOAD_APP_OF_APPS_GIT_PATH:-examples/default}"
WORKLOAD_APP_OF_APPS_GIT_REF="${WORKLOAD_APP_OF_APPS_GIT_REF:-main}"
# Print Argo access only via --argocd-print-access (atomic; provisioned / workload cluster only).
ARGOCD_PRINT_ACCESS_TARGET="${ARGOCD_PRINT_ACCESS_TARGET:-workload}"
# Set by --argocd-print-access / --argocd-print-access-only; early exit (needs kubectl + KIND_* / WORKLOAD_* / CAPI manifest for workload)
ARGOCD_PRINT_ACCESS_STANDALONE="${ARGOCD_PRINT_ACCESS_STANDALONE:-false}"
# Port-forward is only via --argocd-port-forward (atomic; not run after a full bootstrap).
ARGOCD_PORT_FORWARD_STANDALONE="${ARGOCD_PORT_FORWARD_STANDALONE:-false}"
# Port-forward: provisioned (CAPI/Proxmox) cluster only
ARGOCD_PORT_FORWARD_TARGET="${ARGOCD_PORT_FORWARD_TARGET:-workload}"
# Local listen port (legacy ARGOCD_PORT_FORWARD_WORKLOAD_PORT still honored if set)
ARGOCD_PORT_FORWARD_PORT="${ARGOCD_PORT_FORWARD_PORT:-${ARGOCD_PORT_FORWARD_WORKLOAD_PORT:-8443}}"
# --workload-rollout: re-apply to CAPMOX workload without Phase 1/2 (see parse_options + early exit)
WORKLOAD_ROLLOUT_STANDALONE="${WORKLOAD_ROLLOUT_STANDALONE:-false}"
# --kind-backup / --kind-restore: snapshot or apply management kind cluster state (empty = not a standalone op)
BOOTSTRAP_KIND_STATE_OP="${BOOTSTRAP_KIND_STATE_OP:-}"
# Required for restore; for backup, optional (also BOOTSTRAP_KIND_BACKUP_OUT)
BOOTSTRAP_KIND_STATE_PATH="${BOOTSTRAP_KIND_STATE_PATH:-}"
WORKLOAD_ROLLOUT_MODE="${WORKLOAD_ROLLOUT_MODE:-argocd}"
WORKLOAD_ROLLOUT_NO_WAIT="${WORKLOAD_ROLLOUT_NO_WAIT:-false}"
PROXMOX_CSI_ENABLED="${PROXMOX_CSI_ENABLED:-true}"
# Post-install smoke: 1 Gi PVC on StorageClass, wait Bound, delete (Argo PostSync hook Job or standalone kubectl).
PROXMOX_CSI_SMOKE_ENABLED="${PROXMOX_CSI_SMOKE_ENABLED:-true}"
# PostSync hooks live in argo-postsync-hooks/* (parallel layout to the published workload-smoketests monorepo).
ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED="${ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED:-true}"
ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL="${ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL-https://github.com/lpasquali/workload-smoketests.git}"
ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH="${ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH-}"
ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF="${ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF-}"
ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE="${ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE:-}"
WORKLOAD_POSTSYNC_NAMESPACE="${WORKLOAD_POSTSYNC_NAMESPACE:-workload-smoke}"
PROXMOX_CSI_CHART_REPO_URL="${PROXMOX_CSI_CHART_REPO_URL:-oci://ghcr.io/sergelogvinov/charts}"
PROXMOX_CSI_CHART_NAME="${PROXMOX_CSI_CHART_NAME:-proxmox-csi-plugin}"
PROXMOX_CSI_CHART_VERSION="${PROXMOX_CSI_CHART_VERSION:-0.5.7}"  # chart semver on ghcr; bundles proxmox-csi v1.18.1
PROXMOX_CSI_NAMESPACE="${PROXMOX_CSI_NAMESPACE:-csi-proxmox}"
# proxmox-csi Helm config.features.provider (use proxmox for CAPMOX workload clusters)
PROXMOX_CSI_CONFIG_PROVIDER="${PROXMOX_CSI_CONFIG_PROVIDER:-proxmox}"
KYVERNO_ENABLED="${KYVERNO_ENABLED:-true}"
KYVERNO_CHART_VERSION="${KYVERNO_CHART_VERSION:-3.7.1}"  # chart 3.7.1 → app v1.17.1
KYVERNO_CHART_REPO_URL="${KYVERNO_CHART_REPO_URL:-https://kyverno.github.io/kyverno/}"
KYVERNO_NAMESPACE="${KYVERNO_NAMESPACE:-kyverno}"
KYVERNO_CLI_VERSION="${KYVERNO_CLI_VERSION:-v1.17.1}"
# When true, Helm sets global.tolerations for control-plane taints so Kyverno can schedule (single-CP or all nodes tainted).
KYVERNO_TOLERATE_CONTROL_PLANE="${KYVERNO_TOLERATE_CONTROL_PLANE:-true}"
CERT_MANAGER_ENABLED="${CERT_MANAGER_ENABLED:-true}"
CERT_MANAGER_CHART_VERSION="${CERT_MANAGER_CHART_VERSION:-v1.20.2}"
CERT_MANAGER_CHART_REPO_URL="${CERT_MANAGER_CHART_REPO_URL:-https://charts.jetstack.io}"
CERT_MANAGER_NAMESPACE="${CERT_MANAGER_NAMESPACE:-cert-manager}"
CMCTL_VERSION="${CMCTL_VERSION:-v2.4.1}"
CROSSPLANE_ENABLED="${CROSSPLANE_ENABLED:-true}"
CROSSPLANE_CHART_VERSION="${CROSSPLANE_CHART_VERSION:-2.2.1}"
CROSSPLANE_CHART_REPO_URL="${CROSSPLANE_CHART_REPO_URL:-https://charts.crossplane.io/stable}"
CROSSPLANE_NAMESPACE="${CROSSPLANE_NAMESPACE:-crossplane-system}"
CNPG_ENABLED="${CNPG_ENABLED:-true}"
CNPG_CHART_VERSION="${CNPG_CHART_VERSION:-}"  # empty = latest
CNPG_CHART_REPO_URL="${CNPG_CHART_REPO_URL:-https://cloudnative-pg.github.io/charts}"
CNPG_CHART_NAME="${CNPG_CHART_NAME:-cloudnative-pg}"
CNPG_NAMESPACE="${CNPG_NAMESPACE:-cnpg-system}"
# --- Workload in-cluster (second Argo) add-ons: chart repos / names (enable via *_ENABLED) ---
EXTERNAL_SECRETS_ENABLED="${EXTERNAL_SECRETS_ENABLED:-true}"
EXTERNAL_SECRETS_CHART_REPO_URL="${EXTERNAL_SECRETS_CHART_REPO_URL:-https://charts.external-secrets.io}"
EXTERNAL_SECRETS_CHART_VERSION="${EXTERNAL_SECRETS_CHART_VERSION:-}"  # * = latest
EXTERNAL_SECRETS_NAMESPACE="${EXTERNAL_SECRETS_NAMESPACE:-external-secrets-system}"
INFISICAL_OPERATOR_ENABLED="${INFISICAL_OPERATOR_ENABLED:-true}"
INFISICAL_CHART_REPO_URL="${INFISICAL_CHART_REPO_URL:-https://dl.cloudsmith.io/public/infisical/helm-charts/helm/charts/}"
INFISICAL_CHART_NAME="${INFISICAL_CHART_NAME:-secrets-operator}"
INFISICAL_CHART_VERSION="${INFISICAL_CHART_VERSION:-}"  # * = latest
INFISICAL_NAMESPACE="${INFISICAL_NAMESPACE:-infisical-system}"
SPIRE_ENABLED="${SPIRE_ENABLED:-true}"
# Stack chart: server, agent, CSI, OIDC, SPIRE controller. CRDs: Argo app "<workload>-spire-crds" (sync-wave -3; must be Synced before the spire app creates ClusterSPIFFEID resources).
# No trailing slash (avoids //spire in the Argo source column when combined with chart name).
SPIRE_CHART_REPO_URL="${SPIRE_CHART_REPO_URL:-https://spiffe.github.io/helm-charts-hardened}"
SPIRE_CHART_NAME="${SPIRE_CHART_NAME:-spire}"
# Pin to reduce targetRevision * drift; bump with upstream (spire app 0.28.x pairs with spire-crds 0.5.x+).
SPIRE_CHART_VERSION="${SPIRE_CHART_VERSION:-0.28.4}"
SPIRE_CRDS_CHART_NAME="${SPIRE_CRDS_CHART_NAME:-spire-crds}"
# Pin with spire upgrades (e.g. spire 0.28.x expects spire-crds 0.5.0+ per upstream README).
SPIRE_CRDS_CHART_VERSION="${SPIRE_CRDS_CHART_VERSION:-0.5.0}"
# SPIRE chart global install/upgrade/delete hook Jobs: disable for in-cluster Argo (they break sync, BackoffLimit; use raw helm if you need them).
SPIRE_HELM_ENABLE_GLOBAL_HOOKS="${SPIRE_HELM_ENABLE_GLOBAL_HOOKS:-false}"
SPIRE_NAMESPACE="${SPIRE_NAMESPACE:-spire}"
# When Keycloak is enabled: help SPIRE OIDC JWKS work with Keycloak; use HTTP oidc in-cluster (lab default). Set false + trust SPIRE’s CA in Keycloak for production TLS.
SPIRE_OIDC_INSECURE_HTTP="${SPIRE_OIDC_INSECURE_HTTP:-true}"
# ConfigMap = file bundle only, no Workload API. CSI = upstream default (OIDC needs SPIFFE CSI + spire-agent on the node; use SPIRE_TOLERATE_CONTROL_PLANE=true on tainted control-plane nodes).
SPIRE_OIDC_BUNDLE_SOURCE="${SPIRE_OIDC_BUNDLE_SOURCE:-CSI}"
# SPIRE agent, SPIFFE CSI node plugin, and OIDC must schedule on nodes that run the rest (e.g. single control-plane CAPI); match Kyverno’s pattern.
SPIRE_TOLERATE_CONTROL_PLANE="${SPIRE_TOLERATE_CONTROL_PLANE:-true}"
OTEL_ENABLED="${OTEL_ENABLED:-true}"
# Upstream index lives at open-telemetry.github.io/.../opentelemetry-helm-charts/ (trailing "charts" is required).
OTEL_CHART_REPO_URL="${OTEL_CHART_REPO_URL:-https://open-telemetry.github.io/opentelemetry-helm-charts}"
OTEL_CHART_NAME="${OTEL_CHART_NAME:-opentelemetry-collector}"
# Pin a published chart so Argo can resolve targetRevision; set to * to always track the repo index (may show Status Unknown in some Argo/Helm edge cases).
OTEL_CHART_VERSION="${OTEL_CHART_VERSION:-0.152.0}"
# Chart 0.88+ requires an explicit image repository; k8s distro is upstream default recommendation (see opentelemetry-collector/UPGRADING.md).
OTEL_IMAGE_REPOSITORY="${OTEL_IMAGE_REPOSITORY:-otel/opentelemetry-collector-k8s}"
# deployment | daemonset | statefulset
OTEL_COLLECTOR_MODE="${OTEL_COLLECTOR_MODE:-deployment}"
OTEL_NAMESPACE="${OTEL_NAMESPACE:-opentelemetry}"
GRAFANA_ENABLED="${GRAFANA_ENABLED:-true}"
GRAFANA_CHART_REPO_URL="${GRAFANA_CHART_REPO_URL:-https://grafana.github.io/helm-charts}"
GRAFANA_CHART_VERSION="${GRAFANA_CHART_VERSION:-}"
GRAFANA_NAMESPACE="${GRAFANA_NAMESPACE:-grafana}"
VICTORIAMETRICS_ENABLED="${VICTORIAMETRICS_ENABLED:-true}"
VICTORIAMETRICS_CHART_REPO_URL="${VICTORIAMETRICS_CHART_REPO_URL:-https://victoriametrics.github.io/helm-charts/}"
VICTORIAMETRICS_CHART_NAME="${VICTORIAMETRICS_CHART_NAME:-victoria-metrics-single}"
VICTORIAMETRICS_CHART_VERSION="${VICTORIAMETRICS_CHART_VERSION:-}"  # empty = latest
VICTORIAMETRICS_NAMESPACE="${VICTORIAMETRICS_NAMESPACE:-victoria-metrics}"
# Set BACKSTAGE_ENABLED=true and your chart (repo + name) — no universal default; disabled until configured.
BACKSTAGE_ENABLED="${BACKSTAGE_ENABLED:-false}"
BACKSTAGE_CHART_REPO_URL="${BACKSTAGE_CHART_REPO_URL:-}"
BACKSTAGE_CHART_NAME="${BACKSTAGE_CHART_NAME:-backstage}"
BACKSTAGE_CHART_VERSION="${BACKSTAGE_CHART_VERSION:-}"
BACKSTAGE_NAMESPACE="${BACKSTAGE_NAMESPACE:-backstage}"
KEYCLOAK_ENABLED="${KEYCLOAK_ENABLED:-true}"
KEYCLOAK_CHART_REPO_URL="${KEYCLOAK_CHART_REPO_URL:-https://codecentric.github.io/helm-charts}"
KEYCLOAK_CHART_NAME="${KEYCLOAK_CHART_NAME:-keycloakx}"
KEYCLOAK_CHART_VERSION="${KEYCLOAK_CHART_VERSION:-}"
KEYCLOAK_NAMESPACE="${KEYCLOAK_NAMESPACE:-keycloak}"
# Quarkus/Keycloak 26+: set KEYCLOAK_KC_HOSTNAME to your public URL when using Ingress/ TLS; if unset, bootstrap uses the in-cluster keycloakx Service (codecentric chart: <workload>-keycloak-keycloakx.<ns>.svc.cluster.local). Optional KEYCLOAK_KC_DB (e.g. postgres) to override the implicit dev DB warning.
KEYCLOAK_KC_HOSTNAME_STRICT="${KEYCLOAK_KC_HOSTNAME_STRICT:-false}"
KEYCLOAK_KC_HOSTNAME="${KEYCLOAK_KC_HOSTNAME:-}"
KEYCLOAK_KC_DB="${KEYCLOAK_KC_DB:-}" # e.g. dev-mem, dev-file, or postgres; empty = leave Keycloak default
# Keycloak "operator" in the sense of Keycloak on Kubernetes: optional second chart or Git/kustomize (often OLM-only).
KEYCLOAK_OPERATOR_ENABLED="${KEYCLOAK_OPERATOR_ENABLED:-false}"
KEYCLOAK_OPERATOR_GIT_URL="${KEYCLOAK_OPERATOR_GIT_URL:-}"
KEYCLOAK_OPERATOR_GIT_PATH="${KEYCLOAK_OPERATOR_GIT_PATH:-.}"
KEYCLOAK_OPERATOR_GIT_REF="${KEYCLOAK_OPERATOR_GIT_REF:-main}"
KEYCLOAK_OPERATOR_NAMESPACE="${KEYCLOAK_OPERATOR_NAMESPACE:-keycloak-realm-operator}"
# Empty default: never use ~/.cluster-api/clusterctl.yaml or ./proxmox-admin.yaml (optional explicit paths for legacy use).
CLUSTERCTL_CFG="${CLUSTERCTL_CFG:-}"
PROXMOX_ADMIN_CONFIG="${PROXMOX_ADMIN_CONFIG:-}"
PROXMOX_CSI_CONFIG="${PROXMOX_CSI_CONFIG:-}"
PROXMOX_BOOTSTRAP_SECRET_NAMESPACE="${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE:-proxmox-bootstrap-system}"
# Optional legacy: when set, CAPI+CSI (and admin when admin Secret name matches) are stored in this single Secret instead of capmox + csi.
PROXMOX_BOOTSTRAP_SECRET_NAME="${PROXMOX_BOOTSTRAP_SECRET_NAME:-}"
PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME="${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME:-proxmox-bootstrap-capmox-credentials}"
PROXMOX_BOOTSTRAP_CSI_SECRET_NAME="${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME:-proxmox-bootstrap-csi-credentials}"
PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME="${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME:-proxmox-bootstrap-admin-credentials}"
# Set while loading: Proxmox API secrets on kind override env, clusterctl.yaml, and Terraform state reloads.
PROXMOX_BOOTSTRAP_KIND_SECRET_USED="${PROXMOX_BOOTSTRAP_KIND_SECRET_USED:-false}"
PROXMOX_KIND_CAPMOX_CREDENTIALS_ACTIVE="${PROXMOX_KIND_CAPMOX_CREDENTIALS_ACTIVE:-false}"
PROXMOX_IDENTITY_TF="${PROXMOX_IDENTITY_TF:-proxmox-identity.tf}"
PROXMOX_ADMIN_INSECURE="${PROXMOX_ADMIN_INSECURE:-true}"
CLUSTER_SET_ID="${CLUSTER_SET_ID:-}"
# Re-create CAPI/CSI Proxmox API users/tokens via Terraform and push secrets to kind + workload.
RECREATE_PROXMOX_IDENTITIES="${RECREATE_PROXMOX_IDENTITIES:-false}"
PROXMOX_IDENTITY_RECREATE_SCOPE="${PROXMOX_IDENTITY_RECREATE_SCOPE:-both}"
PROXMOX_IDENTITY_RECREATE_STATE_RM="${PROXMOX_IDENTITY_RECREATE_STATE_RM:-false}"
PROXMOX_IDENTITY_SUFFIX="${PROXMOX_IDENTITY_SUFFIX:-}"
PROXMOX_URL="${PROXMOX_URL:-}"
PROXMOX_TOKEN="${PROXMOX_TOKEN:-}"
PROXMOX_SECRET="${PROXMOX_SECRET:-}"
PROXMOX_ADMIN_USERNAME="${PROXMOX_ADMIN_USERNAME:-root@pam!capi-bootstrap}"
PROXMOX_ADMIN_TOKEN="${PROXMOX_ADMIN_TOKEN:-}"
PROXMOX_REGION="${PROXMOX_REGION:-}"
PROXMOX_NODE="${PROXMOX_NODE:-}"
PROXMOX_SOURCENODE="${PROXMOX_SOURCENODE:-}"
PROXMOX_CSI_TOPOLOGY_LABELS="${PROXMOX_CSI_TOPOLOGY_LABELS:-true}"
PROXMOX_TOPOLOGY_REGION="${PROXMOX_TOPOLOGY_REGION:-}"
PROXMOX_TOPOLOGY_ZONE="${PROXMOX_TOPOLOGY_ZONE:-}"
PROXMOX_TEMPLATE_ID="${PROXMOX_TEMPLATE_ID:-${TEMPLATE_VMID:-104}}"
unset TEMPLATE_VMID 2>/dev/null || true
PROXMOX_BRIDGE="${PROXMOX_BRIDGE:-vmbr0}"
CONTROL_PLANE_ENDPOINT_IP="${CONTROL_PLANE_ENDPOINT_IP:-10.27.192.20}"
CONTROL_PLANE_ENDPOINT_PORT="${CONTROL_PLANE_ENDPOINT_PORT:-6443}"
NODE_IP_RANGES="${NODE_IP_RANGES:-10.27.192.21-10.27.192.30}"
GATEWAY="${GATEWAY:-10.27.192.78}"
IP_PREFIX="${IP_PREFIX:-24}"
DNS_SERVERS="${DNS_SERVERS:-8.8.8.8,8.8.4.4}"
# Set true when the user passes the matching CLI option so we do not override from a running ProxmoxCluster.
DNS_SERVERS_EXPLICIT="${DNS_SERVERS_EXPLICIT:-false}"
GATEWAY_EXPLICIT="${GATEWAY_EXPLICIT:-false}"
IP_PREFIX_EXPLICIT="${IP_PREFIX_EXPLICIT:-false}"
NODE_IP_RANGES_EXPLICIT="${NODE_IP_RANGES_EXPLICIT:-false}"
ALLOWED_NODES_EXPLICIT="${ALLOWED_NODES_EXPLICIT:-false}"
ALLOWED_NODES="${ALLOWED_NODES:-${PROXMOX_NODE}}"
VM_SSH_KEYS="${VM_SSH_KEYS:-}"
PROXMOX_CSI_URL="${PROXMOX_CSI_URL:-}"
PROXMOX_CSI_TOKEN_ID="${PROXMOX_CSI_TOKEN_ID:-}"
PROXMOX_CSI_TOKEN_SECRET="${PROXMOX_CSI_TOKEN_SECRET:-}"
PROXMOX_CSI_USER_ID="${PROXMOX_CSI_USER_ID:-}"
PROXMOX_CSI_TOKEN_PREFIX="${PROXMOX_CSI_TOKEN_PREFIX:-csi}"
PROXMOX_CSI_INSECURE="${PROXMOX_CSI_INSECURE:-$PROXMOX_ADMIN_INSECURE}"
PROXMOX_CSI_STORAGE_CLASS_NAME="${PROXMOX_CSI_STORAGE_CLASS_NAME:-proxmox-data-xfs}"
PROXMOX_CSI_STORAGE="${PROXMOX_CSI_STORAGE:-local-lvm}"
PROXMOX_CLOUDINIT_STORAGE="${PROXMOX_CLOUDINIT_STORAGE:-local}"
PROXMOX_MEMORY_ADJUSTMENT="${PROXMOX_MEMORY_ADJUSTMENT:-0}"
PROXMOX_CSI_RECLAIM_POLICY="${PROXMOX_CSI_RECLAIM_POLICY:-Delete}"
PROXMOX_CSI_FSTYPE="${PROXMOX_CSI_FSTYPE:-xfs}"
PROXMOX_CSI_DEFAULT_CLASS="${PROXMOX_CSI_DEFAULT_CLASS:-true}"
PROXMOX_CAPI_USER_ID="${PROXMOX_CAPI_USER_ID:-}"
PROXMOX_CAPI_TOKEN_PREFIX="${PROXMOX_CAPI_TOKEN_PREFIX:-capi}"
CONTROL_PLANE_BOOT_VOLUME_DEVICE="${CONTROL_PLANE_BOOT_VOLUME_DEVICE:-scsi0}"
CONTROL_PLANE_BOOT_VOLUME_SIZE="${CONTROL_PLANE_BOOT_VOLUME_SIZE:-100}"
CONTROL_PLANE_NUM_SOCKETS="${CONTROL_PLANE_NUM_SOCKETS:-2}"
CONTROL_PLANE_NUM_CORES="${CONTROL_PLANE_NUM_CORES:-1}"
CONTROL_PLANE_MEMORY_MIB="${CONTROL_PLANE_MEMORY_MIB:-8192}"
WORKER_BOOT_VOLUME_DEVICE="${WORKER_BOOT_VOLUME_DEVICE:-scsi0}"
WORKER_BOOT_VOLUME_SIZE="${WORKER_BOOT_VOLUME_SIZE:-100}"
WORKER_NUM_SOCKETS="${WORKER_NUM_SOCKETS:-2}"
WORKER_NUM_CORES="${WORKER_NUM_CORES:-4}"
WORKER_MEMORY_MIB="${WORKER_MEMORY_MIB:-16384}"
WORKLOAD_CLUSTER_NAME="${WORKLOAD_CLUSTER_NAME:-capi-quickstart}"
WORKLOAD_CILIUM_CLUSTER_ID="${WORKLOAD_CILIUM_CLUSTER_ID:-}"
WORKLOAD_CLUSTER_NAMESPACE="${WORKLOAD_CLUSTER_NAMESPACE:-default}"
# Set to 1 by --workload-cluster-name / --workload-cluster-namespace (disables implicit single-cluster pick)
WORKLOAD_CLUSTER_NAME_EXPLICIT="${WORKLOAD_CLUSTER_NAME_EXPLICIT:-0}"
WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT="${WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT:-0}"
WORKLOAD_KUBERNETES_VERSION="${WORKLOAD_KUBERNETES_VERSION:-v1.35.0}"
CONTROL_PLANE_MACHINE_COUNT="${CONTROL_PLANE_MACHINE_COUNT:-1}"
WORKER_MACHINE_COUNT="${WORKER_MACHINE_COUNT:-2}"
EXP_CLUSTER_RESOURCE_SET="${EXP_CLUSTER_RESOURCE_SET:-false}"
CLUSTER_TOPOLOGY="${CLUSTER_TOPOLOGY:-true}"
EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION="${EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION:-true}"

# --- Helpers ------------------------------------------------------------------
log()  { printf '✅ 🎉 %s\n' "$*"; }
warn() { printf '⚠️ 🙈 %s\n' "$*" >&2; }
die()  { printf '❌ 💩 %s\n' "$*" >&2; exit 1; }

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "Required command not found on PATH: $1"
}

require_file() {
  [[ -f "$1" ]] || die "Required file not found: $1"
}

# Prompt user for yes/no confirmation.
# Usage: confirm "Continue?" && do_something
confirm() {
  local msg="$1"
  local response
  printf '\033[1;33m[?]\033[0m %s (yes/no): ' "$msg" >&2
  read -r response
  [[ "$response" =~ ^[Yy] ]]
}

# True when stdin is a TTY or /dev/tty is available (IDE tasks often have a non-TTY stdin).
bootstrap_can_interactive_prompt() {
  [[ -t 0 ]] && return 0
  [[ -r /dev/tty && -w /dev/tty ]] && return 0
  return 1
}

# Read one line from stdin, or from /dev/tty when stdin is not a TTY.
bootstrap_read_line() {
  local __line __var="$1"
  if [[ -t 0 ]]; then
    IFS= read -r __line || true
  else
    IFS= read -r __line < /dev/tty || true
  fi
  printf -v "$__var" %s "$__line"
}

# Turn menu input into a single index 1..max (e.g. users type "1-1" after seeing an old "1-1" prompt).
bootstrap_normalize_numeric_menu_choice() {
  local raw="$1" max="$2" num
  raw="${raw#"${raw%%[![:space:]]*}"}"
  raw="${raw%"${raw##*[![:space:]]}"}"
  [[ -z "$raw" ]] && { echo ""; return 0; }

  if [[ "$raw" =~ ^[0-9]+$ ]]; then
    num="$raw"
  elif [[ "$raw" =~ ^([0-9]+)-([0-9]+)$ ]]; then
    num="${BASH_REMATCH[1]}"
  elif [[ "$raw" =~ ^[^0-9]*([0-9]+) ]]; then
    num="${BASH_REMATCH[1]}"
  else
    echo ""
    return 0
  fi

  [[ -n "$max" && "$max" -gt 0 ]] && { (( num < 1 || num > max )) && { echo ""; return 0; }; }
  echo "$num"
}

# Run a command with sudo only when not already root.
RUN_PRIVILEGED() { [[ "$(id -u)" -eq 0 ]] && "$@" || sudo "$@"; }

# Treat common textual values as true.
is_true() {
  case "${1:-}" in
    1|true|TRUE|yes|YES|on|ON)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

# Workload Argo: only CAAPH (HelmChartProxy, app-of-apps from Git). kind-argocd (management Argo + in-script
# Applications) was removed from this script.
is_workload_gitops_caaph_mode() {
  case "${WORKLOAD_GITOPS_MODE:-caaph}" in
    caaph) return 0 ;;
    *) return 1 ;;
  esac
}

# If merge/fetch left workload app-of-apps Git fields empty, re-apply the same defaults as the header / parse_options.
reapply_workload_git_defaults() {
  if is_workload_gitops_caaph_mode; then
    if [[ -z "${WORKLOAD_APP_OF_APPS_GIT_URL:-}" && -n "${WORKLOAD_ARGO_GIT_BASE_URL_DEFAULT:-}" ]]; then
      WORKLOAD_APP_OF_APPS_GIT_URL="${WORKLOAD_ARGO_GIT_BASE_URL_DEFAULT}/workload-app-of-apps"
    fi
    if [[ -z "${WORKLOAD_APP_OF_APPS_GIT_PATH:-}" && -n "${WORKLOAD_GIT_EXAMPLES_EXAMPLE:-}" ]]; then
      WORKLOAD_APP_OF_APPS_GIT_PATH="examples/${WORKLOAD_GIT_EXAMPLES_EXAMPLE}"
    fi
    if [[ -z "${WORKLOAD_APP_OF_APPS_GIT_REF:-}" && -n "${WORKLOAD_GIT_EXAMPLES_EXAMPLE_DEFAULT_REF:-}" ]]; then
      WORKLOAD_APP_OF_APPS_GIT_REF="${WORKLOAD_GIT_EXAMPLES_EXAMPLE_DEFAULT_REF}"
    fi
  fi
}

# When true, push to kind (proxmox-bootstrap-system); never the default well-known local YAML paths.
persist_local_secrets() {
  is_true "${BOOTSTRAP_PERSIST_LOCAL_SECRETS:-false}"
}

_clusterctl_cfg_file_present() { [[ -n "${CLUSTERCTL_CFG:-}" && -f "$CLUSTERCTL_CFG" ]]; }
_proxmox_admin_cfg_file_present() { [[ -n "${PROXMOX_ADMIN_CONFIG:-}" && -f "$PROXMOX_ADMIN_CONFIG" ]]; }
have_clusterctl_creds_in_env() {
  [[ -n "${PROXMOX_URL:-}" && -n "${PROXMOX_TOKEN:-}" && -n "${PROXMOX_SECRET:-}" ]]
}

# Read a single top-level scalar from a small YAML/ENV-style file (KEY: value, optional quotes).
# Prints nothing if missing. Always returns 0 so ${VAR:-$(_get_yaml_value ...)} is safe with set -e.
_get_yaml_value() {
  local _f="${1:-}" _k="${2:-}"
  [[ -n "$_f" && -f "$_f" && -n "$_k" ]] || return 0
  command -v python3 >/dev/null 2>&1 || return 0
  python3 -c '
import re, sys
path, want = sys.argv[1], sys.argv[2]
try:
    text = open(path, encoding="utf-8", errors="replace").read()
except OSError:
    raise SystemExit(0)
for line in text.splitlines():
    s = line.split("#", 1)[0].rstrip()
    if not s or ":" not in s:
        continue
    m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$", s)
    if not m:
        continue
    k, val = m.group(1), m.group(2).strip()
    if k != want:
        continue
    if (val.startswith(chr(34)) and val.endswith(chr(34))) or (val.startswith(chr(39)) and val.endswith(chr(39))):
        val = val[1:-1]
    sys.stdout.write(val)
    break
' "$_f" "$_k" 2>/dev/null || true
  return 0
}

# kind-<KIND_CLUSTER_NAME> when that context exists, else kubectl current-context if it is kind-*.
# Ensures merge/sync/apply hit the same cluster as try_load (critical when default capi-provisioner is wrong).
_resolve_bootstrap_kubectl_context() {
  local ctx _cur
  command -v kubectl >/dev/null 2>&1 || return 1
  ctx="kind-${KIND_CLUSTER_NAME:-capi-provisioner}"
  if (kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | contains_line "$ctx"); then
    printf '%s\n' "$ctx"
    return 0
  fi
  _cur="$(kubectl config current-context 2>/dev/null || true)"
  if [[ "$_cur" =~ ^kind- ]]; then
    printf '%s\n' "$_cur"
    return 0
  fi
  return 1
}

# Push _get_all_bootstrap_variables_as_yaml to Secret ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME} (key ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_KEY})
# whenever the management kind cluster exists. Secrets / admin / workload manifest fields are not written here; see
# ${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME}, ${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} (or legacy ${PROXMOX_BOOTSTRAP_SECRET_NAME}), and ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME}. No-op if kind is not on this host yet.
sync_bootstrap_config_to_kind() {
  local ctx cname
  command -v kubectl >/dev/null 2>&1 || return 0
  command -v kind >/dev/null 2>&1 || return 0
  ctx="$(_resolve_bootstrap_kubectl_context 2>/dev/null)" || return 0
  cname="${ctx#kind-}"
  if ! (kind get clusters 2>/dev/null | tr -d '\r' | contains_line "$cname"); then
    return 0
  fi
  apply_bootstrap_config_to_management_cluster
}

# Merge Proxmox API material from the current process environment into kind Secrets.
# CAPI/CSI data keys are PROXMOX_* only (no url/token/secret or capi_token_* mirrors). Existing alias keys are dropped on the next apply.
# Default: ${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME} (CAPI) + ${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} (CSI) + ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME} (proxmox-admin.yaml).
# Legacy: set PROXMOX_BOOTSTRAP_SECRET_NAME to store CAPI+CSI in a single Secret (and admin in the same Secret if its name matches).
# When capmox-system exists and PROXMOX_URL/PROXMOX_TOKEN/PROXMOX_SECRET are set, also applies capmox-system/capmox-manager-credentials (see update_capmox_manager_secret_on_kind) so a deleted in-cluster copy is restored on the next sync.
sync_proxmox_bootstrap_literal_credentials_to_kind() {
  local ctx cname existing_json
  command -v kubectl >/dev/null 2>&1 || return 0
  command -v kind >/dev/null 2>&1 || return 0
  ctx="$(_resolve_bootstrap_kubectl_context 2>/dev/null)" || return 0
  cname="${ctx#kind-}"
  if ! (kind get clusters 2>/dev/null | tr -d '\r' | contains_line "$cname"); then
    return 0
  fi

  kubectl --context "$ctx" create namespace "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" \
    --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - >/dev/null

  if [[ -n "${PROXMOX_BOOTSTRAP_SECRET_NAME:-}" ]]; then
    # --- Legacy: CAPI+CSI in one Secret; admin is proxmox-admin.yaml (same or separate Secret name) ---
    local use_split=0
    if [[ -n "${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME:-}" && "$PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME" != "$PROXMOX_BOOTSTRAP_SECRET_NAME" ]]; then
      use_split=1
    fi
    existing_json="$(kubectl --context "$ctx" get secret "$PROXMOX_BOOTSTRAP_SECRET_NAME" \
      -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null)" || true
    [[ -z "$existing_json" ]] && existing_json="{}"
    printf '%s' "$existing_json" | PROXMOX_BOOTSTRAP_SECRET_NAME="$PROXMOX_BOOTSTRAP_SECRET_NAME" \
      PROXMOX_BOOTSTRAP_SECRET_NAMESPACE="$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" \
      python3 -c '
import base64, json, os, sys

def b64e(s: str) -> str:
    return base64.b64encode(s.encode("utf-8")).decode("ascii")

def main_keys_workload():
    return [
        "PROXMOX_URL", "PROXMOX_TOKEN", "PROXMOX_SECRET",
        "PROXMOX_CSI_URL", "PROXMOX_CSI_TOKEN_ID", "PROXMOX_CSI_TOKEN_SECRET",
        "PROXMOX_REGION", "PROXMOX_NODE",
    ]

KEYS = main_keys_workload()
name = os.environ["PROXMOX_BOOTSTRAP_SECRET_NAME"]
ns = os.environ["PROXMOX_BOOTSTRAP_SECRET_NAMESPACE"]
try:
    cur = json.load(sys.stdin)
except Exception:
    cur = {}
data = {}
allowed = set(KEYS)
if cur.get("data") and isinstance(cur["data"], dict):
    for k, v in cur["data"].items():
        if k in allowed and isinstance(v, str) and v:
            data[k] = v
for k in KEYS:
    v = os.environ.get(k, "")
    if v is None or str(v) == "":
        continue
    data[k] = b64e(str(v))
out = {
    "apiVersion": "v1",
    "kind": "Secret",
    "metadata": {"name": name, "namespace": ns, "labels": (cur.get("metadata") or {}).get("labels")},
    "type": "Opaque",
    "data": data,
}
if not out["metadata"]["labels"]:
    del out["metadata"]["labels"]
json.dump(out, sys.stdout)
' | kubectl --context "$ctx" apply -f - \
      || warn "Failed to update ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_SECRET_NAME} (legacy CAPI/CSI keys)."
    log "Updated ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_SECRET_NAME} (legacy CAPI/CSI from current environment)."

    if [[ "$use_split" -eq 1 ]]; then
      _apply_proxmox_bootstrap_admin_yaml_to_kind "$ctx" "${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME}" || true
    else
      _apply_proxmox_bootstrap_admin_yaml_to_kind "$ctx" "${PROXMOX_BOOTSTRAP_SECRET_NAME}" || true
    fi
    update_capmox_manager_secret_on_kind
    return 0
  fi

  # --- CAPMOX (clusterctl) + CSI: split defaults ---
  existing_json="$(kubectl --context "$ctx" get secret "$PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME" \
    -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null)" || true
  [[ -z "$existing_json" ]] && existing_json="{}"
  printf '%s' "$existing_json" | PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME="$PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME" \
    PROXMOX_BOOTSTRAP_SECRET_NAMESPACE="$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" \
    python3 -c '
import base64, json, os, sys

def b64e(s: str) -> str:
    return base64.b64encode(s.encode("utf-8")).decode("ascii")

KEYS = ["PROXMOX_URL", "PROXMOX_TOKEN", "PROXMOX_SECRET", "PROXMOX_REGION", "PROXMOX_NODE"]
allowed = set(KEYS)
name = os.environ["PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME"]
ns = os.environ["PROXMOX_BOOTSTRAP_SECRET_NAMESPACE"]
try:
    cur = json.load(sys.stdin)
except Exception:
    cur = {}
data = {}
if cur.get("data") and isinstance(cur["data"], dict):
    for k, v in cur["data"].items():
        if k in allowed and isinstance(v, str) and v:
            data[k] = v
for k in KEYS:
    v = os.environ.get(k, "")
    if v is None or str(v) == "":
        continue
    data[k] = b64e(str(v))
out = {
    "apiVersion": "v1",
    "kind": "Secret",
    "metadata": {"name": name, "namespace": ns, "labels": (cur.get("metadata") or {}).get("labels")},
    "type": "Opaque",
    "data": data,
}
if not out["metadata"]["labels"]:
    del out["metadata"]["labels"]
json.dump(out, sys.stdout)
' | kubectl --context "$ctx" apply -f - \
    || warn "Failed to update ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME}."
  log "Updated ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME} (CAPI / clusterctl keys from current environment)."

  if [[ -n "${PROXMOX_URL:-}" ]]; then
    PROXMOX_CSI_URL="${PROXMOX_CSI_URL:-$(proxmox_api_json_url)}"
    export PROXMOX_CSI_URL
  fi
  existing_json="$(kubectl --context "$ctx" get secret "$PROXMOX_BOOTSTRAP_CSI_SECRET_NAME" \
    -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null)" || true
  [[ -z "$existing_json" ]] && existing_json="{}"
  printf '%s' "$existing_json" | PROXMOX_BOOTSTRAP_CSI_SECRET_NAME="$PROXMOX_BOOTSTRAP_CSI_SECRET_NAME" \
    PROXMOX_BOOTSTRAP_SECRET_NAMESPACE="$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" \
    python3 -c '
import base64, json, os, sys

def b64e(s: str) -> str:
    return base64.b64encode(s.encode("utf-8")).decode("ascii")

KEYS = [
    "PROXMOX_URL", "PROXMOX_REGION", "PROXMOX_NODE",
    "PROXMOX_CSI_URL", "PROXMOX_CSI_TOKEN_ID", "PROXMOX_CSI_TOKEN_SECRET",
    "PROXMOX_CSI_USER_ID", "PROXMOX_CSI_TOKEN_PREFIX", "PROXMOX_CSI_INSECURE",
    "PROXMOX_CSI_STORAGE_CLASS_NAME", "PROXMOX_CSI_STORAGE",
    "PROXMOX_CSI_RECLAIM_POLICY", "PROXMOX_CSI_FSTYPE", "PROXMOX_CSI_DEFAULT_CLASS",
    "PROXMOX_CSI_TOPOLOGY_LABELS", "PROXMOX_TOPOLOGY_REGION", "PROXMOX_TOPOLOGY_ZONE",
    "PROXMOX_CSI_CHART_REPO_URL", "PROXMOX_CSI_CHART_NAME", "PROXMOX_CSI_CHART_VERSION",
    "PROXMOX_CSI_NAMESPACE", "PROXMOX_CSI_CONFIG_PROVIDER",
    "PROXMOX_CSI_SMOKE_ENABLED",
    "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL", "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH",
    "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF", "ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE",
]
allowed = set(KEYS)
name = os.environ["PROXMOX_BOOTSTRAP_CSI_SECRET_NAME"]
ns = os.environ["PROXMOX_BOOTSTRAP_SECRET_NAMESPACE"]
try:
    cur = json.load(sys.stdin)
except Exception:
    cur = {}
data = {}
if cur.get("data") and isinstance(cur["data"], dict):
    for k, v in cur["data"].items():
        if k in allowed and isinstance(v, str) and v:
            data[k] = v
for k in KEYS:
    v = os.environ.get(k, "")
    if v is None or str(v) == "":
        continue
    data[k] = b64e(str(v))
out = {
    "apiVersion": "v1",
    "kind": "Secret",
    "metadata": {"name": name, "namespace": ns, "labels": (cur.get("metadata") or {}).get("labels")},
    "type": "Opaque",
    "data": data,
}
if not out["metadata"]["labels"]:
    del out["metadata"]["labels"]
json.dump(out, sys.stdout)
' | kubectl --context "$ctx" apply -f - \
    || warn "Failed to update ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME}."
  log "Updated ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} (CSI: API URL, token id/secret, user id, chart/storage toggles, … from current environment)."
  if [[ -z "${PROXMOX_CSI_TOKEN_ID:-}" || -z "${PROXMOX_CSI_TOKEN_SECRET:-}" ]]; then
    warn "PROXMOX_CSI_TOKEN_ID/PROXMOX_CSI_TOKEN_SECRET are unset; ${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} will not contain CSI tokens until Terraform identity outputs or the environment set them (URL/region are still written when known)."
  fi

  _apply_proxmox_bootstrap_admin_yaml_to_kind "$ctx" "${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME}" || true
  update_capmox_manager_secret_on_kind
}

# Merge existing Secret data with proxmox-admin.yaml from the current environment (key ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY}).
_apply_proxmox_bootstrap_admin_yaml_to_kind() {
  local ctx="$1" target_secret="$2" existing_json
  command -v kubectl >/dev/null 2>&1 || return 0
  [[ -n "$target_secret" ]] || return 0
  existing_json="$(kubectl --context "$ctx" get secret "$target_secret" \
    -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null)" || true
  [[ -z "$existing_json" ]] && existing_json="{}"
  printf '%s' "$existing_json" | PROXMOX_BOOTSTRAP_SECRET_NAMESPACE="$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" \
    PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY="${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY:-proxmox-admin.yaml}" \
    target_secret="$target_secret" \
    python3 -c '
import base64, json, os, sys

def b64e(s: str) -> str:
    return base64.b64encode(s.encode("utf-8")).decode("ascii")

ak = os.environ.get("PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY", "proxmox-admin.yaml")
name = os.environ["target_secret"]
ns = os.environ["PROXMOX_BOOTSTRAP_SECRET_NAMESPACE"]
admin_keys = [
    "PROXMOX_URL", "PROXMOX_ADMIN_USERNAME", "PROXMOX_ADMIN_TOKEN", "PROXMOX_ADMIN_INSECURE",
]
if not any(os.environ.get(k, "") for k in admin_keys):
    sys.exit(0)
try:
    cur = json.load(sys.stdin)
except Exception:
    cur = {}
data = {}
if cur.get("data") and isinstance(cur["data"], dict):
    for k, v in cur["data"].items():
        if isinstance(v, str) and v:
            data[k] = v
import json as pyjson
lines = []
for k in admin_keys:
    v = os.environ.get(k, "")
    if v is None or str(v) == "":
        continue
    lines.append(f"{k}: {pyjson.dumps(str(v))}")
text = "\n".join(lines) + ("\n" if lines else "")
if not text.strip():
    sys.exit(0)
data[ak] = b64e(text)
out = {
    "apiVersion": "v1",
    "kind": "Secret",
    "metadata": {"name": name, "namespace": ns, "labels": (cur.get("metadata") or {}).get("labels")},
    "type": "Opaque",
    "data": data,
}
if not out["metadata"]["labels"]:
    del out["metadata"]["labels"]
json.dump(out, sys.stdout)
' | kubectl --context "$ctx" apply -f - \
    || { warn "Failed to update ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${target_secret} (proxmox-admin.yaml)."; return 1; }
  log "Updated ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${target_secret} (merged ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY} from current environment)."
}

# Cilium Ingress requires kube-proxy replacement. Defaults: CILIUM_INGRESS=true, CILIUM_KUBE_PROXY_REPLACEMENT=true; use false for kube-proxy + no Cilium ingress.
cilium_needs_kube_proxy_replacement() {
  local kpr
  kpr="$(printf '%s' "${CILIUM_KUBE_PROXY_REPLACEMENT:-auto}" | tr '[:upper:]' '[:lower:]')"
  case "$kpr" in
    auto | '')
      is_true "$CILIUM_INGRESS"
      ;;
    true | 1 | yes | on)
      return 0
      ;;
    false | 0 | no | off)
      if is_true "$CILIUM_INGRESS"; then
        die "Cilium Ingress requires kube-proxy replacement — set CILIUM_KUBE_PROXY_REPLACEMENT to auto or true (not false)."
      fi
      return 1
      ;;
    *)
      die "Invalid CILIUM_KUBE_PROXY_REPLACEMENT='${CILIUM_KUBE_PROXY_REPLACEMENT}' (use auto, true, or false)."
      ;;
  esac
}

# Checks if a string is present in a stream of lines.
# Usage: stream_command | contains_line "string_to_find"
contains_line() {
  local target="$1" line
  while IFS= read -r line; do
    if [[ "$line" == "$target" ]]; then
      return 0
    fi
  done
  return 1
}

# Derives a CIDR for the Cilium LB IPAM pool from the node IP range and prefix.
default_cilium_lb_ipam_pool_cidr_from_nodes() {
  local first_ip
  # Take the first IP from the first range.
  first_ip="$(echo "$NODE_IP_RANGES" | cut -d, -f1 | cut -d- -f1)"

  if [[ -z "$first_ip" || -z "$IP_PREFIX" ]]; then
    return
  fi

  python3 -c "
import ipaddress, sys
try:
    network = ipaddress.ip_network(f'{sys.argv[1]}/{sys.argv[2]}', strict=False)
    print(network.with_prefixlen)
except Exception:
    pass
" "$first_ip" "$IP_PREFIX" 2>/dev/null || true
}

append_cilium_lb_ipam_pool_manifest() {
  local manifest_path="$1"
  local pool_cidr pool_name pool_start pool_stop

  is_true "$CILIUM_LB_IPAM" || return 0
  # Allow an empty (or newly created) file so we can build a CiliumLoadBalancerIPPool doc alone for kubectl apply.
  [[ -f "$manifest_path" ]] || return 0

  pool_cidr="${CILIUM_LB_IPAM_POOL_CIDR:-}"
  pool_start="${CILIUM_LB_IPAM_POOL_START:-}"
  pool_stop="${CILIUM_LB_IPAM_POOL_STOP:-}"

  # If no specific pool config is given, derive a default CIDR from the node network.
  if [[ -z "$pool_cidr" && -z "$pool_start" ]]; then
    pool_cidr="$(default_cilium_lb_ipam_pool_cidr_from_nodes)"
  fi

  # A range requires both start and stop.
  if [[ -n "$pool_start" && -z "$pool_stop" ]] || [[ -z "$pool_start" && -n "$pool_stop" ]]; then
    die "Cilium LB-IPAM pool range requires both --cilium-lb-ipam-pool-start and --cilium-lb-ipam-pool-stop."
  fi

  # If nothing is configured, do nothing.
  if [[ -z "$pool_cidr" && -z "$pool_start" ]]; then
    return 0
  fi

  pool_name="${CILIUM_LB_IPAM_POOL_NAME:-${WORKLOAD_CLUSTER_NAME}-lb-pool}"

  cat >>"$manifest_path" <<EOF

---
apiVersion: cilium.io/v2
kind: CiliumLoadBalancerIPPool
metadata:
  name: "${pool_name}"
spec:
  allowFirstLastIPs: "No"
  blocks:
EOF

  if [[ -n "$pool_start" && -n "$pool_stop" ]]; then
    cat >>"$manifest_path" <<EOF
    - start: "${pool_start}"
      stop: "${pool_stop}"
EOF
  fi

  if [[ -n "$pool_cidr" ]]; then
    cat >>"$manifest_path" <<EOF
    - cidr: "${pool_cidr}"
EOF
  fi
}

# Generate a UUID v4
generate_uuid_v4() {
  if command -v uuidgen >/dev/null 2>&1; then
    uuidgen
  else
    # Fallback: generate UUID v4 from /dev/urandom
    # Format: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx
    local b=$(od -An -tx1 -N16 /dev/urandom | tr -d ' ')
    printf '%s\n' "${b:0:8}-${b:8:4}-4${b:12:3}-a${b:15:3}-${b:18:12}"
  fi
}

derive_proxmox_identity_suffix() {
  local source_id="$1" compact

  compact="$(printf '%s' "$source_id" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9')"
  [[ -n "$compact" ]] || die "Cannot derive a Proxmox identity suffix from CLUSTER_SET_ID='${source_id}'."

  # Keep generated Proxmox role/user/token names compact and safe.
  if (( ${#compact} > 12 )); then
    compact="${compact:0:12}"
  fi

  printf '%s\n' "$compact"
}

DEFAULT_PROXMOX_CSI_USER_BASE="kubernetes-csi@pve"
DEFAULT_PROXMOX_CAPI_USER_BASE="capmox@pve"

proxmox_user_id_with_suffix() {
  local user_base="$1" suffix="$2" user realm

  if [[ "$user_base" == *"@"* ]]; then
    user="${user_base%@*}"
    realm="${user_base#*@}"
    printf '%s-%s@%s\n' "$user" "$suffix" "$realm"
  else
    printf '%s-%s\n' "$user_base" "$suffix"
  fi
}

refresh_derived_identity_user_ids() {
  if [[ -z "$PROXMOX_CSI_USER_ID" ]]; then
    PROXMOX_CSI_USER_ID="$(proxmox_user_id_with_suffix "$DEFAULT_PROXMOX_CSI_USER_BASE" "$PROXMOX_IDENTITY_SUFFIX")"
  fi

  if [[ -z "$PROXMOX_CAPI_USER_ID" ]]; then
    PROXMOX_CAPI_USER_ID="$(proxmox_user_id_with_suffix "$DEFAULT_PROXMOX_CAPI_USER_BASE" "$PROXMOX_IDENTITY_SUFFIX")"
  fi
}

proxmox_token_name() {
  local token_prefix="$1"
  printf '%s-%s\n' "$token_prefix" "$PROXMOX_IDENTITY_SUFFIX"
}

proxmox_token_name_for_set() {
  local token_prefix="$1" set_id="$2"
  printf '%s-%s\n' "$token_prefix" "$set_id"
}

proxmox_token_id() {
  local user_id="$1" token_prefix="$2"
  printf '%s!%s\n' "$user_id" "$(proxmox_token_name "$token_prefix")"
}

proxmox_token_id_for_set() {
  local user_id="$1" token_prefix="$2" set_id="$3"
  printf '%s!%s\n' "$user_id" "$(proxmox_token_name_for_set "$token_prefix" "$set_id")"
}

refresh_derived_identity_token_ids() {
  refresh_derived_identity_user_ids

  # Only fabricate a *token ID* when both id and secret are empty; otherwise a derived id + a real
  # secret from kind (or vice versa) would pair incorrectly and the PVE /version call returns 401.
  if [[ -z "$PROXMOX_TOKEN" && -z "${PROXMOX_SECRET:-}" && -n "$PROXMOX_CAPI_USER_ID" && -n "$PROXMOX_CAPI_TOKEN_PREFIX" ]]; then
    PROXMOX_TOKEN="$(proxmox_token_id "$PROXMOX_CAPI_USER_ID" "$PROXMOX_CAPI_TOKEN_PREFIX")"
  fi

  if [[ -z "$PROXMOX_CSI_TOKEN_ID" && -z "${PROXMOX_CSI_TOKEN_SECRET:-}" && -n "$PROXMOX_CSI_USER_ID" && -n "$PROXMOX_CSI_TOKEN_PREFIX" ]]; then
    PROXMOX_CSI_TOKEN_ID="$(proxmox_token_id "$PROXMOX_CSI_USER_ID" "$PROXMOX_CSI_TOKEN_PREFIX")"
  fi
}

derive_cilium_cluster_id() {
  local source_id="$1"
  local derived

  if [[ "$source_id" =~ ^[0-9]+$ ]]; then
    derived="$source_id"
  else
    derived="$(printf '%s' "$source_id" | cksum | awk '{print $1}')"
  fi

  derived=$(( (derived % 255) + 1 ))
  printf '%s\n' "$derived"
}

refresh_derived_cilium_cluster_id() {
  if [[ -z "$WORKLOAD_CILIUM_CLUSTER_ID" ]]; then
    WORKLOAD_CILIUM_CLUSTER_ID="$(derive_cilium_cluster_id "$CLUSTER_SET_ID")"
  fi
}

resolve_available_cluster_set_id_for_roles() {
  local api_url auth_header resolved_id
  local old_cluster_set_id old_identity_suffix old_csi_user_derived old_capi_user_derived old_csi_token_derived old_capi_token_derived

  # UUID-based CLUSTER_SET_ID values are already unique enough for identity naming.
  # Only resolve clashes for numeric IDs where legacy increment behavior applies.
  [[ "$CLUSTER_SET_ID" =~ ^[0-9]+$ ]] || return 0

  api_url="$(proxmox_api_json_url)"
  auth_header="Authorization: PVEAPIToken=${PROXMOX_ADMIN_USERNAME}=${PROXMOX_ADMIN_TOKEN}"

  resolved_id="$(python3 - "$api_url" "$auth_header" "$CLUSTER_SET_ID" "$PROXMOX_CSI_TOKEN_PREFIX" "$PROXMOX_CAPI_TOKEN_PREFIX" "$DEFAULT_PROXMOX_CSI_USER_BASE" "$DEFAULT_PROXMOX_CAPI_USER_BASE" "$PROXMOX_CSI_USER_ID" "$PROXMOX_CAPI_USER_ID" "$PROXMOX_ADMIN_INSECURE" <<'PY'
import json
import ssl
import sys
import urllib.parse
import urllib.request


def as_bool(value):
  return str(value).strip().lower() in {"1", "true", "yes", "on"}


def user_with_suffix(base, suffix):
  if "@" in base:
    user, realm = base.split("@", 1)
    return f"{user}-{suffix}@{realm}"
  return f"{base}-{suffix}"


def fetch_json(url, auth_header, insecure):
  req = urllib.request.Request(url, headers={"Authorization": auth_header})
  ctx = None
  if insecure:
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
  with urllib.request.urlopen(req, context=ctx) as resp:
    return json.loads(resp.read().decode("utf-8"))


def token_name_exists(api_base, auth_header, insecure, user_id, token_name):
  encoded_user = urllib.parse.quote(user_id, safe="")
  url = f"{api_base}/access/users/{encoded_user}/token"
  try:
    payload = fetch_json(url, auth_header, insecure)
  except Exception:
    return False

  full_token_id = f"{user_id}!{token_name}"
  for item in payload.get("data", []):
    raw = item.get("tokenid", "")
    if raw == full_token_id:
      return True
    if "!" in raw and raw.split("!", 1)[1] == token_name:
      return True
  return False


api_base = sys.argv[1].rstrip("/")
auth_header = sys.argv[2]
start = int(sys.argv[3])
csi_prefix = sys.argv[4]
capi_prefix = sys.argv[5]
default_csi_user_base = sys.argv[6]
default_capi_user_base = sys.argv[7]
explicit_csi_user = sys.argv[8].strip()
explicit_capi_user = sys.argv[9].strip()
insecure = as_bool(sys.argv[10])

roles_payload = fetch_json(f"{api_base}/access/roles", auth_header, insecure)
users_payload = fetch_json(f"{api_base}/access/users", auth_header, insecure)
roles = {item.get("roleid", "") for item in roles_payload.get("data", [])}
users = {item.get("userid", "") for item in users_payload.get("data", [])}

candidate = start
while True:
  csi_role = f"Kubernetes-CSI-{candidate}"
  capi_role = f"Kubernetes-CAPI-{candidate}"
  csi_user = explicit_csi_user or user_with_suffix(default_csi_user_base, candidate)
  capi_user = explicit_capi_user or user_with_suffix(default_capi_user_base, candidate)
  csi_token_name = f"{csi_prefix}-{candidate}"
  capi_token_name = f"{capi_prefix}-{candidate}"

  # Role names are globally unique.
  if csi_role in roles or capi_role in roles:
    candidate += 1
    continue

  # Derived user IDs must also be globally unique.
  if not explicit_csi_user and csi_user in users:
    candidate += 1
    continue
  if not explicit_capi_user and capi_user in users:
    candidate += 1
    continue

  # Token names are unique per user; check only when the user already exists.
  if csi_user in users and token_name_exists(api_base, auth_header, insecure, csi_user, csi_token_name):
    candidate += 1
    continue
  if capi_user in users and token_name_exists(api_base, auth_header, insecure, capi_user, capi_token_name):
    candidate += 1
    continue

  print(candidate)
  break
PY
)" || {
    warn "Failed to compute an available CLUSTER_SET_ID from Proxmox identity inventory; using explicit CLUSTER_SET_ID=${CLUSTER_SET_ID}."
    return 1
  }

  if [[ "$resolved_id" != "$CLUSTER_SET_ID" ]]; then
    old_cluster_set_id="$CLUSTER_SET_ID"
    old_identity_suffix="$(derive_proxmox_identity_suffix "$old_cluster_set_id")"
    old_csi_user_derived="$(proxmox_user_id_with_suffix "$DEFAULT_PROXMOX_CSI_USER_BASE" "$old_identity_suffix")"
    old_capi_user_derived="$(proxmox_user_id_with_suffix "$DEFAULT_PROXMOX_CAPI_USER_BASE" "$old_identity_suffix")"
    old_csi_token_derived="$(proxmox_token_id_for_set "$old_csi_user_derived" "$PROXMOX_CSI_TOKEN_PREFIX" "$old_identity_suffix")"
    old_capi_token_derived="$(proxmox_token_id_for_set "$old_capi_user_derived" "$PROXMOX_CAPI_TOKEN_PREFIX" "$old_identity_suffix")"

    warn "CLUSTER_SET_ID=${CLUSTER_SET_ID} is already in use in Proxmox identity resources; using CLUSTER_SET_ID=${resolved_id}."
    CLUSTER_SET_ID="$resolved_id"
    PROXMOX_IDENTITY_SUFFIX="$(derive_proxmox_identity_suffix "$CLUSTER_SET_ID")"

    if [[ -z "$PROXMOX_CSI_USER_ID" || "$PROXMOX_CSI_USER_ID" == "$old_csi_user_derived" ]]; then
      PROXMOX_CSI_USER_ID="$(proxmox_user_id_with_suffix "$DEFAULT_PROXMOX_CSI_USER_BASE" "$PROXMOX_IDENTITY_SUFFIX")"
    fi
    if [[ -z "$PROXMOX_CAPI_USER_ID" || "$PROXMOX_CAPI_USER_ID" == "$old_capi_user_derived" ]]; then
      PROXMOX_CAPI_USER_ID="$(proxmox_user_id_with_suffix "$DEFAULT_PROXMOX_CAPI_USER_BASE" "$PROXMOX_IDENTITY_SUFFIX")"
    fi

    if [[ -z "$PROXMOX_CSI_TOKEN_ID" || "$PROXMOX_CSI_TOKEN_ID" == "$old_csi_token_derived" ]]; then
      PROXMOX_CSI_TOKEN_ID="$(proxmox_token_id "$PROXMOX_CSI_USER_ID" "$PROXMOX_CSI_TOKEN_PREFIX")"
    fi
    if [[ -z "$PROXMOX_TOKEN" || "$PROXMOX_TOKEN" == "$old_capi_token_derived" ]]; then
      PROXMOX_TOKEN="$(proxmox_token_id "$PROXMOX_CAPI_USER_ID" "$PROXMOX_CAPI_TOKEN_PREFIX")"
    fi

    refresh_derived_identity_token_ids
    refresh_derived_cilium_cluster_id
  fi
}

# Displays the script's usage information (file header: options + most env; trim number if header grows and --help lags).
usage() {
  sed -n '2,300p' "${BASH_SOURCE[0]}"
}

# Parse command-line options.
parse_options() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      -b|--build-all)
        BUILD_ALL=true
        shift
        ;;
      -f|--force)
        FORCE=true
        shift
        ;;
      --no-delete-kind)
        NO_DELETE_KIND=true
        shift
        ;;
      --persist-local-secrets)
        BOOTSTRAP_PERSIST_LOCAL_SECRETS=true
        shift
        ;;
      --kind-cluster-name)
        KIND_CLUSTER_NAME="$2"
        shift 2
        ;;
      --kind-config)
        KIND_CONFIG="$2"
        shift 2
        ;;
      --proxmox-bootstrap-admin-secret)
        PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME="$2"
        shift 2
        ;;
      --capi-manifest)
        CAPI_MANIFEST="$2"
        shift 2
        ;;
      --regenerate-capi-manifest)
        BOOTSTRAP_REGENERATE_CAPI_MANIFEST=true
        shift
        ;;
      --bootstrap-config-file)
        PROXMOX_BOOTSTRAP_CONFIG_FILE="$2"
        shift 2
        ;;
      -p|--purge)
        PURGE=true
        shift
        ;;
      -u|--admin-username)
        PROXMOX_ADMIN_USERNAME="$2"
        shift 2
        ;;
      -t|--admin-token)
        PROXMOX_ADMIN_TOKEN="$2"
        shift 2
        ;;
      --proxmox-url)
        PROXMOX_URL="$2"
        shift 2
        ;;
      --proxmox-token)
        PROXMOX_TOKEN="$2"
        shift 2
        ;;
      --proxmox-secret)
        PROXMOX_SECRET="$2"
        shift 2
        ;;
      -r|--region)
        PROXMOX_REGION="$2"
        shift 2
        ;;
      -n|--node)
        PROXMOX_NODE="$2"
        shift 2
        ;;
      --template-id)
        PROXMOX_TEMPLATE_ID="$2"
        shift 2
        ;;
      --template-vmid)
        PROXMOX_TEMPLATE_ID="$2"
        shift 2
        ;;
      --bridge)
        PROXMOX_BRIDGE="$2"
        shift 2
        ;;
      --control-plane-endpoint-ip)
        CONTROL_PLANE_ENDPOINT_IP="$2"
        shift 2
        ;;
      --control-plane-endpoint-port)
        CONTROL_PLANE_ENDPOINT_PORT="$2"
        shift 2
        ;;
      --node-ip-ranges)
        NODE_IP_RANGES_EXPLICIT=true
        NODE_IP_RANGES="$2"
        shift 2
        ;;
      --gateway)
        GATEWAY_EXPLICIT=true
        GATEWAY="$2"
        shift 2
        ;;
      --ip-prefix)
        IP_PREFIX_EXPLICIT=true
        IP_PREFIX="$2"
        shift 2
        ;;
      --dns-servers)
        DNS_SERVERS_EXPLICIT=true
        DNS_SERVERS="$2"
        shift 2
        ;;
      --allowed-nodes)
        ALLOWED_NODES_EXPLICIT=true
        ALLOWED_NODES="$2"
        shift 2
        ;;
      --csi-url)
        PROXMOX_CSI_URL="$2"
        shift 2
        ;;
      --csi-token-id)
        PROXMOX_CSI_TOKEN_ID="$2"
        shift 2
        ;;
      --csi-token-secret)
        PROXMOX_CSI_TOKEN_SECRET="$2"
        shift 2
        ;;
      --csi-user-id)
        PROXMOX_CSI_USER_ID="$2"
        shift 2
        ;;
      --csi-token-prefix)
        PROXMOX_CSI_TOKEN_PREFIX="$2"
        shift 2
        ;;
      --csi-insecure)
        PROXMOX_CSI_INSECURE="$2"
        shift 2
        ;;
      --csi-storage-class)
        PROXMOX_CSI_STORAGE_CLASS_NAME="$2"
        shift 2
        ;;
      --csi-storage)
        PROXMOX_CSI_STORAGE="$2"
        shift 2
        ;;
      --cloudinit-storage)
        PROXMOX_CLOUDINIT_STORAGE="$2"
        shift 2
        ;;
      --memory-adjustment)
        PROXMOX_MEMORY_ADJUSTMENT="$2"
        shift 2
        ;;
      --disable-argocd)
        ARGOCD_ENABLED=false
        shift
        ;;
      --disable-workload-argocd)
        WORKLOAD_ARGOCD_ENABLED=false
        shift
        ;;
      --argocd-version)
        die "--argocd-version was removed; use --argocd-app-version for Argo CD image / ArgoCD CR spec.version (ARGOCD_VERSION)."
        ;;
      --argocd-app-version)
        ARGOCD_VERSION="$2"
        shift 2
        ;;
      --argocd-server-insecure)
        ARGOCD_SERVER_INSECURE="$2"
        shift 2
        ;;
      --workload-gitops-mode)
        case "$2" in
          caaph) WORKLOAD_GITOPS_MODE=caaph ;;
          *) die "Only --workload-gitops-mode caaph is supported (got: $2). The legacy kind-argocd/management Argo path was removed." ;;
        esac
        shift 2
        ;;
      --workload-app-of-apps-git-url)
        WORKLOAD_APP_OF_APPS_GIT_URL="$2"
        shift 2
        ;;
      --workload-app-of-apps-git-path)
        WORKLOAD_APP_OF_APPS_GIT_PATH="$2"
        shift 2
        ;;
      --workload-app-of-apps-git-ref)
        WORKLOAD_APP_OF_APPS_GIT_REF="$2"
        shift 2
        ;;
      --argocd-print-access|--argocd-print-access-only)
        ARGOCD_PRINT_ACCESS_STANDALONE=true
        if [[ -n "${2:-}" && "$2" != --* ]]; then
          case "$2" in
            workload) ARGOCD_PRINT_ACCESS_TARGET=workload; shift ;;
            kind|both) warn "Argo CD on the management (kind) cluster is not used by this script — use workload only."; ARGOCD_PRINT_ACCESS_TARGET=workload; shift ;;
          esac
        fi
        shift
        ;;
      --argocd-port-forward|--argocd-port-forward-only)
        ARGOCD_PORT_FORWARD_STANDALONE=true
        if [[ -n "${2:-}" && "$2" != --* ]]; then
          case "$2" in
            workload)
              ARGOCD_PORT_FORWARD_TARGET=workload
              ARGOCD_PRINT_ACCESS_TARGET="${ARGOCD_PRINT_ACCESS_TARGET:-workload}"
              shift
              ;;
            kind|both) warn "Port-forward targets the provisioned cluster only (workload) — not kind."; ARGOCD_PORT_FORWARD_TARGET=workload; shift ;;
          esac
        fi
        shift
        ;;
      --workload-rollout)
        WORKLOAD_ROLLOUT_STANDALONE=true
        if [[ -n "${2:-}" && "$2" != --* ]]; then
          case "$2" in
            argocd|capi|all) WORKLOAD_ROLLOUT_MODE="$2"; shift ;;
          esac
        fi
        shift
        ;;
      --workload-rollout-no-wait)
        WORKLOAD_ROLLOUT_NO_WAIT=true
        shift
        ;;
      --kind-backup)
        BOOTSTRAP_KIND_STATE_OP=backup
        if [[ -n "${2:-}" && "$2" != --* ]]; then
          BOOTSTRAP_KIND_BACKUP_OUT="$2"
          shift 2
        else
          shift
        fi
        ;;
      --kind-restore)
        [[ -n "${2:-}" && "$2" != --* ]] || die "--kind-restore requires an archive path (.tar.gz, .tar.gz.age, or .tar.gz.enc)"
        BOOTSTRAP_KIND_STATE_OP=restore
        BOOTSTRAP_KIND_STATE_PATH="$2"
        shift 2
        ;;
      --disable-proxmox-csi)
        PROXMOX_CSI_ENABLED=false
        shift
        ;;
      --proxmox-csi-version)
        PROXMOX_CSI_CHART_VERSION="$2"
        shift 2
        ;;
      --disable-proxmox-csi-smoketest)
        PROXMOX_CSI_SMOKE_ENABLED=false
        shift
        ;;
      --disable-argocd-workload-postsync-hooks)
        ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED=false
        shift
        ;;
      --argocd-workload-postsync-hooks-git-url)
        ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL="$2"
        shift 2
        ;;
      --argocd-workload-postsync-hooks-git-path)
        ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH="$2"
        shift 2
        ;;
      --argocd-workload-postsync-hooks-git-ref)
        ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF="$2"
        shift 2
        ;;
      --disable-kyverno)
        KYVERNO_ENABLED=false
        shift
        ;;
      --kyverno-version)
        KYVERNO_CHART_VERSION="$2"
        shift 2
        ;;
      --disable-cert-manager)
        CERT_MANAGER_ENABLED=false
        shift
        ;;
      --cert-manager-version)
        CERT_MANAGER_CHART_VERSION="$2"
        shift 2
        ;;
      --disable-crossplane)
        CROSSPLANE_ENABLED=false
        shift
        ;;
      --crossplane-version)
        CROSSPLANE_CHART_VERSION="$2"
        shift 2
        ;;
      --disable-cnpg)
        CNPG_ENABLED=false
        shift
        ;;
      --cnpg-version)
        CNPG_CHART_VERSION="$2"
        shift 2
        ;;
      --disable-victoriametrics)
        VICTORIAMETRICS_ENABLED=false
        shift
        ;;
      --victoriametrics-version)
        VICTORIAMETRICS_CHART_VERSION="$2"
        shift 2
        ;;
      --csi-reclaim-policy)
        PROXMOX_CSI_RECLAIM_POLICY="$2"
        shift 2
        ;;
      --csi-fstype)
        PROXMOX_CSI_FSTYPE="$2"
        shift 2
        ;;
      --csi-default-class)
        PROXMOX_CSI_DEFAULT_CLASS="$2"
        shift 2
        ;;
      --capi-user-id)
        PROXMOX_CAPI_USER_ID="$2"
        shift 2
        ;;
      --capi-token-prefix)
        PROXMOX_CAPI_TOKEN_PREFIX="$2"
        shift 2
        ;;
      --cluster-set-id)
        CLUSTER_SET_ID="$2"
        shift 2
        ;;
      --recreate-proxmox-identities)
        RECREATE_PROXMOX_IDENTITIES=true
        shift
        ;;
      --recreate-proxmox-identities-scope)
        PROXMOX_IDENTITY_RECREATE_SCOPE="$2"
        shift 2
        ;;
      --recreate-proxmox-identities-state-rm)
        PROXMOX_IDENTITY_RECREATE_STATE_RM=true
        shift
        ;;
      --control-plane-boot-volume-device)
        CONTROL_PLANE_BOOT_VOLUME_DEVICE="$2"
        shift 2
        ;;
      --control-plane-boot-volume-size)
        CONTROL_PLANE_BOOT_VOLUME_SIZE="$2"
        shift 2
        ;;
      --control-plane-num-sockets)
        CONTROL_PLANE_NUM_SOCKETS="$2"
        shift 2
        ;;
      --control-plane-num-cores)
        CONTROL_PLANE_NUM_CORES="$2"
        shift 2
        ;;
      --control-plane-memory-mib)
        CONTROL_PLANE_MEMORY_MIB="$2"
        shift 2
        ;;
      --worker-boot-volume-device)
        WORKER_BOOT_VOLUME_DEVICE="$2"
        shift 2
        ;;
      --worker-boot-volume-size)
        WORKER_BOOT_VOLUME_SIZE="$2"
        shift 2
        ;;
      --worker-num-sockets)
        WORKER_NUM_SOCKETS="$2"
        shift 2
        ;;
      --worker-num-cores)
        WORKER_NUM_CORES="$2"
        shift 2
        ;;
      --worker-memory-mib)
        WORKER_MEMORY_MIB="$2"
        shift 2
        ;;
      --workload-cluster-name)
        WORKLOAD_CLUSTER_NAME="$2"
        WORKLOAD_CLUSTER_NAME_EXPLICIT=1
        shift 2
        ;;
      --workload-cluster-namespace)
        WORKLOAD_CLUSTER_NAMESPACE="$2"
        WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT=1
        shift 2
        ;;
      --workload-cilium-cluster-id)
        WORKLOAD_CILIUM_CLUSTER_ID="$2"
        shift 2
        ;;
      --workload-k8s-version)
        WORKLOAD_KUBERNETES_VERSION="$2"
        shift 2
        ;;
      --control-plane-count)
        CONTROL_PLANE_MACHINE_COUNT="$2"
        shift 2
        ;;
      --worker-count)
        WORKER_MACHINE_COUNT="$2"
        shift 2
        ;;
      --capi-proxmox-machine-template-spec-rev-skip)
        CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV=false
        shift
        ;;
      --cilium-wait-duration)
        CILIUM_WAIT_DURATION="$2"
        shift 2
        ;;
      --cilium-ingress)
        CILIUM_INGRESS="$2"
        shift 2
        ;;
      --cilium-kube-proxy-replacement)
        CILIUM_KUBE_PROXY_REPLACEMENT="$2"
        shift 2
        ;;
      --cilium-lb-ipam)
        CILIUM_LB_IPAM="$2"
        shift 2
        ;;
      --cilium-lb-ipam-pool-cidr)
        CILIUM_LB_IPAM_POOL_CIDR="$2"
        shift 2
        ;;
      --cilium-lb-ipam-pool-start)
        CILIUM_LB_IPAM_POOL_START="$2"
        shift 2
        ;;
      --cilium-lb-ipam-pool-stop)
        CILIUM_LB_IPAM_POOL_STOP="$2"
        shift 2
        ;;
      --cilium-lb-ipam-pool-name)
        CILIUM_LB_IPAM_POOL_NAME="$2"
        shift 2
        ;;
      --cilium-ipam-cluster-pool-ipv4)
        CILIUM_IPAM_CLUSTER_POOL_IPV4="$2"
        shift 2
        ;;
      --cilium-ipam-cluster-pool-ipv4-mask-size)
        CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE="$2"
        shift 2
        ;;
      --cilium-gateway-api)
        CILIUM_GATEWAY_API_ENABLED="$2"
        shift 2
        ;;
      --argocd-disable-operator-ingress)
        ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS="$2"
        shift 2
        ;;
      --cilium-hubble)
        CILIUM_HUBBLE="$2"
        shift 2
        ;;
      --cilium-hubble-ui)
        CILIUM_HUBBLE_UI="$2"
        shift 2
        ;;
      --exp-cluster-resource-set)
        EXP_CLUSTER_RESOURCE_SET="$2"
        shift 2
        ;;
      --cluster-topology)
        CLUSTER_TOPOLOGY="$2"
        shift 2
        ;;
      --exp-kubeadm-bootstrap-format-ignition)
        EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION="$2"
        shift 2
        ;;
      --disable-metrics-server)
        ENABLE_METRICS_SERVER=false
        shift
        ;;
      --disable-workload-metrics-server)
        ENABLE_WORKLOAD_METRICS_SERVER=false
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        die "Unknown option: $1"
        ;;
    esac
  done
}

# Normalise uname -m to the convention used by most Go release binaries.
_arch() {
  local m; m="$(uname -m)"
  case "$m" in
    x86_64)          echo "amd64" ;;
    aarch64|arm64)   echo "arm64" ;;
    armv7l)          echo "arm" ;;
    s390x)           echo "s390x" ;;
    ppc64le)         echo "ppc64le" ;;
    *)               echo "$m" ;;
  esac
}

# Install a binary from a URL to /usr/local/bin if not already present.
# Usage: install_binary <name> <url>
install_binary() {
  local name="$1" url="$2"
  log "Installing ${name}..."
  curl -fsSL "$url" -o "/tmp/${name}.bin" \
    || die "Failed to download ${name} from ${url} (curl: network, 404, or wrong *_VERSION pin?)."
  RUN_PRIVILEGED install -m 0755 "/tmp/${name}.bin" "/usr/local/bin/${name}"
  rm -f "/tmp/${name}.bin"
}

# For *_VERSION in proxmox-bootstrap-config: reinstall local CLIs when the on-disk build does not match.
_normalize_git_version() {
  local s="${1:-}"
  s="${s#v}"
  s="${s%%-*}"
  printf '%s' "$s"
}
_versions_match() {
  local a b
  a="$(_normalize_git_version "$1")"
  b="$(_normalize_git_version "$2")"
  [[ -n "$a" && -n "$b" && "$a" == "$b" ]]
}

# Check whether an arm64 manifest exists for a given image.
# Returns 0 (true) if arm64 is available, 1 otherwise.
_has_arm64_image() {
  local image="$1"
  docker manifest inspect "$image" 2>/dev/null \
    | python3 -c "
import sys,json
m=json.load(sys.stdin)
manifests=m.get('manifests',m.get('Manifests',[]))
sys.exit(0 if any(x.get('platform',{}).get('architecture')=='arm64' for x in manifests) else 1)
" 2>/dev/null
}

# Build a Docker image from source if no arm64 image is available in the registry,
# then load it into the kind cluster.
# Usage: build_if_no_arm64 <image:tag> <git-repo-url> <git-tag> <clone-dir> [kind-cluster-name]
build_if_no_arm64() {
  local image="$1" repo="$2" tag="$3" dir="$4" cluster="${5:-${KIND_CLUSTER_NAME}}"

  if ! is_true "$BUILD_ALL"; then
    if _has_arm64_image "$image"; then
      log "arm64 image available in registry: ${image} — skipping local pull/build/load."
      return 0
    fi

    warn "No arm64 image found for ${image} — building from source..."
  else
    warn "BUILD_ALL enabled — building ${image} from source even though a registry image may exist."
  fi

  rm -rf "$dir"
  git clone --filter=blob:none --branch "$tag" --depth 1 "$repo" "$dir"
  docker build -t "$image" "$dir"

  log "Loading locally built image ${image} into kind cluster '${cluster}'..."
  kind load docker-image "$image" --name "$cluster"
}

# Wait until a webhook Service has at least one endpoint address.
# Usage: wait_for_service_endpoint <namespace> <service> [timeout-seconds]
wait_for_service_endpoint() {
  local ns="$1" svc="$2" timeout="${3:-300}" elapsed=0
  while (( elapsed < timeout )); do
    if [[ -n "$(kubectl get endpoints "$svc" -n "$ns" -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null)" ]]; then
      log "Webhook endpoint ready: ${ns}/${svc}"
      return 0
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done
  die "Timed out waiting for webhook endpoint: ${ns}/${svc}"
}

# Apply multi-doc workload manifest to the management cluster; skip ProxmoxCluster documents when the
# CR already exists so kubectl does not send no-op patches through the capmox mutating webhook (connection
# refused / idempotency issues on reruns).
apply_workload_cluster_manifest_to_management_cluster() {
  local manifest="$1" ctx="kind-${KIND_CLUSTER_NAME}"

  [[ -f "$manifest" ]] || die "Manifest not found: ${manifest}"

  python3 - "$manifest" "$ctx" <<'PY'
import json
import subprocess
import sys
from pathlib import Path

manifest = Path(sys.argv[1])
ctx = sys.argv[2]
text = manifest.read_text()
parts = [p.strip() + "\n" for p in text.split("\n---\n") if p.strip()]

for doc in parts:
  dry = subprocess.run(
    ["kubectl", "--context", ctx, "create", "--dry-run=client", "-o", "json", "-f", "-"],
    input=doc,
    capture_output=True,
    text=True,
  )
  if dry.returncode != 0:
    if dry.stderr:
      print(dry.stderr, file=sys.stderr, end="")
    res = subprocess.run(
      ["kubectl", "--context", ctx, "apply", "-f", "-"],
      input=doc,
      text=True,
    )
    sys.exit(res.returncode)

  obj = json.loads(dry.stdout)
  kind = obj.get("kind")
  api = obj.get("apiVersion", "")
  md = obj.get("metadata") or {}
  name = md.get("name")
  ns = md.get("namespace") or "default"

  if (
    kind == "ProxmoxCluster"
    and "infrastructure.cluster.x-k8s.io" in api
    and name
  ):
    get = subprocess.run(
      [
        "kubectl",
        "--context",
        ctx,
        "get",
        "proxmoxcluster",
        name,
        "-n",
        ns,
        "-o",
        "jsonpath={.metadata.deletionTimestamp}"
      ],
      capture_output=True,
      text=True,
    )
    if get.returncode == 0 and not get.stdout.strip():
      print(
        "Skipping apply for existing ProxmoxCluster "
        f"{ns}/{name} (already reconciled; avoids redundant webhook/patch).",
        file=sys.stderr,
      )
      continue

  res = subprocess.run(
    ["kubectl", "--context", ctx, "apply", "-f", "-"],
    input=doc,
    text=True,
  )
  if res.returncode != 0:
    sys.exit(res.returncode)

sys.exit(0)
PY
}

# Create tmp/kind with kind-config.yaml + meta + README for recreating the same kind management cluster (copy KIND_CONFIG, or build from Docker node labels / minimal fallback).
kind_bootstrap_state_backup_write_kind_dir() {
  local b="${1:-}"
  [[ -n "$b" && -d "$b" ]] || return 0
  [[ -n "${KIND_CLUSTER_NAME:-}" ]] || {
    warn "KIND_CLUSTER_NAME unset — skipping kind/ recipe in backup"
    return 0
  }
  if ! KIND_BKP_KIND_ROOT="$b" KIND_BKP_KIND_NAME="${KIND_CLUSTER_NAME}" KIND_BKP_PATH_CONFIG="${KIND_CONFIG:-}" python3 - <<'PYK'
import json
import os
import subprocess
import sys

b = os.environ.get("KIND_BKP_KIND_ROOT", "")
name = (os.environ.get("KIND_BKP_KIND_NAME") or "").strip()
kcfg = (os.environ.get("KIND_BKP_PATH_CONFIG") or "").strip()
if not b or not name:
    print("KIND_BKP_KIND_ROOT or KIND_BKP_KIND_NAME missing", file=sys.stderr)
    sys.exit(1)
kdir = os.path.join(b, "kind")
os.makedirs(kdir, exist_ok=True)
meta = {"kind_cluster_name": name, "config_source": "unknown"}


def write_file(path, text):
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)

if kcfg and os.path.isfile(kcfg):
    with open(kcfg, encoding="utf-8") as f:
        raw = f.read()
    write_file(os.path.join(kdir, "kind-config.yaml"), raw)
    meta["config_source"] = "KIND_CONFIG_file"
    meta["config_path"] = kcfg
else:
    lines = []
    try:
        r = subprocess.run(
            [
                "docker",
                "ps",
                "-a",
                "--filter",
                f"label=io.x-k8s.kind.cluster={name}",
                "--format",
                r'{{.Label "io.x-k8s.kind.role"}}\t{{.Image}}\t{{.Names}}',
            ],
            capture_output=True,
            text=True,
            timeout=60,
        )
        for ln in (r.stdout or "").splitlines():
            ln = (ln or "").strip()
            if not ln or "\t" not in ln:
                continue
            parts = ln.split("\t", 2)
            if len(parts) >= 2:
                lines.append(ln)
    except (OSError, subprocess.SubprocessError) as e:
        meta["docker_error"] = str(e)[:200]
    if lines:
        def sort_key(s):
            role = s.split("\t", 1)[0]
            return (0 if role == "control-plane" else 1, s)

        lines = sorted(lines, key=sort_key)
        y = [
            "kind: Cluster",
            "apiVersion: kind.x-k8s.io/v1alpha4",
            "nodes:",
        ]
        for ln in lines:
            p = ln.split("\t", 2)
            role, image = p[0], p[1]
            y.append(f"- role: {role}")
            y.append(f"  image: {image}")
        write_file(os.path.join(kdir, "kind-config.yaml"), "\n".join(y) + "\n")
        meta["config_source"] = "docker_nodes"
    else:
        y = [
            "kind: Cluster",
            "apiVersion: kind.x-k8s.io/v1alpha4",
            "nodes:",
            "- role: control-plane",
        ]
        write_file(os.path.join(kdir, "kind-config.yaml"), "\n".join(y) + "\n")
        meta["config_source"] = "fallback_minimal"

try:
    kv = subprocess.run(
        ["kind", "version"],
        capture_output=True,
        text=True,
        timeout=15,
    )
    if kv.returncode == 0 and (kv.stdout or "").strip():
        meta["kind_cli"] = (kv.stdout or "").strip()[:2000]
except (OSError, subprocess.SubprocessError):
    pass

with open(os.path.join(kdir, "meta.json"), "w", encoding="utf-8") as f:
    json.dump(meta, f, indent=2)
    f.write("\n")
readme = f"""# Recreate kind management cluster

The management cluster is Docker-backed and does not store the original kind
config. This bundle is either a copy of KIND_CONFIG (if that file was set when
the backup ran) or a rebuild from \`docker\` (node \`image\` + \`role\` labels).

1. Install kind + Docker as usual, then from the directory that contains
   \`kind/kind-config.yaml\` (e.g. after \`tar -xzf\` your backup):
   \`\`\`bash
   kind create cluster --name {name} --config kind/kind-config.yaml
   \`\`\`
2. Re-merge kubeconfig if needed: \`kind export kubeconfig --name {name}\`
3. Then restore the namespaced data (e.g. \`kind_bootstrap_state_restore\` from
   this script, or \`kubectl apply\` the \`data/\` tree) against context
   \`kind-{name}\`.
"""
with open(os.path.join(kdir, "README"), "w", encoding="utf-8") as f:
    f.write(readme)
PYK
  then
    err "Failed to write kind/ backup bundle (python)"
    return 1
  fi
  log "Kind backup: wrote ${b}/kind/ (config + README to recreate the cluster)"
}

# Comma- or space-separated; default = proxmox bootstrap + workload CAPI namespace.
kind_bootstrap_state_backup_namespaces() {
  if [[ -n "${BOOTSTRAP_KIND_BACKUP_NAMESPACES:-}" ]]; then
    tr ', ' '\n' <<< "$BOOTSTRAP_KIND_BACKUP_NAMESPACES" | tr -d '\r' | sed '/^$/d' | sort -u
  else
    {
      [[ -n "${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE:-}" ]] && echo "${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}"
      [[ -n "${WORKLOAD_CLUSTER_NAMESPACE:-}" ]] && echo "${WORKLOAD_CLUSTER_NAMESPACE}"
    } | tr -d '\r' | sort -u
  fi
}

# Export all namespaced resources from kind management cluster to a .tar (optionally .tar.gz + age / openssl). Optional arg: output path.
# Env: BOOTSTRAP_KIND_BACKUP_NAMESPACES, BOOTSTRAP_KIND_BACKUP_OUT, BOOTSTRAP_KIND_BACKUP_ENCRYPT, AGE_PASSPHRASE, BOOTSTRAP_KIND_BACKUP_PASSPHRASE
kind_bootstrap_state_backup() {
  local ctx dest tmp abs want p
  command -v kubectl >/dev/null 2>&1 || {
    err "kubectl required for kind_bootstrap_state_backup"
    return 1
  }
  ctx="kind-${KIND_CLUSTER_NAME}"
  kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | grep -qx "$ctx" || {
    err "kube context ${ctx} not found"
    return 1
  }
  dest="${1:-${BOOTSTRAP_KIND_BACKUP_OUT:-}}"
  if [[ -z "$dest" ]]; then
    dest="bootstrap-kind-backup-$(date '+%Y%m%d-%H%M%S').tar"
  fi
  case "$dest" in
  /*) abs="$dest" ;;
  *) abs="$(pwd)/$dest" ;;
  esac
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/bkp.XXXXXX")" || return 1
  export KIND_BKP_CTX="$ctx"
  mkdir -p -- "$tmp/data"
  : >"$tmp/namespaces.lst"
  while IFS= read -r n; do
    [[ -n "$n" ]] || continue
    echo "$n" >>"$tmp/namespaces.lst"
    export KIND_BKP_NS="$n"
    if ! python3 - "$tmp" <<'PY'
import json, os, subprocess, sys, time

def run(cmd, **kw):
  return subprocess.run(cmd, **kw)

def main():
  base = os.environ.get("KIND_BKP_DIR", sys.argv[1] if len(sys.argv) > 1 else None)
  ctx = os.environ["KIND_BKP_CTX"]
  ns = os.environ["KIND_BKP_NS"]
  if not base:
    print("KIND_BKP_DIR missing", file=sys.stderr)
    os._exit(1)
  odir = os.path.join(base, "data", ns)
  os.makedirs(odir, exist_ok=True)
  meta = {
    "version": 1,
    "context": ctx,
    "namespace": ns,
    "ts": int(time.time()),
  }
  p = run(
    ["kubectl", "--context", ctx, "get", "namespace", ns, "-o", "json"],
    capture_output=True, text=True,
  )
  if p.returncode == 0 and p.stdout.strip():
    with open(os.path.join(odir, "namespace.json"), "w", encoding="utf-8") as f:
      f.write(p.stdout)
  r = run(
    ["kubectl", "--context", ctx, "api-resources", "--verbs=list", "--namespaced", "-o", "name"],
    capture_output=True, text=True, check=False,
  )
  if r.returncode != 0 or not (r.stdout or "").strip():
    print("api-resources list failed; skipping object dump", file=sys.stderr)
  lines = (r.stdout or "").splitlines()
  jlp = os.path.join(odir, "objects.jsonl")
  n_obj = 0
  with open(jlp, "w", encoding="utf-8") as outf:
    for gvr in lines:
      gvr = (gvr or "").strip()
      if not gvr or gvr == "events":
        continue
      gl = run(
        [
          "kubectl", "--context", ctx, "get", gvr, "-n", ns, "-o", "json",
          "--request-timeout=5m",
        ],
        capture_output=True, text=True,
      )
      if gl.returncode != 0 or not (gl.stdout or "").strip():
        continue
      try:
        data = json.loads(gl.stdout)
      except json.JSONDecodeError:
        continue
      for item in data.get("items", []) or []:
        j = json.dumps(item, ensure_ascii=False, separators=(",", ":")) + "\n"
        outf.write(j)
        n_obj += 1
  with open(os.path.join(odir, "meta.json"), "w", encoding="utf-8") as f:
    json.dump({**meta, "objects": n_obj}, f, indent=0)
    f.write("\n")

if __name__ == "__main__":
  main()
PY
    then
      err "backup failed for namespace (python)"
      rm -rf -- "$tmp"
      return 1
    fi
  done < <(kind_bootstrap_state_backup_namespaces)
  if [[ ! -s "$tmp/namespaces.lst" ]]; then
    err "No namespaces to backup (set BOOTSTRAP_KIND_BACKUP_NAMESPACES?)"
    rm -rf -- "$tmp"
    return 1
  fi
  if ! kind_bootstrap_state_backup_write_kind_dir "$tmp"; then
    rm -rf -- "$tmp"
    return 1
  fi
  p="${BOOTSTRAP_KIND_BACKUP_PASSPHRASE:-${AGE_PASSPHRASE:-}}"
  want="${BOOTSTRAP_KIND_BACKUP_ENCRYPT:-auto}"
  if [[ "$want" = "auto" ]]; then
    if [[ -n "$p" ]] && command -v age >/dev/null 2>&1; then
      want=age
    elif [[ -n "$p" ]] && command -v openssl >/dev/null 2>&1; then
      want=openssl
    else
      want=none
    fi
  fi
  if [[ "$want" != "none" && -z "$p" ]]; then
    warn "BOOTSTRAP_KIND_BACKUP_ENCRYPT is ${want} but no passphrase set — writing unencrypted tar (set BOOTSTRAP_KIND_BACKUP_PASSPHRASE or AGE_PASSPHRASE, or set BOOTSTRAP_KIND_BACKUP_ENCRYPT=none)"
    want=none
  fi
  if [[ "$want" = "none" ]]; then
    (cd -- "$tmp" && tar -cf - namespaces.lst data kind) | gzip -1 >"$abs.gz" || {
      rm -rf -- "$tmp"
      return 1
    }
    rm -f -- "$abs" 2>/dev/null
    log "Wrote $abs.gz ($(du -h "$abs.gz" | cut -f1)) — encrypt: none"
  elif [[ "$want" = "age" ]]; then
    export AGE_PASSPHRASE="${p:-${AGE_PASSPHRASE:-}}"
    (cd -- "$tmp" && tar -cf - namespaces.lst data kind) | gzip -1 | age -e -o "${abs}.gz.age" || {
      rm -rf -- "$tmp"
      return 1
    }
    log "Wrote ${abs}.gz.age ($(du -h "${abs}.gz.age" | cut -f1)) — age"
  else
    export BOOTSTRAP_KIND_BACKUP_PASSPHRASE="$p"
    (cd -- "$tmp" && tar -cf - namespaces.lst data kind) | gzip -1 | openssl enc -aes-256-cbc -pbkdf2 -salt -pass env:BOOTSTRAP_KIND_BACKUP_PASSPHRASE -out "${abs}.gz.enc" || {
      rm -rf -- "$tmp"
      return 1
    }
    log "Wrote ${abs}.gz.enc ($(du -h "${abs}.gz.enc" | cut -f1)) — openssl"
  fi
  rm -rf -- "$tmp"
}

# Restore backup produced by kind_bootstrap_state_backup into the current kind context. First arg: backup file (.tar.gz, .tar.gz.age, or .tar.gz.enc).
# Strips status / managedFields / cluster metadata so kubectl apply can (re)create resources.
kind_bootstrap_state_restore() {
  local src abs ctx want tmp
  local p="${BOOTSTRAP_KIND_BACKUP_PASSPHRASE:-${AGE_PASSPHRASE:-}}"
  command -v kubectl >/dev/null 2>&1 || {
    err "kubectl required for kind_bootstrap_state_restore"
    return 1
  }
  src="${1:-}"
  [[ -n "$src" && -e "$src" ]] || {
    err "Usage: pass backup archive path (e.g. bootstrap-kind-backup-*.tar.gz, *.tar.gz.age, *.tar.gz.enc)"
    return 1
  }
  case "$src" in
  /*) abs="$src" ;;
  *) abs="$(pwd)/$src" ;;
  esac
  ctx="kind-${KIND_CLUSTER_NAME}"
  kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | grep -qx "$ctx" || {
    err "kube context ${ctx} not found"
    return 1
  }
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/bkr.XXXXXX")" || return 1
  case "$abs" in
  *.age) want=age ;;
  *.enc) want=openssl ;;
  *) want=none ;;
  esac
  if [[ "$want" = "age" ]]; then
    [[ -n "$p" ]] || {
      err "Set BOOTSTRAP_KIND_BACKUP_PASSPHRASE or AGE_PASSPHRASE to decrypt"
      rm -rf -- "$tmp"
      return 1
    }
    export AGE_PASSPHRASE="$p"
    age -d -o - "$abs" | gunzip -cd | tar -xf - -C "$tmp" || {
      rm -rf -- "$tmp"
      return 1
    }
  elif [[ "$want" = "openssl" ]]; then
    export BOOTSTRAP_KIND_BACKUP_PASSPHRASE="${BOOTSTRAP_KIND_BACKUP_PASSPHRASE:-$p}"
    [[ -n "${BOOTSTRAP_KIND_BACKUP_PASSPHRASE:-}" ]] || {
      err "Set BOOTSTRAP_KIND_BACKUP_PASSPHRASE to decrypt"
      rm -rf -- "$tmp"
      return 1
    }
    openssl enc -aes-256-cbc -d -pbkdf2 -pass env:BOOTSTRAP_KIND_BACKUP_PASSPHRASE -in "$abs" | gunzip -cd | tar -xf - -C "$tmp" || {
      rm -rf -- "$tmp"
      return 1
    }
  else
    case "$abs" in
    *.gz) gunzip -cd -- "$abs" | tar -xf - -C "$tmp" || {
        rm -rf -- "$tmp"
        return 1
      } ;;
    *) tar -xf -- "$abs" -C "$tmp" || {
        rm -rf -- "$tmp"
        return 1
      } ;;
    esac
  fi
  [[ -d "$tmp/data" ]] || {
    err "Invalid backup archive: missing data/ after extract"
    rm -rf -- "$tmp"
    return 1
  }
  if [[ -d "$tmp/kind" ]]; then
    log "This archive includes kind/ (kind-config.yaml, README) — use it to recreate the management kind cluster; see kind/README in the extracted tree or tarball."
  fi
  export KIND_R_CTX="$ctx"
  export KIND_R_ROOT="$tmp"
  if ! python3 - <<'PY'
import json, os, subprocess, sys

def clean(obj):
  obj.pop("status", None)
  m = obj.get("metadata", {})
  if not isinstance(m, dict):
    return
  for k in (
    "resourceVersion", "uid", "selfLink", "generation", "creationTimestamp",
    "deletionTimestamp", "deletionGracePeriodSeconds", "managedFields", "ownerReferences",
  ):
    m.pop(k, None)
  if "annotations" in m and isinstance(m["annotations"], dict):
    a = m["annotations"]
    a.pop("kubectl.kubernetes.io/last-applied-configuration", None)
    a.pop("deployment.kubernetes.io/revision", None)
    if not a:
      del m["annotations"]

def main():
  root = os.environ.get("KIND_R_ROOT", "")
  ctx = os.environ.get("KIND_R_CTX", "")
  if not root or not ctx:
    print("KIND_R_ROOT / KIND_R_CTX missing", file=sys.stderr)
    return 1
  data = os.path.join(root, "data")
  for ns in sorted(os.listdir(data)):
    nd = os.path.join(data, ns)
    if not os.path.isdir(nd):
      continue
    njp = os.path.join(nd, "namespace.json")
    if os.path.isfile(njp):
      with open(njp, encoding="utf-8") as f:
        obj = json.load(f)
      clean(obj)
      doc = json.dumps(obj, ensure_ascii=False)
      r = subprocess.run(
        ["kubectl", "--context", ctx, "apply", "-f", "-"],
        input=doc, text=True, capture_output=True,
      )
      if r.returncode != 0:
        print(r.stderr or r.stdout, file=sys.stderr)
        return r.returncode
    jlp = os.path.join(nd, "objects.jsonl")
    if not os.path.isfile(jlp):
      continue
    with open(jlp, encoding="utf-8") as f:
      for line in f:
        line = line.strip()
        if not line:
          continue
        try:
          obj = json.loads(line)
        except json.JSONDecodeError as e:
          print("skip line:", e, file=sys.stderr)
          continue
        clean(obj)
        doc = json.dumps(obj, ensure_ascii=False)
        r = subprocess.run(
          ["kubectl", "--context", ctx, "apply", "-f", "-"],
          input=doc, text=True, capture_output=True,
        )
        if r.returncode != 0:
          print(r.stderr or r.stdout, file=sys.stderr)
          return r.returncode
  return 0

if __name__ == "__main__":
  raise SystemExit(main())
PY
  then
    err "restore apply failed (python)"
    rm -rf -- "$tmp"
    return 1
  fi
  log "kind_bootstrap_state_restore: applied from $abs into $ctx"
  rm -rf -- "$tmp"
  unset -v KIND_R_ROOT KIND_R_CTX
}

# If we just ran clusterctl while a Cluster with the same name already exists, applying may fail or leave a split-brain
# when immutable spec fields changed — user should delete the Cluster first (see header: CAPI_MANIFEST immutability).
warn_regenerated_capi_manifest_immutable_risk() {
  is_true "${BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING:-false}" && return 0
  is_true "${BOOTSTRAP_CLUSTERCTL_REGENERATED_MANIFEST:-false}" || return 0
  local ctx
  ctx="kind-${KIND_CLUSTER_NAME}"
  command -v kubectl >/dev/null 2>&1 || return 0
  (kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | grep -qx "$ctx") || return 0
  kubectl --context "$ctx" get cluster "$WORKLOAD_CLUSTER_NAME" -n "$WORKLOAD_CLUSTER_NAMESPACE" &>/dev/null || return 0
  warn "Regenerated workload manifest (clusterctl) while Cluster ${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME} already exists. If you changed immutable fields (pod/service CIDRs, cluster name, infra API, control plane wiring, …), delete that Cluster and wait for cleanup before re-applying; otherwise kubectl apply may error or ignore changes. Set BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING=true to hide this."
}

ensure_kind() {
  local have os arch
  if command -v kind >/dev/null 2>&1; then
    # kind prints "kind version 0.31.0" (no leading v) — grep "v[0-9]..." never matches; pipefail would exit the script.
    have="$(
      kind --version 2>&1 | grep -oE 'v?[0-9][0-9.]+' | head -1
    )" || true
    if _versions_match "$have" "$KIND_VERSION"; then
      return
    fi
    warn "kind (${have:-unknown}) does not match KIND_VERSION=${KIND_VERSION} — reinstalling..."
  else
    warn "kind not found — installing..."
  fi
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(_arch)"
  install_binary kind \
    "https://github.com/kubernetes-sigs/kind/releases/download/${KIND_VERSION}/kind-${os}-${arch}"
}

ensure_kubectl() {
  local have os arch
  if command -v kubectl >/dev/null 2>&1; then
    have="$(
      kubectl version -o json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("clientVersion",{}).get("gitVersion",""))' 2>/dev/null || true
    )"
    if _versions_match "$have" "$KUBECTL_VERSION"; then
      return
    fi
    warn "kubectl (${have:-unknown}) does not match KUBECTL_VERSION=${KUBECTL_VERSION} — reinstalling..."
  else
    warn "kubectl not found — installing..."
  fi
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(_arch)"
  install_binary kubectl \
    "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${os}/${arch}/kubectl"
}

ensure_clusterctl() {
  local have os arch
  if command -v clusterctl >/dev/null 2>&1; then
    have="$(
      clusterctl version -o json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); v=d.get("clusterctl") or d.get("ClientVersion") or d.get("clientVersion") or {}; print((v or {}).get("GitVersion", (v or {}).get("gitVersion","")) or "")' 2>/dev/null || true
    )"
    if _versions_match "$have" "$CLUSTERCTL_VERSION"; then
      return
    fi
    warn "clusterctl (${have:-unknown}) does not match CLUSTERCTL_VERSION=${CLUSTERCTL_VERSION} — reinstalling..."
  else
    warn "clusterctl not found — installing..."
  fi
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(_arch)"
  install_binary clusterctl \
    "https://github.com/kubernetes-sigs/cluster-api/releases/download/${CLUSTERCTL_VERSION}/clusterctl-${os}-${arch}"
}

ensure_cilium_cli() {
  local have os arch tarball
  if command -v cilium >/dev/null 2>&1; then
    have="$(
      cilium version 2>&1 | head -1 | grep -oE 'v?[0-9][0-9.]+' | head -1
    )" || true
    if _versions_match "$have" "$CILIUM_CLI_VERSION"; then
      return
    fi
    warn "cilium CLI (${have:-unknown}) does not match CILIUM_CLI_VERSION=${CILIUM_CLI_VERSION} — reinstalling..."
  else
    warn "cilium CLI not found — installing..."
  fi
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(_arch)"
  tarball="cilium-${os}-${arch}.tar.gz"
  curl -fsSL "https://github.com/cilium/cilium-cli/releases/download/${CILIUM_CLI_VERSION}/${tarball}" \
    | RUN_PRIVILEGED tar -xz -C /usr/local/bin cilium \
    || die "Failed to install cilium CLI (curl or tar: check CILIUM_CLI_VERSION=${CILIUM_CLI_VERSION} and network)."
  RUN_PRIVILEGED chmod +x /usr/local/bin/cilium
}

ensure_argocd_cli() {
  local have arch argo_arch
  [[ "$(uname -s)" == "Linux" ]] || die "argocd CLI install is supported on Linux only (amd64/arm64), not $(uname -s)."
  if command -v argocd >/dev/null 2>&1; then
    have="$(
      argocd version --client 2>&1 | grep -oE 'v?[0-9][0-9.]+' | head -1
    )" || true
    if _versions_match "$have" "$ARGOCD_VERSION"; then
      return
    fi
    warn "argocd CLI (${have:-unknown}) does not match ARGOCD_VERSION=${ARGOCD_VERSION} — reinstalling..."
  else
    warn "argocd CLI not found — installing..."
  fi
  arch="$(_arch)"
  case "$arch" in
    amd64) argo_arch=amd64 ;;
    arm64) argo_arch=arm64 ;;
    *) die "Unsupported architecture for argocd CLI on Linux: ${arch} (need amd64 or arm64)." ;;
  esac
  install_binary argocd \
    "https://github.com/argoproj/argo-cd/releases/download/${ARGOCD_VERSION}/argocd-linux-${argo_arch}"
}

ensure_kyverno_cli() {
  local have arch ky_arch tarball
  [[ "$(uname -s)" == "Linux" ]] || die "kyverno CLI install is supported on Linux only (amd64/arm64), not $(uname -s)."
  if command -v kyverno >/dev/null 2>&1; then
    have="$(
      kyverno version 2>&1 | grep -oE 'v?[0-9][0-9.]+' | head -1
    )" || true
    if _versions_match "$have" "$KYVERNO_CLI_VERSION"; then
      return
    fi
    warn "kyverno CLI (${have:-unknown}) does not match KYVERNO_CLI_VERSION=${KYVERNO_CLI_VERSION} — reinstalling..."
  else
    warn "kyverno CLI not found — installing..."
  fi
  arch="$(_arch)"
  case "$arch" in
    amd64) ky_arch=x86_64 ;;
    arm64) ky_arch=arm64 ;;
    *) die "Unsupported architecture for kyverno CLI on Linux: ${arch} (need amd64 or arm64)." ;;
  esac
  tarball="kyverno-cli_${KYVERNO_CLI_VERSION}_linux_${ky_arch}.tar.gz"
  curl -fsSL "https://github.com/kyverno/kyverno/releases/download/${KYVERNO_CLI_VERSION}/${tarball}" \
    | RUN_PRIVILEGED tar -xz -C /usr/local/bin kyverno \
    || die "Failed to install kyverno CLI (check KYVERNO_CLI_VERSION=${KYVERNO_CLI_VERSION} and network)."
  RUN_PRIVILEGED chmod +x /usr/local/bin/kyverno
}

ensure_cmctl() {
  local have arch tarball
  [[ "$(uname -s)" == "Linux" ]] || die "cmctl install is supported on Linux only (amd64/arm64), not $(uname -s)."
  if command -v cmctl >/dev/null 2>&1; then
    have="$(
      cmctl version 2>&1 | grep -oE 'v?[0-9][0-9.]+' | head -1
    )" || true
    if _versions_match "$have" "$CMCTL_VERSION"; then
      return
    fi
    warn "cmctl (${have:-unknown}) does not match CMCTL_VERSION=${CMCTL_VERSION} — reinstalling..."
  else
    warn "cmctl (cert-manager) not found — installing..."
  fi
  arch="$(_arch)"
  case "$arch" in
    amd64 | arm64) ;;
    *) die "Unsupported architecture for cmctl on Linux: ${arch} (need amd64 or arm64)." ;;
  esac
  tarball="cmctl_linux_${arch}.tar.gz"
  curl -fsSL "https://github.com/cert-manager/cmctl/releases/download/${CMCTL_VERSION}/${tarball}" \
    | RUN_PRIVILEGED tar -xz -C /usr/local/bin cmctl \
    || die "Failed to install cmctl (check CMCTL_VERSION=${CMCTL_VERSION} and network)."
  RUN_PRIVILEGED chmod +x /usr/local/bin/cmctl
}

ensure_system_dependencies() {
  log "Checking and installing system-wide dependencies..."

  # Check for git
  if ! command -v git >/dev/null 2>&1; then
    warn "git not found — installing..."
    if command -v apt-get >/dev/null 2>&1; then
      RUN_PRIVILEGED apt-get update -qq && RUN_PRIVILEGED apt-get install -y git
    elif command -v dnf >/dev/null 2>&1; then
      RUN_PRIVILEGED dnf install -y git
    elif command -v yum >/dev/null 2>&1; then
      RUN_PRIVILEGED yum install -y git
    elif command -v apk >/dev/null 2>&1; then
      RUN_PRIVILEGED apk add git
    else
      die "git not found and package manager not detected — install git manually."
    fi
  fi

  # Check for curl
  if ! command -v curl >/dev/null 2>&1; then
    warn "curl not found — installing..."
    if command -v apt-get >/dev/null 2>&1; then
      RUN_PRIVILEGED apt-get update -qq && RUN_PRIVILEGED apt-get install -y curl
    elif command -v dnf >/dev/null 2>&1; then
      RUN_PRIVILEGED dnf install -y curl
    elif command -v yum >/dev/null 2>&1; then
      RUN_PRIVILEGED yum install -y curl
    elif command -v apk >/dev/null 2>&1; then
      RUN_PRIVILEGED apk add curl
    else
      die "curl not found and package manager not detected — install curl manually."
    fi
  fi

  # Check for python3
  if ! command -v python3 >/dev/null 2>&1; then
    warn "python3 not found — installing..."
    if command -v apt-get >/dev/null 2>&1; then
      RUN_PRIVILEGED apt-get update -qq && RUN_PRIVILEGED apt-get install -y python3
    elif command -v dnf >/dev/null 2>&1; then
      RUN_PRIVILEGED dnf install -y python3
    elif command -v yum >/dev/null 2>&1; then
      RUN_PRIVILEGED yum install -y python3
    elif command -v apk >/dev/null 2>&1; then
      RUN_PRIVILEGED apk add python3
    else
      die "python3 not found and package manager not detected — install python3 manually."
    fi
  fi

  log "System-wide dependencies check complete."
}

ensure_opentofu() {
  local have os arch zip_path url
  if command -v tofu >/dev/null 2>&1; then
    have="$(
      tofu version -json 2>/dev/null | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("terraform_version",""))' 2>/dev/null || true
    )"
    if _versions_match "$have" "$OPENTOFU_VERSION"; then
      return
    fi
    warn "tofu (${have:-unknown}) does not match OPENTOFU_VERSION=${OPENTOFU_VERSION} — reinstalling..."
  else
    warn "tofu not found — installing..."
  fi

  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(_arch)"
  zip_path="/tmp/tofu_${OPENTOFU_VERSION}_${os}_${arch}.zip"
  url="https://github.com/opentofu/opentofu/releases/download/v${OPENTOFU_VERSION}/tofu_${OPENTOFU_VERSION}_${os}_${arch}.zip"

  curl -fsSL "$url" -o "$zip_path" \
    || die "Failed to download OpenTofu from ${url} (check OPENTOFU_VERSION=${OPENTOFU_VERSION} and network)."
  python3 - "$zip_path" <<'PY'
import sys, zipfile

zip_path = sys.argv[1]
with zipfile.ZipFile(zip_path) as archive:
    with archive.open("tofu") as src, open("/tmp/tofu.bin", "wb") as dst:
        dst.write(src.read())
PY

  RUN_PRIVILEGED install -m 0755 /tmp/tofu.bin /usr/local/bin/tofu
  rm -f "$zip_path" /tmp/tofu.bin
}

install_bpg_proxmox_provider() {
  local tmp_dir plugin_cache
  tmp_dir="$(mktemp -d)"
  plugin_cache="${HOME}/.terraform.d/plugin-cache"

  mkdir -p "$plugin_cache"
  cat > "${tmp_dir}/main.tf" <<'EOF'
terraform {
  required_providers {
    proxmox = {
      source = "bpg/proxmox"
    }
  }
}
EOF

  log "Installing OpenTofu provider bpg/proxmox..."
  TF_PLUGIN_CACHE_DIR="$plugin_cache" tofu -chdir="$tmp_dir" init -backend=false -upgrade >/dev/null
  rm -rf "$tmp_dir"
}

write_embedded_terraform_files() {
  local state_dir="${HOME}/.bootstrap-capi/proxmox-identity-terraform"
  mkdir -p "$state_dir"

  cat > "${state_dir}/${PROXMOX_IDENTITY_TF}" <<'EOF'
# Plugin: bpg/proxmox

terraform {
  required_providers {
    proxmox = {
      source = "bpg/proxmox"
    }
  }
}

provider "proxmox" {}

variable "cluster_set_id" {
  description = "Shared suffix used for generated role and token names"
  type        = string
  default     = "1"
}

variable "csi_user_id" {
  description = "Proxmox user ID for the CSI identity"
  type        = string
}

variable "csi_token_prefix" {
  description = "Token prefix for the CSI identity"
  type        = string
}

variable "capi_user_id" {
  description = "Proxmox user ID for the CAPI identity"
  type        = string
}

variable "capi_token_prefix" {
  description = "Token prefix for the CAPI identity"
  type        = string
}

locals {
  identities = {
    csi = {
      role_id = "Kubernetes-CSI-${var.cluster_set_id}"
      privileges = [
        # proxmox-csi lists cluster resources for storage config; Datastore.* alone is not enough
        "Sys.Audit",
        "VM.Audit",
        "VM.Config.Disk",
        "Datastore.Allocate",
        "Datastore.AllocateSpace",
        "Datastore.Audit",
      ]
      user_comment          = "Kubernetes"
      user_id               = var.csi_user_id
      token_comment         = "Kubernetes CSI"
      token_prefix          = var.csi_token_prefix
      privileges_separation = false
    }
    capi = {
      role_id = "Kubernetes-CAPI-${var.cluster_set_id}"
      privileges = [
        "Datastore.Allocate",
        "Datastore.AllocateSpace",
        "Datastore.AllocateTemplate",
        "Datastore.Audit",
        "Pool.Allocate",
        "SDN.Use",
        "Sys.Audit",
        "Sys.Console",
        "Sys.Modify",
        "VM.Allocate",
        "VM.Audit",
        "VM.Clone",
        "VM.Config.CDROM",
        "VM.Config.Cloudinit",
        "VM.Config.CPU",
        "VM.Config.Disk",
        "VM.Config.HWType",
        "VM.Config.Memory",
        "VM.Config.Network",
        "VM.Config.Options",
        "VM.Console",
        "VM.GuestAgent.Audit",
        "VM.GuestAgent.Unrestricted",
        "VM.Migrate",
        "VM.PowerMgmt",
      ]
      user_comment          = "Cluster API Proxmox provider"
      user_id               = var.capi_user_id
      token_comment         = "Cluster API Proxmox provider token"
      token_prefix          = var.capi_token_prefix
      privileges_separation = false
    }
  }
}

resource "proxmox_virtual_environment_role" "identity" {
  for_each = local.identities

  role_id    = each.value.role_id
  privileges = each.value.privileges
}

resource "proxmox_virtual_environment_user" "identity" {
  for_each = local.identities

  acl {
    path      = "/"
    propagate = true
    role_id   = proxmox_virtual_environment_role.identity[each.key].role_id
  }

  comment = each.value.user_comment
  user_id = each.value.user_id
}

resource "proxmox_virtual_environment_user_token" "identity" {
  for_each = local.identities

  comment               = each.value.token_comment
  token_name            = "${each.value.token_prefix}-${var.cluster_set_id}"
  user_id               = proxmox_virtual_environment_user.identity[each.key].user_id
  privileges_separation = each.value.privileges_separation
}

resource "proxmox_virtual_environment_acl" "identity" {
  for_each = local.identities

  token_id = proxmox_virtual_environment_user_token.identity[each.key].id
  role_id  = proxmox_virtual_environment_role.identity[each.key].role_id

  path      = "/"
  propagate = true
}

output "capi_token_id" {
  value = proxmox_virtual_environment_user_token.identity["capi"].id
}

output "capi_token_secret" {
  value     = proxmox_virtual_environment_user_token.identity["capi"].value
  sensitive = true
}

output "csi_token_id" {
  value = proxmox_virtual_environment_user_token.identity["csi"].id
}

output "csi_token_secret" {
  value     = proxmox_virtual_environment_user_token.identity["csi"].value
  sensitive = true
}
EOF
}

apply_proxmox_identity_terraform() {
  local state_dir endpoint api_token
  local -a tf_vars
  state_dir="${HOME}/.bootstrap-capi/proxmox-identity-terraform"
  endpoint="${PROXMOX_URL}"
  api_token="${PROXMOX_ADMIN_USERNAME}=${PROXMOX_ADMIN_TOKEN}"

  write_embedded_terraform_files

  log "Applying Terraform identity bootstrap for CAPI/CSI users..."
  PROXMOX_VE_ENDPOINT="$endpoint" \
  PROXMOX_VE_API_TOKEN="$api_token" \
  PROXMOX_VE_INSECURE="$PROXMOX_ADMIN_INSECURE" \
  tofu -chdir="$state_dir" init -upgrade

  tf_vars=(
    -var "cluster_set_id=${PROXMOX_IDENTITY_SUFFIX}"
    -var "csi_user_id=${PROXMOX_CSI_USER_ID}"
    -var "csi_token_prefix=${PROXMOX_CSI_TOKEN_PREFIX}"
    -var "capi_user_id=${PROXMOX_CAPI_USER_ID}"
    -var "capi_token_prefix=${PROXMOX_CAPI_TOKEN_PREFIX}"
  )

  PROXMOX_VE_ENDPOINT="$endpoint" \
  PROXMOX_VE_API_TOKEN="$api_token" \
  PROXMOX_VE_INSECURE="$PROXMOX_ADMIN_INSECURE" \
  tofu -chdir="$state_dir" apply -auto-approve "${tf_vars[@]}"
}

# Infer PROXMOX_IDENTITY_SUFFIX / user ids from token IDs (user@pve!prefix-suffix) when Terraform state is missing.
infer_proxmox_identity_from_token_ids() {
  [[ -n "${PROXMOX_CSI_TOKEN_ID:-}" && "$PROXMOX_CSI_TOKEN_ID" == *'!'* ]] || return 1
  [[ -n "${PROXMOX_TOKEN:-}" && "$PROXMOX_TOKEN" == *'!'* ]] || return 1
  local a pfx suf u suf2
  u="${PROXMOX_CSI_TOKEN_ID%%!*}"
  a="${PROXMOX_CSI_TOKEN_ID#*!}"
  PROXMOX_CSI_USER_ID="$u"
  pfx="${a%%-*}"
  [[ -n "$pfx" && "$a" == "${pfx}"-* ]] || return 1
  suf="${a#"${pfx}"-}"
  PROXMOX_CSI_TOKEN_PREFIX="$pfx"
  u="${PROXMOX_TOKEN%%!*}"
  a="${PROXMOX_TOKEN#*!}"
  PROXMOX_CAPI_USER_ID="$u"
  pfx="${a%%-*}"
  [[ -n "$pfx" && "$a" == "${pfx}"-* ]] || return 1
  suf2="${a#"${pfx}"-}"
  PROXMOX_CAPI_TOKEN_PREFIX="$pfx"
  if [[ -z "$suf" || -z "$suf2" || "$suf" != "$suf2" ]]; then
    return 1
  fi
  PROXMOX_IDENTITY_SUFFIX="$suf"
  if [[ -z "${CLUSTER_SET_ID:-}" ]]; then
    CLUSTER_SET_ID="$suf"
  fi
  return 0
}

resolve_recreate_proxmox_identity_context() {
  local state_dir tf_file
  state_dir="${HOME}/.bootstrap-capi/proxmox-identity-terraform"
  tf_file="${state_dir}/terraform.tfstate"
  if [[ -f "$tf_file" ]]; then
    local -a tf_in=()
    mapfile -t tf_in < <(extract_identity_tf_inputs_from_state "$tf_file")
    [[ -n "${tf_in[0]:-}" && -n "${tf_in[1]:-}" && -n "${tf_in[2]:-}" && -n "${tf_in[3]:-}" && -n "${tf_in[4]:-}" ]] \
      || die "Could not read identity inputs from ${tf_file} (state incomplete)."
    if [[ -z "${CLUSTER_SET_ID:-}" ]]; then
      CLUSTER_SET_ID="${tf_in[0]}"
    fi
    PROXMOX_CSI_USER_ID="${tf_in[1]}"
    PROXMOX_CSI_TOKEN_PREFIX="${tf_in[2]}"
    PROXMOX_CAPI_USER_ID="${tf_in[3]}"
    PROXMOX_CAPI_TOKEN_PREFIX="${tf_in[4]}"
    if [[ -z "$PROXMOX_IDENTITY_SUFFIX" ]]; then
      PROXMOX_IDENTITY_SUFFIX="$(derive_proxmox_identity_suffix "$CLUSTER_SET_ID")"
    fi
    log "Re-creation: identity from Terraform state (${state_dir}): cluster_set_id var=${tf_in[0]}."
    return 0
  fi
  warn "No Terraform state at ${tf_file} — inferring from PROXMOX_CSI_TOKEN_ID and PROXMOX_TOKEN (CAPI) in env/kind."
  infer_proxmox_identity_from_token_ids \
    || die "Cannot resolve identity: restore ${tf_file} or set PROXMOX_CSI_TOKEN_ID + PROXMOX_TOKEN to existing token *names* (user@pve!prefix-suffix) from Kubernetes Secrets."
  if [[ -z "$PROXMOX_IDENTITY_SUFFIX" ]]; then
    die "Recreate: PROXMOX_IDENTITY_SUFFIX is empty after inference."
  fi
  log "Re-creation: inferred Proxmox identity suffix ${PROXMOX_IDENTITY_SUFFIX} from token id format."
}

validate_cluster_set_id_format() {
  if [[ "$CLUSTER_SET_ID" =~ ^[0-9]+$ ]]; then
    (( CLUSTER_SET_ID >= 1 )) || die "Numeric CLUSTER_SET_ID must be >= 1."
  elif [[ "$CLUSTER_SET_ID" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]]; then
    :
  elif [[ "$CLUSTER_SET_ID" =~ ^[0-9a-f]{12}$ ]]; then
    : # 12-char Terraform cluster_set_id / compact suffix (recreate)
  else
    die "CLUSTER_SET_ID must be a positive integer, a UUID v4, or a 12-hex Proxmox identity suffix (recreate); got: ${CLUSTER_SET_ID}"
  fi
}

proxmox_identity_terraform_state_rm_all() {
  local state_dir addr
  state_dir="${HOME}/.bootstrap-capi/proxmox-identity-terraform"
  [[ -f "${state_dir}/terraform.tfstate" ]] || { warn "No OpenTofu state to clear at ${state_dir}."; return 0; }
  log "Removing all resources from Proxmox identity Terraform state (PVE may be empty; next apply is create-only)..."
  while IFS= read -r addr; do
    [[ -n "$addr" ]] || continue
    tofu -chdir="$state_dir" state rm "$addr" </dev/null \
      || warn "state rm failed for ${addr}"
  done < <(tofu -chdir="$state_dir" state list 2>/dev/null || true)
}

# CAPMOX controller reads url/token/secret from this Secret; also invoked from sync_proxmox_bootstrap_literal_credentials_to_kind.
update_capmox_manager_secret_on_kind() {
  local ctx
  [[ -n "${PROXMOX_URL:-}" && -n "${PROXMOX_TOKEN:-}" && -n "${PROXMOX_SECRET:-}" ]] || return 0
  ctx="kind-${KIND_CLUSTER_NAME}"
  kubectl --context "$ctx" get namespace capmox-system >/dev/null 2>&1 || {
    warn "namespace capmox-system not on ${ctx} — skip capmox-manager-credentials update."
    return 0
  }
  kubectl --context "$ctx" -n capmox-system create secret generic capmox-manager-credentials \
    --from-literal=url="${PROXMOX_URL}" \
    --from-literal=token="${PROXMOX_TOKEN}" \
    --from-literal=secret="${PROXMOX_SECRET}" \
    --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - \
    || die "Failed to update capmox-system/capmox-manager-credentials on ${ctx}."
  log "Updated capmox-system/capmox-manager-credentials on ${ctx}."
}

rollout_restart_capmox_controller() {
  local ctx
  ctx="kind-${KIND_CLUSTER_NAME}"
  kubectl --context "$ctx" -n capmox-system rollout restart deployment/capmox-controller-manager 2>/dev/null \
    && kubectl --context "$ctx" -n capmox-system rollout status deployment/capmox-controller-manager --timeout=180s 2>/dev/null \
    || warn "capmox-controller-manager restart skipped or not ready (check capmox-system)."
}

rollout_restart_proxmox_csi_on_workload() {
  local kcfg
  kcfg="$(mktemp)"
  if ! kubectl --context "kind-${KIND_CLUSTER_NAME}" -n "$WORKLOAD_CLUSTER_NAMESPACE" \
    get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' 2>/dev/null | base64 -d > "$kcfg"; then
    rm -f "$kcfg"
    warn "No workload kubeconfig — skip Proxmox CSI controller restart on workload."
    return 0
  fi
  if kubectl --kubeconfig "$kcfg" -n "$PROXMOX_CSI_NAMESPACE" get deploy proxmox-csi-plugin-controller >/dev/null 2>&1; then
    kubectl --kubeconfig "$kcfg" -n "$PROXMOX_CSI_NAMESPACE" rollout restart deploy/proxmox-csi-plugin-controller
    kubectl --kubeconfig "$kcfg" -n "$PROXMOX_CSI_NAMESPACE" rollout status deploy/proxmox-csi-plugin-controller --timeout=300s
    log "Restarted Proxmox CSI controller on workload ${WORKLOAD_CLUSTER_NAME}."
  else
    warn "proxmox-csi controller deployment not found in ${PROXMOX_CSI_NAMESPACE} — skip restart."
  fi
  rm -f "$kcfg"
}

# Terraform: replace/rotate CAPI+CSI Proxmox identities, refresh PROXMOX_* in env, push kind Secrets + local clusterctl/CSI file stubs (same as apply + generate on first bootstrap).
# Must run before `clusterctl init` (tokens must be in the environment for the ephemeral clusterctl file).
# Paired with recreate_identities_resync_and_rollout_capmox (after capmox-system is installed) and optionally recreate_identities_workload_csi_secrets (after a workload CAPI manifest exists).
recreate_proxmox_identities_terraform() {
  local state_dir
  state_dir="${HOME}/.bootstrap-capi/proxmox-identity-terraform"
  command -v tofu >/dev/null 2>&1 || die "OpenTofu (tofu) is required for --recreate-proxmox-identities."
  ensure_proxmox_admin_config
  if [[ -z "$PROXMOX_URL" || -z "$PROXMOX_ADMIN_USERNAME" || -z "$PROXMOX_ADMIN_TOKEN" ]]; then
    die "Recreate: need PROXMOX_URL, PROXMOX_ADMIN_USERNAME, PROXMOX_ADMIN_TOKEN (set env, kind Secret ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME}, or PROXMOX_ADMIN_CONFIG to a legacy file)."
  fi
  resolve_recreate_proxmox_identity_context
  validate_cluster_set_id_format
  if [[ -z "$PROXMOX_IDENTITY_SUFFIX" ]]; then
    PROXMOX_IDENTITY_SUFFIX="$(derive_proxmox_identity_suffix "$CLUSTER_SET_ID")"
  fi
  refresh_derived_identity_user_ids
  check_proxmox_admin_api_connectivity
  write_embedded_terraform_files
  PROXMOX_VE_ENDPOINT="$PROXMOX_URL" \
  PROXMOX_VE_API_TOKEN="${PROXMOX_ADMIN_USERNAME}=${PROXMOX_ADMIN_TOKEN}" \
  PROXMOX_VE_INSECURE="$PROXMOX_ADMIN_INSECURE" \
  tofu -chdir="$state_dir" init -upgrade
  if is_true "$PROXMOX_IDENTITY_RECREATE_STATE_RM"; then
    proxmox_identity_terraform_state_rm_all
    PROXMOX_VE_ENDPOINT="$PROXMOX_URL" \
    PROXMOX_VE_API_TOKEN="${PROXMOX_ADMIN_USERNAME}=${PROXMOX_ADMIN_TOKEN}" \
    PROXMOX_VE_INSECURE="$PROXMOX_ADMIN_INSECURE" \
    tofu -chdir="$state_dir" apply -auto-approve \
      -var "cluster_set_id=${PROXMOX_IDENTITY_SUFFIX}" \
      -var "csi_user_id=${PROXMOX_CSI_USER_ID}" \
      -var "csi_token_prefix=${PROXMOX_CSI_TOKEN_PREFIX}" \
      -var "capi_user_id=${PROXMOX_CAPI_USER_ID}" \
      -var "capi_token_prefix=${PROXMOX_CAPI_TOKEN_PREFIX}"
  else
    local -a rargs=() scope
    scope="${PROXMOX_IDENTITY_RECREATE_SCOPE:-both}"
    case "$scope" in
      both)
        rargs+=(
          -replace='proxmox_virtual_environment_role.identity["capi"]'
          -replace='proxmox_virtual_environment_role.identity["csi"]'
          -replace='proxmox_virtual_environment_user.identity["capi"]'
          -replace='proxmox_virtual_environment_user.identity["csi"]'
          -replace='proxmox_virtual_environment_user_token.identity["capi"]'
          -replace='proxmox_virtual_environment_user_token.identity["csi"]'
          -replace='proxmox_virtual_environment_acl.identity["capi"]'
          -replace='proxmox_virtual_environment_acl.identity["csi"]'
        )
        ;;
      csi)
        rargs+=(
          -replace='proxmox_virtual_environment_role.identity["csi"]'
          -replace='proxmox_virtual_environment_user.identity["csi"]'
          -replace='proxmox_virtual_environment_user_token.identity["csi"]'
          -replace='proxmox_virtual_environment_acl.identity["csi"]'
        )
        ;;
      capi)
        rargs+=(
          -replace='proxmox_virtual_environment_role.identity["capi"]'
          -replace='proxmox_virtual_environment_user.identity["capi"]'
          -replace='proxmox_virtual_environment_user_token.identity["capi"]'
          -replace='proxmox_virtual_environment_acl.identity["capi"]'
        )
        ;;
      *)
        die "Invalid --recreate-proxmox-identities-scope: ${scope} (use capi, csi, or both)."
        ;;
    esac
    PROXMOX_VE_ENDPOINT="$PROXMOX_URL" \
    PROXMOX_VE_API_TOKEN="${PROXMOX_ADMIN_USERNAME}=${PROXMOX_ADMIN_TOKEN}" \
    PROXMOX_VE_INSECURE="$PROXMOX_ADMIN_INSECURE" \
    tofu -chdir="$state_dir" apply -auto-approve \
      -var "cluster_set_id=${PROXMOX_IDENTITY_SUFFIX}" \
      -var "csi_user_id=${PROXMOX_CSI_USER_ID}" \
      -var "csi_token_prefix=${PROXMOX_CSI_TOKEN_PREFIX}" \
      -var "capi_user_id=${PROXMOX_CAPI_USER_ID}" \
      -var "capi_token_prefix=${PROXMOX_CAPI_TOKEN_PREFIX}" \
      "${rargs[@]}"
  fi
  generate_configs_from_terraform_outputs
  log "Proxmox identity Terraform re-apply complete (outputs merged into the environment, kind, and local stubs where enabled)."
}

# After capmox-system and its webhook exist: re-push in-cluster creds and restart the CAPMOX controller to pick up rotated tokens.
recreate_identities_resync_and_rollout_capmox() {
  is_true "${RECREATE_PROXMOX_IDENTITIES:-false}" || return 0
  log "Re-syncing in-cluster CAPI/capmox credentials after Proxmox provider is installed (recreate mode)..."
  sync_bootstrap_config_to_kind || true
  sync_proxmox_bootstrap_literal_credentials_to_kind || true
  rollout_restart_capmox_controller
}

# After the workload CAPI manifest path is available: update CSI config Secret on the workload, restart CSI, final sync. Safe to no-op on missing files/secrets.
recreate_identities_workload_csi_secrets() {
  is_true "${RECREATE_PROXMOX_IDENTITIES:-false}" || return 0
  if [[ -n "${CAPI_MANIFEST:-}" && -s "$CAPI_MANIFEST" ]]; then
    discover_workload_cluster_identity "$CAPI_MANIFEST"
    if [[ -n "$WORKLOAD_CLUSTER_NAME" && -n "$WORKLOAD_CLUSTER_NAMESPACE" && -n "${PROXMOX_CSI_URL:-}" \
      && -n "${PROXMOX_CSI_TOKEN_ID:-}" && -n "${PROXMOX_CSI_TOKEN_SECRET:-}" && -n "${PROXMOX_REGION:-}" ]]; then
      apply_proxmox_csi_config_secret_to_workload_cluster
    else
      warn "Skipping workload Proxmox CSI config Secret (missing region or CSI values — set PROXMOX_REGION)."
    fi
  else
    warn "Skipping workload CSI config — no readable CAPI_MANIFEST; update ${WORKLOAD_CLUSTER_NAME:-cluster}-proxmox-csi-config on the workload by hand or pass --capi-manifest."
  fi
  is_true "${PROXMOX_CSI_ENABLED:-true}" && rollout_restart_proxmox_csi_on_workload
  sync_bootstrap_config_to_kind || true
  sync_proxmox_bootstrap_literal_credentials_to_kind || true
  log "Proxmox CAPI/CSI identity re-creation: workload-side CSI updates and final syncs finished (recreate mode)."
}

# Manual maintenance on an already running stack: use recreate_proxmox_identities_terraform, then
# sync_proxmox_bootstrap_literal_credentials_to_kind; recreate_identities_resync_and_rollout_capmox and
# recreate_identities_workload_csi_secrets implement the full bootstrap when RECREATE_PROXMOX_IDENTITIES is true.

proxmox_api_json_url() {
  if [[ "$PROXMOX_URL" == */api2/json ]]; then
    printf '%s\n' "$PROXMOX_URL"
  else
    printf '%s/api2/json\n' "${PROXMOX_URL%/}"
  fi
}

# Base for https://pve:8006 — strip an accidental /api2/json so callers can append /api2/json/... once.
pve_api_host_base_url() {
  local b="${PROXMOX_URL%/}"
  if [[ "$b" == */api2/json ]]; then
    b="${b%/api2/json}"
  fi
  printf '%s\n' "$b"
}

normalize_proxmox_token_secret() {
  local raw_secret="$1" token_id="${2:-}"

  # Some providers return token output as "<token_id>=<secret>".
  if [[ -n "$token_id" && "$raw_secret" == "${token_id}="* ]]; then
    printf '%s\n' "${raw_secret#${token_id}=}"
    return 0
  fi

  # Fallback: if it still contains '=' take the suffix as secret.
  if [[ "$raw_secret" == *=* ]]; then
    printf '%s\n' "${raw_secret##*=}"
    return 0
  fi

  printf '%s\n' "$raw_secret"
}

validate_proxmox_token_secret() {
  local label="$1" secret="$2"

  [[ -n "$secret" ]] || die "${label} is empty after normalization."
  if [[ "$secret" == *=* ]]; then
    die "${label} is malformed (contains '='). It should be only the token secret value."
  fi
}

# $1 = PVEAPIToken value only (e.g. user@pam!id=uuid), not the "Authorization:" prefix.
_resolve_proxmox_region_and_node_from_pve_auth_value() {
  local api_url pve_auth_value resolved

  [[ -n "$PROXMOX_URL" ]] || return 0
  [[ -n "$1" ]] || return 0

  if [[ -n "$PROXMOX_REGION" && -n "$PROXMOX_NODE" ]]; then
    return 0
  fi

  api_url="$(proxmox_api_json_url)"
  pve_auth_value="$1"

  resolved="$(python3 - "$api_url" "$pve_auth_value" "$PROXMOX_ADMIN_INSECURE" "$PROXMOX_REGION" "$PROXMOX_NODE" <<'PY'
import json
import ssl
import sys
import urllib.request


def as_bool(value):
  return str(value).strip().lower() in {"1", "true", "yes", "on"}


def make_ctx(insecure):
  if not insecure:
    return None
  ctx = ssl.create_default_context()
  ctx.check_hostname = False
  ctx.verify_mode = ssl.CERT_NONE
  return ctx


def fetch_json(url, pve_auth_value, ctx):
  req = urllib.request.Request(url, headers={"Authorization": pve_auth_value})
  with urllib.request.urlopen(req, context=ctx) as resp:
    return json.loads(resp.read().decode("utf-8"))


api_base = sys.argv[1].rstrip("/")
# Full header value: "PVEAPIToken=<token_id>=<secret>"
pve_auth_value = sys.argv[2]
if not pve_auth_value.lower().startswith("pveapitoken="):
  pve_auth_value = f"PVEAPIToken={pve_auth_value}"
auth_header = pve_auth_value
insecure = as_bool(sys.argv[3])
region = sys.argv[4].strip()
node = sys.argv[5].strip()
ctx = make_ctx(insecure)

if not node:
  try:
    nodes_payload = fetch_json(f"{api_base}/nodes", auth_header, ctx)
    nodes = nodes_payload.get("data", [])
    local_nodes = [item.get("node", "") for item in nodes if str(item.get("local", "")).lower() in {"1", "true"}]
    if local_nodes:
      node = local_nodes[0]
    else:
      candidates = sorted([item.get("node", "") for item in nodes if item.get("node")])
      if candidates:
        node = candidates[0]
  except Exception:
    pass

if not region:
  try:
    cluster_payload = fetch_json(f"{api_base}/cluster/status", auth_header, ctx)
    cluster_name = ""
    for item in cluster_payload.get("data", []):
      if item.get("type") == "cluster" and item.get("name"):
        cluster_name = item.get("name")
        break
    if cluster_name:
      region = cluster_name
  except Exception:
    pass

if not region and node:
  region = node

print(region)
print(node)
PY
)" || return 0

  mapfile -t _resolved_region_node <<<"$resolved"

  if [[ -z "$PROXMOX_REGION" && -n "${_resolved_region_node[0]:-}" ]]; then
    PROXMOX_REGION="${_resolved_region_node[0]}"
    log "Derived PROXMOX_REGION from Proxmox API: ${PROXMOX_REGION}"
  fi

  if [[ -z "$PROXMOX_NODE" && -n "${_resolved_region_node[1]:-}" ]]; then
    PROXMOX_NODE="${_resolved_region_node[1]}"
    log "Derived PROXMOX_NODE from Proxmox API: ${PROXMOX_NODE}"
  fi

  unset _resolved_region_node
}

resolve_proxmox_region_and_node_from_admin_api() {
  [[ -n "$PROXMOX_ADMIN_USERNAME" && -n "$PROXMOX_ADMIN_TOKEN" ]] || return 0
  _resolve_proxmox_region_and_node_from_pve_auth_value "PVEAPIToken=${PROXMOX_ADMIN_USERNAME}=${PROXMOX_ADMIN_TOKEN}"
}

# Derive region/node using the same clusterctl / CAPI token (when admin token is unavailable).
resolve_proxmox_region_and_node_from_clusterctl_api() {
  local sec
  [[ -n "$PROXMOX_TOKEN" && -n "$PROXMOX_SECRET" ]] || return 0
  sec="$(normalize_proxmox_token_secret "$PROXMOX_SECRET" "$PROXMOX_TOKEN")"
  _resolve_proxmox_region_and_node_from_pve_auth_value "PVEAPIToken=${PROXMOX_TOKEN}=${sec}"
}

check_proxmox_admin_api_connectivity() {
  local url_base http_code

  [[ -n "$PROXMOX_URL" ]] || die "PROXMOX_URL is required for Terraform identity bootstrap."
  [[ -n "$PROXMOX_ADMIN_USERNAME" ]] || die "PROXMOX_ADMIN_USERNAME is required for Terraform identity bootstrap."
  [[ -n "$PROXMOX_ADMIN_TOKEN" ]] || die "PROXMOX_ADMIN_TOKEN is required for Terraform identity bootstrap."

  url_base="${PROXMOX_URL%/}"
  if [[ "$url_base" == */api2/json ]]; then
    url_base="${url_base%/api2/json}"
  fi

  log "Validating Proxmox admin API credentials at ${url_base}..."
  http_code="$(curl -sk -o /dev/null -w "%{http_code}" \
    -H "Authorization: PVEAPIToken=${PROXMOX_ADMIN_USERNAME}=${PROXMOX_ADMIN_TOKEN}" \
    "${url_base}/api2/json/version")"

  case "$http_code" in
    200)
      log "Proxmox admin API token validated (HTTP 200 on /version)."
      ;;
    401)
      die "Proxmox admin API token unauthorized (401). Check PROXMOX_ADMIN_USERNAME token ID and PROXMOX_ADMIN_TOKEN secret."
      ;;
    000)
      die "Could not reach Proxmox API at ${url_base}. Check PROXMOX_URL and network connectivity."
      ;;
    *)
      die "Unexpected HTTP ${http_code} while validating admin token at ${url_base}."
      ;;
  esac

  http_code="$(curl -sk -o /dev/null -w "%{http_code}" \
    -H "Authorization: PVEAPIToken=${PROXMOX_ADMIN_USERNAME}=${PROXMOX_ADMIN_TOKEN}" \
    "${url_base}/api2/json/access/roles")"

  case "$http_code" in
    200)
      log "Proxmox admin token can access /access/roles (required for role bootstrap)."
      ;;
    401)
      die "Proxmox admin token cannot access /access/roles (401). Token lacks required privileges for Terraform role creation."
      ;;
    *)
      die "Unexpected HTTP ${http_code} while checking /access/roles permissions for Terraform bootstrap."
      ;;
  esac
}

generate_configs_from_terraform_outputs() {
  local state_dir csi_api_url capi_token_id capi_token_secret csi_token_id csi_token_secret
  state_dir="${HOME}/.bootstrap-capi/proxmox-identity-terraform"
  csi_api_url="$(proxmox_api_json_url)"

  capi_token_id="$(tofu -chdir="$state_dir" output -raw capi_token_id)"
  capi_token_secret="$(tofu -chdir="$state_dir" output -raw capi_token_secret)"
  csi_token_id="$(tofu -chdir="$state_dir" output -raw csi_token_id)"
  csi_token_secret="$(tofu -chdir="$state_dir" output -raw csi_token_secret)"

  capi_token_secret="$(normalize_proxmox_token_secret "$capi_token_secret" "$capi_token_id")"
  csi_token_secret="$(normalize_proxmox_token_secret "$csi_token_secret" "$csi_token_id")"
  validate_proxmox_token_secret "Terraform capi_token_secret" "$capi_token_secret"
  validate_proxmox_token_secret "Terraform csi_token_secret" "$csi_token_secret"

  # Terraform apply just finished — outputs are authoritative (tokens may have been rotated; values
  # merged from kind before apply are stale). Always refresh env, then sync to kind below.
  PROXMOX_TOKEN="$capi_token_id"
  PROXMOX_SECRET="$capi_token_secret"
  PROXMOX_CSI_TOKEN_ID="$csi_token_id"
  PROXMOX_CSI_TOKEN_SECRET="$csi_token_secret"
  PROXMOX_CSI_URL="${PROXMOX_CSI_URL:-$csi_api_url}"
  refresh_derived_identity_token_ids

  sync_bootstrap_config_to_kind || true
  sync_proxmox_bootstrap_literal_credentials_to_kind || true
  if ! persist_local_secrets; then
    log "Local CSI YAML persistence is off; bootstrap config was still pushed to kind when the cluster is reachable (use --persist-local-secrets to also write PROXMOX_CSI_CONFIG when set)."
  fi

  write_clusterctl_config_if_missing
  write_csi_config_if_missing
}

write_clusterctl_config_if_missing() {
  refresh_derived_identity_token_ids

  _clusterctl_cfg_file_present && return 0

  if [[ -z "$PROXMOX_URL" || -z "$PROXMOX_TOKEN" || -z "$PROXMOX_SECRET" ]]; then
    return 0
  fi

  sync_bootstrap_config_to_kind || true
  sync_proxmox_bootstrap_literal_credentials_to_kind || true
  if [[ -n "${PROXMOX_BOOTSTRAP_SECRET_NAME:-}" ]]; then
    if [[ "$PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME" != "$PROXMOX_BOOTSTRAP_SECRET_NAME" ]]; then
      log "Bootstrap state synced to kind: ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME} (config.yaml), ${PROXMOX_BOOTSTRAP_SECRET_NAME} (CAPI+CSI), ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME} (proxmox-admin.yaml) when the management cluster is reachable; clusterctl uses a temp file for the CLI only."
    else
      log "Bootstrap state synced to kind: ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME} (config.yaml) and ${PROXMOX_BOOTSTRAP_SECRET_NAME} (legacy combined) when the management cluster is reachable; clusterctl uses a temp file for the CLI only."
    fi
  else
    log "Bootstrap state synced to kind: ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME} (config.yaml), ${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME} + ${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} + ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME} when the management cluster is reachable; clusterctl uses a temp file for the CLI only."
  fi
}

write_csi_config_if_missing() {
  refresh_derived_identity_token_ids

  [[ -n "${PROXMOX_CSI_CONFIG:-}" ]] || return 0

  if [[ -f "$PROXMOX_CSI_CONFIG" ]]; then
    return 0
  fi

  persist_local_secrets || return 0

  PROXMOX_CSI_URL="${PROXMOX_CSI_URL:-$(proxmox_api_json_url)}"

  if [[ -z "$PROXMOX_CSI_TOKEN_ID" || -z "$PROXMOX_CSI_TOKEN_SECRET" || -z "$PROXMOX_REGION" ]]; then
    return 0
  fi

  cat > "$PROXMOX_CSI_CONFIG" <<EOF
config:
  clusters:
    - url: ${PROXMOX_CSI_URL}
      insecure: ${PROXMOX_CSI_INSECURE}
      token_id: "${PROXMOX_CSI_TOKEN_ID}"
      token_secret: "${PROXMOX_CSI_TOKEN_SECRET}"
      region: "${PROXMOX_REGION}"

storageClass:
  - name: ${PROXMOX_CSI_STORAGE_CLASS_NAME}
    storage: ${PROXMOX_CSI_STORAGE}
    reclaimPolicy: ${PROXMOX_CSI_RECLAIM_POLICY}
    fstype: ${PROXMOX_CSI_FSTYPE}
    annotations:
      storageclass.kubernetes.io/is-default-class: "${PROXMOX_CSI_DEFAULT_CLASS}"
EOF
  log "Generated ${PROXMOX_CSI_CONFIG}."
}

# Non-secret bootstrap state: not API token fields, not workload CAPI manifest (workload.yaml).
# PROXMOX_CSI_* driver settings (URL, user id, chart, storage, …) are stored in the CSI kind Secret, not here — see PROXMOX_BOOTSTRAP_CSI_SECRET_NAME.
# Values are read from the **current shell** (including defaults from script init); Python os.environ is not
# used, because most defaults are not exported and would appear empty.
_get_all_bootstrap_variables_as_yaml() {
  local _n
  local -a _bootstrap_cfg_snapshot_vars=(
    KIND_VERSION KUBECTL_VERSION CLUSTERCTL_VERSION
    OPENTOFU_VERSION
    CILIUM_CLI_VERSION CILIUM_VERSION CILIUM_WAIT_DURATION
    CILIUM_INGRESS CILIUM_KUBE_PROXY_REPLACEMENT
    CILIUM_LB_IPAM CILIUM_LB_IPAM_POOL_CIDR CILIUM_LB_IPAM_POOL_START CILIUM_LB_IPAM_POOL_STOP CILIUM_LB_IPAM_POOL_NAME
    CILIUM_HUBBLE CILIUM_HUBBLE_UI
    CILIUM_IPAM_CLUSTER_POOL_IPV4 CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE
    CILIUM_GATEWAY_API_ENABLED
    ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS
    CAPMOX_VERSION
    IPAM_IMAGE
    ARGOCD_VERSION
    WORKLOAD_GITOPS_MODE WORKLOAD_APP_OF_APPS_GIT_URL WORKLOAD_APP_OF_APPS_GIT_PATH WORKLOAD_APP_OF_APPS_GIT_REF
    PROXMOX_CSI_CHART_VERSION
    KYVERNO_CHART_VERSION KYVERNO_CLI_VERSION
    CERT_MANAGER_CHART_VERSION CMCTL_VERSION
    CROSSPLANE_CHART_VERSION CNPG_CHART_VERSION
    EXTERNAL_SECRETS_CHART_VERSION INFISICAL_CHART_VERSION
    SPIRE_CHART_VERSION SPIRE_CRDS_CHART_VERSION
    OTEL_CHART_VERSION GRAFANA_CHART_VERSION VICTORIAMETRICS_CHART_VERSION
    BACKSTAGE_CHART_VERSION KEYCLOAK_CHART_VERSION
    KIND_CLUSTER_NAME CLUSTER_ID PROXMOX_URL PROXMOX_ADMIN_INSECURE
    PROXMOX_REGION PROXMOX_NODE PROXMOX_SOURCENODE
    PROXMOX_CLOUDINIT_STORAGE
    PROXMOX_TEMPLATE_ID PROXMOX_BRIDGE
    CONTROL_PLANE_ENDPOINT_IP CONTROL_PLANE_ENDPOINT_PORT NODE_IP_RANGES GATEWAY IP_PREFIX DNS_SERVERS ALLOWED_NODES VM_SSH_KEYS
    PROXMOX_BOOTSTRAP_CONFIG_FILE BOOTSTRAP_REGENERATE_CAPI_MANIFEST BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING
    CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV
    PROXMOX_CAPI_USER_ID PROXMOX_CAPI_TOKEN_PREFIX PROXMOX_MEMORY_ADJUSTMENT
    CONTROL_PLANE_BOOT_VOLUME_DEVICE CONTROL_PLANE_BOOT_VOLUME_SIZE CONTROL_PLANE_NUM_SOCKETS CONTROL_PLANE_NUM_CORES CONTROL_PLANE_MEMORY_MIB
    WORKER_BOOT_VOLUME_DEVICE WORKER_BOOT_VOLUME_SIZE WORKER_NUM_SOCKETS WORKER_NUM_CORES WORKER_MEMORY_MIB
    WORKLOAD_CLUSTER_NAME WORKLOAD_CLUSTER_NAMESPACE WORKLOAD_CILIUM_CLUSTER_ID WORKLOAD_KUBERNETES_VERSION
    CONTROL_PLANE_MACHINE_COUNT WORKER_MACHINE_COUNT
    ARGOCD_ENABLED WORKLOAD_ARGOCD_ENABLED ARGOCD_SERVER_INSECURE WORKLOAD_ARGOCD_NAMESPACE \
    ARGOCD_OPERATOR_VERSION \
    ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED
    ENABLE_METRICS_SERVER ENABLE_WORKLOAD_METRICS_SERVER WORKLOAD_METRICS_SERVER_INSECURE_TLS
    METRICS_SERVER_MANIFEST_URL METRICS_SERVER_GIT_CHART_TAG
    PROXMOX_CSI_ENABLED KYVERNO_ENABLED CERT_MANAGER_ENABLED CROSSPLANE_ENABLED CNPG_ENABLED
    VICTORIAMETRICS_ENABLED EXTERNAL_SECRETS_ENABLED INFISICAL_OPERATOR_ENABLED SPIRE_ENABLED
    OTEL_ENABLED GRAFANA_ENABLED BACKSTAGE_ENABLED KEYCLOAK_ENABLED
    EXP_CLUSTER_RESOURCE_SET CLUSTER_TOPOLOGY EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION
    CLUSTER_SET_ID PROXMOX_IDENTITY_SUFFIX
    PROXMOX_CSI_SMOKE_ENABLED
    ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF
  )
  {
    for _n in "${_bootstrap_cfg_snapshot_vars[@]}"; do
      case "$_n" in
        PROXMOX_TOKEN|PROXMOX_SECRET|PROXMOX_CSI_TOKEN_ID|PROXMOX_CSI_TOKEN_SECRET|PROXMOX_ADMIN_TOKEN) continue ;;
        *) printf '%s\0' "$_n" "${!_n-}" ;;
      esac
    done
  } | python3 -c '
import json, sys
b = sys.stdin.buffer.read()
parts = b.split(b"\0")
out = []
i = 0
while i + 1 < len(parts):
    k = parts[i].decode("utf-8", "replace")
    v = parts[i + 1].decode("utf-8", "replace")
    if k:
        out.append("%s: %s" % (k, json.dumps(v)))
    i += 2
print("\n".join(out) + ("\n" if out else ""))
'
}

apply_bootstrap_config_to_management_cluster() {
  local ctx config_yaml tmpf
  local cfg_key="${PROXMOX_BOOTSTRAP_CONFIG_SECRET_KEY:-config.yaml}"
  ctx="$(_resolve_bootstrap_kubectl_context 2>/dev/null)" || {
    warn "Skipping bootstrap config Secret apply — no kind management context in kubeconfig (set KIND_CLUSTER_NAME / --kind-cluster-name or kind export kubeconfig)."
    return 0
  }
  config_yaml="$(
    {
      cat <<EONOTICE
# config.yaml — non-secret bootstrap state only. API token secrets are NEVER stored in this file.
# Kind Secrets in ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE} (use kubectl get secret -n ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}):
#   - ${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME}  — CAPI/clusterctl: PROXMOX_TOKEN, PROXMOX_SECRET, …
#   - ${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME}     — all CSI: PROXMOX_CSI_URL, PROXMOX_CSI_USER_ID, tokens, chart/storage/smoke, …
#   - ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME}   — Terraform PVE admin (data key ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY})
#   Legacy: if PROXMOX_BOOTSTRAP_SECRET_NAME is set, CAPI+CSI may share that Secret.
# The snapshot below does NOT list PROXMOX_CSI_* (except PROXMOX_CSI_ENABLED as a high-level flag) — use the CSI Secret for driver settings.
# VM_SSH_KEYS holds workload SSH *public* keys (comma-separated); they are not API secrets but are persisted for reproducible clusterctl runs.

EONOTICE
      _get_all_bootstrap_variables_as_yaml
    }
  )"
  tmpf="$(mktemp "${TMPDIR:-/tmp}/bootstrap-cfg-XXXXXX.yaml")"
  bootstrap_register_exit_trap
  printf '%s' "$config_yaml" > "$tmpf"

  kubectl --context "$ctx" create namespace "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" \
    --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - >/dev/null

  kubectl --context "$ctx" -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" \
    create secret generic "$PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME" \
    --from-file="${cfg_key}=$tmpf" \
    --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - \
    || { rm -f "$tmpf"; die "Failed to apply ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME} on management cluster."; }
  rm -f "$tmpf"

  log "Updated bootstrap config Secret ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME} on ${ctx} (key ${cfg_key}: non-secret snapshot + file header; API tokens are in capmox/csi/admin Secrets in that namespace — never in this file)."
}

try_load_bootstrap_config_from_kind() {
  local ctx exports
  command -v kubectl >/dev/null 2>&1 || return 0
  # As CLI is not yet parsed, align with a single kind current-context when default name has no context.
  ctx="$(_resolve_bootstrap_kubectl_context 2>/dev/null)" || return 0

  exports="$(kubectl --context "$ctx" get secret "$PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME" \
    -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null | python3 -c '
import json, re, shlex, sys, base64

def parse_bootstrap_map(text: str) -> dict:
    text = (text or "").lstrip()
    if not text:
        return {}
    if text.startswith("{"):
        return json.loads(text)
    out = {}
    for line in text.splitlines():
        s = line.split("#", 1)[0].rstrip()
        if not s or ":" not in s:
            continue
        m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$", s)
        if not m:
            continue
        k, val = m.group(1), m.group(2).strip()
        if len(val) >= 2 and ((val[0] == val[-1] == "\x22") or (val[0] == val[-1] == "\x27")):
            val = val[1:-1]
        out[k] = val
    return out

try:
    sec = json.load(sys.stdin)
except Exception:
    sys.exit(0)
d = sec.get("data") or {}
raw = d.get("config.yaml") or d.get("config.json")
if not raw:
    sys.exit(0)
text = base64.b64decode(raw).decode("utf-8", errors="replace")
try:
    config = parse_bootstrap_map(text)
except Exception as e:
    print(f"Python error loading bootstrap config: {e}", file=sys.stderr)
    raise
# Legacy: undifferentiated BOOT_* / NUM_* / MEMORY_MIB — map into WORKER_* when missing, then drop legacy keys.
_legacy_to_worker = [
    ("BOOT_VOLUME_DEVICE", "WORKER_BOOT_VOLUME_DEVICE"),
    ("BOOT_VOLUME_SIZE", "WORKER_BOOT_VOLUME_SIZE"),
    ("NUM_SOCKETS", "WORKER_NUM_SOCKETS"),
    ("NUM_CORES", "WORKER_NUM_CORES"),
    ("MEMORY_MIB", "WORKER_MEMORY_MIB"),
]
for _old, _new in _legacy_to_worker:
    if (config.get(_new) is None or str(config.get(_new) or "") == "") and config.get(
        _old
    ) not in (None, ""):
        config[_new] = config[_old]
    config.pop(_old, None)
# Legacy: TEMPLATE_VMID only — use PROXMOX_TEMPLATE_ID
if (config.get("PROXMOX_TEMPLATE_ID") is None or str(config.get("PROXMOX_TEMPLATE_ID") or "") == "") and str(
    config.get("TEMPLATE_VMID") or ""
) != "":
    config["PROXMOX_TEMPLATE_ID"] = config["TEMPLATE_VMID"]
config.pop("TEMPLATE_VMID", None)
for k, v in config.items():
    if v is not None and str(v) != "":
        print(f"export {k}={shlex.quote(str(v))}")
' 2>/dev/null || true)" || true

  if [[ -z "$exports" ]]; then
    warn "Bootstrap config secret not found or empty on ${ctx}. Will use env/CLI and save config after kind is up."
    return 0
  fi

  eval "$exports"
  log "Loaded bootstrap configuration from Secret on ${ctx}."
}

# After KIND_CLUSTER_NAME and kubeconfig are known: merge from ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME} (config.yaml).
# Snapshot keys (same set as the YAML pushed by apply_bootstrap_config_to_management_cluster) are overlaid from
# the in-cluster Secret so k9s edits are not clobbered by script defaults. Keys passed explicitly on the CLI
# (NODE_IP_RANGES_EXPLICIT, GATEWAY_EXPLICIT, …) are not overwritten. Other keys: fill only if env is empty.
# Safe with set -e (failed kubectl → || true).
merge_proxmox_bootstrap_secrets_from_kind() {
  local ctx config_body cred_json cred_json_capmox cred_json_csi exports
  command -v kubectl >/dev/null 2>&1 || return 0
  ctx="$(_resolve_bootstrap_kubectl_context 2>/dev/null)" || return 0

  # 1) config.yaml (or legacy config.json)
  config_body="$(kubectl --context "$ctx" get secret "$PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME" \
    -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null | python3 -c '
import json, re, sys, base64, os, shlex
try:
    sec = json.load(sys.stdin)
except Exception:
    sys.exit(0)
d = sec.get("data") or {}
raw = d.get("config.yaml") or d.get("config.json")
if not raw:
    sys.exit(0)
text = base64.b64decode(raw).decode("utf-8", errors="replace")
if not text.strip():
    sys.exit(0)
if text.lstrip().startswith("{"):
    cfg = json.loads(text)
else:
    cfg = {}
    for line in text.splitlines():
        s = line.split("#", 1)[0].rstrip()
        if not s or ":" not in s:
            continue
        m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$", s)
        if not m:
            continue
        k, val = m.group(1), m.group(2).strip()
        if len(val) >= 2 and val[0] in "\"\x27" and val[-1] == val[0]:
            val = val[1:-1]
        cfg[k] = val
# Legacy: undifferentiated VM sizing keys — copy into WORKER_* when only legacy present, do not export legacy keys.
_legacy_to_worker = [
    ("BOOT_VOLUME_DEVICE", "WORKER_BOOT_VOLUME_DEVICE"),
    ("BOOT_VOLUME_SIZE", "WORKER_BOOT_VOLUME_SIZE"),
    ("NUM_SOCKETS", "WORKER_NUM_SOCKETS"),
    ("NUM_CORES", "WORKER_NUM_CORES"),
    ("MEMORY_MIB", "WORKER_MEMORY_MIB"),
]
for _old, _new in _legacy_to_worker:
    if str(cfg.get(_new) or "") == "" and str(cfg.get(_old) or "") != "":
        cfg[_new] = cfg[_old]
    cfg.pop(_old, None)
# Legacy: TEMPLATE_VMID only — use PROXMOX_TEMPLATE_ID
if str(cfg.get("PROXMOX_TEMPLATE_ID") or "") == "" and str(cfg.get("TEMPLATE_VMID") or "") != "":
    cfg["PROXMOX_TEMPLATE_ID"] = cfg["TEMPLATE_VMID"]
cfg.pop("TEMPLATE_VMID", None)
# See _get_all_bootstrap_variables_as_yaml — keys that round-trip in config.yaml; in-cluster value wins (unless
# a matching *_EXPLICIT flag is set for the same run).
SNAPSHOT = frozenset(
    {
        "KIND_VERSION", "KUBECTL_VERSION", "CLUSTERCTL_VERSION",
        "CILIUM_CLI_VERSION", "CILIUM_VERSION", "CILIUM_WAIT_DURATION",
        "CILIUM_INGRESS", "CILIUM_KUBE_PROXY_REPLACEMENT",
        "CILIUM_LB_IPAM", "CILIUM_LB_IPAM_POOL_CIDR", "CILIUM_LB_IPAM_POOL_START", "CILIUM_LB_IPAM_POOL_STOP", "CILIUM_LB_IPAM_POOL_NAME",
        "CILIUM_HUBBLE", "CILIUM_HUBBLE_UI",
        "CILIUM_IPAM_CLUSTER_POOL_IPV4", "CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE",
        "CILIUM_GATEWAY_API_ENABLED",
        "ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS",
        "CAPMOX_VERSION",
        "KIND_CLUSTER_NAME", "CLUSTER_ID", "PROXMOX_URL", "PROXMOX_ADMIN_INSECURE",
        "PROXMOX_REGION", "PROXMOX_NODE", "PROXMOX_SOURCENODE",
        "PROXMOX_CLOUDINIT_STORAGE",
        "PROXMOX_TEMPLATE_ID", "PROXMOX_BRIDGE",
        "CONTROL_PLANE_ENDPOINT_IP", "CONTROL_PLANE_ENDPOINT_PORT", "NODE_IP_RANGES", "GATEWAY", "IP_PREFIX", "DNS_SERVERS", "ALLOWED_NODES", "VM_SSH_KEYS",
        "PROXMOX_BOOTSTRAP_CONFIG_FILE", "BOOTSTRAP_REGENERATE_CAPI_MANIFEST", "BOOTSTRAP_SKIP_IMMUTABLE_MANIFEST_WARNING",
        "CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV",
        "PROXMOX_CAPI_USER_ID", "PROXMOX_CAPI_TOKEN_PREFIX", "PROXMOX_MEMORY_ADJUSTMENT",
        "CONTROL_PLANE_BOOT_VOLUME_DEVICE", "CONTROL_PLANE_BOOT_VOLUME_SIZE", "CONTROL_PLANE_NUM_SOCKETS", "CONTROL_PLANE_NUM_CORES", "CONTROL_PLANE_MEMORY_MIB",
        "WORKER_BOOT_VOLUME_DEVICE", "WORKER_BOOT_VOLUME_SIZE", "WORKER_NUM_SOCKETS", "WORKER_NUM_CORES", "WORKER_MEMORY_MIB",
        "WORKLOAD_CLUSTER_NAME", "WORKLOAD_CLUSTER_NAMESPACE", "WORKLOAD_CILIUM_CLUSTER_ID", "WORKLOAD_KUBERNETES_VERSION",
        "CONTROL_PLANE_MACHINE_COUNT", "WORKER_MACHINE_COUNT",
        "ARGOCD_ENABLED", "WORKLOAD_ARGOCD_ENABLED", "ARGOCD_SERVER_INSECURE", "WORKLOAD_ARGOCD_NAMESPACE", "ARGOCD_OPERATOR_VERSION",
        "ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED", "ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED",
        "ENABLE_METRICS_SERVER", "ENABLE_WORKLOAD_METRICS_SERVER", "WORKLOAD_METRICS_SERVER_INSECURE_TLS",
        "METRICS_SERVER_MANIFEST_URL", "METRICS_SERVER_GIT_CHART_TAG",
        "PROXMOX_CSI_ENABLED", "PROXMOX_CSI_SMOKE_ENABLED",
        "ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED", "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL", "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH", "ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF",
        "KYVERNO_ENABLED", "CERT_MANAGER_ENABLED", "CROSSPLANE_ENABLED", "CNPG_ENABLED",
        "VICTORIAMETRICS_ENABLED", "EXTERNAL_SECRETS_ENABLED", "INFISICAL_OPERATOR_ENABLED", "SPIRE_ENABLED",
        "OTEL_ENABLED", "GRAFANA_ENABLED", "BACKSTAGE_ENABLED", "KEYCLOAK_ENABLED",
        "EXP_CLUSTER_RESOURCE_SET", "CLUSTER_TOPOLOGY", "EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION",
        "CLUSTER_SET_ID", "PROXMOX_IDENTITY_SUFFIX",
        "OPENTOFU_VERSION",
        "IPAM_IMAGE",
        "ARGOCD_VERSION",
        "WORKLOAD_GITOPS_MODE", "WORKLOAD_APP_OF_APPS_GIT_URL", "WORKLOAD_APP_OF_APPS_GIT_PATH", "WORKLOAD_APP_OF_APPS_GIT_REF",
        "PROXMOX_CSI_CHART_VERSION",
        "KYVERNO_CHART_VERSION", "KYVERNO_CLI_VERSION",
        "CERT_MANAGER_CHART_VERSION", "CMCTL_VERSION",
        "CROSSPLANE_CHART_VERSION", "CNPG_CHART_VERSION",
        "EXTERNAL_SECRETS_CHART_VERSION", "INFISICAL_CHART_VERSION",
        "SPIRE_CHART_VERSION", "SPIRE_CRDS_CHART_VERSION",
        "OTEL_CHART_VERSION", "GRAFANA_CHART_VERSION", "VICTORIAMETRICS_CHART_VERSION",
        "BACKSTAGE_CHART_VERSION", "KEYCLOAK_CHART_VERSION",
    }
)
# CLI can lock a key: do not take that key from the in-cluster Secret in this run.
EXPLICIT = {
    "NODE_IP_RANGES": "NODE_IP_RANGES_EXPLICIT",
    "GATEWAY": "GATEWAY_EXPLICIT",
    "IP_PREFIX": "IP_PREFIX_EXPLICIT",
    "DNS_SERVERS": "DNS_SERVERS_EXPLICIT",
    "ALLOWED_NODES": "ALLOWED_NODES_EXPLICIT",
    "WORKLOAD_CLUSTER_NAME": "WORKLOAD_CLUSTER_NAME_EXPLICIT",
    "WORKLOAD_CLUSTER_NAMESPACE": "WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT",
}
def is_explicit_set(ex: str) -> bool:
    v = (os.environ.get(ex) or "").strip().lower()
    if "workload_cluster" in ex.lower():
        return v in ("1", "true", "yes", "y")
    return v in ("1", "true", "yes", "y", "on")
def should_export_key(k: str) -> bool:
    exn = EXPLICIT.get(k)
    if exn and is_explicit_set(exn):
        return False
    return k in SNAPSHOT
for k, v in cfg.items():
    if v is None or str(v) == "":
        continue
    exn = EXPLICIT.get(k)
    if exn and is_explicit_set(exn):
        continue
    if k in SNAPSHOT:
        print("export " + k + "=" + shlex.quote(str(v)))
    else:
        if os.environ.get(k, "") == "":
            print("export " + k + "=" + shlex.quote(str(v)))
' 2>/dev/null || true)" || true
  if [[ -n "$config_body" ]]; then
    # shellcheck disable=SC2086
    eval "$config_body"
    log "Merged bootstrap state from ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME} (config.yaml: snapshot keys overlay in-cluster; CLI --*-explicit take precedence) on ${ctx}."
    PROXMOX_BOOTSTRAP_KIND_SECRET_USED=true
  fi

  # 2) CAPI + CSI secrets: legacy single Secret, or capmox + csi (fallback: old proxmox-bootstrap-credentials)
  cred_json=""
  cred_json_capmox=""
  cred_json_csi=""
  if [[ -n "${PROXMOX_BOOTSTRAP_SECRET_NAME:-}" ]]; then
    cred_json="$(kubectl --context "$ctx" get secret "$PROXMOX_BOOTSTRAP_SECRET_NAME" \
      -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null)" || true
  else
    cred_json_capmox="$(kubectl --context "$ctx" get secret "$PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME" \
      -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null)" || true
    cred_json_csi="$(kubectl --context "$ctx" get secret "$PROXMOX_BOOTSTRAP_CSI_SECRET_NAME" \
      -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null)" || true
    if [[ -z "$cred_json_capmox" && -z "$cred_json_csi" ]]; then
      cred_json="$(kubectl --context "$ctx" get secret "proxmox-bootstrap-credentials" \
        -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null)" || true
    fi
  fi
  if [[ -n "${cred_json:-}" ]]; then
    exports="$(echo "$cred_json" | PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY="${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY:-proxmox-admin.yaml}" \
      python3 -c '
import json, sys, os, base64, shlex, re
try:
    sec = json.load(sys.stdin)
except Exception:
    sys.exit(0)
data = {}
if sec.get("data"):
    for k, v in sec["data"].items():
        data[k] = base64.b64decode(v).decode("utf-8", errors="replace")
if not data:
    sys.exit(0)
ak = os.environ.get("PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY", "proxmox-admin.yaml")
if ak in data and data[ak] and str(data[ak]).strip():
    ytxt = str(data[ak])
    for line in ytxt.splitlines():
        s = line.split("#", 1)[0].rstrip()
        if not s or ":" not in s:
            continue
        m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$", s)
        if not m:
            continue
        mk, mval = m.group(1), m.group(2).strip()
        if len(mval) >= 2 and mval[0] in "\"\x27" and mval[-1] == mval[0]:
            mval = mval[1:-1]
        if mval:
            data[mk] = mval
aliases = [
    ("url", "PROXMOX_URL"), ("token", "PROXMOX_TOKEN"), ("secret", "PROXMOX_SECRET"),
    ("capi_token_id", "PROXMOX_TOKEN"), ("capi_token_secret", "PROXMOX_SECRET"),
    ("csi_token_id", "PROXMOX_CSI_TOKEN_ID"), ("csi_token_secret", "PROXMOX_CSI_TOKEN_SECRET"),
    ("admin_username", "PROXMOX_ADMIN_USERNAME"), ("admin_token", "PROXMOX_ADMIN_TOKEN"),
]
for old, new in aliases:
    if old in data and new not in data:
        data[new] = data[old]
for k, v in data.items():
    if v is None or v == "":
        continue
    if not re.match(r"^[A-Z][A-Z0-9_]*$", k):
        continue
    if os.environ.get(k, "") == "":
        print("export " + k + "=" + shlex.quote(v))
' 2>/dev/null || true)" || true
    if [[ -n "$exports" ]]; then
      # shellcheck disable=SC2086
      eval "$exports"
      log "Filled unset values from cluster API secrets on ${ctx} (legacy or migration combined Secret)."
      PROXMOX_BOOTSTRAP_KIND_SECRET_USED=true
    fi
  fi
  if [[ -z "${PROXMOX_BOOTSTRAP_SECRET_NAME:-}" && -n "${cred_json_capmox:-}" ]]; then
    exports="$(echo "$cred_json_capmox" | PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY="${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY:-proxmox-admin.yaml}" \
      python3 -c '
import json, sys, os, base64, shlex, re
try:
    sec = json.load(sys.stdin)
except Exception:
    sys.exit(0)
data = {}
if sec.get("data"):
    for k, v in sec["data"].items():
        data[k] = base64.b64decode(v).decode("utf-8", errors="replace")
if not data:
    sys.exit(0)
ak = os.environ.get("PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY", "proxmox-admin.yaml")
if ak in data and data[ak] and str(data[ak]).strip():
    ytxt = str(data[ak])
    for line in ytxt.splitlines():
        s = line.split("#", 1)[0].rstrip()
        if not s or ":" not in s:
            continue
        m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$", s)
        if not m:
            continue
        mk, mval = m.group(1), m.group(2).strip()
        if len(mval) >= 2 and mval[0] in "\"\x27" and mval[-1] == mval[0]:
            mval = mval[1:-1]
        if mval:
            data[mk] = mval
aliases = [
    ("url", "PROXMOX_URL"), ("token", "PROXMOX_TOKEN"), ("secret", "PROXMOX_SECRET"),
    ("capi_token_id", "PROXMOX_TOKEN"), ("capi_token_secret", "PROXMOX_SECRET"),
    ("csi_token_id", "PROXMOX_CSI_TOKEN_ID"), ("csi_token_secret", "PROXMOX_CSI_TOKEN_SECRET"),
    ("admin_username", "PROXMOX_ADMIN_USERNAME"), ("admin_token", "PROXMOX_ADMIN_TOKEN"),
]
for old, new in aliases:
    if old in data and new not in data:
        data[new] = data[old]
for k, v in data.items():
    if v is None or v == "":
        continue
    if not re.match(r"^[A-Z][A-Z0-9_]*$", k):
        continue
    if os.environ.get(k, "") == "":
        print("export " + k + "=" + shlex.quote(v))
' 2>/dev/null || true)" || true
    if [[ -n "$exports" ]]; then
      # shellcheck disable=SC2086
      eval "$exports"
      log "Filled unset values from ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME} on ${ctx}."
      PROXMOX_BOOTSTRAP_KIND_SECRET_USED=true
    fi
  fi
  if [[ -z "${PROXMOX_BOOTSTRAP_SECRET_NAME:-}" && -n "${cred_json_csi:-}" ]]; then
    exports="$(echo "$cred_json_csi" | python3 -c '
import json, sys, os, base64, shlex, re
try:
    sec = json.load(sys.stdin)
except Exception:
    sys.exit(0)
data = {}
if sec.get("data"):
    for k, v in sec["data"].items():
        data[k] = base64.b64decode(v).decode("utf-8", errors="replace")
if not data:
    sys.exit(0)
aliases = [("url", "PROXMOX_URL"), ("csi_token_id", "PROXMOX_CSI_TOKEN_ID"), ("csi_token_secret", "PROXMOX_CSI_TOKEN_SECRET")]
for old, new in aliases:
    if old in data and new not in data:
        data[new] = data[old]
for k, v in data.items():
    if v is None or v == "":
        continue
    if not re.match(r"^[A-Z][A-Z0-9_]*$", k):
        continue
    if os.environ.get(k, "") == "":
        print("export " + k + "=" + shlex.quote(v))
' 2>/dev/null || true)" || true
    if [[ -n "$exports" ]]; then
      # shellcheck disable=SC2086
      eval "$exports"
      log "Filled unset values from ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} on ${ctx}."
      PROXMOX_BOOTSTRAP_KIND_SECRET_USED=true
    fi
  fi

  # 2b) Admin Secret (or skip if same as legacy single Secret already merged in 2, unless only yaml is present)
  if [[ -n "${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME:-}" ]] && { [[ -z "${PROXMOX_BOOTSTRAP_SECRET_NAME:-}" ]] || [[ "$PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME" != "$PROXMOX_BOOTSTRAP_SECRET_NAME" ]]; }; then
    cred_json="$(kubectl --context "$ctx" get secret "$PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME" \
      -n "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" -o json 2>/dev/null)" || true
    if [[ -n "$cred_json" ]]; then
      exports="$(echo "$cred_json" | PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY="${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY:-proxmox-admin.yaml}" \
        python3 -c '
import json, sys, os, base64, shlex, re
try:
    sec = json.load(sys.stdin)
except Exception:
    sys.exit(0)
data = {}
if sec.get("data"):
    for k, v in sec["data"].items():
        data[k] = base64.b64decode(v).decode("utf-8", errors="replace")
if not data:
    sys.exit(0)
ak = os.environ.get("PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY", "proxmox-admin.yaml")
if ak in data and data[ak] and str(data[ak]).strip():
    ytxt = str(data[ak])
    for line in ytxt.splitlines():
        s = line.split("#", 1)[0].rstrip()
        if not s or ":" not in s:
            continue
        m = re.match(r"^([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(.*)$", s)
        if not m:
            continue
        mk, mval = m.group(1), m.group(2).strip()
        if len(mval) >= 2 and mval[0] in "\"\x27" and mval[-1] == mval[0]:
            mval = mval[1:-1]
        if mval:
            data[mk] = mval
aliases = [
    ("url", "PROXMOX_URL"), ("admin_username", "PROXMOX_ADMIN_USERNAME"), ("admin_token", "PROXMOX_ADMIN_TOKEN"),
    ("insecure", "PROXMOX_ADMIN_INSECURE"),
]
for old, new in aliases:
    if old in data and new not in data:
        data[new] = data[old]
_admin_keys = ("PROXMOX_URL", "PROXMOX_ADMIN_USERNAME", "PROXMOX_ADMIN_TOKEN", "PROXMOX_ADMIN_INSECURE")
for k, v in data.items():
    if v is None or v == "":
        continue
    if k not in _admin_keys:
        continue
    if not re.match(r"^[A-Z][A-Z0-9_]*$", k):
        continue
    if os.environ.get(k, "") == "":
        print("export " + k + "=" + shlex.quote(v))
' 2>/dev/null || true)" || true
      if [[ -n "$exports" ]]; then
        # shellcheck disable=SC2086
        eval "$exports"
        log "Filled unset values from ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME} (Terraform / admin) on ${ctx}."
        PROXMOX_BOOTSTRAP_KIND_SECRET_USED=true
      fi
    fi
  fi

  # 3) CAPMOX CAPI client identity (same as manager Deployment) if still missing
  if [[ -z "${PROXMOX_URL:-}" || -z "${PROXMOX_TOKEN:-}" || -z "${PROXMOX_SECRET:-}" ]]; then
    cred_json="$(kubectl --context "$ctx" get secret "capmox-manager-credentials" -n capmox-system -o json 2>/dev/null)" || true
    if [[ -n "$cred_json" ]]; then
      exports="$(echo "$cred_json" | python3 -c '
import json, sys, os, base64, shlex
try:
    sec = json.load(sys.stdin)
except Exception:
    sys.exit(0)
data = {}
if sec.get("data"):
    for k, v in sec["data"].items():
        data[k] = base64.b64decode(v).decode("utf-8", errors="replace")
m = { "url": "PROXMOX_URL", "token": "PROXMOX_TOKEN", "secret": "PROXMOX_SECRET" }
for a, b in m.items():
    v = data.get(a)
    if v and not os.environ.get(b, ""):
        print("export " + b + "=" + shlex.quote(v))
' 2>/dev/null || true)" || true
      if [[ -n "$exports" ]]; then
        # shellcheck disable=SC2086
        eval "$exports"
        log "Filled unset CAPI Proxmox API values from capmox-system/capmox-manager-credentials on ${ctx}."
        PROXMOX_KIND_CAPMOX_CREDENTIALS_ACTIVE=true
      fi
    fi
  fi

  reapply_workload_git_defaults
  bootstrap_sync_capi_controller_images_to_clusterctl_version
}

# metrics-server on kind needs kubelet TLS workaround; safe to re-run (applies manifest once, patches once).
install_metrics_server_on_kind_management_cluster() {
  local ctx url args
  is_true "${ENABLE_METRICS_SERVER:-true}" || return 0

  ctx="kind-${KIND_CLUSTER_NAME}";
  (kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | contains_line "$ctx") || return 0

  url="${METRICS_SERVER_MANIFEST_URL:-https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml}"

  if ! kubectl --context "$ctx" get deployment metrics-server -n kube-system >/dev/null 2>&1; then
    log "Installing metrics-server on ${ctx} (kubectl top / HPA)..."
    curl -fsSL "$url" | kubectl --context "$ctx" apply -f - \
      || die "Failed to apply metrics-server manifest from ${url}"
  else
    log "metrics-server already installed on ${ctx}."
  fi

  args="$(kubectl --context "$ctx" get deploy metrics-server -n kube-system -o jsonpath='{.spec.template.spec.containers[0].args[*]}' 2>/dev/null || true)"
  if [[ "$args" != *kubelet-insecure-tls* ]]; then
    log "Patching metrics-server for kind kubelet access (--kubelet-insecure-tls)..."
    kubectl --context "$ctx" -n kube-system patch deployment metrics-server --type=json \
      -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}, {"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-preferred-address-types=InternalIP,Hostname"}]' \
      || die "Failed to patch metrics-server for kind."
  fi

  kubectl --context "$ctx" rollout status deployment/metrics-server -n kube-system --timeout=180s \
    || warn "metrics-server rollout not ready within 180s — kubectl top may fail until it stabilizes."
  log "metrics-server configured on ${ctx} (e.g. kubectl --context ${ctx} top nodes)."
}

# CAPI/Proxmox workload cluster: kubelet Resource Metrics API via direct kubectl (no Argo on the management kind cluster).
install_metrics_server_on_workload_cluster() {
  is_true "${ENABLE_WORKLOAD_METRICS_SERVER:-true}" || return 0

  local mgmt_kctx workload_kcfg url args
  mgmt_kctx="kind-${KIND_CLUSTER_NAME}"
  (kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | contains_line "$mgmt_kctx") || return 0
  if ! kubectl --context "$mgmt_kctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" &>/dev/null; then
    warn "workload metrics-server: ${WORKLOAD_CLUSTER_NAME}-kubeconfig not in ${WORKLOAD_CLUSTER_NAMESPACE} — skipping (cluster not up?)."
    return 0
  fi

  workload_kcfg="$(mktemp)"
  kubectl --context "$mgmt_kctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" \
    get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$workload_kcfg" \
    || { rm -f "$workload_kcfg"; return 0; }

  url="${METRICS_SERVER_MANIFEST_URL:-https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml}"
  if ! KUBECONFIG="$workload_kcfg" kubectl get deployment metrics-server -n kube-system &>/dev/null; then
    log "Installing metrics-server on CAPI workload cluster ${WORKLOAD_CLUSTER_NAME} (separate from kind)..."
    curl -fsSL "$url" | KUBECONFIG="$workload_kcfg" kubectl apply -f - \
      || die "Failed to apply metrics-server on workload from ${url}"
  else
    log "metrics-server already on workload cluster ${WORKLOAD_CLUSTER_NAME}."
  fi

  if is_true "${WORKLOAD_METRICS_SERVER_INSECURE_TLS:-true}"; then
    args="$(KUBECONFIG="$workload_kcfg" kubectl get deploy metrics-server -n kube-system -o jsonpath='{.spec.template.spec.containers[0].args[*]}' 2>/dev/null || true)"
    if [[ "$args" != *kubelet-insecure-tls* ]]; then
      log "Patching workload metrics-server (--kubelet-insecure-tls) — set WORKLOAD_METRICS_SERVER_INSECURE_TLS=false if kubelet has proper certs."
      KUBECONFIG="$workload_kcfg" kubectl -n kube-system patch deployment metrics-server --type=json \
        -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}]' \
        || die "Failed to patch metrics-server on workload."
    fi
  fi

  KUBECONFIG="$workload_kcfg" kubectl rollout status deployment/metrics-server -n kube-system --timeout=180s \
    || warn "Workload metrics-server not ready in 180s — kubectl top on workload may still fail until it stabilizes."
  rm -f "$workload_kcfg"
  log "Workloads on ${WORKLOAD_CLUSTER_NAME} can use kubectl top / HPA (Resource Metrics) after metrics-server is healthy."
}

BOOTSTRAP_CLUSTERCTL_CONFIG_PATH=""
BOOTSTRAP_EPHEMERAL_CLUSTERCTL=""

bootstrap_cleanup_ephemeral_clusterctl_config() {
  [[ -n "${BOOTSTRAP_EPHEMERAL_CLUSTERCTL:-}" && -f "$BOOTSTRAP_EPHEMERAL_CLUSTERCTL" ]] \
    && rm -f "$BOOTSTRAP_EPHEMERAL_CLUSTERCTL"
}

bootstrap_register_exit_trap() {
  if ! is_true "$BOOTSTRAP_EXIT_TRAP_REGISTERED"; then
    trap 'bootstrap_exit_cleanup_all' EXIT
    BOOTSTRAP_EXIT_TRAP_REGISTERED=true
  fi
}

bootstrap_exit_cleanup_all() {
  bootstrap_cleanup_ephemeral_clusterctl_config
  if is_true "$BOOTSTRAP_KIND_CONFIG_EPHEMERAL"; then
    [[ -n "${BOOTSTRAP_EPHEMERAL_KIND_CONFIG:-}" && -f "$BOOTSTRAP_EPHEMERAL_KIND_CONFIG" ]] \
      && rm -f "$BOOTSTRAP_EPHEMERAL_KIND_CONFIG"
  fi
  if is_true "$BOOTSTRAP_CAPI_MANIFEST_EPHEMERAL"; then
    [[ -n "${CAPI_MANIFEST:-}" && -f "$CAPI_MANIFEST" ]] && rm -f "$CAPI_MANIFEST"
  fi
}

# Minimal kind config in $TMPDIR when KIND_CONFIG is unset.
bootstrap_ensure_kind_config() {
  if [[ -n "${KIND_CONFIG:-}" ]]; then
    require_file "$KIND_CONFIG"
    return 0
  fi
  bootstrap_register_exit_trap
  BOOTSTRAP_EPHEMERAL_KIND_CONFIG="$(mktemp "${TMPDIR:-/tmp}/bootstrap-capi-kind.XXXXXX.yaml")"
  cat > "$BOOTSTRAP_EPHEMERAL_KIND_CONFIG" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
EOF
  KIND_CONFIG="$BOOTSTRAP_EPHEMERAL_KIND_CONFIG"
  BOOTSTRAP_KIND_CONFIG_EPHEMERAL=true
  log "Using ephemeral kind config ${KIND_CONFIG} (set KIND_CONFIG or --kind-config to use a file on disk)."
}

# Workload CAPI manifest on kind: ConfigMap in CAPI_MANIFEST_CONFIGMAP_NAMESPACE; name is a stable short hash
# of kind + workload namespace + workload name (stays under DNS label length). No file under ~/.bootstrap-capi/ unless
# CAPI_MANIFEST or --capi-manifest is set to a local path.
capi_manifest_try_load_from_secret() {
  is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}" || return 0
  [[ -n "${CAPI_MANIFEST:-}" ]] || return 0
  local ctx ns name key
  ctx="kind-${KIND_CLUSTER_NAME}"
  ns="${CAPI_MANIFEST_SECRET_NAMESPACE}"
  name="${CAPI_MANIFEST_SECRET_NAME}"
  key="${CAPI_MANIFEST_SECRET_KEY}"
  command -v kubectl >/dev/null 2>&1 || return 0;
  (kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | contains_line "$ctx") || return 0
  kubectl --context "$ctx" get ns "$ns" &>/dev/null || return 0
  kubectl --context "$ctx" get secret -n "$ns" "$name" &>/dev/null || return 0
  if ! kubectl --context "$ctx" get secret -n "$ns" "$name" -o jsonpath="{.data['${key//./\\.}']}" 2>/dev/null | base64 -d > "${CAPI_MANIFEST}.load"; then
    rm -f "${CAPI_MANIFEST}.load"
    return 0
  fi
  if [[ ! -s "${CAPI_MANIFEST}.load" ]]; then
    rm -f "${CAPI_MANIFEST}.load"
    return 0
  fi
  mv -f "${CAPI_MANIFEST}.load" "$CAPI_MANIFEST"
  log "Loaded workload manifest from Secret ${ns}/${name} (key ${key}, context ${ctx})."
}

# Local config file used to decide whether the clusterctl-generated workload (workload.yaml / CAPI manifest) is stale.
bootstrap_resolved_local_config_yaml_path() {
  if [[ -n "${PROXMOX_BOOTSTRAP_CONFIG_FILE:-}" && -f "${PROXMOX_BOOTSTRAP_CONFIG_FILE}" ]]; then
    printf '%s' "${PROXMOX_BOOTSTRAP_CONFIG_FILE}"
    return 0
  fi
  if [[ -f "${PWD}/config.yaml" ]]; then
    printf '%s' "${PWD}/config.yaml"
    return 0
  fi
  return 0
}

# When the workload is stored in a kind Secret, we use a per-management-cluster stamp mtime (not the ephemeral CAPI file).
capi_bootstrap_workload_gencode_stamp_path() {
  local d
  d="${XDG_STATE_HOME:-$HOME/.local/state}/bootstrap-capi/gencode/${KIND_CLUSTER_NAME:-capi-provisioner}"
  printf '%s' "${d}/workload.last-clusterctl"
}

capi_bootstrap_touch_workload_gencode_stamp() {
  local s
  s="$(capi_bootstrap_workload_gencode_stamp_path)"
  if [[ -z "$s" ]]; then
    return 0
  fi
  if ! mkdir -p "$(dirname "$s")" 2>/dev/null; then
    return 0
  fi
  if command -v touch >/dev/null 2>&1; then
    touch -m "$s" 2>/dev/null || : > "$s" 2>/dev/null
  else
    : > "$s" 2>/dev/null
  fi
}

# Returns 0 (true) if the workload CAPI manifest should be re-generated (clusterctl) before re-apply.
capi_bootstrap_workload_clusterctl_is_stale() {
  is_true "${BOOTSTRAP_REGENERATE_CAPI_MANIFEST:-false}" && return 0
  local cfg
  cfg="$(bootstrap_resolved_local_config_yaml_path)"
  [[ -n "$cfg" && -f "$cfg" ]] || return 1

  if is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}"; then
    local st
    st="$(capi_bootstrap_workload_gencode_stamp_path)"
    if [[ ! -f "$st" ]]; then
      # New host: no stamp yet; if a local config.yaml exists, re-run clusterctl so we do not keep an
      # in-cluster workload manifest that was generated with different inputs.
      [[ -f "$cfg" ]] && return 0
      return 1
    fi
    if [[ "$cfg" -nt "$st" ]]; then
      return 0
    fi
    return 1
  fi
  if [[ -n "${CAPI_MANIFEST:-}" && -f "$CAPI_MANIFEST" && "$cfg" -nt "$CAPI_MANIFEST" ]]; then
    return 0
  fi
  return 1
}

capi_manifest_push_to_secret() {
  is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}" || return 0
  [[ -n "${CAPI_MANIFEST:-}" && -s "$CAPI_MANIFEST" ]] || return 0
  local ctx ns name key sz
  ctx="kind-${KIND_CLUSTER_NAME}"
  ns="${CAPI_MANIFEST_SECRET_NAMESPACE}"
  name="${CAPI_MANIFEST_SECRET_NAME}"
  key="${CAPI_MANIFEST_SECRET_KEY}"
  sz="$(wc -c < "$CAPI_MANIFEST" 2>/dev/null | tr -d ' ')"
  if [[ -z "$sz" ]]; then
    return 0
  fi
  if [[ "${sz}" -ge 1000000 ]]; then
    die "Workload manifest is ${sz} bytes (Secret data limit is ~1 MiB). Set CAPI_MANIFEST or use --capi-manifest with a file path, or reduce the manifest."
  fi
  kubectl --context "$ctx" create namespace "$ns" --dry-run=client -o yaml | kubectl --context "$ctx" apply -f - >/dev/null
  if ! kubectl --context "$ctx" create secret generic "$name" -n "$ns" \
    --from-file="${key}=${CAPI_MANIFEST}" \
    --dry-run=client -o yaml \
    | kubectl --context "$ctx" apply -f -; then
    die "Failed to store workload manifest in Secret ${ns}/${name} (key ${key})."
  fi
  kubectl --context "$ctx" -n "$ns" label secret "$name" "app.kubernetes.io/managed-by=bootstrap-capi" --overwrite >/dev/null 2>&1 || true
  log "Wrote workload manifest to Secret ${ns}/${name} (key ${key}). No persistent file under ~/.bootstrap-capi — debug via k9s or kubectl get secret -n ${ns} ${name} -o yaml."
  capi_bootstrap_touch_workload_gencode_stamp
}

capi_manifest_delete_secret() {
  is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}" || return 0
  local ctx ns name
  ctx="kind-${KIND_CLUSTER_NAME}"
  ns="${CAPI_MANIFEST_SECRET_NAMESPACE}"
  name="${CAPI_MANIFEST_SECRET_NAME}"
  command -v kubectl >/dev/null 2>&1 || return 0
  (kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | contains_line "$ctx") || return 0
  kubectl --context "$ctx" delete secret -n "$ns" "$name" --ignore-not-found >/dev/null 2>&1 || true
}

bootstrap_ensure_capi_manifest_path() {
  if [[ -n "${CAPI_MANIFEST:-}" ]]; then
    BOOTSTRAP_CAPI_USE_SECRET=false
    BOOTSTRAP_CAPI_MANIFEST_EPHEMERAL=false
    BOOTSTRAP_CAPI_MANIFEST_USER_SET=true
    return 0
  fi
  bootstrap_register_exit_trap
  BOOTSTRAP_CAPI_USE_SECRET=true
  CAPI_MANIFEST="$(mktemp "${TMPDIR:-/tmp}/capi-wl-XXXXXX.yaml")"
  : > "$CAPI_MANIFEST"
  BOOTSTRAP_CAPI_MANIFEST_EPHEMERAL=true
  BOOTSTRAP_CAPI_MANIFEST_USER_SET=false
  log "Workload CAPI manifest is stored in the management cluster as a Secret (namespace ${CAPI_MANIFEST_SECRET_NAMESPACE}, secret ${CAPI_MANIFEST_SECRET_NAME}, data key ${CAPI_MANIFEST_SECRET_KEY}) — this process only uses a temp file. Use --capi-manifest for a file on disk; inspect live YAML with k9s or kubectl."
}

# After interactive reuse of an existing Cluster CR, point the default manifest path at the chosen workload name.
bootstrap_refresh_default_capi_manifest_path() {
  is_true "$BOOTSTRAP_CAPI_MANIFEST_USER_SET" && return 0
  if is_true "$BOOTSTRAP_CAPI_USE_SECRET"; then
    [[ -n "${CAPI_MANIFEST:-}" && -f "$CAPI_MANIFEST" ]] && : > "$CAPI_MANIFEST"
    log "Workload selection updated; will load or generate for ${KIND_CLUSTER_NAME} ${WORKLOAD_CLUSTER_NAMESPACE:-default}/${WORKLOAD_CLUSTER_NAME} (Secret ${CAPI_MANIFEST_SECRET_NAME})."
    return 0
  fi
  die "bootstrap-capi: internal error — CAPI manifest path refresh with neither user file nor Secret mode."
}

# Offer existing cluster.cluster.x-k8s.io on the management kind cluster so reruns align WORKLOAD_* with real CRs.
maybe_interactive_select_workload_cluster_from_management() {
  local ctx raw choice i matched
  local -a c_ns=() c_name=()

  is_true "$FORCE" && return 0

  ctx="kind-${KIND_CLUSTER_NAME}"
  (kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | contains_line "$ctx") || return 0
  kubectl --context "$ctx" get crd clusters.cluster.x-k8s.io >/dev/null 2>&1 || return 0

  raw="$(kubectl --context "$ctx" get clusters.cluster.x-k8s.io -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"\t"}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  [[ -n "$raw" ]] || return 0

  while IFS=$'\t' read -r _ns _n || [[ -n "${_ns:-}" || -n "${_n:-}" ]]; do
    [[ -n "${_n:-}" ]] || continue
    c_ns+=("${_ns:-default}")
    c_name+=("$_n")
  done <<< "$(printf '%s\n' "$raw")"

  ((${#c_name[@]} > 0)) || return 0

  if ! bootstrap_can_interactive_prompt; then
    if ((${#c_name[@]} == 1)); then
      WORKLOAD_CLUSTER_NAMESPACE="${c_ns[0]}"
      WORKLOAD_CLUSTER_NAME="${c_name[0]}"
      bootstrap_refresh_default_capi_manifest_path
      log "Non-interactive session: using the only Cluster '${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME}' on ${ctx}."
    elif ((${#c_name[@]} > 1)); then
      matched=false
      for i in "${!c_name[@]}"; do
        if [[ "${c_name[$i]}" == "$WORKLOAD_CLUSTER_NAME" && "${c_ns[$i]}" == "${WORKLOAD_CLUSTER_NAMESPACE:-default}" ]]; then
          matched=true
          break
        fi
      done
      if is_true "$matched"; then
        log "Non-interactive session: WORKLOAD_CLUSTER_NAME/namespace match an existing Cluster; keeping '${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME}'."
      else
        warn "Non-interactive session: Cluster API Clusters on ${ctx}: $(for i in "${!c_name[@]}"; do printf '%s/%s ' "${c_ns[$i]}" "${c_name[$i]}"; done). Set WORKLOAD_CLUSTER_NAME and WORKLOAD_CLUSTER_NAMESPACE to match one, or use a terminal for the picker."
      fi
    fi
    return 0
  fi

  printf '\n\033[1;36mExisting Cluster API workload Cluster(s) on %s:\033[0m\n' "$ctx" >&2
  for i in "${!c_name[@]}"; do
    printf '  %d) namespace \033[1m%s\033[0m  cluster \033[1m%s\033[0m\n' "$((i + 1))" "${c_ns[$i]}" "${c_name[$i]}" >&2
  done
  if ((${#c_name[@]} == 1)); then
    printf '\033[1;33m[?]\033[0m Enter \033[1m1\033[0m to reuse that cluster (updates manifest path), or press Enter to keep WORKLOAD_CLUSTER_NAME=\033[1m%s\033[0m namespace=\033[1m%s\033[0m: ' \
      "${WORKLOAD_CLUSTER_NAME}" "${WORKLOAD_CLUSTER_NAMESPACE:-default}" >&2
  else
    printf '\033[1;33m[?]\033[0m Enter a number from \033[1m1\033[0m to \033[1m%d\033[0m to reuse that cluster (updates manifest path), or press Enter to keep WORKLOAD_CLUSTER_NAME=\033[1m%s\033[0m namespace=\033[1m%s\033[0m: ' \
      "${#c_name[@]}" "${WORKLOAD_CLUSTER_NAME}" "${WORKLOAD_CLUSTER_NAMESPACE:-default}" >&2
  fi
  bootstrap_read_line choice
  choice="$(bootstrap_normalize_numeric_menu_choice "$choice" "${#c_name[@]}")"
  if [[ -n "$choice" ]] && (( choice >= 1 && choice <= ${#c_name[@]} )); then
    i=$((choice - 1))
    WORKLOAD_CLUSTER_NAMESPACE="${c_ns[$i]}"
    WORKLOAD_CLUSTER_NAME="${c_name[$i]}"
    bootstrap_refresh_default_capi_manifest_path
    log "Using existing Cluster '${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME}' from ${ctx}."
  else
    log "Keeping WORKLOAD_CLUSTER_NAME='${WORKLOAD_CLUSTER_NAME}' namespace='${WORKLOAD_CLUSTER_NAMESPACE:-default}' (no Cluster selected from API)."
  fi
}

# Offer the kubectl current-context (if kind-*) or an existing kind cluster; --force skips.
maybe_interactive_select_kind_cluster() {
  is_true "$FORCE" && return 0
  command -v kind >/dev/null 2>&1 || return 0
  command -v kubectl >/dev/null 2>&1 || return 0

  local raw line cur_ctx from_ctx u choice
  local -a names=()

  cur_ctx="$(kubectl config current-context 2>/dev/null || true)"

  raw="$(kind get clusters 2>/dev/null || true)"
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    [[ -n "$line" ]] && names+=("$line")
  done <<< "$raw"

  # Non-interactive (CI, some IDE runs): avoid silently using default name when another kind cluster exists.
  if ! bootstrap_can_interactive_prompt; then
    if ((${#names[@]} == 1)); then
      KIND_CLUSTER_NAME="${names[0]}"
      log "Non-interactive session: using the only kind cluster on this host ('${KIND_CLUSTER_NAME}')."
    elif ((${#names[@]} > 1)) && [[ "$cur_ctx" =~ ^kind- ]]; then
      from_ctx="${cur_ctx#kind-}"
      if printf '%s\n' "${names[@]}" | grep -qx "$from_ctx"; then
        KIND_CLUSTER_NAME="$from_ctx"
        log "Non-interactive session: kubectl context ${cur_ctx} matches an existing kind cluster — using '${KIND_CLUSTER_NAME}'."
      else
        warn "Non-interactive session: multiple kind clusters (${names[*]}); kubectl context is ${cur_ctx}. Set KIND_CLUSTER_NAME or --kind-cluster-name (default '${KIND_CLUSTER_NAME}' may create a second cluster)."
      fi
    elif ((${#names[@]} > 1)); then
      warn "Non-interactive session: multiple kind clusters (${names[*]}). Set KIND_CLUSTER_NAME or --kind-cluster-name, or run in a real terminal for the interactive picker."
    fi
    return 0
  fi

  # 1) kubectl points at kind-<name> and that cluster is registered with kind → prefer for updates.
  if [[ "$cur_ctx" =~ ^kind- ]]; then
    from_ctx="${cur_ctx#kind-}"
    if printf '%s\n' "${names[@]}" | contains_line "$from_ctx"; then
      if [[ "$from_ctx" != "$KIND_CLUSTER_NAME" ]]; then
        printf '\n\033[1;36mkubectl\033[0m current-context is \033[1m%s\033[0m (kind cluster \033[1m%s\033[0m).\n' "$cur_ctx" "$from_ctx" >&2
        printf '\033[1;33m[?]\033[0m Use it for this run instead of creating or switching to %s=%s? [Y/n]: ' \
          "KIND_CLUSTER_NAME" "${KIND_CLUSTER_NAME}" >&2
        bootstrap_read_line u
        if [[ -z "$u" || "$u" =~ ^[Yy] ]]; then
          KIND_CLUSTER_NAME="$from_ctx"
          log "Using kind cluster '${KIND_CLUSTER_NAME}' from kubectl current-context."
          return 0
        fi
      else
        log "Using kind cluster '${KIND_CLUSTER_NAME}' from kubectl current-context."
        return 0
      fi
    fi
  fi

  # 2) kind lists no clusters but kubeconfig still has a kind-* context that responds → offer it (avoid duplicate default).
  if [[ ${#names[@]} -eq 0 && "$cur_ctx" =~ ^kind- ]]; then
    from_ctx="${cur_ctx#kind-}"
    if kubectl cluster-info --request-timeout=5s >/dev/null 2>&1; then
      printf '\n\033[1;33m[?]\033[0m No clusters reported by '\''kind get clusters'\'', but kubectl context is \033[1m%s\033[0m and the API answers.\n' "$cur_ctx" >&2
      printf '    Use kind cluster '\''%s'\'' for updates instead of %s=%s? [Y/n]: ' \
        "$from_ctx" "KIND_CLUSTER_NAME" "${KIND_CLUSTER_NAME}" >&2
      bootstrap_read_line u
      if [[ -z "$u" || "$u" =~ ^[Yy] ]]; then
        KIND_CLUSTER_NAME="$from_ctx"
        log "Using kind cluster '${KIND_CLUSTER_NAME}' from kubeconfig (cluster reachable)."
        return 0
      fi
    fi
    return 0
  fi

  ((${#names[@]} > 0)) || return 0

  printf '\n\033[1;36mExisting kind cluster(s) on this machine:\033[0m\n' >&2
  local i
  for i in "${!names[@]}"; do
    printf '  %d) %s\n' "$((i + 1))" "${names[$i]}" >&2
  done
  if [[ "$cur_ctx" =~ ^kind- ]]; then
    printf '  (kubectl context: %s)\n' "$cur_ctx" >&2
  fi
  if ((${#names[@]} == 1)); then
    printf '\033[1;33m[?]\033[0m Enter \033[1m1\033[0m to use that cluster, or press Enter to keep %s=%s (a new cluster may be created): ' \
      "KIND_CLUSTER_NAME" "${KIND_CLUSTER_NAME}" >&2
  else
    printf '\033[1;33m[?]\033[0m Enter a number from \033[1m1\033[0m to \033[1m%d\033[0m to use that cluster, or press Enter to keep %s=%s (a new cluster may be created): ' \
      "${#names[@]}" "KIND_CLUSTER_NAME" "${KIND_CLUSTER_NAME}" >&2
  fi
  bootstrap_read_line choice
  choice="$(bootstrap_normalize_numeric_menu_choice "$choice" "${#names[@]}")"
  if [[ -n "$choice" ]] && (( choice >= 1 && choice <= ${#names[@]} )); then
    KIND_CLUSTER_NAME="${names[$((choice - 1))]}"
    log "Using kind cluster '${KIND_CLUSTER_NAME}' (selected from existing clusters)."
  else
    log "Keeping KIND_CLUSTER_NAME='${KIND_CLUSTER_NAME}' (no existing cluster selected)."
  fi
}

# clusterctl requires a YAML path; use a temp file or an explicit local CLUSTERCTL_CFG (legacy).
# Do not substitute admin API tokens for CAPI (PVEAPIToken) — use kind Secrets or PROXMOX_TOKEN / PROXMOX_SECRET only.
bootstrap_sync_clusterctl_config_file() {
  local missing_cfg=()
  [[ -z "${PROXMOX_URL:-}" ]] && missing_cfg+=(PROXMOX_URL)
  [[ -z "${PROXMOX_TOKEN:-}" ]] && missing_cfg+=(PROXMOX_TOKEN)
  [[ -z "${PROXMOX_SECRET:-}" ]] && missing_cfg+=(PROXMOX_SECRET)

  if [[ ${#missing_cfg[@]} -gt 0 ]]; then
    die "bootstrap_sync_clusterctl_config_file: Proxmox credentials are not set. Missing: ${missing_cfg[*]}"
  fi

  if _clusterctl_cfg_file_present; then
    BOOTSTRAP_CLUSTERCTL_CONFIG_PATH="$CLUSTERCTL_CFG"
    return 0
  fi

  if [[ -z "$BOOTSTRAP_EPHEMERAL_CLUSTERCTL" ]]; then
    BOOTSTRAP_EPHEMERAL_CLUSTERCTL="$(mktemp "${TMPDIR:-/tmp}/bootstrap-capi-clusterctl.XXXXXX.yaml")"
    bootstrap_register_exit_trap
  fi
  cat > "$BOOTSTRAP_EPHEMERAL_CLUSTERCTL" <<EOF
PROXMOX_URL: "${PROXMOX_URL}"
PROXMOX_TOKEN: "${PROXMOX_TOKEN}"
PROXMOX_SECRET: "${PROXMOX_SECRET}"
EOF
  BOOTSTRAP_CLUSTERCTL_CONFIG_PATH="$BOOTSTRAP_EPHEMERAL_CLUSTERCTL"
  log "Using ephemeral clusterctl config under ${TMPDIR:-/tmp} (bootstrap state lives in kind Secret ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME}, not a local clusterctl path)."
}

apply_role_resource_overrides() {
  local manifest="$1"

  python3 - \
    "$manifest" \
    "$CONTROL_PLANE_BOOT_VOLUME_DEVICE" \
    "$CONTROL_PLANE_BOOT_VOLUME_SIZE" \
    "$CONTROL_PLANE_NUM_SOCKETS" \
    "$CONTROL_PLANE_NUM_CORES" \
    "$CONTROL_PLANE_MEMORY_MIB" \
    "$WORKER_BOOT_VOLUME_DEVICE" \
    "$WORKER_BOOT_VOLUME_SIZE" \
    "$WORKER_NUM_SOCKETS" \
    "$WORKER_NUM_CORES" \
    "$WORKER_MEMORY_MIB" \
    "$PROXMOX_MEMORY_ADJUSTMENT" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text()

cfg = {
    "cp_disk": sys.argv[2],
    "cp_size": sys.argv[3],
    "cp_sockets": sys.argv[4],
    "cp_cores": sys.argv[5],
    "cp_mem": sys.argv[6],
    "wk_disk": sys.argv[7],
    "wk_size": sys.argv[8],
    "wk_sockets": sys.argv[9],
    "wk_cores": sys.argv[10],
    "wk_mem": sys.argv[11],
}

def patch(block, disk, size, sockets, cores, mem):
  block = re.sub(r'(disk:\s*)[^\n]+', rf'\g<1>{disk}', block)
  block = re.sub(r'(sizeGb:\s*)[^\n]+', rf'\g<1>{size}', block)
  block = re.sub(r'(numSockets:\s*)[^\n]+', rf'\g<1>{sockets}', block)
  block = re.sub(r'(numCores:\s*)[^\n]+', rf'\g<1>{cores}', block)
  block = re.sub(r'(memoryMiB:\s*)[^\n]+', rf'\g<1>{mem}', block)
  return block

cp_pattern = r'(kind:\s*ProxmoxMachineTemplate\nmetadata:\n\s*name:\s*[^\n]*control-plane[^\n]*\n.*?)(?=\n---\n|\Z)'
wk_pattern = r'(kind:\s*ProxmoxMachineTemplate\nmetadata:\n\s*name:\s*[^\n]*worker[^\n]*\n.*?)(?=\n---\n|\Z)'

text = re.sub(cp_pattern, lambda m: patch(m.group(1), cfg["cp_disk"], cfg["cp_size"], cfg["cp_sockets"], cfg["cp_cores"], cfg["cp_mem"]), text, flags=re.S)
text = re.sub(wk_pattern, lambda m: patch(m.group(1), cfg["wk_disk"], cfg["wk_size"], cfg["wk_sockets"], cfg["wk_cores"], cfg["wk_mem"]), text, flags=re.S)

def scalar_to_yaml_list(match):
  indent = match.group(1)
  key = match.group(2)
  raw = match.group(3).strip().strip('"').strip("'")
  items = [v.strip() for v in raw.split(',') if v.strip()]
  lines = [f"{indent}{key}:"] + [f"{indent}- {item}" for item in items]
  return '\n'.join(lines)

# Convert comma-separated scalar values to YAML lists for these fields.
for field in ('allowedNodes', 'dnsServers', 'addresses'):
  text = re.sub(
    r'^( *)(${field}):\s*"?([^"\n\[]+)"?\s*$'.replace('${field}', re.escape(field)),
    scalar_to_yaml_list,
    text,
    flags=re.MULTILINE,
  )

# Inject schedulerHints.memoryAdjustment into the ProxmoxCluster spec so memory
# overcommit is permitted (0 = check disabled, 100 = no overcommit, 150 = 1.5x).
# Split by document to avoid cross-document regex matches that could inject into
# the CAPI Cluster object (strict decoding rejects unknown fields in v1beta2).
memory_adjustment = sys.argv[12]
docs = text.split('\n---\n')
for idx, doc in enumerate(docs):
  if re.search(r'^kind:\s*ProxmoxCluster\s*$', doc, re.MULTILINE) is None:
    continue
  if re.search(r'^apiVersion:\s*infrastructure\.cluster\.x-k8s\.io/', doc, re.MULTILINE) is None:
    continue
  if re.search(r'^\s{2}schedulerHints:', doc, re.MULTILINE):
    break
  spec_m = re.search(r'^(spec:\n(?:  .*\n)+)', doc, re.MULTILINE)
  if spec_m:
    old_block = spec_m.group(1)
    new_block = old_block.rstrip('\n') + f"\n  schedulerHints:\n    memoryAdjustment: {memory_adjustment}\n"
    docs[idx] = doc[:spec_m.start()] + new_block + doc[spec_m.end():]
  break
text = '\n---\n'.join(docs)

path.write_text(text)
PY
}

# Proxmox CSI schedules volumes using topology.kubernetes.io/region|zone on Nodes.
# topology.kubernetes.io/region = PROXMOX_TOPOLOGY_REGION or PROXMOX_REGION (same string
# as proxmox-csi Helm values clusters[].region). topology.kubernetes.io/zone = PVE node
# name (default PROXMOX_NODE, same as ProxmoxMachineTemplate sourceNode when using defaults).
patch_capi_manifest_proxmox_csi_topology_labels() {
  local manifest="$1"
  local region zone
  region="${PROXMOX_TOPOLOGY_REGION:-$PROXMOX_REGION}"
  zone="${PROXMOX_TOPOLOGY_ZONE:-$PROXMOX_NODE}"

  is_true "${PROXMOX_CSI_TOPOLOGY_LABELS:-true}" || return 0
  [[ -s "$manifest" ]] || return 0
  [[ -n "$region" && -n "$zone" ]] || {
    warn "Skipping Proxmox CSI topology node-labels: set PROXMOX_REGION and PROXMOX_NODE (region must match CSI clusters[].region; PROXMOX_TOPOLOGY_REGION / PROXMOX_TOPOLOGY_ZONE override defaults)."
    return 0
  }

  python3 - "$manifest" "$region" "$zone" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
region, zone = sys.argv[2], sys.argv[3]
text = path.read_text()

# Remove stale topology node-labels (e.g. sample manifest region=pve) so we re-inject
# from PROXMOX_REGION on every run.
text = re.sub(
    r"(?m)^[ \t]*- name: node-labels\s*\n"
    r"[ \t]*value:\s*"
    r"(?:\"[^\"]*topology\.kubernetes\.io/region=[^\"]*,\s*topology\.kubernetes\.io/zone=[^\"]*\"|"
    r"'[^']*topology\.kubernetes\.io/region=[^']*,\s*topology\.kubernetes\.io/zone=[^']*')\s*\n",
    "",
    text,
)

labels = (
    f"topology.kubernetes.io/region={region},"
    f"topology.kubernetes.io/zone={zone}"
)
# kubelet --node-labels value; quote for YAML (slashes in keys).
yaml_value = '"' + labels.replace("\\", "\\\\").replace('"', '\\"') + '"'

pat = re.compile(
    r"(?m)^([ \t]*)- name: provider-id\s*\n"
    r"([ \t]*)value:\s*"
    r'(?:"proxmox://\'\{\{ ds\.meta_data\.instance_id \}\}\'"|proxmox://\'\{\{ ds\.meta_data\.instance_id \}\}\')\s*\n'
)


def repl(m: re.Match) -> str:
    i1, i2 = m.group(1), m.group(2)
    return m.group(0) + f"{i1}- name: node-labels\n{i2}value: {yaml_value}\n"


new_text, n = pat.subn(repl, text)
if n == 0:
  sys.exit(0)
path.write_text(new_text)
PY
}

# When Cilium uses kube-proxy replacement, kubeadm must skip the kube-proxy addon.
# The kubernetes Service ClusterIP (10.96.0.1) is not routable until Cilium is up; the Cilium Helm values (HelmChartProxy/CAAPH)
# set k8sServiceHost/k8sServicePort from Cluster labels to CONTROL_PLANE_ENDPOINT_IP / CONTROL_PLANE_ENDPOINT_PORT.
# When kube-proxy is kept, ensure that skip is not present (avoids a cluster with no kube-proxy and KPR off).
# Ref: https://docs.cilium.io/en/stable/network/kubernetes/kubeproxy-free/
patch_capi_manifest_kubeadm_skip_kube_proxy_for_cilium() {
  local manifest="$1"
  local mode

  [[ -s "$manifest" ]] || return 0
  if cilium_needs_kube_proxy_replacement; then
    mode=add
  else
    mode=remove
  fi

  python3 - "$manifest" "$mode" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
mode = sys.argv[2]
text = path.read_text()
docs = text.split("\n---\n")


def add_skip(doc: str) -> str:
    if re.search(r"^kind:\s*KubeadmControlPlane\s*$", doc, re.MULTILINE) is None:
        return doc
    if re.search(
        r"^\s+-\s+addon/kube-proxy\s*$",
        doc,
        re.MULTILINE,
    ):
        return doc
    new_doc, n = re.subn(
        r"(^    initConfiguration:\n)(?!\s+skipPhases:)",
        r"\1      skipPhases:\n        - addon/kube-proxy\n",
        doc,
        count=1,
        flags=re.MULTILINE,
    )
    return new_doc if n else doc


def remove_skip(doc: str) -> str:
    if re.search(r"^kind:\s*KubeadmControlPlane\s*$", doc, re.MULTILINE) is None:
        return doc
    new_doc, n = re.subn(
        r"(^    initConfiguration:\n)\s+skipPhases:\n\s+-\s+addon/kube-proxy\n",
        r"\1",
        doc,
        count=1,
        flags=re.MULTILINE,
    )
    return new_doc if n else doc


if mode == "add":
    new_docs = [add_skip(d) for d in docs]
else:
    new_docs = [remove_skip(d) for d in docs]

new_text = "\n---\n".join(new_docs)
if new_text != text:
    path.write_text(new_text)
PY
}

# ProxmoxMachineTemplate.spec is immutable. Before apply, set metadata.name to <logical-stem>-t<sha256(spec)[:8]>
# and point KubeadmControlPlane / MachineDeployment infrastructureRef to the same (clusterctl still emits stem names).
patch_capi_manifest_proxmox_machine_template_spec_revisions() {
  local manifest="$1" out
  [[ -s "$manifest" ]] || return 0
  is_true "${CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV:-true}" || return 0

  out="$(
  python3 - "$manifest" <<'PY'
import hashlib
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text()


def stem_name(name: str) -> str:
  m = re.match(r"^(.*)-t[0-9a-f]{8}$", name)
  return m.group(1) if m else name


def set_pmt_metadata_name(doc: str, new_name: str) -> str:
  m_top = re.search(
    r"^(?:apiVersion:.*\n|kind:.*\n)*metadata:\n(.*?)^spec:\n",
    doc,
    re.DOTALL | re.MULTILINE,
  )
  if not m_top:
    return doc
  sub = m_top.group(1)
  mn = re.search(r"^  name:\s*(\S+)\s*$", sub, re.MULTILINE)
  if not mn:
    return doc
  new_sub = re.sub(
    r"^  name:.*$",
    f"  name: {new_name}",
    sub,
    count=1,
    flags=re.MULTILINE,
  )
  return doc[: m_top.start(1)] + new_sub + doc[m_top.end(1) :]


def patch() -> str:
  parts = [p.strip() + "\n" for p in text.split("\n---\n") if p.strip()]
  ref_map = {}
  new_parts = []

  for doc in parts:
    if re.search(r"^kind:\s*ProxmoxMachineTemplate\s*$", doc, re.MULTILINE) is None:
      new_parts.append(doc)
      continue
    if re.search(
      r"^apiVersion:\s*infrastructure\.cluster\.x-k8s\.io/",
      doc,
      re.MULTILINE,
    ) is None:
      new_parts.append(doc)
      continue
    m_top = re.search(
      r"^(?:apiVersion:.*\n|kind:.*\n)*metadata:\n(.*?)^spec:\n",
      doc,
      re.DOTALL | re.MULTILINE,
    )
    if not m_top:
      new_parts.append(doc)
      continue
    mn = re.search(r"^  name:\s*(\S+)\s*$", m_top.group(1), re.MULTILINE)
    if not mn:
      new_parts.append(doc)
      continue
    read_name = mn.group(1)
    spec_m = re.search(r"^spec:\n", doc, re.MULTILINE)
    if not spec_m:
      new_parts.append(doc)
      continue
    spec_body = doc[spec_m.start() :]
    h = hashlib.sha256(spec_body.encode("utf-8")).hexdigest()[:8]
    stem = stem_name(read_name)
    new_name = f"{stem}-t{h}"
    ref_map[stem] = new_name
    if read_name == new_name:
      new_parts.append(doc)
    else:
      new_parts.append(set_pmt_metadata_name(doc, new_name))

  out = "\n---\n".join(new_parts)
  for stem, newn in sorted(ref_map.items(), key=lambda x: -len(x[0])):
    if stem == newn:
      continue
    out = re.sub(
      r"^([ \t]*name:[ \t]*)" + re.escape(stem) + r"([ \t]*)$",
      r"\1" + newn + r"\2",
      out,
      flags=re.MULTILINE,
    )
  return out


new_text = patch()
if new_text == text:
  sys.exit(0)
path.write_text(new_text)
names = []
for d in new_text.split("\n---\n"):
  if re.search(r"^kind:\s*ProxmoxMachineTemplate\s*$", d, re.MULTILINE) and re.search(
    r"^apiVersion:\s*infrastructure\.cluster\.x-k8s\.io/", d, re.MULTILINE
  ):
    mt = re.search(
      r"^(?:apiVersion:.*\n|kind:.*\n)*metadata:\n(.*?)^spec:\n", d, re.DOTALL | re.MULTILINE
    )
    if mt:
      mn = re.search(r"^  name:\s*(\S+)\s*$", mt.group(1), re.MULTILINE)
      if mn:
        names.append(mn.group(1))
print("pmt=" + (",".join(names) if names else "0"), end="")
PY
  )" || die "CAPI ProxmoxMachineTemplate spec-revision patch failed (python3)."
  if [[ -n "$out" ]]; then
    log "CAPI manifest: ProxmoxMachineTemplate spec revision names (${out}). Old templates may remain; KCP/MD now reference the new name when spec changed (CAPI_PROXMOX_MACHINE_TEMPLATE_SPEC_REV)."
  fi
}

# When reusing a management cluster, ProxmoxMachineTemplates may already exist — use their clone hints
# if env/bootstrap Secret did not set template ID or source node.
try_fill_workload_manifest_inputs_from_management_cluster() {
  local ctx tid src_node
  command -v kubectl >/dev/null 2>&1 || return 0

  ctx="kind-${KIND_CLUSTER_NAME}"
  kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | grep -qx "$ctx" || return 0
  kubectl --context "$ctx" get crd proxmoxmachinetemplates.infrastructure.cluster.x-k8s.io >/dev/null 2>&1 || return 0

  if [[ -z "${PROXMOX_TEMPLATE_ID:-}" ]]; then
    tid="$(kubectl --context "$ctx" get proxmoxmachinetemplates.infrastructure.cluster.x-k8s.io -A \
      -o jsonpath='{range .items[*]}{.spec.template.spec.templateID}{"\n"}{end}' 2>/dev/null \
      | grep -E '^[0-9]+$' | head -1 || true)"
    if [[ -n "$tid" ]]; then
      PROXMOX_TEMPLATE_ID="$tid"
      log "Set PROXMOX_TEMPLATE_ID=${tid} from an existing ProxmoxMachineTemplate on ${ctx}."
    fi
  fi

  if [[ -z "${PROXMOX_NODE:-}" ]]; then
    src_node="$(kubectl --context "$ctx" get proxmoxmachinetemplates.infrastructure.cluster.x-k8s.io -A \
      -o jsonpath='{range .items[*]}{.spec.template.spec.sourceNode}{"\n"}{end}' 2>/dev/null \
      | grep -v '^[[:space:]]*$' | head -1 || true)"
    if [[ -n "$src_node" ]]; then
      PROXMOX_NODE="$src_node"
      if ! is_true "${ALLOWED_NODES_EXPLICIT:-false}"; then
        ALLOWED_NODES="${ALLOWED_NODES:-$PROXMOX_NODE}"
      fi
      log "Set PROXMOX_NODE from ProxmoxMachineTemplate sourceNode on ${ctx}."
    fi
  fi

  # Reuse network + DNS from the live ProxmoxCluster when a workload is selected (avoids re-passing --dns-servers / --gateway, etc.)
  # Note: this can overwrite NODE_IP_RANGES / GATEWAY / … from proxmox-bootstrap-config; the caller should run
  # merge_proxmox_bootstrap_secrets_from_kind again after this if the in-cluster config Secret is the source of truth.
  if [[ -n "${WORKLOAD_CLUSTER_NAME:-}" && -n "${WORKLOAD_CLUSTER_NAMESPACE:-}" ]] \
    && kubectl --context "$ctx" get crd proxmoxclusters.infrastructure.cluster.x-k8s.io >/dev/null 2>&1; then
    local cluster_json pc_name pc_json fill_line
    cluster_json="$(kubectl --context "$ctx" get cluster -n "$WORKLOAD_CLUSTER_NAMESPACE" "$WORKLOAD_CLUSTER_NAME" -o json 2>/dev/null || true)"
    if [[ -n "$cluster_json" ]]; then
      local inf_kind=""
      {
        read -r inf_kind
        read -r pc_name
      } < <(printf '%s' "$cluster_json" | python3 -c "
import json, sys
d = json.load(sys.stdin)
ref = (d.get('spec') or {}).get('infrastructureRef') or {}
print((ref.get('kind') or '').lower())
print(ref.get('name') or '')
" 2>/dev/null) || true
      if [[ -n "$inf_kind" && "$inf_kind" != "proxmoxcluster" ]]; then
        :
      else
        [[ -n "$pc_name" ]] || pc_name="$WORKLOAD_CLUSTER_NAME"
        pc_json=""
        if kubectl --context "$ctx" get proxmoxcluster -n "$WORKLOAD_CLUSTER_NAMESPACE" "$pc_name" -o json &>/dev/null; then
          pc_json="$(kubectl --context "$ctx" get proxmoxcluster -n "$WORKLOAD_CLUSTER_NAMESPACE" "$pc_name" -o json)"
        elif kubectl --context "$ctx" get proxmoxcluster -n "$WORKLOAD_CLUSTER_NAMESPACE" "$WORKLOAD_CLUSTER_NAME" -o json &>/dev/null; then
          pc_name="$WORKLOAD_CLUSTER_NAME"
          pc_json="$(kubectl --context "$ctx" get proxmoxcluster -n "$WORKLOAD_CLUSTER_NAMESPACE" "$pc_name" -o json)"
        fi
        if [[ -n "$pc_json" ]]; then
          while IFS= read -r fill_line; do
            [[ -n "$fill_line" ]] || continue
            case "$fill_line" in
              dns:*)
                if ! is_true "${DNS_SERVERS_EXPLICIT:-false}"; then
                  DNS_SERVERS="${fill_line#dns:}"
                  log "Set DNS_SERVERS from ProxmoxCluster ${WORKLOAD_CLUSTER_NAMESPACE}/${pc_name} (aligned with the running cluster)."
                fi
                ;;
              gateway:*)
                if ! is_true "${GATEWAY_EXPLICIT:-false}"; then
                  GATEWAY="${fill_line#gateway:}"
                  log "Set GATEWAY from ProxmoxCluster ${pc_name}."
                fi
                ;;
              ip_prefix:*)
                if ! is_true "${IP_PREFIX_EXPLICIT:-false}"; then
                  IP_PREFIX="${fill_line#ip_prefix:}"
                  log "Set IP_PREFIX from ProxmoxCluster ${pc_name}."
                fi
                ;;
              node_ranges:*)
                if ! is_true "${NODE_IP_RANGES_EXPLICIT:-false}"; then
                  NODE_IP_RANGES="${fill_line#node_ranges:}"
                  log "Set NODE_IP_RANGES from ProxmoxCluster ${pc_name}."
                fi
                ;;
              allowed:*)
                if ! is_true "${ALLOWED_NODES_EXPLICIT:-false}"; then
                  ALLOWED_NODES="${fill_line#allowed:}"
                  log "Set ALLOWED_NODES from ProxmoxCluster ${pc_name}."
                fi
                ;;
            esac
          done < <(export PC_JSON="$pc_json"; python3 <<'PY'
import json, os, sys
d = json.loads(os.environ["PC_JSON"])
spec = d.get("spec") or {}
ds = spec.get("dnsServers")
if isinstance(ds, list) and ds:
    print("dns:" + ",".join(str(x) for x in ds))
v4 = spec.get("ipv4Config")
if isinstance(v4, dict):
    g = v4.get("gateway")
    if g is not None and str(g).strip() != "":
        print("gateway:" + str(g).strip())
    p = v4.get("prefix")
    if p is not None:
        print("ip_prefix:" + str(p))
    a = v4.get("addresses")
    if isinstance(a, list) and a:
        print("node_ranges:" + ",".join(str(x) for x in a))
an = spec.get("allowedNodes")
if isinstance(an, list) and an:
    print("allowed:" + ",".join(str(x) for x in an))
PY
)
        fi
      fi
    fi
  fi
}

generate_workload_manifest_if_missing() {
  BOOTSTRAP_CLUSTERCTL_REGENERATED_MANIFEST=false
  if [[ -s "$CAPI_MANIFEST" ]]; then
    if capi_bootstrap_workload_clusterctl_is_stale; then
      log "Bootstrap config is newer than the last clusterctl-generated workload manifest (workload.yaml / CAPI YAML) — regenerating with clusterctl."
      : > "$CAPI_MANIFEST"
    else
      if is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}"; then
        log "Reusing existing workload manifest from the management Secret (use --purge to clear kind state, or set CAPI_MANIFEST / --capi-manifest for a local file; after editing config.yaml use --regenerate-capi-manifest if you only changed the in-cluster Secret)."
      else
        log "Reusing existing workload manifest ${CAPI_MANIFEST} (remove the file or use --purge to force clusterctl generate again; edit and save config.yaml / set PROXMOX_BOOTSTRAP_CONFIG_FILE so it is newer than this file to auto-regenerate)."
      fi
      return 0
    fi
  fi

  if [[ -f "$CAPI_MANIFEST" && ! -s "$CAPI_MANIFEST" ]]; then
    if is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}"; then
      warn "Ephemeral manifest file was empty; generating workload manifest with clusterctl."
    else
      warn "${CAPI_MANIFEST} exists but is empty; regenerating it."
    fi
  fi

  local missing_manifest_cfg=()
  [[ -z "$PROXMOX_URL" ]]          && missing_manifest_cfg+=(PROXMOX_URL)
  [[ -z "$PROXMOX_REGION" ]]       && missing_manifest_cfg+=(PROXMOX_REGION)
  [[ -z "$PROXMOX_NODE" ]]         && missing_manifest_cfg+=(PROXMOX_NODE)
  [[ -z "$PROXMOX_TEMPLATE_ID" ]]  && missing_manifest_cfg+=(PROXMOX_TEMPLATE_ID)

  if [[ ${#missing_manifest_cfg[@]} -gt 0 ]]; then
    warn "Missing workload manifest inputs: ${missing_manifest_cfg[*]}"
    if is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}"; then
      die "Set them as command-line options or environment variables before generating the workload cluster manifest."
    else
      die "Set them as command-line options or environment variables before generating ${CAPI_MANIFEST}."
    fi
  fi

  if is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}"; then
    log "Generating workload cluster manifest with clusterctl (Secret is updated after discover/label, before apply)..."
  else
    log "${CAPI_MANIFEST} not found — generating workload cluster manifest with clusterctl..."
  fi

  PROXMOX_CSI_URL="${PROXMOX_CSI_URL:-$(proxmox_api_json_url)}"

  # Ephemeral clusterctl config (TMPDIR) when no local CLUSTERCTL_CFG; required when we load manifest from kind
  # Secret then clear it for stale regen — e.g. workload-rollout / capi path never reached main bootstrap_sync.
  local ctl_cfg="${BOOTSTRAP_CLUSTERCTL_CONFIG_PATH:-}"
  [[ -z "$ctl_cfg" && -n "${CLUSTERCTL_CFG:-}" ]] && ctl_cfg="$CLUSTERCTL_CFG"
  if [[ -z "$ctl_cfg" || ! -f "$ctl_cfg" ]]; then
    bootstrap_sync_clusterctl_config_file
    ctl_cfg="${BOOTSTRAP_CLUSTERCTL_CONFIG_PATH:-}"
    [[ -z "$ctl_cfg" && -n "${CLUSTERCTL_CFG:-}" ]] && ctl_cfg="$CLUSTERCTL_CFG"
  fi
  [[ -n "$ctl_cfg" && -f "$ctl_cfg" ]] \
    || die "clusterctl config is not available after bootstrap_sync_clusterctl_config_file (set PROXMOX_URL, PROXMOX_TOKEN, PROXMOX_SECRET, or CLUSTERCTL_CFG)."

  # These variables should be loaded from the central config secret.
  BRIDGE="${BRIDGE:-$PROXMOX_BRIDGE}"
  PROXMOX_SOURCENODE="${PROXMOX_SOURCENODE:-$PROXMOX_NODE}"
  
  # If VM_SSH_KEYS is still empty, read this host's ~/.ssh/authorized_keys for clusterctl; set VM_SSH_KEYS in env or in config.yaml to pin keys and skip.
  if [[ -z "${VM_SSH_KEYS:-}" && -f "$HOME/.ssh/authorized_keys" ]]; then
    log "Loading SSH keys from local ~/.ssh/authorized_keys (override with VM_SSH_KEYS or config.yaml)..."
    VM_SSH_KEYS="$(grep -v '^\s*#' "$HOME/.ssh/authorized_keys" | grep -v '^\s*$' | paste -sd ',' - || true)"
  fi

  local tmp_manifest
  tmp_manifest="$(mktemp)"

  if ! PROXMOX_URL="$PROXMOX_URL" \
  PROXMOX_REGION="$PROXMOX_REGION" \
  PROXMOX_NODE="$PROXMOX_NODE" \
  PROXMOX_TEMPLATE_ID="$PROXMOX_TEMPLATE_ID" \
  TEMPLATE_VMID="${PROXMOX_TEMPLATE_ID}" \
  BRIDGE="$BRIDGE" \
  PROXMOX_SOURCENODE="$PROXMOX_SOURCENODE" \
  VM_SSH_KEYS="$VM_SSH_KEYS" \
  CONTROL_PLANE_ENDPOINT_IP="$CONTROL_PLANE_ENDPOINT_IP" \
  NODE_IP_RANGES="$NODE_IP_RANGES" \
  GATEWAY="$GATEWAY" \
  IP_PREFIX="$IP_PREFIX" \
  DNS_SERVERS="$DNS_SERVERS" \
  ALLOWED_NODES="$ALLOWED_NODES" \
  PROXMOX_CLOUDINIT_STORAGE="$PROXMOX_CLOUDINIT_STORAGE" \
  BOOT_VOLUME_DEVICE="$WORKER_BOOT_VOLUME_DEVICE" \
  BOOT_VOLUME_SIZE="$WORKER_BOOT_VOLUME_SIZE" \
  NUM_SOCKETS="$WORKER_NUM_SOCKETS" \
  NUM_CORES="$WORKER_NUM_CORES" \
  MEMORY_MIB="$WORKER_MEMORY_MIB" \
  clusterctl generate cluster "$WORKLOAD_CLUSTER_NAME" \
    --config "$ctl_cfg" \
    --kubernetes-version "$WORKLOAD_KUBERNETES_VERSION" \
    --control-plane-machine-count "$CONTROL_PLANE_MACHINE_COUNT" \
    --worker-machine-count "$WORKER_MACHINE_COUNT" \
    --infrastructure "$INFRA_PROVIDER" > "$tmp_manifest"; then
    rm -f "$tmp_manifest"
    die "clusterctl generate cluster failed. Verify required template variables in ${ctl_cfg}."
  fi

  if [[ ! -s "$tmp_manifest" ]]; then
    rm -f "$tmp_manifest"
    die "clusterctl generate cluster produced an empty manifest. Check template variables and provider templates."
  fi

  mv -f "$tmp_manifest" "$CAPI_MANIFEST"

  apply_role_resource_overrides "$CAPI_MANIFEST"

  sync_bootstrap_config_to_kind || true
  BOOTSTRAP_CLUSTERCTL_REGENERATED_MANIFEST=true

  if is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}"; then
    log "Generated workload cluster manifest (ephemeral file; pushed to the management Secret after discover/label, before apply)."
  else
    log "Generated ${CAPI_MANIFEST}."
  fi
}

discover_workload_cluster_identity() {
  local manifest="$1"
  local cluster_ident

  [[ -s "$manifest" ]] || die "Manifest ${manifest} is missing or empty. Regenerate it before continuing."

  cluster_ident="$(python3 - "$manifest" <<'PY'
import pathlib
import re
import sys

text = pathlib.Path(sys.argv[1]).read_text()
match = re.search(
    r'apiVersion:\s*cluster\.x-k8s\.io/[^\n]+\nkind:\s*Cluster\nmetadata:\n(?P<meta>(?:  .*(?:\n|$))+)',
    text,
)
if not match:
    raise SystemExit(1)

meta = match.group('meta')
name = re.search(r'^  name:\s*"?([^"\n]+)"?$', meta, re.M)
namespace = re.search(r'^  namespace:\s*"?([^"\n]+)"?$', meta, re.M)
if not name:
    raise SystemExit(1)

print(name.group(1))
print(namespace.group(1) if namespace else 'default')
PY
)" || die "Could not determine workload cluster name/namespace from ${manifest}."

  mapfile -t _cluster_ident <<<"$cluster_ident"
  WORKLOAD_CLUSTER_NAME="${_cluster_ident[0]}"
  WORKLOAD_CLUSTER_NAMESPACE="${_cluster_ident[1]:-default}"
  unset _cluster_ident
}

ensure_workload_cluster_label() {
  local manifest="$1"
  local cluster_name="$2"

  python3 - "$manifest" "$cluster_name" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
cluster_name = sys.argv[2]
original = path.read_text()
docs = original.split('\n---\n')

for index, doc in enumerate(docs):
    lines = doc.splitlines()
    if not any(line.strip() == 'kind: Cluster' for line in lines):
        continue
    if not any(line.startswith('apiVersion: cluster.x-k8s.io/') for line in lines):
        continue

    metadata_idx = next((i for i, line in enumerate(lines) if line.strip() == 'metadata:'), None)
    if metadata_idx is None:
        break

    block_end = len(lines)
    for i in range(metadata_idx + 1, len(lines)):
        if lines[i] and not lines[i].startswith(' '):
            block_end = i
            break

    labels_idx = next((i for i in range(metadata_idx + 1, block_end) if lines[i].strip() == 'labels:'), None)
    label_line = f'    cluster.x-k8s.io/cluster-name: "{cluster_name}"'

    if labels_idx is not None:
      existing = any(lines[i].strip().startswith('cluster.x-k8s.io/cluster-name:') for i in range(labels_idx + 1, block_end) if lines[i].startswith('    '))
      if not existing:
        insert_at = labels_idx + 1
        while insert_at < block_end and lines[insert_at].startswith('    '):
          insert_at += 1
        lines.insert(insert_at, label_line)
    else:
      insert_at = metadata_idx + 1
      while insert_at < block_end and lines[insert_at].startswith('  '):
        insert_at += 1
      lines.insert(insert_at, '  labels:')
      lines.insert(insert_at + 1, label_line)

    docs[index] = '\n'.join(lines)
    break

path.write_text('\n---\n'.join(docs) + ('\n' if original.endswith('\n') else ''))
PY
}

# Add labels the HelmChartProxy Cilium valuesTemplate reads (CAAPH Go templates) — must run on the
# workload Cluster in CAPI_MANIFEST after WORKLOAD_CILIUM_CLUSTER_ID is set.
patch_capi_cluster_caaph_helm_labels() {
  local manifest="${1:-$CAPI_MANIFEST}"
  [[ -s "$manifest" ]] || return 0
  refresh_derived_cilium_cluster_id
  python3 - "$manifest" <<'PY'
import pathlib, os, re, sys

path = pathlib.Path(sys.argv[1])
text = path.read_text()
e = {k: os.environ.get(k, "") for k in os.environ}
cid = (e.get("WORKLOAD_CILIUM_CLUSTER_ID") or "").strip()
host = (e.get("CONTROL_PLANE_ENDPOINT_IP") or "").strip()
port = (e.get("CONTROL_PLANE_ENDPOINT_PORT") or "6443").strip()

def quote(s: str) -> str:
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'

def inject(doc: str) -> str:
    if re.search(r"^kind:\s*Cluster\s*$", doc, re.MULTILINE) is None:
        return doc
    if re.search(r"^apiVersion:\s*cluster\.x-k8s\.io/", doc, re.MULTILINE) is None:
        return doc
    lines = doc.splitlines()
    meta = next((i for i, l in enumerate(lines) if l.strip() == "metadata:"), None)
    if meta is None:
        return doc
    end = len(lines)
    for i in range(meta + 1, len(lines)):
        if lines[i] and not lines[i].startswith(" "):
            end = i
            break
    li = next((i for i in range(meta + 1, end) if lines[i].strip() == "labels:"), None)
    to_add = [
        ("caaph", "enabled"),
        ("caaph.cilium.cluster-id", cid),
        ("caaph.cilium.k8s-service-host", host),
        ("caaph.cilium.k8s-service-port", port),
    ]
    def have(k: str) -> bool:
        pat = re.compile(r"^    " + re.escape(k) + r":")
        return any(pat.match(x) for x in lines[meta:end])

    new_lines = lines[:]
    for k, v in to_add:
        if not v and k not in ("caaph",):
            continue
        if have(k):
            continue
        if li is not None:
            # insert after labels:
            i = li + 1
            while i < end and (new_lines[i].startswith("    ") and not new_lines[i].startswith("      ")):
                i += 1
            new_lines.insert(i, f"    {k}: {quote(v) if v != 'enabled' else 'enabled'}")
        else:
            ins = meta + 1
            while ins < end and new_lines[ins].startswith("  "):
                ins += 1
            new_lines[ins:ins] = ["  labels:", f"    {k}: {quote(v) if v != 'enabled' else 'enabled'}"]
            li = ins
            end = end + 2
    return "\n".join(new_lines) + ("\n" if doc.endswith("\n") else "")

parts = [p for p in text.split("\n---\n")]
out = [inject(p) for p in parts]
path.write_text("\n---\n".join(out))
PY
}

# Render HelmChartProxy for Cilium (valuesTemplate: CAAPH Go templates; see CAAPH HelmChartProxy spec).
# Ref: https://github.com/kubernetes-sigs/cluster-api-addon-provider-helm
caaph_print_helmchartproxy_cilium_yaml() {
  python3 <<'PY'
import os

e = {k: os.environ.get(k, "") for k in os.environ}
ver = (e.get("CILIUM_VERSION") or "1.19.3").lstrip("v")
ns = e.get("WORKLOAD_CLUSTER_NAMESPACE") or "default"
name = e.get("WORKLOAD_CLUSTER_NAME") or "cluster"
wt = (e.get("_CAAPH_CILIUM_KPR") or "0").strip() in ("1", "true", "yes")

def g(expr: str) -> str:
    return "{{ " + expr + " }}"

lines = [
    "cluster:",
    f"  name: {g('.Cluster.metadata.name')}",
    f"  id: {g('index .Cluster.metadata.labels \"caaph.cilium.cluster-id\"')}",
    f"kubeProxyReplacement: {'true' if wt else 'false'}",
]
if wt:
    lines += [
        f"k8sServiceHost: {g('index .Cluster.metadata.labels \"caaph.cilium.k8s-service-host\"')}",
        f"k8sServicePort: {g('index .Cluster.metadata.labels \"caaph.cilium.k8s-service-port\"')}",
    ]
ing = (e.get("CILIUM_INGRESS") or "true").lower() in ("1", "true", "yes")
if ing:
    lines += ["ingressController:", "  enabled: true", "  default: true"]
hub = (e.get("CILIUM_HUBBLE") or "true").lower() in ("1", "true", "yes")
if hub:
    lines += [
        "hubble:",
        "  enabled: true",
        "  relay:",
        "    enabled: true",
    ]
    hui = (e.get("CILIUM_HUBBLE_UI") or "true").lower() in ("1", "true", "yes")
    if hui:
        lines += ["  ui:", "    enabled: true"]
# Cluster-pool pod IPv4 range (overrides chart default / avoids tiny or LAN-overlapping pools such as 192.168.0.0/24).
pool = (e.get("CILIUM_IPAM_CLUSTER_POOL_IPV4") or "10.244.0.0/16").strip().replace('"', "")
mask = (e.get("CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE") or "24").strip()
if not mask.isdigit():
    mask = "24"
lines += [
    "ipam:",
    "  operator:",
    f"    clusterPoolIPv4PodCIDRList: [\"{pool}\"]",
    f"    clusterPoolIPv4MaskSize: {int(mask, 10)}",
]
gwa = (e.get("CILIUM_GATEWAY_API_ENABLED") or "false").lower() in ("1", "true", "yes")
if gwa:
    lines += ["gatewayAPI:", "  enabled: true"]
val_t = "\n".join(lines) + "\n"

# Escape for YAML string block (|)
val_t_escaped = val_t.rstrip() + "\n"
meta_name = f"{name}-caaph-cilium"
print("apiVersion: addons.cluster.x-k8s.io/v1alpha1")
print("kind: HelmChartProxy")
print("metadata:")
print(f"  name: {meta_name}")
print(f"  namespace: {ns}")
print("spec:")
print("  clusterSelector:")
print("    matchLabels:")
print("      caaph: enabled")
print("  chartName: cilium")
print("  repoURL: https://helm.cilium.io/")
print(f"  version: \"{ver}\"")
print("  namespace: kube-system")
print("  options:")
print("    wait: true")
print("    waitForJobs: true")
print("    timeout: 15m0s")
print("    install:")
print("      createNamespace: true")
print("  valuesTemplate: |")
for ln in val_t_escaped.splitlines():
    print("    " + ln)
PY
}

apply_workload_cilium_helmchartproxy() {
  local mctx
  mctx="kind-${KIND_CLUSTER_NAME}"
  log "Cilium: installing via Cluster API add-on provider Helm (HelmChartProxy → workload cluster) per https://cluster-api.sigs.k8s.io/tasks/workload-bootstrap-gitops …"
  if cilium_needs_kube_proxy_replacement; then
    export _CAAPH_CILIUM_KPR=1
    log "Cilium: kube-proxy replacement (k8sServiceHost/Port from Cluster labels: caaph.cilium.k8s-service-*; kubeadm skips addon/kube-proxy when patched)."
  else
    export _CAAPH_CILIUM_KPR=0
    log "Cilium: kube-proxy replacement off — node kube-proxy in use."
  fi
  is_true "$CILIUM_HUBBLE_UI" && ! is_true "$CILIUM_HUBBLE" && die "CILIUM_HUBBLE_UI requires CILIUM_HUBBLE=true"
  caaph_print_helmchartproxy_cilium_yaml | kubectl --context "$mctx" apply -f - \
    || die "Failed to apply HelmChartProxy (Cilium) on the management cluster."
  _gwa_log=""
  is_true "${CILIUM_GATEWAY_API_ENABLED:-false}" && _gwa_log="; Gateway API enabled in Cilium helm values (ensure gateway.networking.k8s.io CRDs per Cilium ${CILIUM_VERSION#v} docs)"
  log "Applied HelmChartProxy ${WORKLOAD_CLUSTER_NAME}-caaph-cilium (Cilium v${CILIUM_VERSION#v}; IPAM pool IPv4 ${CILIUM_IPAM_CLUSTER_POOL_IPV4} /${CILIUM_IPAM_CLUSTER_POOL_IPV4_MASK_SIZE} per node; changing CIDR usually requires a new Cluster)${_gwa_log}."
  unset _CAAPH_CILIUM_KPR
}

# After the workload API is up: CiliumLoadBalancerIPPool (not in the Cilium chart HelmChartProxy; apply directly).
apply_workload_cilium_lbb_to_workload_if_enabled() {
  local wk f
  is_true "$CILIUM_LB_IPAM" || return 0
  f="$(mktemp)"
  : > "$f"
  append_cilium_lb_ipam_pool_manifest "$f"
  [[ -s "$f" ]] || {
    rm -f "$f"
    return 0
  }
  wk="$(mktemp)"
  kubectl --context "kind-${KIND_CLUSTER_NAME}" -n "$WORKLOAD_CLUSTER_NAMESPACE" \
    get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$wk" \
    || {
      rm -f "$f" "$wk"
      return 0
    }
  if is_true "$CILIUM_LB_IPAM"; then
    log "Cilium LB-IPAM: applying CiliumLoadBalancerIPPool to workload (${CILIUM_LB_IPAM_POOL_NAME:-${WORKLOAD_CLUSTER_NAME}-lb-pool} / ${CILIUM_LB_IPAM_POOL_CIDR:-derived})."
  fi
  KUBECONFIG="$wk" kubectl apply -f "$f" || true
  rm -f "$f" "$wk"
}

# Install [Argo CD Operator](https://argocd-operator.readthedocs.io/en/latest/install/manual/) on the **workload** cluster
# (remote kustomize), then an ArgoCD CR (metadata.name=argocd) so CAAPH argocd-apps can sync the root app-of-apps.
apply_workload_argocd_operator_and_argocd_cr() {
  local wk op_url cr_f _a
  wk="$(mktemp)"
  kubectl --context "kind-${KIND_CLUSTER_NAME}" -n "$WORKLOAD_CLUSTER_NAMESPACE" \
    get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$wk" \
    || die "Cannot read workload kubeconfig (${WORKLOAD_CLUSTER_NAME}-kubeconfig) — install the Argo CD Operator only after the workload API is ready."
  op_url="https://github.com/argoproj-labs/argocd-operator/config/default?ref=${ARGOCD_OPERATOR_VERSION}"
  # Client-side `kubectl apply` stores last-applied-configuration; argocds.argoproj.io CRD exceeds the 256KiB annotation
  # limit. Server-Side Apply avoids that (ref: KEP-555, https://github.com/argoproj/argo-cd/issues/12043).
  log "Installing Argo CD Operator on the workload cluster (ref ${ARGOCD_OPERATOR_VERSION}; kubectl apply -k --server-side ${op_url})…"
  KUBECONFIG="$wk" kubectl apply --server-side --force-conflicts --field-manager=bootstrap-capi-argocd-operator -k "$op_url" \
    || die "Failed to apply Argo CD Operator (network, ref ${ARGOCD_OPERATOR_VERSION}, or kubectl that supports --server-side; need >= 1.18)."
  log "Waiting for Argo CD Operator controller (initial start)…"
  KUBECONFIG="$wk" kubectl wait -n argocd-operator-system deploy/argocd-operator-controller-manager \
    --for=condition=Available --timeout=300s 2>/dev/null \
    || die "Argo CD Operator controller is not Available in argocd-operator-system (see pods in that namespace)."
  log "Allowing cluster-scoped sync from Argo in ${WORKLOAD_ARGOCD_NAMESPACE} (ARGOCD_CLUSTER_CONFIG_NAMESPACES on the operator)…"
  KUBECONFIG="$wk" kubectl -n argocd-operator-system set env deploy/argocd-operator-controller-manager \
    "ARGOCD_CLUSTER_CONFIG_NAMESPACES=${WORKLOAD_ARGOCD_NAMESPACE}" --overwrite \
    || die "Failed to patch argocd-operator-controller-manager with ARGOCD_CLUSTER_CONFIG_NAMESPACES."
  KUBECONFIG="$wk" kubectl -n argocd-operator-system rollout status deploy/argocd-operator-controller-manager --timeout=300s \
    || warn "Argo CD Operator rollout after env patch not reported ready in 300s — continuing."
  KUBECONFIG="$wk" kubectl wait -n argocd-operator-system deploy/argocd-operator-controller-manager \
    --for=condition=Available --timeout=300s \
    || die "Argo CD Operator controller is not Available after config patch."
  KUBECONFIG="$wk" kubectl create namespace "$WORKLOAD_ARGOCD_NAMESPACE" --dry-run=client -o yaml | KUBECONFIG="$wk" kubectl apply -f - \
    || die "Failed to ensure namespace ${WORKLOAD_ARGOCD_NAMESPACE} on the workload cluster."
  local _ao_prom=false _ao_mon=false
  is_true "${ARGOCD_OPERATOR_ARGOCD_PROMETHEUS_ENABLED:-false}" && _ao_prom=true
  is_true "${ARGOCD_OPERATOR_ARGOCD_MONITORING_ENABLED:-false}" && _ao_mon=true
  cr_f="$(mktemp)"
  if is_true "${ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS:-false}"; then
    if is_true "$ARGOCD_SERVER_INSECURE"; then
      cat > "$cr_f" <<EACR0
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: argocd
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
spec:
  version: ${ARGOCD_VERSION}
  prometheus:
    enabled: ${_ao_prom}
  monitoring:
    enabled: ${_ao_mon}
  notifications:
    enabled: true
  server:
    insecure: true
    ingress:
      enabled: false
    grpc:
      ingress:
        enabled: false
EACR0
    else
      cat > "$cr_f" <<EACR0b
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: argocd
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
spec:
  version: ${ARGOCD_VERSION}
  prometheus:
    enabled: ${_ao_prom}
  monitoring:
    enabled: ${_ao_mon}
  notifications:
    enabled: true
  server:
    ingress:
      enabled: false
    grpc:
      ingress:
        enabled: false
EACR0b
    fi
  elif is_true "$ARGOCD_SERVER_INSECURE"; then
    cat > "$cr_f" <<EACR
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: argocd
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
spec:
  version: ${ARGOCD_VERSION}
  prometheus:
    enabled: ${_ao_prom}
  monitoring:
    enabled: ${_ao_mon}
  notifications:
    enabled: true
  server:
    insecure: true
    grpc:
      ingress:
        enabled: true
EACR
  else
    cat > "$cr_f" <<EACR2
apiVersion: argoproj.io/v1beta1
kind: ArgoCD
metadata:
  name: argocd
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
spec:
  version: ${ARGOCD_VERSION}
  prometheus:
    enabled: ${_ao_prom}
  monitoring:
    enabled: ${_ao_mon}
  notifications:
    enabled: true
  server:
    grpc:
      ingress:
        enabled: true
EACR2
  fi
  log "Creating ArgoCD custom resource (argocd/${WORKLOAD_ARGOCD_NAMESPACE})…"
  KUBECONFIG="$wk" kubectl apply -f "$cr_f" \
    || die "Failed to apply ArgoCD custom resource on the workload cluster."
  is_true "${ARGOCD_DISABLE_OPERATOR_MANAGED_INGRESS:-false}" && log "ArgoCD CR: operator-managed server/gRPC Ingress disabled — expose Argo with Gateway API (e.g. workload-app-of-apps examples/gateway-api) or port-forward."
  rm -f "$cr_f"
  # argocd-redis is pre-created by apply_workload_argocd_redis_secret_to_workload_cluster (from kind) before the CR; if
  # argocd-server was already created and still errors, a one-time restart can pick up the mount.
  if KUBECONFIG="$wk" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" get deploy argocd-server &>/dev/null; then
    KUBECONFIG="$wk" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" rollout restart deploy/argocd-server 2>/dev/null || true
    log "Restarted argocd-server after ArgoCD CR (argocd-redis pre-provisioned from bootstrap)."
  fi
  rm -f "$wk"
  log "Argo CD Operator will reconcile Argo CD in ${WORKLOAD_ARGOCD_NAMESPACE} (admin password: secret argocd-cluster, key admin.password, when ready)."
}

# CAAPH: Cilium (elsewhere) + in-bootstrap Argo CD Operator + ArgoCD CR, then argocd-apps (root app-of-apps name = cluster name) via HelmChartProxy.
caaph_apply_workload_argo_helm_proxies() {
  is_workload_gitops_caaph_mode || return 0
  is_true "${WORKLOAD_ARGOCD_ENABLED:-true}" || return 0
  local mctx
  mctx="kind-${KIND_CLUSTER_NAME}"
  [[ -n "$WORKLOAD_APP_OF_APPS_GIT_URL" ]] || die "WORKLOAD_APP_OF_APPS_GIT_URL is required in caaph mode (validated at start)."
  log "Argo CD on the workload: Argo CD Operator + ArgoCD CR (in-bootstrap), then CAAPH argocd-apps (root Application name ${WORKLOAD_CLUSTER_NAME})."
  apply_workload_argocd_operator_and_argocd_cr
  {
    cat <<'EOPY3'
apiVersion: addons.cluster.x-k8s.io/v1alpha1
kind: HelmChartProxy
metadata:
EOPY3
    printf '  name: %s\n' "${WORKLOAD_CLUSTER_NAME}-caaph-argocd-apps"
    printf '  namespace: %s\n' "$WORKLOAD_CLUSTER_NAMESPACE"
    cat <<EOPY4
spec:
  clusterSelector:
    matchLabels:
      caaph: enabled
  chartName: argocd-apps
  repoURL: https://argoproj.github.io/argo-helm
EOPY4
    cat <<EOPY5
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  options:
    wait: true
    waitForJobs: true
    timeout: 20m0s
    install:
      createNamespace: true
  valuesTemplate: |
EOPY5
    u="$(printf '%s' "$WORKLOAD_APP_OF_APPS_GIT_URL" | sed "s/'/'\"'\"'/g")"
    p="$(printf '%s' "$WORKLOAD_APP_OF_APPS_GIT_PATH" | sed "s/'/'\"'\"'/g")"
    r="$(printf '%s' "$WORKLOAD_APP_OF_APPS_GIT_REF" | sed "s/'/'\"'\"'/g")"
    # argo-helm "argocd-apps" iterates .Values.applications as a *map* (keys = Application .metadata.name).
    # A YAML list would make the template use numeric indices (0,1,…) as names → invalid Application CRs.
    cat <<EOPY6
    applications:
      "${WORKLOAD_CLUSTER_NAME}":
        namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
        finalizers:
          - resources-finalizer.argocd.argoproj.io
        project: default
        source:
          repoURL: '${u}'
          path: '${p}'
          targetRevision: '${r}'
        destination:
          server: https://kubernetes.default.svc
          namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
        syncPolicy:
          automated:
            prune: true
            selfHeal: true
          syncOptions:
            - CreateNamespace=true
EOPY6
  } | kubectl --context "$mctx" apply -f - \
    || die "Failed to apply HelmChartProxy (argocd-apps / app-of-apps)."
  log "Applied HelmChartProxy ${WORKLOAD_CLUSTER_NAME}-caaph-argocd-apps (root app-of-apps Application name: ${WORKLOAD_CLUSTER_NAME}; repo ${WORKLOAD_APP_OF_APPS_GIT_URL})."
}

# Wait for Argo CD server on the workload (after CAAPH installs the chart).
caaph_wait_workload_argocd_server() {
  is_workload_gitops_caaph_mode || return 0
  is_true "${WORKLOAD_ARGOCD_ENABLED:-true}" || return 0
  local wk
  wk="$(mktemp)"
  kubectl --context "kind-${KIND_CLUSTER_NAME}" -n "$WORKLOAD_CLUSTER_NAMESPACE" \
    get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$wk" \
    || {
      rm -f "$wk"
      return 0
    }
  log "Waiting for Argo CD server (workload ${WORKLOAD_CLUSTER_NAME}, ns ${WORKLOAD_ARGOCD_NAMESPACE})…"
  for _ in {1..120}; do
    if KUBECONFIG="$wk" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" get deploy argocd-server &>/dev/null; then
      if KUBECONFIG="$wk" kubectl wait -n "$WORKLOAD_ARGOCD_NAMESPACE" deploy/argocd-server --for=condition=Available --timeout=2m 2>/dev/null; then
        rm -f "$wk"
        log "Argo CD server is available on the workload cluster."
        return 0
      fi
    fi
    sleep 5
  done
  rm -f "$wk"
  warn "Argo CD server did not become Available in time — check HelmReleaseProxy and pods on the workload."
}

# After argocd-server is up: show whether the root/child Application CRs exist and warn if sync lags. Bootstrap does
# not block until the app-of-apps Git tree is applied to the cluster (unlike the old in-script apply path).
caaph_log_workload_argo_apps_status() {
  is_workload_gitops_caaph_mode || return 0
  is_true "${WORKLOAD_ARGOCD_ENABLED:-true}" || return 0
  local wk
  wk="$(mktemp)"
  if ! kubectl --context "kind-${KIND_CLUSTER_NAME}" -n "$WORKLOAD_CLUSTER_NAMESPACE" \
    get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' 2>/dev/null | base64 -d > "$wk" \
    || [[ ! -s "$wk" ]]; then
    rm -f "$wk"
    return 0
  fi
  log "App-of-apps: this script only waits for argocd-server — it does not wait for the argocd-apps Helm install, Git sync, or platform Deployments. Check sync below."
  if ! KUBECONFIG="$wk" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" get applications.argoproj.io -o name &>/dev/null; then
    warn "Could not list Application resources in ${WORKLOAD_ARGOCD_NAMESPACE} (CRD or RBAC) — is Argo fully installed on the workload?"
    rm -f "$wk"
    return 0
  fi
  KUBECONFIG="$wk" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" get applications.argoproj.io 2>/dev/null || true
  if KUBECONFIG="$wk" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" get "application/${WORKLOAD_CLUSTER_NAME}" &>/dev/null; then
    KUBECONFIG="$wk" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" get "application/${WORKLOAD_CLUSTER_NAME}" \
      -o custom-columns=NAME:.metadata.name,SYNC:.status.sync.status,HEALTH:.status.health.status 2>/dev/null || true
    local s
    s="$(KUBECONFIG="$wk" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" get "application/${WORKLOAD_CLUSTER_NAME}" -o jsonpath='{.status.sync.status}' 2>/dev/null)"
    [[ -n "$s" && "$s" != "Synced" ]] && warn "Root app ${WORKLOAD_CLUSTER_NAME} is not Synced yet (${s}) — from a machine with workload kube: argocd app sync ${WORKLOAD_CLUSTER_NAME} (or use the Argo CD UI), then watch child apps."
  else
    warn "No root Application ${WORKLOAD_CLUSTER_NAME} in ${WORKLOAD_ARGOCD_NAMESPACE} yet. Often: CAAPH is still running the 'argocd-apps' install on the workload, the HelmChartProxy does not match the cluster (CAPI Cluster needs label caaph=enabled), or the chart failed; check: kubectl get helmchartproxy -A, controller logs, and Argo/Helm on the workload."
  fi
  rm -f "$wk"
}

load_csi_vars_from_config() {
  # Populate PROXMOX_CSI_URL/TOKEN_ID/TOKEN_SECRET/REGION from a previously
  # generated proxmox-csi.yaml when they are empty in the current env. Without
  # this, idempotent reruns that skip Terraform (existing kind + Terraform state
  # or env-only credentials) can miss CSI values for the Argo Application.
  [[ -f "$PROXMOX_CSI_CONFIG" ]] || return 0

  local line val
  for key in url token_id token_secret region; do
    # Matches "      url:", "    - url:", or plain "url:" — covers the YAML list-item line too.
    line="$(grep -E "^[^A-Za-z_]*${key}:" "$PROXMOX_CSI_CONFIG" 2>/dev/null | head -1 || true)"
    [[ -z "$line" ]] && continue
    val="$(printf '%s' "$line" | sed -E "s/^[^:]*:[[:space:]]*\"?([^\"]*)\"?[[:space:]]*$/\1/" || true)"
    case "$key" in
      url)          [[ -z "$PROXMOX_CSI_URL" ]]          && PROXMOX_CSI_URL="$val" ;;
      token_id)     [[ -z "$PROXMOX_CSI_TOKEN_ID" ]]     && PROXMOX_CSI_TOKEN_ID="$val" ;;
      token_secret) [[ -z "$PROXMOX_CSI_TOKEN_SECRET" ]] && PROXMOX_CSI_TOKEN_SECRET="$val" ;;
      region)       [[ -z "$PROXMOX_REGION" ]]           && PROXMOX_REGION="$val" ;;
    esac
  done
  return 0
}

ensure_helm_present() {
  if ! command -v helm >/dev/null 2>&1; then
    warn "helm not found — installing..."
    curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | DESIRED_VERSION="" bash
    command -v helm >/dev/null 2>&1 || die "helm installation failed."
  fi
}

# Decode initial Argo admin password: Helm chart (argocd-initial-admin-secret), Argo CD Operator (argocd-cluster), or argocd-secret fallback.
argocd_read_initial_admin_password() {
  local ctx_arg=() ns="${2:-argocd}" pw
  [[ -n "${1:-}" ]] && ctx_arg=(--context "$1")
  pw="$(kubectl "${ctx_arg[@]}" get secret argocd-initial-admin-secret -n "$ns" -o jsonpath='{.data.password}' 2>/dev/null | base64 -d)"
  [[ -n "$pw" ]] && { printf '%s' "$pw"; return 0; }
  pw="$(kubectl "${ctx_arg[@]}" get secret argocd-cluster -n "$ns" -o jsonpath='{.data.admin\.password}' 2>/dev/null | base64 -d)"
  [[ -n "$pw" ]] && { printf '%s' "$pw"; return 0; }
  # argocd-secret stores a bcrypt hash — skip if it starts with $2.
  pw="$(kubectl "${ctx_arg[@]}" get secret argocd-secret -n "$ns" -o jsonpath='{.data.admin\.password}' 2>/dev/null | base64 -d)"
  [[ -n "$pw" ]] && [[ "$pw" != \$2* ]] && { printf '%s' "$pw"; return 0; }
  return 1
}

# For standalone --argocd-print-access / --argocd-port-forward: if <namespace>/<name>-kubeconfig
# is missing, resolve namespace from cluster.x-k8s.io Cluster name, and/or pick the only
# CAPI cluster that has a kubeconfig Secret (when neither workload name nor namespace was set on the CLI).
argocd_standalone_discover_workload_kubeconfig_ref() {
  local kctx="kind-${KIND_CLUSTER_NAME}"
  local -a cands=() name_ns=()
  local c_ns c_name s _line

  kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | grep -qx "$kctx" || return 1
  if kubectl --context "$kctx" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -n "$WORKLOAD_CLUSTER_NAMESPACE" &>/dev/null; then
    return 0
  fi

  # Match CAPI Cluster.metadata.name to resolve namespace (kubeconfig Secret lives there, not always default).
  while read -r _line; do
    [[ -n "$_line" ]] && name_ns+=("$_line")
  done < <(kubectl --context "$kctx" get cluster -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name --no-headers 2>/dev/null \
    | awk -v want="${WORKLOAD_CLUSTER_NAME}" '$2 == want { print $1 }')

  if ((${#name_ns[@]} == 1)); then
    if [[ "${WORKLOAD_CLUSTER_NAMESPACE}" != "${name_ns[0]}" ]]; then
      WORKLOAD_CLUSTER_NAMESPACE="${name_ns[0]}"
      log "Resolved namespace to ${WORKLOAD_CLUSTER_NAMESPACE} (CAPI Cluster ${WORKLOAD_CLUSTER_NAME})."
    fi
    if kubectl --context "$kctx" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -n "$WORKLOAD_CLUSTER_NAMESPACE" &>/dev/null; then
      return 0
    fi
  elif ((${#name_ns[@]} > 1)); then
    warn "Multiple CAPI Clusters named ${WORKLOAD_CLUSTER_NAME} in namespaces: ${name_ns[*]}. Set --workload-cluster-namespace."
    return 1
  fi

  if is_true "${WORKLOAD_CLUSTER_NAME_EXPLICIT:-0}" || is_true "${WORKLOAD_CLUSTER_NAMESPACE_EXPLICIT:-0}"; then
    return 1
  fi

  while IFS=$'\t' read -r c_ns c_name; do
    [[ -n "$c_ns" && -n "$c_name" ]] || continue
    if kubectl --context "$kctx" get secret "${c_name}-kubeconfig" -n "$c_ns" &>/dev/null; then
      cands+=("${c_ns}/${c_name}")
    fi
  done < <(kubectl --context "$kctx" get cluster -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"\t"}{.metadata.name}{"\n"}{end}' 2>/dev/null)

  if ((${#cands[@]} == 0)); then
    return 1
  fi
  if ((${#cands[@]} == 1)); then
    s="${cands[0]}"
    WORKLOAD_CLUSTER_NAMESPACE="${s%%/*}"
    WORKLOAD_CLUSTER_NAME="${s#*/}"
    log "Using workload ${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME} (the only CAPI cluster on ${kctx} with a *-kubeconfig Secret)."
    return 0
  fi
  warn "More than one CAPI cluster with a kubeconfig Secret on ${kctx}: ${cands[*]}. Use --workload-cluster-name and --workload-cluster-namespace, or CAPI_MANIFEST, for discover_workload_cluster_identity."
  return 1
}

# CAPI / Proxmox workload Argo only (see standalone --argocd-print-access; management kind has no Argo).
argocd_print_access_info() {
  local kctx pf_port wl_pf_addr login_extra kcfg_tmp wpw
  kctx="kind-${KIND_CLUSTER_NAME}"
  pf_port="${ARGOCD_PORT_FORWARD_PORT:-8443}"
  wl_pf_addr="127.0.0.1:${pf_port}"
  if is_true "$ARGOCD_SERVER_INSECURE"; then
    login_extra="--insecure --grpc-web"
  else
    login_extra="--grpc-web"
  fi

  printf '\n\033[1;36m── Argo CD access (initial admin; rotate after first login) — provisioned cluster only ──\033[0m\n'

  printf '\n\033[1;33m[CAPI / Proxmox workload] cluster %s / Argo namespace %s\033[0m\n' "$WORKLOAD_CLUSTER_NAME" "$WORKLOAD_ARGOCD_NAMESPACE"
  if ! kubectl --context "$kctx" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -n "$WORKLOAD_CLUSTER_NAMESPACE" &>/dev/null; then
    warn "Secret ${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME}-kubeconfig not found. Use --workload-cluster-name / --workload-cluster-namespace (or env), CAPI_MANIFEST, or wait until CAPI creates this Secret in the Cluster's namespace."
    if kubectl --context "$kctx" get cluster -A &>/dev/null; then
      log "  CAPI Clusters (management): $(kubectl --context "$kctx" get cluster -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name --no-headers 2>/dev/null | paste -sd' ' -)"
    fi
  else
    kcfg_tmp="$(mktemp)"
    kubectl --context "$kctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$kcfg_tmp" || true
    if KUBECONFIG="$kcfg_tmp" kubectl get namespace "$WORKLOAD_ARGOCD_NAMESPACE" &>/dev/null; then
      wpw="$(KUBECONFIG="$kcfg_tmp" argocd_read_initial_admin_password "" "$WORKLOAD_ARGOCD_NAMESPACE" || true)"
      if [[ -n "$wpw" ]]; then
        log "  Initial admin password: ${wpw}"
      else
        warn "  Admin password not found in ${WORKLOAD_ARGOCD_NAMESPACE} (checked argocd-initial-admin-secret, argocd-cluster, argocd-secret — not installed or password rotated?)."
      fi
    else
      warn "  Namespace ${WORKLOAD_ARGOCD_NAMESPACE} not on workload — run bootstrap with workload Argo enabled first."
    fi
    printf '  Write kubeconfig and port-forward (local port matches ARGOCD_PORT_FORWARD_PORT, default %s):\n' \
      "${ARGOCD_PORT_FORWARD_PORT:-8443}"
    printf '    kubectl --context "%s" -n "%s" get secret "%s-kubeconfig" -o jsonpath={.data.value} | base64 -d > /tmp/%s-kubeconfig.yaml\n' \
      "$kctx" "$WORKLOAD_CLUSTER_NAMESPACE" "$WORKLOAD_CLUSTER_NAME" "$WORKLOAD_CLUSTER_NAME"
    printf '    export KUBECONFIG=/tmp/%s-kubeconfig.yaml\n' "$WORKLOAD_CLUSTER_NAME"
    printf '    kubectl port-forward --address 127.0.0.1 -n "%s" svc/argocd-server %s:443\n' "$WORKLOAD_ARGOCD_NAMESPACE" "$pf_port"
    printf '  Login to Argo on the CAPI cluster:\n'
    printf '    argocd login %s --username admin --password '\''<password>'\'' %s\n' "$wl_pf_addr" "$login_extra"
    rm -f "$kcfg_tmp"
  fi
  printf '\n'
}

# Port-forward to Argo on the provisioned (CAPI) cluster only; reads kubeconfig from the management Secret (blocks until Ctrl+C).
argocd_run_port_forwards() {
  local kctx pf_port target kcfg_file _p
  kctx="kind-${KIND_CLUSTER_NAME}"
  pf_port="${ARGOCD_PORT_FORWARD_PORT:-8443}"
  target="${ARGOCD_PORT_FORWARD_TARGET:-workload}"
  kcfg_file=""

  if [[ "$target" != workload ]]; then
    warn "port-forward: only the provisioned cluster is supported (ARGOCD_PORT_FORWARD_TARGET=workload); ignoring ${target}."
  fi

  if ! kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | grep -qx "$kctx"; then
    die "port-forward: kubectl context ${kctx} not found (need management cluster to read ${WORKLOAD_CLUSTER_NAME}-kubeconfig)."
  fi

  _argocd_pf_cleanup() {
    [[ -n "$_p" ]] && kill "$_p" 2>/dev/null || true
    [[ -n "$kcfg_file" && -f "$kcfg_file" ]] && rm -f "$kcfg_file"
  }
  trap '_argocd_pf_cleanup; stty sane 2>/dev/null || true; exit 130' INT TERM

  if ! kubectl --context "$kctx" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -n "$WORKLOAD_CLUSTER_NAMESPACE" &>/dev/null; then
    die "port-forward: ${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME}-kubeconfig not found (is the CAPI cluster ready?)."
  fi
  kcfg_file="$(mktemp)"
  if ! kubectl --context "$kctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$kcfg_file"; then
    rm -f "$kcfg_file"
    die "port-forward: could not read workload kubeconfig"
  fi
  if ! KUBECONFIG="$kcfg_file" kubectl get namespace "$WORKLOAD_ARGOCD_NAMESPACE" &>/dev/null; then
    _argocd_pf_cleanup
    trap - INT TERM
    die "port-forward: namespace ${WORKLOAD_ARGOCD_NAMESPACE} not found on the CAPI cluster — is workload Argo installed?"
  fi
  KUBECONFIG="$kcfg_file" kubectl port-forward --address 127.0.0.1 -n "$WORKLOAD_ARGOCD_NAMESPACE" svc/argocd-server "${pf_port}:443" &
  _p=$!
  log "Port-forward: CAPI / Proxmox workload Argo → 127.0.0.1:${pf_port} (pid ${_p}) — Ctrl+C to stop — see --argocd-print-access for password."
  wait "$_p" || true
  _argocd_pf_cleanup
  trap - INT TERM
}

# Values for kubernetes-sigs/metrics-server Helm chart (in-cluster Argo: git path charts/metrics-server).
workload_argocd_metrics_server_helm_values() {
  if is_true "${WORKLOAD_METRICS_SERVER_INSECURE_TLS:-true}"; then
    cat <<'ENDVAL'
defaultArgs:
  - --cert-dir=/tmp
  - --kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname
  - --kubelet-use-node-status-port
  - --metric-resolution=15s
args:
  - --kubelet-insecure-tls
ENDVAL
  else
    :
  fi
}

# Fallback when the CAPI *-kubeconfig Secret on the kind cluster is missing or empty: same kubeconfig
# `clusterctl get kubeconfig` uses after the Cluster is Available.
_workload_kubeconfig_file_from_clusterctl() {
  local kcfg
  command -v clusterctl >/dev/null 2>&1 || return 1
  [[ -n "${BOOTSTRAP_CLUSTERCTL_CONFIG_PATH:-}" && -f "$BOOTSTRAP_CLUSTERCTL_CONFIG_PATH" ]] || return 1
  kcfg="$(mktemp)" || return 1
  if clusterctl get kubeconfig "$WORKLOAD_CLUSTER_NAME" \
    --config "$BOOTSTRAP_CLUSTERCTL_CONFIG_PATH" \
    --namespace "$WORKLOAD_CLUSTER_NAMESPACE" > "$kcfg" 2>/dev/null && [[ -s "$kcfg" ]]; then
    printf '%s' "$kcfg"
    return 0
  fi
  rm -f "$kcfg"
  return 1
}

# Create Secret argocd-redis (key `auth`) on the CAPI cluster using the workload kubeconfig from the
# management (kind) cluster — same pattern as `apply_proxmox_csi_config_secret_to_workload_cluster`.
# Optional 1: path to an existing workload kubeconfig file; otherwise reads ${WORKLOAD_CLUSTER_NAME}-kubeconfig
# on the kind cluster, or `clusterctl get kubeconfig` if the Secret is not ready yet. Idempotent: leaves an existing argocd-redis unchanged.
apply_workload_argocd_redis_secret_to_workload_cluster() {
  is_true "${WORKLOAD_ARGOCD_ENABLED:-true}" || return 0
  local mgmt_ctx workload_kcfg redis_pw _fb
  local _cleanup_kcfg=0
  mgmt_ctx="kind-${KIND_CLUSTER_NAME}"
  if [[ -n "${1:-}" && -f "$1" ]]; then
    workload_kcfg="$1"
  else
    workload_kcfg="$(mktemp)"
    _cleanup_kcfg=1
    if kubectl --context "$mgmt_ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" \
      get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' 2>/dev/null | base64 -d > "$workload_kcfg" \
      && [[ -s "$workload_kcfg" ]]; then
      :
    else
      rm -f "$workload_kcfg"
      _fb="$(_workload_kubeconfig_file_from_clusterctl || true)"
      if [[ -n "$_fb" && -f "$_fb" ]]; then
        workload_kcfg="$_fb"
        log "Using clusterctl workload kubeconfig for argocd-redis (${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME})."
      else
        die "Cannot read workload kubeconfig for argocd-redis: no data in ${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME}-kubeconfig on ${mgmt_ctx}, and clusterctl get kubeconfig failed (set namespace/name, or wait until the Cluster is Available)."
      fi
    fi
  fi

  KUBECONFIG="$workload_kcfg" kubectl create namespace "$WORKLOAD_ARGOCD_NAMESPACE" --dry-run=client -o yaml \
    | KUBECONFIG="$workload_kcfg" kubectl apply -f - \
    || die "Failed to ensure namespace ${WORKLOAD_ARGOCD_NAMESPACE} on the workload for argocd-redis."
  if KUBECONFIG="$workload_kcfg" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" get secret argocd-redis &>/dev/null; then
    log "Workload ${WORKLOAD_ARGOCD_NAMESPACE}/argocd-redis already present — not overwriting (idempotent bootstrap)."
    ((_cleanup_kcfg)) && rm -f "$workload_kcfg"
    return 0
  fi
  if command -v openssl >/dev/null 2>&1; then
    redis_pw="$(openssl rand -base64 32 | tr -d '\n\r')"
  else
    redis_pw="$(python3 -c "import secrets; print(secrets.token_urlsafe(32))")"
  fi
  KUBECONFIG="$workload_kcfg" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" create secret generic argocd-redis \
    --from-literal=auth="$redis_pw" \
    --dry-run=client -o yaml | KUBECONFIG="$workload_kcfg" kubectl apply -f - \
    || die "Failed to create argocd-redis on the workload cluster."
  log "Created ${WORKLOAD_ARGOCD_NAMESPACE}/argocd-redis on the workload (key auth) via kind/management bootstrap."
  ((_cleanup_kcfg)) && rm -f "$workload_kcfg"
}

# Push Proxmox CSI credentials to the workload cluster as a Kubernetes Secret so the
# Argo CD Application only references existingConfigSecret (tokens are not in Application YAML).
apply_proxmox_csi_config_secret_to_workload_cluster() {
  local mgmt_ctx workload_kcfg secret_name cfg_yaml
  mgmt_ctx="kind-${KIND_CLUSTER_NAME}"
  secret_name="${WORKLOAD_CLUSTER_NAME}-proxmox-csi-config"

  workload_kcfg="$(mktemp)"
  kubectl --context "$mgmt_ctx" \
    -n "$WORKLOAD_CLUSTER_NAMESPACE" \
    get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" \
    -o jsonpath='{.data.value}' | base64 -d > "$workload_kcfg" \
    || die "Cannot read workload kubeconfig to apply Proxmox CSI config Secret."

  kubectl --kubeconfig "$workload_kcfg" create namespace "$PROXMOX_CSI_NAMESPACE" \
    --dry-run=client -o yaml | kubectl --kubeconfig "$workload_kcfg" apply -f -

  cfg_yaml="$(cat <<EOF
features:
  provider: ${PROXMOX_CSI_CONFIG_PROVIDER}
clusters:
  - url: "${PROXMOX_CSI_URL}"
    insecure: ${PROXMOX_CSI_INSECURE}
    token_id: "${PROXMOX_CSI_TOKEN_ID}"
    token_secret: "${PROXMOX_CSI_TOKEN_SECRET}"
    region: "${PROXMOX_REGION}"
EOF
)"

  kubectl --kubeconfig "$workload_kcfg" -n "$PROXMOX_CSI_NAMESPACE" create secret generic "$secret_name" \
    --from-literal=config.yaml="$cfg_yaml" \
    --dry-run=client -o yaml | kubectl --kubeconfig "$workload_kcfg" apply -f - \
    || die "Failed to apply Proxmox CSI config Secret on workload cluster."

  # workload-app-of-apps `base/platform/proxmox-csi.yaml` uses existingConfigSecret: proxmox-csi-config; bootstrap
  # also writes ${WORKLOAD_CLUSTER_NAME}-proxmox-csi-config for forked overlays. Mirror the same config under the
  # short name when they differ so the default path (examples/default) matches the cluster without a Kustomize patch.
  if [[ "$secret_name" != "proxmox-csi-config" ]]; then
    kubectl --kubeconfig "$workload_kcfg" -n "$PROXMOX_CSI_NAMESPACE" create secret generic proxmox-csi-config \
      --from-literal=config.yaml="$cfg_yaml" \
      --dry-run=client -o yaml | kubectl --kubeconfig "$workload_kcfg" apply -f - \
      || die "Failed to apply proxmox-csi-config alias Secret on workload cluster."
  fi

  rm -f "$workload_kcfg"
  log "Applied ${secret_name} (and proxmox-csi-config when names differ) — Proxmox API credentials in ${PROXMOX_CSI_NAMESPACE}; Argo Application will not embed them."
}

# PostSync hook git (argo-postsync-hooks/*) for in-cluster App multi-source (Argo CD 2.6+).
workload_postsync_hooks_bootstrap_dir() {
  (cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)
}

workload_postsync_hooks_discover_url() {
  if [[ -n "${ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL:-}" ]]; then
    printf '%s\n' "$ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL"
    return 0
  fi
  local root url
  root="$(workload_postsync_hooks_bootstrap_dir)"
  url="$(cd "$root" 2>/dev/null && git remote get-url origin 2>/dev/null)" || return 0
  [[ -n "$url" ]] || return 0
  if [[ "$url" == git@*:* ]]; then
    local h
    h="${url#git@}"
    url="https://${h%%:*}/${h#*:}"
  fi
  [[ "$url" == https://* || "$url" == http://* ]] || return 0
  if [[ "$url" != *.git ]]; then
    url="${url}.git"
  fi
  printf '%s\n' "$url"
}

workload_postsync_hooks_discover_ref() {
  if [[ -n "${ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF:-}" ]]; then
    printf '%s\n' "$ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_REF"
    return 0
  fi
  local root r
  root="$(workload_postsync_hooks_bootstrap_dir)"
  r="$(cd "$root" 2>/dev/null && git rev-parse --abbrev-ref HEAD 2>/dev/null)" || r=""
  if [[ -n "$r" && "$r" != "HEAD" ]]; then
    printf '%s\n' "$r"
    return 0
  fi
  r="$(cd "$root" 2>/dev/null && git rev-parse --short=12 HEAD 2>/dev/null)" || r="main"
  [[ -n "$r" ]] || r="main"
  printf '%s\n' "$r"
}

workload_postsync_hooks_full_relpath() {
  local short="${1:-}"
  [[ -n "$short" ]] || { printf '\n'; return; }
  local pfx="${ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_PATH-}"
  pfx="${pfx#./}"
  pfx="${pfx%/}"
  if [[ -n "$pfx" ]]; then
    printf '%s/argo-postsync-hooks/%s\n' "$pfx" "$short"
  else
    printf 'argo-postsync-hooks/%s\n' "$short"
  fi
}

workload_postsync_hooks_resolve_kubectl_image() {
  if [[ -n "${ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE:-}" ]]; then
    printf '%s\n' "$ARGO_WORKLOAD_POSTSYNC_HOOKS_KUBECTL_IMAGE"
    return 0
  fi
  local tag
  tag="$(proxmox_csi_smoke_kubectl_oci_tag)"
  printf '%s\n' "registry.k8s.io/kubectl:${tag}"
}

workload_postsync_kustomize_block_for_job() {
  local job="$1" ns img
  ns="${WORKLOAD_POSTSYNC_NAMESPACE:-workload-smoke}"
  img="$(workload_postsync_hooks_resolve_kubectl_image)"
  cat <<KZ
    kustomize:
      namespace: ${ns}
      patches:
        - target:
            group: batch
            version: v1
            kind: Job
            name: ${job}
          patch: |
            - op: replace
              path: /spec/template/spec/containers/0/image
              value: '${img}'
KZ
}

# In-cluster Application: Helm chart from a Git repo subpath (optional 8=releaseName, 9=PostSync hook short name e.g. metrics-server).
_wl_argocd_render_helm_git() {
  local name="$1" dest_ns="$2" repo_url="$3" rel_path="$4" ref="$5" sync_wave="$6" values_yaml="$7"
  local release_name="${8:-}"
  local hook_short="${9:-}"
  local safe_ref hsref hurl hpath kz
  safe_ref="$(printf '%s' "$ref" | sed "s/'/'\"'\"'/g")"
  local indented_values="" indented_values_ms=""
  if [[ -n "$values_yaml" ]]; then
    indented_values="$(printf '%s\n' "$values_yaml" | sed 's/^/        /')"
    indented_values_ms="$(printf '%s\n' "$values_yaml" | sed 's/^/          /')"
  fi
  hurl=""; hpath=""; kz=""; hsref=""
  if is_true "${ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED:-true}" && [[ -n "$hook_short" ]]; then
    hurl="$(workload_postsync_hooks_discover_url 2>/dev/null || true)"
    if [[ -n "$hurl" ]]; then
      hsref="$(workload_postsync_hooks_discover_ref | sed "s/'/'\"'\"'/g")"
      hpath="$(workload_postsync_hooks_full_relpath "$hook_short")"
      kz="$(workload_postsync_kustomize_block_for_job "${hook_short}-smoketest")"
    else
      warn "ARGO_WORKLOAD_POSTSYNC_HOOKS: no git URL; skipping PostSync hook for ${name} (set ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL)."
    fi
  fi
  if [[ -n "$hurl" && -n "$hpath" && -n "$kz" ]]; then
    cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${dest_ns}
  sources:
    - repoURL: ${repo_url}
      path: ${rel_path}
      targetRevision: '${safe_ref}'
      helm:
$(if [[ -n "$release_name" ]]; then printf '        releaseName: %s\n' "$release_name"; fi)
        valuesObject:
$(if [[ -n "$indented_values_ms" ]]; then printf '%s\n' "$indented_values_ms"; else printf '          {}\n'; fi)
    - repoURL: ${hurl}
      path: ${hpath}
      targetRevision: '${hsref}'
$(printf '%s\n' "$kz" | sed 's/^/  /')
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
  else
    cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${dest_ns}
  source:
    repoURL: ${repo_url}
    path: ${rel_path}
    targetRevision: '${safe_ref}'
    helm:
$(if [[ -n "$release_name" ]]; then printf '      releaseName: %s\n' "$release_name"; fi)
      valuesObject:
$(if [[ -n "$indented_values" ]]; then printf '%s\n' "$indented_values"; else printf '        {}\n'; fi)
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
  fi
}

# Extra Helm YAML for _wl_argocd_render_kyverno (under valuesObject).
_kyverno_argocd_values_toleration_fragment() {
  is_true "${KYVERNO_TOLERATE_CONTROL_PLANE:-true}" || return 0
  cat <<'EOT'
        global:
          tolerations:
            - key: "node-role.kubernetes.io/control-plane"
              operator: Exists
              effect: NoSchedule
            - key: "node-role.kubernetes.io/master"
              operator: Exists
              effect: NoSchedule
EOT
}

# Kyverno: use ServerSideApply (large CRDs exceed client-side last-applied-configuration) plus ServerSideDiff
# on compare (https://kyverno.io/docs/installation/platform-notes/#notes-for-argocd-users — needs Argo CD 2.10+).
# config.preserve=false avoids Helm post-delete hook confusion with Argo; webhookLabels reduce label noise.
# Webhook ignoreDifferences: controller-written CA bundles after install.
# If the Argo UI shows Deployments but no Pod children, check AppProject: list/watch on apps/ReplicaSet is
# required (Pods are linked via ReplicaSet). See https://github.com/argoproj/argo-cd/discussions/11845
# Kyverno workload Pods live in KYVERNO_NAMESPACE, not the Argo CD namespace. Confirm with: kubectl get deploy,po -n that namespace.
# If Deployments are 1/0 with no Pending Pods, likely node taints: KYVERNO_TOLERATE_CONTROL_PLANE=true (default) adds control-plane tolerations.
# Optional 7th: PostSync hook short name (e.g. kyverno → argo-postsync-hooks/kyverno); empty = no second source.
_wl_argocd_render_kyverno() {
  local name="$1" ns="$2" repo_url="$3" chart="$4" version="$5" sync_wave="$6"
  local hook_short="${7:-}"
  local target="${version:-*}" hurl hpath hsref kz
  hurl=""; hpath=""; kz=""; hsref=""
  if is_true "${ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED:-true}" && [[ -n "$hook_short" ]]; then
    hurl="$(workload_postsync_hooks_discover_url 2>/dev/null || true)"
    if [[ -n "$hurl" ]]; then
      hsref="$(workload_postsync_hooks_discover_ref | sed "s/'/'\"'\"'/g")"
      hpath="$(workload_postsync_hooks_full_relpath "$hook_short")"
      kz="$(workload_postsync_kustomize_block_for_job "${hook_short}-smoketest")"
    else
      warn "ARGO_WORKLOAD_POSTSYNC_HOOKS: no git URL; skipping PostSync hook for ${name} (set ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL)."
    fi
  fi
  if [[ -n "$hurl" && -n "$hpath" && -n "$kz" ]]; then
    cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
    argocd.argoproj.io/compare-options: ServerSideDiff=true,IncludeMutationWebhook=true
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${ns}
  sources:
    - repoURL: ${repo_url}
      chart: ${chart}
      targetRevision: "${target}"
      helm:
        valuesObject:
          config:
            preserve: false
            webhookLabels:
              app.kubernetes.io/managed-by: argocd
$(_kyverno_argocd_values_toleration_fragment | sed 's/^/  /')
          admissionController:
            replicas: 1
          backgroundController:
            replicas: 1
          cleanupController:
            replicas: 1
          reportsController:
            replicas: 1
    - repoURL: ${hurl}
      path: ${hpath}
      targetRevision: '${hsref}'
$(printf '%s\n' "$kz" | sed 's/^/  /')
  ignoreDifferences:
    - group: admissionregistration.k8s.io
      kind: MutatingWebhookConfiguration
      jqPathExpressions:
        - .webhooks[]?.clientConfig.caBundle
    - group: admissionregistration.k8s.io
      kind: ValidatingWebhookConfiguration
      jqPathExpressions:
        - .webhooks[]?.clientConfig.caBundle
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
  else
    cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
    argocd.argoproj.io/compare-options: ServerSideDiff=true,IncludeMutationWebhook=true
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${ns}
  source:
    repoURL: ${repo_url}
    chart: ${chart}
    targetRevision: "${target}"
    helm:
      valuesObject:
        config:
          preserve: false
          webhookLabels:
            app.kubernetes.io/managed-by: argocd
$(_kyverno_argocd_values_toleration_fragment)
        admissionController:
          replicas: 1
        backgroundController:
          replicas: 1
        cleanupController:
          replicas: 1
        reportsController:
          replicas: 1
  ignoreDifferences:
    - group: admissionregistration.k8s.io
      kind: MutatingWebhookConfiguration
      jqPathExpressions:
        - .webhooks[]?.clientConfig.caBundle
    - group: admissionregistration.k8s.io
      kind: ValidatingWebhookConfiguration
      jqPathExpressions:
        - .webhooks[]?.clientConfig.caBundle
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
  fi
}

# In-cluster Argo: Helm (HTTP repo) + optional 8th arg PostSync hook short name (e.g. cert-manager).
_wl_argocd_render_helm() {
  local name="$1" ns="$2" repo_url="${3%/}" chart="$4" version="$5" sync_wave="$6" values_yaml="$7"
  local hook_short="${8:-}"
  local target="${version:-*}" hurl hpath hsref kz
  local indented_values="" indented_values_ms=""
  if [[ -n "$values_yaml" ]]; then
    indented_values="$(printf '%s\n' "$values_yaml" | sed 's/^/        /')"
    indented_values_ms="$(printf '%s\n' "$values_yaml" | sed 's/^/          /')"
  fi
  hurl=""; hpath=""; kz=""; hsref=""
  if is_true "${ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED:-true}" && [[ -n "$hook_short" ]]; then
    hurl="$(workload_postsync_hooks_discover_url 2>/dev/null || true)"
    if [[ -n "$hurl" ]]; then
      hsref="$(workload_postsync_hooks_discover_ref | sed "s/'/'\"'\"'/g")"
      hpath="$(workload_postsync_hooks_full_relpath "$hook_short")"
      kz="$(workload_postsync_kustomize_block_for_job "${hook_short}-smoketest")"
    else
      warn "ARGO_WORKLOAD_POSTSYNC_HOOKS: no git URL; skipping PostSync hook for ${name} (set ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL)."
    fi
  fi
  if [[ -n "$hurl" && -n "$hpath" && -n "$kz" ]]; then
    cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${ns}
  sources:
    - repoURL: ${repo_url}
      chart: ${chart}
      targetRevision: "${target}"
      helm:
        valuesObject:
$(if [[ -n "$indented_values_ms" ]]; then printf '%s\n' "$indented_values_ms"; else printf '          {}\n'; fi)
    - repoURL: ${hurl}
      path: ${hpath}
      targetRevision: '${hsref}'
$(printf '%s\n' "$kz" | sed 's/^/  /')
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
  else
    cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${ns}
  source:
    repoURL: ${repo_url}
    chart: ${chart}
    targetRevision: "${target}"
    helm:
      valuesObject:
$(if [[ -n "$indented_values" ]]; then printf '%s\n' "$indented_values"; else printf '        {}\n'; fi)
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
  fi
}

# OCI Helm + optional 7–10: two PostSync hook git paths + kustomize blocks (proxmox-csi PVC + rollout; empty = OCI only).
_wl_argocd_render_helm_oci() {
  local name="$1" ns="$2" oci_repo_url="$3" version="$4" sync_wave="$5" values_yaml="$6"
  local hook1_path="${7:-}" hook1_kz="${8:-}" hook2_path="${9:-}" hook2_kz="${10:-}"
  local target="${version:-*}" hurl sref
  local indented_values="" indented_values_ms=""
  if [[ -n "$values_yaml" ]]; then
    indented_values="$(printf '%s\n' "$values_yaml" | sed 's/^/        /')"
    indented_values_ms="$(printf '%s\n' "$values_yaml" | sed 's/^/          /')"
  fi
  hurl=""; sref=""
  if is_true "${ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED:-true}" && is_true "${PROXMOX_CSI_SMOKE_ENABLED:-true}" && [[ -n "$hook1_path" && -n "$hook1_kz" && -n "$hook2_path" && -n "$hook2_kz" ]]; then
    hurl="$(workload_postsync_hooks_discover_url 2>/dev/null || true)"
    if [[ -n "$hurl" ]]; then
      sref="$(workload_postsync_hooks_discover_ref | sed "s/'/'\"'\"'/g")"
    else
      warn "ARGO_WORKLOAD_POSTSYNC_HOOKS: no git URL; proxmox-csi will sync without PostSync hook Jobs (set ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL)."
    fi
  fi
  if [[ -n "$hurl" && -n "$sref" && -n "$hook1_path" && -n "$hook1_kz" && -n "$hook2_path" && -n "$hook2_kz" ]]; then
    cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${ns}
  sources:
    - repoURL: ${oci_repo_url}
      path: "."
      targetRevision: "${target}"
      helm:
        valuesObject:
$(if [[ -n "$indented_values_ms" ]]; then printf '%s\n' "$indented_values_ms"; else printf '          {}\n'; fi)
    - repoURL: ${hurl}
      path: ${hook1_path}
      targetRevision: '${sref}'
$(printf '%s\n' "$hook1_kz" | sed 's/^/  /')
    - repoURL: ${hurl}
      path: ${hook2_path}
      targetRevision: '${sref}'
$(printf '%s\n' "$hook2_kz" | sed 's/^/  /')
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
  else
    cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${ns}
  source:
    repoURL: ${oci_repo_url}
    path: "."
    targetRevision: "${target}"
    helm:
      valuesObject:
$(if [[ -n "$indented_values" ]]; then printf '%s\n' "$indented_values"; else printf '        {}\n'; fi)
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
  fi
}

# Git + Kustomize (optional 8: PostSync hook short name, second git source; same or different path prefix).
_wl_argocd_render_kustomize_git() {
  local name="$1" dest_ns="$2" repo_url="$3" rel_path="$4" ref="$5" sync_wave="$6" kustomize_block="$7"
  local hook_short="${8:-}"
  local safe_ref hsref hurl hpath kz
  safe_ref="$(printf '%s' "$ref" | sed "s/'/'\"'\"'/g")"
  hurl=""; hpath=""; kz=""; hsref=""
  if is_true "${ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED:-true}" && [[ -n "$hook_short" ]]; then
    hurl="$(workload_postsync_hooks_discover_url 2>/dev/null || true)"
    if [[ -n "$hurl" ]]; then
      hsref="$(workload_postsync_hooks_discover_ref | sed "s/'/'\"'\"'/g")"
      hpath="$(workload_postsync_hooks_full_relpath "$hook_short")"
      kz="$(workload_postsync_kustomize_block_for_job "${hook_short}-smoketest")"
    else
      warn "ARGO_WORKLOAD_POSTSYNC_HOOKS: no git URL; skipping PostSync hook for ${name} (set ARGO_WORKLOAD_POSTSYNC_HOOKS_GIT_URL)."
    fi
  fi
  if [[ -n "$hurl" && -n "$hpath" && -n "$kz" ]]; then
    {
      cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${dest_ns}
  sources:
    - repoURL: ${repo_url}
      path: ${rel_path}
      targetRevision: '${safe_ref}'
EOF
      printf '%s\n' "$kustomize_block" | sed 's/^/  /'
      cat <<EOF
    - repoURL: ${hurl}
      path: ${hpath}
      targetRevision: '${hsref}'
EOF
      printf '%s\n' "$kz" | sed 's/^/  /'
      cat <<EOF
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
    }
  else
    {
      cat <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ${name}
  namespace: ${WORKLOAD_ARGOCD_NAMESPACE}
  annotations:
    argocd.argoproj.io/sync-wave: "${sync_wave}"
spec:
  project: default
  destination:
    server: https://kubernetes.default.svc
    namespace: ${dest_ns}
  source:
    repoURL: ${repo_url}
    path: ${rel_path}
    targetRevision: '${safe_ref}'
EOF
      printf '%s\n' "$kustomize_block"
      cat <<EOF
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
EOF
    }
  fi
}

# OCI / Kustomize+git to Argo: use _wl_argocd_render_helm_oci and _wl_argocd_render_kustomize_git
# (in-cluster / workload destination) — the older management-cluster variants were removed as unused.

proxmox_csi_smoke_bootstrap_dir() { workload_postsync_hooks_bootstrap_dir; }

# Kubernetes version for the PostSync Job kubectl image: CAPI manifest heuristics, else WORKLOAD_KUBERNETES_VERSION.
proxmox_csi_smoke_k8s_version_for_image() {
  local v
  v="$(
  WORKLOAD_KUBERNETES_VERSION="${WORKLOAD_KUBERNETES_VERSION:-v1.35.0}" \
  python3 - "${CAPI_MANIFEST:-}" <<'PY'
import os, re, sys, pathlib
def failback():
    return os.environ.get("WORKLOAD_KUBERNETES_VERSION", "v1.35.0")
if len(sys.argv) < 2 or not (sys.argv[1] or "").strip():
    print(failback())
    raise SystemExit(0)
path = pathlib.Path(sys.argv[1].strip())
if not path.is_file():
    print(failback())
    raise SystemExit(0)
text = path.read_text()
for doc in re.split(r"^---\s*\n", text, flags=re.M):
    if "kind: Cluster" not in doc or "topology:" not in doc:
        continue
    sub = doc.split("topology:", 1)[1]
    head = "\n".join(sub.split("\n")[:120])
    m = re.search(r"(?m)^\s+version:\s*(v?[\d.]+)\s*(?:#.*)?$", head)
    if m:
        print(m.group(1).strip())
        raise SystemExit(0)
for doc in re.split(r"^---\s*\n", text, flags=re.M):
    if "kind: KubeadmControlPlane" not in doc:
        continue
    sub = doc[doc.find("spec:") :] if "spec:" in doc else doc
    m = re.search(r"(?m)^  version:\s*(v?[\d.]+)\s*(?:#.*)?$", sub)
    if m:
        print(m.group(1).strip())
        raise SystemExit(0)
print(failback())
PY
  )" || v=""
  [[ -n "$v" ]] || v="${WORKLOAD_KUBERNETES_VERSION:-v1.35.0}"
  printf '%s\n' "$v"
}

# registry.k8s.io/kubectl image tag (vX.Y.Z); minor-only like 1.35 -> v1.35.0 (Kubernetes standard release tags)
proxmox_csi_smoke_kubectl_oci_tag() {
  local ver
  ver="$(proxmox_csi_smoke_k8s_version_for_image)"
  ver="${ver#v}"
  if [[ "$ver" =~ ^[0-9]+\.[0-9]+$ ]]; then
    ver="${ver}.0"
  fi
  printf 'v%s\n' "$ver"
}

# Kustomize patch block for the proxmox-csi PVC PostSync hook (second/third Application source).
proxmox_csi_smoke_render_kustomize_block() {
  local ns sc img
  ns="${PROXMOX_CSI_NAMESPACE}"
  sc="${PROXMOX_CSI_STORAGE_CLASS_NAME}"
  img="$(workload_postsync_hooks_resolve_kubectl_image)"
  cat <<KZ
    kustomize:
      namespace: ${ns}
      patches:
        - target:
            group: batch
            version: v1
            kind: Job
            name: proxmox-csi-smoke
          patch: |
            - op: replace
              path: /spec/template/spec/containers/0/image
              value: '${img}'
            - op: replace
              path: /spec/template/spec/containers/0/env/0/value
              value: "${ns}"
            - op: replace
              path: /spec/template/spec/containers/0/env/1/value
              value: "${sc}"
KZ
}

# Argo CD on the CAPI workload is installed in-cluster (CAAPH HelmChartProxy and/or the Argo CD Operator); not on the kind management cluster.

# --- Workload Argo: Helm value snippets (VM single Service name, SPIRE+Keycloak OIDC) ----------------
workload_argocd_victoria_helm_values() {
  # Stable Service name = vmsingle; chart requires server.scrape (not top-level scrape). Extra job: all Endpoints
  # whose port name is metrics|http-metrics so Argo CD and other operators are scraped without prometheus.io annotations.
  cat <<'ENDVAL'
server:
  fullnameOverride: vmsingle
  scrape:
    enabled: true
    extraScrapeConfigs:
      - job_name: kubernetes-endpoints-metrics-ports
        kubernetes_sd_configs:
          - role: endpoints
        relabel_configs:
          - action: keep
            source_labels: [__meta_kubernetes_endpoint_port_name]
            regex: metrics|http-metrics
          - action: replace
            source_labels: [__meta_kubernetes_namespace]
            target_label: namespace
          - action: replace
            source_labels: [__meta_kubernetes_service_name]
            target_label: service
ENDVAL
}

# OpenTelemetry: chart no longer provides a default image; mode is required in NOTES/validation.
workload_argocd_opentelemetry_helm_values() {
  {
    echo "mode: ${OTEL_COLLECTOR_MODE:-deployment}"
    echo "image:"
    echo "  repository: ${OTEL_IMAGE_REPOSITORY:-otel/opentelemetry-collector-k8s}"
  }
}

workload_argocd_grafana_helm_values() {
  if ! is_true "${VICTORIAMETRICS_ENABLED:-true}"; then
    printf '%s\n' "service:
  type: ClusterIP"
    return 0
  fi
  cat <<ENDVAL
service:
  type: ClusterIP
datasources:
  datasources.yaml:
    apiVersion: 1
    datasources:
      - name: VictoriaMetrics
        type: prometheus
        url: http://vmsingle.${VICTORIAMETRICS_NAMESPACE}.svc:8428
        access: proxy
        isDefault: true
        jsonData:
          httpMethod: POST
          manageAlerts: true
          prometheusType: Prometheus
dashboardProviders:
  dashboardproviders.yaml:
    apiVersion: 1
    providers:
      - name: default
        orgId: 1
        folder: ""
        type: file
        disableDeletion: false
        editable: true
        options:
          path: /var/lib/grafana/dashboards/default
dashboards:
  default:
    k8s-cluster:
      gnetId: 7249
      revision: 1
      datasource: VictoriaMetrics
    k8s-api-server:
      gnetId: 12006
      revision: 1
      datasource: VictoriaMetrics
    victoriametrics-single:
      gnetId: 10229
      revision: 1
      datasource: VictoriaMetrics
ENDVAL
}

# Control-plane taints: without tolerations, spire-agent and spiffe-csi-driver DaemonSets may not run on
# single-CP (or all-tainted) CAPI nodes, so the SPIFFE CSI volume in the OIDC pod never gets a local agent socket
# (see: "dial unix .../spire-agent.sock: no such file"). Align with Kyverno: SPIRE_TOLERATE_CONTROL_PLANE.
_wl_argocd_spire_subchart_tolerations() {
  is_true "${SPIRE_TOLERATE_CONTROL_PLANE:-true}" || return 0
  cat <<'EOT'
spire-server:
  tolerations:
    - key: "node-role.kubernetes.io/control-plane"
      operator: Exists
      effect: NoSchedule
    - key: "node-role.kubernetes.io/master"
      operator: Exists
      effect: NoSchedule
spire-controller-manager:
  tolerations:
    - key: "node-role.kubernetes.io/control-plane"
      operator: Exists
      effect: NoSchedule
    - key: "node-role.kubernetes.io/master"
      operator: Exists
      effect: NoSchedule
spire-agent:
  tolerations:
    - key: "node-role.kubernetes.io/control-plane"
      operator: Exists
      effect: NoSchedule
    - key: "node-role.kubernetes.io/master"
      operator: Exists
      effect: NoSchedule
spiffe-csi-driver:
  tolerations:
    - key: "node-role.kubernetes.io/control-plane"
      operator: Exists
      effect: NoSchedule
    - key: "node-role.kubernetes.io/master"
      operator: Exists
      effect: NoSchedule
EOT
}

workload_argocd_spire_helm_values() {
  {
    echo "global:"
    echo "  spire:"
    echo "    clusterName: ${WORKLOAD_CLUSTER_NAME}"
    echo "    trustDomain: k8s.${WORKLOAD_CLUSTER_NAME}.local"
    if ! is_true "${SPIRE_HELM_ENABLE_GLOBAL_HOOKS:-false}"; then
      echo "  installAndUpgradeHooks:"
      echo "    enabled: false"
      echo "  deleteHooks:"
      echo "    enabled: false"
    fi
    _wl_argocd_spire_subchart_tolerations
    echo "spiffe-oidc-discovery-provider:"
    if is_true "${SPIRE_TOLERATE_CONTROL_PLANE:-true}"; then
      echo "  tolerations:"
      echo "    - key: \"node-role.kubernetes.io/control-plane\""
      echo "      operator: Exists"
      echo "      effect: NoSchedule"
      echo "    - key: \"node-role.kubernetes.io/master\""
      echo "      operator: Exists"
      echo "      effect: NoSchedule"
    fi
    if [[ "${SPIRE_OIDC_BUNDLE_SOURCE:-CSI}" == "ConfigMap" ]]; then
      echo "  bundleSource: ConfigMap"
    else
      echo "  bundleSource: CSI"
    fi
    echo "  config:"
    if is_true "$KEYCLOAK_ENABLED"; then
      echo "    setKeyUse: true"
    else
      echo "    setKeyUse: false"
    fi
    if is_true "$KEYCLOAK_ENABLED" && is_true "${SPIRE_OIDC_INSECURE_HTTP:-true}"; then
      echo "  tls:"
      echo "    spire:"
      echo "      enabled: false"
    fi
  }
}

workload_argocd_keycloak_helm_values() {
  if ! is_true "$KEYCLOAK_ENABLED"; then
    return 0
  fi
  local oidc_fqdn kc_host
  # keycloakx chart: fullname = <Release.Name>-<Chart.Name> when no override (Release = <workload>-keycloak, Chart = keycloakx).
  kc_host="${KEYCLOAK_KC_HOSTNAME:-}"
  if [[ -z "$kc_host" ]]; then
    kc_host="${WORKLOAD_CLUSTER_NAME}-keycloak-keycloakx.${KEYCLOAK_NAMESPACE}.svc.cluster.local"
  fi
  printf 'args:
  - start
extraEnv: |
  - name: KC_HOSTNAME_STRICT
    value: "%s"
  - name: KC_HOSTNAME
    value: "%s"
' "${KEYCLOAK_KC_HOSTNAME_STRICT:-false}" "$kc_host"
  if [[ -n "${KEYCLOAK_KC_DB:-}" ]]; then
    printf '  - name: KC_DB
    value: "%s"
' "${KEYCLOAK_KC_DB}"
  fi
  if is_true "$SPIRE_ENABLED"; then
    oidc_fqdn="${WORKLOAD_CLUSTER_NAME}-spire-spiffe-oidc-discovery-provider.${SPIRE_NAMESPACE}.svc.cluster.local"
    if is_true "${SPIRE_OIDC_INSECURE_HTTP:-true}"; then
      printf '  - name: SPIFFE_OIDC_WELL_KNOWN_URL
    value: "http://%s/.well-known/openid-configuration"
  - name: SPIFFE_OIDC_ISSUER_HOST
    value: "%s"
  - name: KEYCLOAK_SPIRE_IDP_HELP
    value: "Add an Identity Provider (OpenID v1) in Keycloak using SPIFFE_OIDC_WELL_KNOWN_URL; map SPIFFE SVIDs to users as needed."
' "$oidc_fqdn" "$oidc_fqdn"
    else
      printf '  - name: SPIFFE_OIDC_WELL_KNOWN_URL
    value: "https://%s/.well-known/openid-configuration"
  - name: SPIFFE_OIDC_ISSUER_HOST
    value: "%s"
' "$oidc_fqdn" "$oidc_fqdn"
    fi
  fi
  printf '\n'
}

# In-cluster Argo CD Applications: kyverno (wave 0) first, then the rest. Applied to the workload API server.
apply_workload_argocd_applications() {
  is_true "$ARGOCD_ENABLED" || return 0
  is_true "${WORKLOAD_ARGOCD_ENABLED:-true}" || return 0

  local tmp_dir manifest mgmt_kcfg workload_kcfg
  tmp_dir="$(mktemp -d)"
  manifest="${tmp_dir}/workload-argocd-applications.yaml"
  mgmt_kcfg="kind-${KIND_CLUSTER_NAME}"

  workload_kcfg="$(mktemp)"
  kubectl --context "$mgmt_kcfg" -n "$WORKLOAD_CLUSTER_NAMESPACE" \
    get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$workload_kcfg" \
    || die "Could not read workload kubeconfig to apply in-cluster Argo CD Applications."

  log "Rendering in-cluster Argo CD Applications on workload ${WORKLOAD_CLUSTER_NAME} (platform apps + PostSync hooks from argo-postsync-hooks/* when git URL is set)..."
  : > "$manifest"

  # sync-wave: -3 = metrics-server (kube-system), -2 = CSI (+ optional PostSync PVC/rollout hooks), 0+ = platform.
  if is_true "${ENABLE_WORKLOAD_METRICS_SERVER:-true}"; then
    local mvals
    mvals="$(workload_argocd_metrics_server_helm_values)"
    _wl_argocd_render_helm_git \
      "${WORKLOAD_CLUSTER_NAME}-metrics-server" \
      "kube-system" \
      "https://github.com/kubernetes-sigs/metrics-server" \
      "charts/metrics-server" \
      "$METRICS_SERVER_GIT_CHART_TAG" \
      "-3" \
      "$mvals" \
      "metrics-server" \
      "metrics-server" >> "$manifest"
  fi

  if is_true "$PROXMOX_CSI_ENABLED"; then
    load_csi_vars_from_config
    PROXMOX_CSI_URL="${PROXMOX_CSI_URL:-$(proxmox_api_json_url)}"
    [[ -n "$PROXMOX_CSI_URL" && -n "$PROXMOX_CSI_TOKEN_ID" && -n "$PROXMOX_CSI_TOKEN_SECRET" && -n "$PROXMOX_REGION" ]] \
      || die "Proxmox CSI credentials incomplete — cannot render in-cluster Argo Application."

    apply_proxmox_csi_config_secret_to_workload_cluster

    local csi_values csi_oci
    csi_values="$(cat <<YAML
existingConfigSecret: "${WORKLOAD_CLUSTER_NAME}-proxmox-csi-config"
existingConfigSecretKey: config.yaml
config:
  features:
    provider: ${PROXMOX_CSI_CONFIG_PROVIDER}
  clusters: []
storageClass:
  - name: "${PROXMOX_CSI_STORAGE_CLASS_NAME}"
    storage: "${PROXMOX_CSI_STORAGE}"
    reclaimPolicy: "${PROXMOX_CSI_RECLAIM_POLICY}"
    fstype: "${PROXMOX_CSI_FSTYPE}"
    annotations:
      storageclass.kubernetes.io/is-default-class: "${PROXMOX_CSI_DEFAULT_CLASS}"
YAML
)"
    csi_oci="${PROXMOX_CSI_CHART_REPO_URL%/}"
    if [[ "$csi_oci" != */"${PROXMOX_CSI_CHART_NAME}" ]]; then
      csi_oci="${csi_oci}/${PROXMOX_CSI_CHART_NAME}"
    fi
    if is_true "${PROXMOX_CSI_SMOKE_ENABLED:-true}" && is_true "${ARGO_WORKLOAD_POSTSYNC_HOOKS_ENABLED:-true}"; then
      local pvc_p roll_p k1 k2
      pvc_p="$(workload_postsync_hooks_full_relpath proxmox-csi-pvc)"
      roll_p="$(workload_postsync_hooks_full_relpath proxmox-csi-rollout)"
      k1="$(proxmox_csi_smoke_render_kustomize_block)"
      k2="$(workload_postsync_kustomize_block_for_job proxmox-csi-rollout-smoketest)"
      _wl_argocd_render_helm_oci \
        "${WORKLOAD_CLUSTER_NAME}-proxmox-csi" \
        "$PROXMOX_CSI_NAMESPACE" \
        "$csi_oci" \
        "$PROXMOX_CSI_CHART_VERSION" \
        "-2" \
        "$csi_values" \
        "$pvc_p" \
        "$k1" \
        "$roll_p" \
        "$k2" >> "$manifest"
    else
      _wl_argocd_render_helm_oci \
        "${WORKLOAD_CLUSTER_NAME}-proxmox-csi" \
        "$PROXMOX_CSI_NAMESPACE" \
        "$csi_oci" \
        "$PROXMOX_CSI_CHART_VERSION" \
        "-2" \
        "$csi_values" >> "$manifest"
    fi
  fi

  if is_true "$KYVERNO_ENABLED"; then
    _wl_argocd_render_kyverno \
      "${WORKLOAD_CLUSTER_NAME}-kyverno" \
      "$KYVERNO_NAMESPACE" \
      "$KYVERNO_CHART_REPO_URL" \
      "kyverno" \
      "$KYVERNO_CHART_VERSION" \
      "0" \
      "kyverno" >> "$manifest"
  fi

  if is_true "$CERT_MANAGER_ENABLED"; then
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-cert-manager" \
      "$CERT_MANAGER_NAMESPACE" \
      "$CERT_MANAGER_CHART_REPO_URL" \
      "cert-manager" \
      "$CERT_MANAGER_CHART_VERSION" \
      "1" \
      "crds:
  enabled: true" \
      "cert-manager" >> "$manifest"
  fi

  if is_true "$CROSSPLANE_ENABLED"; then
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-crossplane" \
      "$CROSSPLANE_NAMESPACE" \
      "$CROSSPLANE_CHART_REPO_URL" \
      "crossplane" \
      "$CROSSPLANE_CHART_VERSION" \
      "2" \
      "" \
      "crossplane" >> "$manifest"
  fi

  if is_true "$CNPG_ENABLED"; then
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-cnpg" \
      "$CNPG_NAMESPACE" \
      "$CNPG_CHART_REPO_URL" \
      "$CNPG_CHART_NAME" \
      "$CNPG_CHART_VERSION" \
      "2" \
      "" \
      "cnpg" >> "$manifest"
  fi

  if is_true "$EXTERNAL_SECRETS_ENABLED"; then
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-external-secrets" \
      "$EXTERNAL_SECRETS_NAMESPACE" \
      "$EXTERNAL_SECRETS_CHART_REPO_URL" \
      "external-secrets" \
      "$EXTERNAL_SECRETS_CHART_VERSION" \
      "3" \
      "" \
      "external-secrets" >> "$manifest"
  fi

  if is_true "$INFISICAL_OPERATOR_ENABLED"; then
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-infisical-secrets-operator" \
      "$INFISICAL_NAMESPACE" \
      "$INFISICAL_CHART_REPO_URL" \
      "$INFISICAL_CHART_NAME" \
      "$INFISICAL_CHART_VERSION" \
      "4" \
      "" \
      "infisical" >> "$manifest"
  fi

  if is_true "$SPIRE_ENABLED"; then
    # CRDs in the first platform wave (with metrics-server) so ClusterSPIFFEID exists before the spire app (e.g. spike-keeper) syncs.
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-spire-crds" \
      "$SPIRE_NAMESPACE" \
      "$SPIRE_CHART_REPO_URL" \
      "$SPIRE_CRDS_CHART_NAME" \
      "$SPIRE_CRDS_CHART_VERSION" \
      "-3" \
      "" \
      "" >> "$manifest"
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-spire" \
      "$SPIRE_NAMESPACE" \
      "$SPIRE_CHART_REPO_URL" \
      "$SPIRE_CHART_NAME" \
      "$SPIRE_CHART_VERSION" \
      "5" \
      "$(workload_argocd_spire_helm_values)" \
      "spire" >> "$manifest"
  fi

  if is_true "$VICTORIAMETRICS_ENABLED"; then
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-victoria-metrics-single" \
      "$VICTORIAMETRICS_NAMESPACE" \
      "$VICTORIAMETRICS_CHART_REPO_URL" \
      "$VICTORIAMETRICS_CHART_NAME" \
      "$VICTORIAMETRICS_CHART_VERSION" \
      "6" \
      "$(workload_argocd_victoria_helm_values)" \
      "victoria-metrics" >> "$manifest"
  fi

  if is_true "$OTEL_ENABLED"; then
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-opentelemetry-collector" \
      "$OTEL_NAMESPACE" \
      "$OTEL_CHART_REPO_URL" \
      "$OTEL_CHART_NAME" \
      "$OTEL_CHART_VERSION" \
      "6" \
      "$(workload_argocd_opentelemetry_helm_values)" \
      "opentelemetry" >> "$manifest"
  fi

  if is_true "$GRAFANA_ENABLED"; then
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-grafana" \
      "$GRAFANA_NAMESPACE" \
      "$GRAFANA_CHART_REPO_URL" \
      "grafana" \
      "$GRAFANA_CHART_VERSION" \
      "6" \
      "$(workload_argocd_grafana_helm_values)" \
      "grafana" >> "$manifest"
  fi

  if is_true "$BACKSTAGE_ENABLED"; then
    if [[ -z "${BACKSTAGE_CHART_REPO_URL:-}" ]]; then
      warn "BACKSTAGE_ENABLED but BACKSTAGE_CHART_REPO_URL is empty — set a Helm repo and chart name, or set BACKSTAGE_ENABLED=false. Skipping Backstage."
    else
      _wl_argocd_render_helm \
        "${WORKLOAD_CLUSTER_NAME}-backstage" \
        "$BACKSTAGE_NAMESPACE" \
        "$BACKSTAGE_CHART_REPO_URL" \
        "$BACKSTAGE_CHART_NAME" \
        "$BACKSTAGE_CHART_VERSION" \
        "7" \
        "" \
        "backstage" >> "$manifest"
    fi
  fi

  if is_true "$KEYCLOAK_ENABLED"; then
    _wl_argocd_render_helm \
      "${WORKLOAD_CLUSTER_NAME}-keycloak" \
      "$KEYCLOAK_NAMESPACE" \
      "$KEYCLOAK_CHART_REPO_URL" \
      "$KEYCLOAK_CHART_NAME" \
      "$KEYCLOAK_CHART_VERSION" \
      "8" \
      "$(workload_argocd_keycloak_helm_values)" \
      "keycloak" >> "$manifest"
  fi

  if is_true "$KEYCLOAK_OPERATOR_ENABLED" && is_true "$KEYCLOAK_ENABLED"; then
    if [[ -z "${KEYCLOAK_OPERATOR_GIT_URL:-}" ]]; then
      warn "KEYCLOAK_OPERATOR_ENABLED but KEYCLOAK_OPERATOR_GIT_URL is empty — skipping Keycloak operator Application."
    else
      _wl_argocd_render_kustomize_git \
        "${WORKLOAD_CLUSTER_NAME}-keycloak-realm-operator" \
        "$KEYCLOAK_OPERATOR_NAMESPACE" \
        "$KEYCLOAK_OPERATOR_GIT_URL" \
        "$KEYCLOAK_OPERATOR_GIT_PATH" \
        "$KEYCLOAK_OPERATOR_GIT_REF" \
        "9" \
        "    kustomize: {}" \
        "" >> "$manifest"
    fi
  fi

  [[ -s "$manifest" ]] || { log "No in-cluster Argo CD Applications to apply (all add-ons disabled)."; rm -f "$workload_kcfg"; rm -rf "$tmp_dir"; return 0; }

  KUBECONFIG="$workload_kcfg" kubectl apply -f "$manifest" \
    || die "Failed to apply in-cluster Argo CD Applications on the workload cluster."

  rm -f "$workload_kcfg"
  rm -rf "$tmp_dir"
  log "In-cluster Argo CD Applications submitted on the workload."
}

wait_for_workload_argocd_applications_healthy() {
  is_true "$ARGOCD_ENABLED" || return 0
  is_true "${WORKLOAD_ARGOCD_ENABLED:-true}" || return 0

  local mgmt_kcfg="kind-${KIND_CLUSTER_NAME}" workload_kcfg
  local -a apps=()
  is_true "${ENABLE_WORKLOAD_METRICS_SERVER:-true}" && apps+=("${WORKLOAD_CLUSTER_NAME}-metrics-server")
  is_true "$PROXMOX_CSI_ENABLED"        && apps+=("${WORKLOAD_CLUSTER_NAME}-proxmox-csi")
  is_true "$KYVERNO_ENABLED"            && apps+=("${WORKLOAD_CLUSTER_NAME}-kyverno")
  is_true "$CERT_MANAGER_ENABLED"        && apps+=("${WORKLOAD_CLUSTER_NAME}-cert-manager")
  is_true "$CROSSPLANE_ENABLED"          && apps+=("${WORKLOAD_CLUSTER_NAME}-crossplane")
  is_true "$CNPG_ENABLED"                && apps+=("${WORKLOAD_CLUSTER_NAME}-cnpg")
  is_true "$EXTERNAL_SECRETS_ENABLED"   && apps+=("${WORKLOAD_CLUSTER_NAME}-external-secrets")
  is_true "$INFISICAL_OPERATOR_ENABLED" && apps+=("${WORKLOAD_CLUSTER_NAME}-infisical-secrets-operator")
  is_true "$SPIRE_ENABLED"               && apps+=("${WORKLOAD_CLUSTER_NAME}-spire-crds" "${WORKLOAD_CLUSTER_NAME}-spire")
  is_true "$VICTORIAMETRICS_ENABLED"     && apps+=("${WORKLOAD_CLUSTER_NAME}-victoria-metrics-single")
  is_true "$OTEL_ENABLED"                && apps+=("${WORKLOAD_CLUSTER_NAME}-opentelemetry-collector")
  is_true "$GRAFANA_ENABLED"             && apps+=("${WORKLOAD_CLUSTER_NAME}-grafana")
  is_true "$BACKSTAGE_ENABLED" && [[ -n "${BACKSTAGE_CHART_REPO_URL:-}" ]] && apps+=("${WORKLOAD_CLUSTER_NAME}-backstage")
  is_true "$KEYCLOAK_ENABLED"            && apps+=("${WORKLOAD_CLUSTER_NAME}-keycloak")
  is_true "$KEYCLOAK_OPERATOR_ENABLED" && is_true "$KEYCLOAK_ENABLED" && [[ -n "${KEYCLOAK_OPERATOR_GIT_URL:-}" ]] && apps+=("${WORKLOAD_CLUSTER_NAME}-keycloak-realm-operator")

  if [[ ${#apps[@]} -eq 0 ]]; then
    log "No in-cluster Argo Applications to wait for."
  else
    workload_kcfg="$(mktemp)"
    kubectl --context "$mgmt_kcfg" -n "$WORKLOAD_CLUSTER_NAMESPACE" \
      get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -o jsonpath='{.data.value}' | base64 -d > "$workload_kcfg" \
      || die "Could not read workload kubeconfig to wait for in-cluster Argo CD Applications."

    local app
    for app in "${apps[@]}"; do
      log "Waiting for Argo Application ${app} (workload) to become Synced+Healthy..."
      if ! KUBECONFIG="$workload_kcfg" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" wait \
        --for=jsonpath='{.status.sync.status}'=Synced "application/${app}" --timeout=30m; then
        rm -f "$workload_kcfg"
        die "Argo Application ${app} (workload) did not reach Synced."
      fi
      if ! KUBECONFIG="$workload_kcfg" kubectl -n "$WORKLOAD_ARGOCD_NAMESPACE" wait \
        --for=jsonpath='{.status.health.status}'=Healthy "application/${app}" --timeout=30m; then
        rm -f "$workload_kcfg"
        die "Argo Application ${app} (workload) did not reach Healthy."
      fi
    done
    rm -f "$workload_kcfg"
  fi
  log "All in-cluster Argo CD Applications on the workload are Synced+Healthy."
}

wait_for_workload_cluster_ready() {
  local kubeconfig_path attempt

  discover_workload_cluster_identity "$CAPI_MANIFEST"

  # Available=True is the aggregate condition: True only once
  # InfrastructureReady + ControlPlaneAvailable + WorkersAvailable + RemoteConnectionProbe are all True.
  # In CAPI v1.11, v1beta2-style conditions are written directly into .status.conditions
  # (the separate .status.v1beta2.conditions path is not used), so the legacy condition
  # walker picks them up.
  log "Waiting for workload cluster ${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME} Available..."
  kubectl wait cluster "$WORKLOAD_CLUSTER_NAME" \
    --namespace "$WORKLOAD_CLUSTER_NAMESPACE" \
    --for=condition=Available \
    --timeout=60m

  kubeconfig_path="$(mktemp)"
  clusterctl get kubeconfig "$WORKLOAD_CLUSTER_NAME" \
    --config "$BOOTSTRAP_CLUSTERCTL_CONFIG_PATH" \
    --namespace "$WORKLOAD_CLUSTER_NAMESPACE" > "$kubeconfig_path" \
    || die "Failed to fetch kubeconfig for workload cluster ${WORKLOAD_CLUSTER_NAME} after Available=True."
  log "Workload cluster Available; kubeconfig fetched."

  log "Waiting for Cilium rollout in workload cluster ${WORKLOAD_CLUSTER_NAME}..."
  KUBECONFIG="$kubeconfig_path" kubectl rollout status daemonset/cilium -n kube-system --timeout=20m
  KUBECONFIG="$kubeconfig_path" kubectl rollout status deployment/cilium-operator -n kube-system --timeout=20m
  KUBECONFIG="$kubeconfig_path" kubectl wait nodes --all --for=condition=Ready --timeout=20m

  rm -f "$kubeconfig_path"
}

purge_stale_host_networking() {
  log "Purging stale host networking state from previous kind/CNI runs..."

  # Remove leftover CNI config files
  if [[ -d /etc/cni/net.d ]]; then
    RUN_PRIVILEGED find /etc/cni/net.d -maxdepth 1 -type f \
      \( -name '*kindnet*' -o -name '*cilium*' -o -name '*flannel*' -o -name '*cni*' \) \
      -delete 2>/dev/null || true
  fi

  # Remove leftover CNI state
  if [[ -d /var/lib/cni ]]; then
    RUN_PRIVILEGED rm -rf /var/lib/cni/networks /var/lib/cni/results 2>/dev/null || true
  fi

  # Remove stale kind bridge interfaces left behind by previous kind clusters.
  # Only target interfaces with names that are unambiguously kind-owned:
  #   kind*   — the kind bridge (e.g. kind, kindnet)
  #   lxc*    — created by kind node containers
  # Deliberately avoid br-*, veth*, cni* — those can belong to live Docker
  # networks or other running containers unrelated to kind.
  local iface
  for iface in $(ip link show 2>/dev/null \
      | awk -F': ' '/^[0-9]+: (lxc|kind)/{print $2}' \
      | cut -d@ -f1); do
    RUN_PRIVILEGED ip link delete "$iface" 2>/dev/null || true
  done

  # Flush any stale iptables rules injected by Cilium / CNI
  if command -v iptables >/dev/null 2>&1; then
    # Remove Cilium-owned chains (non-destructive: only named chains)
    for table in filter nat mangle; do
      RUN_PRIVILEGED iptables -t "$table" -S 2>/dev/null \
        | awk '/^-N CILIUM/{print $2}' \
        | xargs -I{} sh -c \
          "iptables -t $table -F {} 2>/dev/null; iptables -t $table -X {} 2>/dev/null" \
          || true
    done
  fi

  # Remove leftover kernel-level network namespaces (kind creates named netns per node)
  if command -v ip >/dev/null 2>&1; then
    for ns in $(ip netns list 2>/dev/null | awk '{print $1}' | grep -E '^cni-'); do
      RUN_PRIVILEGED ip netns delete "$ns" 2>/dev/null || true
    done
  fi

  log "Host networking state purged."
}

extract_identity_tf_inputs_from_state() {
  local state_file="$1"

  python3 - "$state_file" <<'PY'
import json
import re
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as fh:
    payload = json.load(fh)

vals = {
    "cluster_set_id": "",
    "csi_user_id": "",
    "csi_token_prefix": "",
    "capi_user_id": "",
    "capi_token_prefix": "",
}

for resource in payload.get("resources", []):
    rtype = resource.get("type", "")
    for inst in resource.get("instances", []):
        attrs = inst.get("attributes", {})
        idx = str(inst.get("index_key", ""))

        if rtype == "proxmox_virtual_environment_user" and idx in {"csi", "capi"}:
            vals[f"{idx}_user_id"] = attrs.get("user_id", "")

        if rtype == "proxmox_virtual_environment_user_token" and idx in {"csi", "capi"}:
            token_name = attrs.get("token_name", "")
            match = re.match(r"(.+)-([^-]+)$", token_name)
            if match:
                vals[f"{idx}_token_prefix"] = match.group(1)
                if not vals["cluster_set_id"]:
                    vals["cluster_set_id"] = match.group(2)

        if rtype == "proxmox_virtual_environment_role" and not vals["cluster_set_id"]:
            role_id = attrs.get("role_id", "")
            match = re.match(r"Kubernetes-(?:CSI|CAPI)-(.+)$", role_id)
            if match:
                vals["cluster_set_id"] = match.group(1)

for key in [
    "cluster_set_id",
    "csi_user_id",
    "csi_token_prefix",
    "capi_user_id",
    "capi_token_prefix",
]:
    print(vals[key])
PY
}

destroy_proxmox_identity_terraform_state() {
  local state_dir="$1" state_file endpoint api_token
  local cluster_set_id csi_user_id csi_token_prefix capi_user_id capi_token_prefix
  local -a tf_inputs tf_vars

  state_file="${state_dir}/terraform.tfstate"
  [[ -f "$state_file" ]] || {
    log "No Terraform state file found at ${state_file}; skipping Terraform destroy."
    return 0
  }

  command -v tofu >/dev/null 2>&1 || die "OpenTofu (tofu) is required to destroy existing Proxmox identity resources during purge."

  # Admin credentials should be loaded from the central config secret.

  [[ -n "$PROXMOX_URL" ]] || die "Cannot purge Terraform identities: PROXMOX_URL is required."
  [[ -n "$PROXMOX_ADMIN_USERNAME" ]] || die "Cannot purge Terraform identities: PROXMOX_ADMIN_USERNAME is required."
  [[ -n "$PROXMOX_ADMIN_TOKEN" ]] || die "Cannot purge Terraform identities: PROXMOX_ADMIN_TOKEN is required."

  mapfile -t tf_inputs < <(extract_identity_tf_inputs_from_state "$state_file")
  cluster_set_id="${tf_inputs[0]:-}"
  csi_user_id="${tf_inputs[1]:-}"
  csi_token_prefix="${tf_inputs[2]:-}"
  capi_user_id="${tf_inputs[3]:-}"
  capi_token_prefix="${tf_inputs[4]:-}"

  [[ -n "$cluster_set_id" ]] || die "Cannot determine cluster_set_id from Terraform state ${state_file}."
  [[ -n "$csi_user_id" ]] || die "Cannot determine csi_user_id from Terraform state ${state_file}."
  [[ -n "$csi_token_prefix" ]] || die "Cannot determine csi_token_prefix from Terraform state ${state_file}."
  [[ -n "$capi_user_id" ]] || die "Cannot determine capi_user_id from Terraform state ${state_file}."
  [[ -n "$capi_token_prefix" ]] || die "Cannot determine capi_token_prefix from Terraform state ${state_file}."

  endpoint="$PROXMOX_URL"
  api_token="${PROXMOX_ADMIN_USERNAME}=${PROXMOX_ADMIN_TOKEN}"

  tf_vars=(
    -var "cluster_set_id=${cluster_set_id}"
    -var "csi_user_id=${csi_user_id}"
    -var "csi_token_prefix=${csi_token_prefix}"
    -var "capi_user_id=${capi_user_id}"
    -var "capi_token_prefix=${capi_token_prefix}"
  )

  log "Destroying Terraform-managed Proxmox identity resources before purge..."
  PROXMOX_VE_ENDPOINT="$endpoint" \
  PROXMOX_VE_API_TOKEN="$api_token" \
  PROXMOX_VE_INSECURE="$PROXMOX_ADMIN_INSECURE" \
  tofu -chdir="$state_dir" init -upgrade >/dev/null

  PROXMOX_VE_ENDPOINT="$endpoint" \
  PROXMOX_VE_API_TOKEN="$api_token" \
  PROXMOX_VE_INSECURE="$PROXMOX_ADMIN_INSECURE" \
  tofu -chdir="$state_dir" destroy -auto-approve -input=false "${tf_vars[@]}"

  log "Terraform-managed Proxmox identity resources destroyed."
}

delete_workload_cluster_before_kind_deletion() {
  local kube_ctx cluster_name namespace

  kube_ctx="kind-${KIND_CLUSTER_NAME}"
  cluster_name="${WORKLOAD_CLUSTER_NAME:-}"
  namespace="${WORKLOAD_CLUSTER_NAMESPACE:-default}"

  if [[ -s "$CAPI_MANIFEST" ]]; then
    discover_workload_cluster_identity "$CAPI_MANIFEST" || true
    cluster_name="${WORKLOAD_CLUSTER_NAME:-$cluster_name}"
    namespace="${WORKLOAD_CLUSTER_NAMESPACE:-$namespace}"
  fi

  if [[ -z "$cluster_name" ]]; then
    warn "WORKLOAD_CLUSTER_NAME is empty; skipping workload cluster deletion before kind teardown."
    return 0
  fi

  if ! kubectl --context "$kube_ctx" get cluster "$cluster_name" -n "$namespace" >/dev/null 2>&1; then
    log "No workload Cluster ${namespace}/${cluster_name} found on ${kube_ctx}; continuing with kind deletion."
    return 0
  fi

  log "Deleting workload Cluster ${namespace}/${cluster_name} before deleting kind cluster ${KIND_CLUSTER_NAME}..."
  kubectl --context "$kube_ctx" delete cluster "$cluster_name" -n "$namespace" --ignore-not-found

  log "Waiting for workload Cluster ${namespace}/${cluster_name} to be deleted..."
  kubectl --context "$kube_ctx" wait --for=delete "cluster/${cluster_name}" -n "$namespace" --timeout=30m
}

purge_generated_artifacts() {
  local state_dir

  state_dir="${HOME}/.bootstrap-capi/proxmox-identity-terraform"

  log "Purging generated files and Terraform state..."

  # Delete the workload CAPI Cluster before revoking the Terraform-managed
  # CAPI token — otherwise CAPMOX loses the credentials it needs to destroy
  # Proxmox VMs, leaving orphaned guests. This requires the management kind
  # cluster to still be running.
  if command -v kind >/dev/null 2>&1 \
    && command -v kubectl >/dev/null 2>&1 \
    && (kind get clusters 2>/dev/null | tr -d '\r' | contains_line "$KIND_CLUSTER_NAME"); then
    kubectl --context "kind-${KIND_CLUSTER_NAME}" delete namespace "$PROXMOX_BOOTSTRAP_SECRET_NAMESPACE" \
      --ignore-not-found --wait=false >/dev/null 2>&1 || true
    delete_workload_cluster_before_kind_deletion
    capi_manifest_delete_secret
  else
    warn "Management kind cluster '${KIND_CLUSTER_NAME}' not present; skipping workload cluster deletion before Terraform destroy (any leftover Proxmox VMs must be cleaned up manually)."
  fi

  if [[ -d "$state_dir" ]]; then
    destroy_proxmox_identity_terraform_state "$state_dir"
  fi

  [[ -n "${CAPI_MANIFEST:-}" ]] && rm -f "$CAPI_MANIFEST"
  [[ -n "${PROXMOX_CSI_CONFIG:-}" ]] && rm -f "$PROXMOX_CSI_CONFIG"
  [[ -n "${PROXMOX_ADMIN_CONFIG:-}" && -f "$PROXMOX_ADMIN_CONFIG" ]] && rm -f "$PROXMOX_ADMIN_CONFIG"
  [[ -n "${CLUSTERCTL_CFG:-}" && -f "$CLUSTERCTL_CFG" ]] && rm -f "$CLUSTERCTL_CFG"
  rm -f "$PROXMOX_IDENTITY_TF"
  rm -rf "$state_dir" "$CAPMOX_BUILD_DIR" ./cluster-api ./cluster-api-ipam-provider-in-cluster

  log "Purge complete."
}

ensure_proxmox_admin_config() {
  local missing_admin_cfg=()

  merge_proxmox_bootstrap_secrets_from_kind || true
  if _proxmox_admin_cfg_file_present; then
    PROXMOX_URL="${PROXMOX_URL:-$(_get_yaml_value "$PROXMOX_ADMIN_CONFIG" PROXMOX_URL)}"
    PROXMOX_ADMIN_USERNAME="${PROXMOX_ADMIN_USERNAME:-$(_get_yaml_value "$PROXMOX_ADMIN_CONFIG" PROXMOX_ADMIN_USERNAME)}"
    PROXMOX_ADMIN_TOKEN="${PROXMOX_ADMIN_TOKEN:-$(_get_yaml_value "$PROXMOX_ADMIN_CONFIG" PROXMOX_ADMIN_TOKEN)}"
  fi

  if [[ -n "$PROXMOX_URL" && -n "$PROXMOX_ADMIN_USERNAME" && -n "$PROXMOX_ADMIN_TOKEN" ]]; then
    return 0
  fi

  [[ -z "$PROXMOX_URL" ]] && missing_admin_cfg+=(PROXMOX_URL)
  [[ -z "$PROXMOX_ADMIN_USERNAME" ]] && missing_admin_cfg+=(PROXMOX_ADMIN_USERNAME)
  [[ -z "$PROXMOX_ADMIN_TOKEN" ]] && missing_admin_cfg+=(PROXMOX_ADMIN_TOKEN)

  if [[ ! -t 0 ]]; then
    die "Missing admin Proxmox configuration: ${missing_admin_cfg[*]}. Set them via environment variables, kind Secret ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME} (admin API), or PROXMOX_ADMIN_CONFIG to a legacy local YAML (not written by this script by default)."
  fi

  if _proxmox_admin_cfg_file_present; then
    warn "Using ${PROXMOX_ADMIN_CONFIG} to supply missing values only; prefer kind Secrets in ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}."
  fi

  if confirm "Enter Proxmox admin API credentials interactively for Terraform bootstrap?"; then
    local _admin_url _admin_username _admin_token

    _admin_url="$PROXMOX_URL"
    _admin_username="$PROXMOX_ADMIN_USERNAME"
    _admin_token="$PROXMOX_ADMIN_TOKEN"

    if [[ -z "$_admin_url" ]]; then
    printf '\033[1;36m[?]\033[0m Proxmox VE URL (e.g. https://pve.example:8006): ' >&2
    read -r _admin_url
    fi

    if [[ -z "$_admin_username" ]]; then
    printf '\033[1;36m[?]\033[0m Proxmox admin username token ID (e.g. root@pam!capi-bootstrap): ' >&2
    read -r _admin_username
    fi

    if [[ -z "$_admin_token" ]]; then
    printf '\033[1;36m[?]\033[0m Proxmox admin token secret (UUID): ' >&2
    read -rs _admin_token; echo >&2
    fi

    PROXMOX_URL="$_admin_url"
    PROXMOX_ADMIN_USERNAME="$_admin_username"
    PROXMOX_ADMIN_TOKEN="$_admin_token"
    sync_bootstrap_config_to_kind || true
    sync_proxmox_bootstrap_literal_credentials_to_kind || true
    if ! persist_local_secrets; then
      log "Local CSI / extra file persistence is off; admin API identity is still synced to kind when the cluster is reachable. No proxmox-admin file is written by default."
    fi
    unset _admin_url _admin_username _admin_token
    return 0
  fi

  warn "Skipping interactive creation. Add admin API identity to kind Secret ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME} (data key ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_KEY:-proxmox-admin.yaml}, or flat keys for migration), export the variables, or set PROXMOX_ADMIN_CONFIG to a legacy file you maintain (not auto-written here)."
  warn "Expected format:"
  cat >&2 <<'EXAMPLE'

  PROXMOX_URL: "https://pve.example:8006"
  PROXMOX_ADMIN_USERNAME: "root@pam!capi-bootstrap"
  PROXMOX_ADMIN_TOKEN: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"

EXAMPLE
  printf '\033[1;33m[?]\033[0m Press ENTER once you have set kind Secrets, env, or a legacy admin YAML (PROXMOX_ADMIN_CONFIG)...' >&2
  read -r _
  merge_proxmox_bootstrap_secrets_from_kind || true
  if _proxmox_admin_cfg_file_present; then
    PROXMOX_URL="${PROXMOX_URL:-$(_get_yaml_value "$PROXMOX_ADMIN_CONFIG" PROXMOX_URL)}"
    PROXMOX_ADMIN_USERNAME="${PROXMOX_ADMIN_USERNAME:-$(_get_yaml_value "$PROXMOX_ADMIN_CONFIG" PROXMOX_ADMIN_USERNAME)}"
    PROXMOX_ADMIN_TOKEN="${PROXMOX_ADMIN_TOKEN:-$(_get_yaml_value "$PROXMOX_ADMIN_CONFIG" PROXMOX_ADMIN_TOKEN)}"
  fi
  if [[ -z "${PROXMOX_URL:-}" || -z "${PROXMOX_ADMIN_USERNAME:-}" || -z "${PROXMOX_ADMIN_TOKEN:-}" ]]; then
    die "Proxmox admin still unset: not in kind Secrets (see ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}), not in the environment, and not in PROXMOX_ADMIN_CONFIG. Aborting."
  fi
  log "Continuing with admin credentials from kind, environment, or legacy PROXMOX_ADMIN_CONFIG file."
}

# Parse command-line options

try_load_bootstrap_config_from_kind

parse_options "$@"
reapply_workload_git_defaults

if [[ -z "${KIND_CLUSTER_NAME:-}" && -n "${CLUSTER_NAME:-}" ]]; then
  KIND_CLUSTER_NAME="$CLUSTER_NAME"
fi
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-capi-provisioner}"

if [[ "${WORKLOAD_GITOPS_MODE:-caaph}" != "caaph" ]]; then
  die "Only WORKLOAD_GITOPS_MODE=caaph is supported (got: ${WORKLOAD_GITOPS_MODE})."
fi
if is_true "$ARGOCD_ENABLED" && is_true "${WORKLOAD_ARGOCD_ENABLED:-true}"; then
  [[ -n "${WORKLOAD_APP_OF_APPS_GIT_URL:-}" ]] \
    || die "CAAPH requires WORKLOAD_APP_OF_APPS_GIT_URL when Argo is enabled on the workload (root app-of-apps; Application metadata.name = ${WORKLOAD_CLUSTER_NAME:-<cluster>}). Ref: https://cluster-api.sigs.k8s.io/tasks/workload-bootstrap-gitops"
fi

# Ensure kind cluster context is in KUBECONFIG if the cluster exists,
# so standalone commands (--workload-rollout, --argocd-print-access, --kind-backup / --kind-restore) can use it.
if command -v kind >/dev/null 2>&1 && command -v kubectl >/dev/null 2>&1; then
  if kind get clusters 2>/dev/null | tr -d '\r' | grep -qx "$KIND_CLUSTER_NAME"; then
    if ! kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | grep -qx "kind-${KIND_CLUSTER_NAME}"; then
      log "Merging kubeconfig for existing kind cluster '${KIND_CLUSTER_NAME}' (context kind-${KIND_CLUSTER_NAME})..."
      kind export kubeconfig --name "$KIND_CLUSTER_NAME" >/dev/null 2>&1 || true
    fi
  fi
fi

# At most one standalone mode: workload vs (Argo print and/or port-forward, workload only) vs kind backup/restore.
_argocd_ops_standalone=false
is_true "${ARGOCD_PRINT_ACCESS_STANDALONE:-false}" && _argocd_ops_standalone=true
is_true "${ARGOCD_PORT_FORWARD_STANDALONE:-false}" && _argocd_ops_standalone=true
_modes=0
is_true "${WORKLOAD_ROLLOUT_STANDALONE:-false}" && _modes=$((_modes + 1))
is_true "$_argocd_ops_standalone" && _modes=$((_modes + 1))
[[ -n "${BOOTSTRAP_KIND_STATE_OP:-}" ]] && _modes=$((_modes + 1))
if ((_modes > 1)); then
  die "Use only one of: --workload-rollout, --argocd-print-access / --argocd-port-forward, --kind-backup, --kind-restore."
fi

if [[ "${BOOTSTRAP_KIND_STATE_OP:-}" = "backup" ]]; then
  require_cmd kubectl
  require_cmd python3
  command -v gzip >/dev/null 2>&1 || die "gzip is required for --kind-backup"
  merge_proxmox_bootstrap_secrets_from_kind || true
  kind_bootstrap_state_backup "${BOOTSTRAP_KIND_BACKUP_OUT:-}"
  exit $?
fi
if [[ "${BOOTSTRAP_KIND_STATE_OP:-}" = "restore" ]]; then
  require_cmd kubectl
  require_cmd python3
  merge_proxmox_bootstrap_secrets_from_kind || true
  kind_bootstrap_state_restore "${BOOTSTRAP_KIND_STATE_PATH}"
  exit $?
fi

# Used by --workload-rollout capi|all: force replacement of control-plane and worker Machines (clusterctl is most reliable).
workload_rollout_capi_touch_rollout() {
  local ctx now _rkcfg _rkctx kcp md kn st _ok
  ctx="kind-${KIND_CLUSTER_NAME}"
  # RFC3339 with ms — repeat runs are not a no-op in the same second.
  now="$(python3 -c 'from datetime import datetime, timezone; d=datetime.now(timezone.utc); print(d.strftime("%Y-%m-%dT%H:%M:%S") + ".{:03d}Z".format(d.microsecond // 1000))')"
  local -a _rollout_kcps _rollout_mds
  mapfile -t _rollout_kcps < <(kubectl --context "$ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" get kubeadmcontrolplane -l "cluster.x-k8s.io/cluster-name=${WORKLOAD_CLUSTER_NAME}" -o name 2>/dev/null) || true
  mapfile -t _rollout_mds < <(kubectl --context "$ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" get machinedeployment -l "cluster.x-k8s.io/cluster-name=${WORKLOAD_CLUSTER_NAME}" -o name 2>/dev/null) || true

  if ((${#_rollout_kcps[@]} == 0)); then
    warn "workload-rollout: no KubeadmControlPlane with label cluster.x-k8s.io/cluster-name=${WORKLOAD_CLUSTER_NAME} in ${WORKLOAD_CLUSTER_NAMESPACE} — nothing to roll for the control plane."
  fi
  if ((${#_rollout_mds[@]} == 0)); then
    warn "workload-rollout: no MachineDeployment with label cluster.x-k8s.io/cluster-name=${WORKLOAD_CLUSTER_NAME} in ${WORKLOAD_CLUSTER_NAMESPACE} — nothing to roll for workers."
  fi

  st="$(kubectl --context "$ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" get cluster "$WORKLOAD_CLUSTER_NAME" -o jsonpath='{.spec.paused}' 2>/dev/null || true)"
  if [[ "$st" == "true" ]]; then
    warn "workload-rollout: Cluster ${WORKLOAD_CLUSTER_NAME} has spec.paused=true — CAPI will not roll Machines until the Cluster is unpaused."
  fi

  for md in "${_rollout_mds[@]}"; do
    [[ -n "$md" ]] || continue
    st="$(kubectl --context "$ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" get "$md" -o jsonpath='{.spec.strategy.type}' 2>/dev/null || true)"
    if [[ "$st" == "OnDelete" ]]; then
      warn "workload-rollout: ${md} uses spec.strategy.type=OnDelete — CAPI does not create replacement Machines until existing Machines are deleted. Use RollingUpdate, or delete Machine objects, or \`kubectl delete machine <name>\` for each node to replace."
    fi
  done

  _rkcfg=""
  if command -v clusterctl >/dev/null 2>&1; then
    _rkcfg="$(mktemp)" || {
      warn "workload-rollout: mktemp failed for kubeconfig; using spec.rolloutAfter only"
      _rkcfg=""
    }
    if [[ -n "$_rkcfg" ]]; then
      kubectl config view --raw > "$_rkcfg" 2>/dev/null || {
        warn "workload-rollout: could not write kubeconfig for clusterctl; using spec.rolloutAfter only"
        rm -f "$_rkcfg"
        _rkcfg=""
      }
    fi
  else
    warn "workload-rollout: clusterctl not on PATH — install it for \`clusterctl alpha rollout restart\` (most reliable). Falling back to spec.rolloutAfter patches only."
  fi
  _rkctx="kind-${KIND_CLUSTER_NAME}"

  for kcp in "${_rollout_kcps[@]}"; do
    [[ -n "$kcp" ]] || continue
    kn="${kcp##*/}"
    _ok=0
    if [[ -n "$_rkcfg" ]]; then
      if clusterctl alpha rollout restart "kubeadmcontrolplane/${kn}" -n "$WORKLOAD_CLUSTER_NAMESPACE" --kubeconfig "$_rkcfg" --kubeconfig-context "$_rkctx"; then
        log "workload-rollout: clusterctl restarted kubeadmcontrolplane/${kn}"
        _ok=1
      else
        warn "workload-rollout: clusterctl alpha rollout restart failed for kubeadmcontrolplane/${kn} (see above) — trying spec.rolloutAfter"
      fi
    fi
    if ((_ok == 0)); then
      if kubectl --context "$ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" patch "$kcp" --type merge -p "{\"spec\":{\"rolloutAfter\":\"${now}\"}}"; then
        log "workload-rollout: set spec.rolloutAfter on $kcp"
      else
        warn "workload-rollout: failed to set spec.rolloutAfter on $kcp"
      fi
    fi
  done

  for md in "${_rollout_mds[@]}"; do
    [[ -n "$md" ]] || continue
    kn="${md##*/}"
    _ok=0
    if [[ -n "$_rkcfg" ]]; then
      if clusterctl alpha rollout restart "machinedeployment/${kn}" -n "$WORKLOAD_CLUSTER_NAMESPACE" --kubeconfig "$_rkcfg" --kubeconfig-context "$_rkctx"; then
        log "workload-rollout: clusterctl restarted machinedeployment/${kn}"
        _ok=1
      else
        warn "workload-rollout: clusterctl alpha rollout restart failed for machinedeployment/${kn} (see above) — trying spec.rolloutAfter"
      fi
    fi
    if ((_ok == 0)); then
      if kubectl --context "$ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" patch "$md" --type merge -p "{\"spec\":{\"rolloutAfter\":\"${now}\"}}"; then
        log "workload-rollout: set spec.rolloutAfter on $md"
      else
        warn "workload-rollout: failed to set spec.rolloutAfter on $md"
      fi
    fi
  done

  [[ -n "$_rkcfg" ]] && rm -f "$_rkcfg"

  kubectl --context "$ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" annotate cluster "$WORKLOAD_CLUSTER_NAME" "reconcile.cluster.x-k8s.io/force-rollout=${now}" --overwrite 2>/dev/null || true
  for pm in $(kubectl --context "$ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" get proxmoxmachines -l "cluster.x-k8s.io/cluster-name=${WORKLOAD_CLUSTER_NAME}" -o name 2>/dev/null); do
    kubectl --context "$ctx" -n "$WORKLOAD_CLUSTER_NAMESPACE" annotate "$pm" "reconcile.cluster.x-k8s.io/request=$(date +%s)" --overwrite 2>/dev/null || true
  done
}

if is_true "${WORKLOAD_ROLLOUT_STANDALONE:-false}"; then
  if is_true "${ARGOCD_PRINT_ACCESS_STANDALONE:-false}" || is_true "${ARGOCD_PORT_FORWARD_STANDALONE:-false}"; then
    die "Use either --workload-rollout or --argocd-print-access / --argocd-port-forward, not together."
  fi
  require_cmd kubectl
  merge_proxmox_bootstrap_secrets_from_kind || true
  sync_bootstrap_config_to_kind || true
  sync_proxmox_bootstrap_literal_credentials_to_kind || true
  if _bc_ctx="$(_resolve_bootstrap_kubectl_context 2>/dev/null)"; then
    KIND_CLUSTER_NAME="${_bc_ctx#kind-}"
  fi
  require_cmd python3
  WORKLOAD_CLUSTER_NAME="${WORKLOAD_CLUSTER_NAME:-capi-quickstart}"
  WORKLOAD_CLUSTER_NAMESPACE="${WORKLOAD_CLUSTER_NAMESPACE:-default}"
  if [[ -n "${CAPI_MANIFEST:-}" && -f "$CAPI_MANIFEST" ]]; then
    discover_workload_cluster_identity "$CAPI_MANIFEST" 2>/dev/null || true
  fi
  if ! kubectl --context "kind-${KIND_CLUSTER_NAME}" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -n "$WORKLOAD_CLUSTER_NAMESPACE" &>/dev/null; then
    argocd_standalone_discover_workload_kubeconfig_ref || true
  fi

  if [[ "${WORKLOAD_ROLLOUT_MODE:-argocd}" == "argocd" || "${WORKLOAD_ROLLOUT_MODE:-argocd}" == "all" ]]; then
    kubectl --context "kind-${KIND_CLUSTER_NAME}" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -n "$WORKLOAD_CLUSTER_NAMESPACE" &>/dev/null \
      || die "Workload kubeconfig not found: namespace ${WORKLOAD_CLUSTER_NAMESPACE} secret ${WORKLOAD_CLUSTER_NAME}-kubeconfig. Set --workload-cluster-name and --workload-cluster-namespace, or CAPI_MANIFEST, or ensure CAPI has created the cluster."
  fi

  case "${WORKLOAD_ROLLOUT_MODE:-argocd}" in
    argocd|capi|all) ;;
    *) die "Invalid --workload-rollout mode: ${WORKLOAD_ROLLOUT_MODE} (use argocd, capi, or all)" ;;
  esac

  log "workload-rollout: mode=${WORKLOAD_ROLLOUT_MODE} (management context kind-${KIND_CLUSTER_NAME}, workload ${WORKLOAD_CLUSTER_NAMESPACE}/${WORKLOAD_CLUSTER_NAME})"

  if ! kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | grep -qx "kind-${KIND_CLUSTER_NAME}"; then
    die "Management cluster context 'kind-${KIND_CLUSTER_NAME}' not found. The kind cluster must be running for this command."
  fi

  # In rollout mode, the config is already loaded. We just need to ensure credentials are set.

  # Credentials should be loaded from the config secret. If not, the sync_clusterctl check will fail.

  if [[ "${WORKLOAD_ROLLOUT_MODE}" == "capi" || "${WORKLOAD_ROLLOUT_MODE}" == "all" ]]; then
    bootstrap_ensure_capi_manifest_path
    try_fill_workload_manifest_inputs_from_management_cluster
    merge_proxmox_bootstrap_secrets_from_kind || true
    PROXMOX_TEMPLATE_ID="${PROXMOX_TEMPLATE_ID:-${TEMPLATE_VMID:-104}}"
    unset TEMPLATE_VMID 2>/dev/null || true
    capi_manifest_try_load_from_secret
    if [[ ! -s "$CAPI_MANIFEST" ]]; then
      bootstrap_sync_clusterctl_config_file
    fi
    generate_workload_manifest_if_missing
    patch_capi_manifest_proxmox_csi_topology_labels "$CAPI_MANIFEST"
    patch_capi_manifest_kubeadm_skip_kube_proxy_for_cilium "$CAPI_MANIFEST"
    patch_capi_manifest_proxmox_machine_template_spec_revisions "$CAPI_MANIFEST"
    discover_workload_cluster_identity "$CAPI_MANIFEST"
    ensure_workload_cluster_label "$CAPI_MANIFEST" "$WORKLOAD_CLUSTER_NAME"
    capi_manifest_push_to_secret
    warn_regenerated_capi_manifest_immutable_risk
    for attempt in 1 2 3; do
      if apply_workload_cluster_manifest_to_management_cluster "$CAPI_MANIFEST"; then
        break
      fi
      if [[ "$attempt" -eq 3 ]]; then
        die "CAPI manifest apply failed after ${attempt} attempts."
      fi
      warn "Apply failed (attempt ${attempt}/3). Retrying in 10s while webhooks settle..."
      sleep 10
    done
    log "workload-rollout: CAPI manifest re-applied to the management cluster."

    log "workload-rollout: Forcing machine rollout (clusterctl alpha rollout restart when available, else spec.rolloutAfter)…"
    workload_rollout_capi_touch_rollout
  fi
  if [[ "${WORKLOAD_ROLLOUT_MODE}" == "argocd" || "${WORKLOAD_ROLLOUT_MODE}" == "all" ]]; then
    is_true "$ARGOCD_ENABLED" || die "ARGOCD_ENABLED is false — cannot use argocd rollout. Use --workload-rollout capi, or set ARGOCD_ENABLED=true for argocd/all."
    is_true "${WORKLOAD_ARGOCD_ENABLED:-true}" || die "WORKLOAD_ARGOCD_ENABLED is false — no workload Argo."
    log "workload-rollout: CAAPH + app-of-apps Git — re-sync from the workload Argo CD (e.g. \`argocd app sync ${WORKLOAD_CLUSTER_NAME}\` with workload kubeconfig, or refresh the root/child Applications in the UI). In-script Application YAML is no longer applied."
  fi
  log "workload-rollout: done."
  exit 0
fi

if is_true "${ARGOCD_PRINT_ACCESS_STANDALONE:-false}" || is_true "${ARGOCD_PORT_FORWARD_STANDALONE:-false}"; then
  require_cmd kubectl
  merge_proxmox_bootstrap_secrets_from_kind || true
  sync_bootstrap_config_to_kind || true
  sync_proxmox_bootstrap_literal_credentials_to_kind || true
  if _bc_ctx="$(_resolve_bootstrap_kubectl_context 2>/dev/null)"; then
    KIND_CLUSTER_NAME="${_bc_ctx#kind-}"
  fi
  WORKLOAD_CLUSTER_NAME="${WORKLOAD_CLUSTER_NAME:-capi-quickstart}"
  WORKLOAD_CLUSTER_NAMESPACE="${WORKLOAD_CLUSTER_NAMESPACE:-default}"
  if [[ -n "${CAPI_MANIFEST:-}" && -f "$CAPI_MANIFEST" ]]; then
    if command -v python3 &>/dev/null; then
      discover_workload_cluster_identity "$CAPI_MANIFEST" 2>/dev/null || true
    fi
  fi
  if ! kubectl --context "kind-${KIND_CLUSTER_NAME}" get secret "${WORKLOAD_CLUSTER_NAME}-kubeconfig" -n "$WORKLOAD_CLUSTER_NAMESPACE" &>/dev/null; then
    argocd_standalone_discover_workload_kubeconfig_ref || true
  fi
  if is_true "${ARGOCD_PRINT_ACCESS_STANDALONE:-false}"; then
    argocd_print_access_info
  fi
  if is_true "${ARGOCD_PORT_FORWARD_STANDALONE:-false}"; then
    argocd_run_port_forwards
  fi
  exit 0
fi

bootstrap_ensure_capi_manifest_path

if is_true "$PURGE"; then
  if ! is_true "$FORCE"; then
    if ! confirm "Purge generated files and Terraform state before continuing?"; then
      die "Purge cancelled by user."
    fi
  fi
  purge_generated_artifacts
fi

# Initialize CLUSTER_SET_ID and identity suffix (skipped when empty until --recreate-proxmox-identities resolves them)
if is_true "$RECREATE_PROXMOX_IDENTITIES"; then
  log "Re-creation mode: identity parameters are resolved in Phase 2 (Terraform state or CAPI/CSI token IDs in kind / env)."
  if [[ -n "${CLUSTER_SET_ID:-}" && -z "${PROXMOX_IDENTITY_SUFFIX:-}" ]]; then
    PROXMOX_IDENTITY_SUFFIX="$(derive_proxmox_identity_suffix "$CLUSTER_SET_ID")"
  fi
  if [[ -n "${CLUSTER_SET_ID:-}" ]]; then
    validate_cluster_set_id_format
  fi
  if [[ -n "${PROXMOX_IDENTITY_SUFFIX:-}" ]]; then
    [[ "$PROXMOX_IDENTITY_SUFFIX" =~ ^[a-z0-9._-]+$ ]] || die "PROXMOX_IDENTITY_SUFFIX contains invalid characters. Allowed: a-z, 0-9, ., _, -."
    (( ${#PROXMOX_IDENTITY_SUFFIX} <= 32 )) || die "PROXMOX_IDENTITY_SUFFIX must be 32 characters or fewer."
    log "Using Proxmox identity suffix: ${PROXMOX_IDENTITY_SUFFIX}"
  fi
else
  if [[ -z "$CLUSTER_SET_ID" ]]; then
    CLUSTER_SET_ID="$(generate_uuid_v4)"
    log "Generated CLUSTER_SET_ID: ${CLUSTER_SET_ID}"
  fi
  if [[ -z "$PROXMOX_IDENTITY_SUFFIX" ]]; then
    PROXMOX_IDENTITY_SUFFIX="$(derive_proxmox_identity_suffix "$CLUSTER_SET_ID")"
  fi
  validate_cluster_set_id_format
  [[ "$PROXMOX_IDENTITY_SUFFIX" =~ ^[a-z0-9._-]+$ ]] || die "PROXMOX_IDENTITY_SUFFIX contains invalid characters. Allowed: a-z, 0-9, ., _, -."
  (( ${#PROXMOX_IDENTITY_SUFFIX} <= 32 )) || die "PROXMOX_IDENTITY_SUFFIX must be 32 characters or fewer."
  log "Using Proxmox identity suffix: ${PROXMOX_IDENTITY_SUFFIX}"
fi

# =============================================================================
# PHASE 1: Install all dependencies
# =============================================================================
log "Phase 1: Installing all dependencies..."

# --- System packages (git, curl, python3) -------------------------------------
ensure_system_dependencies
require_cmd git
require_cmd curl
require_cmd python3

# --- Docker -------------------------------------------------------------------
if ! command -v docker >/dev/null 2>&1; then
  log "Docker not found — installing via get.docker.com..."
  curl -fsSL https://get.docker.com | RUN_PRIVILEGED sh
  if [[ -n "${SUDO_USER:-}" ]]; then
    RUN_PRIVILEGED usermod -aG docker "$SUDO_USER"
  fi
  if command -v systemctl >/dev/null 2>&1; then
    RUN_PRIVILEGED systemctl enable --now docker
  fi
  command -v docker >/dev/null 2>&1 || die "Docker installation failed"
  log "Docker installed successfully."
else
  log "Docker already installed ($(docker --version))."
fi

# --- Kubernetes / CAPI tooling ------------------------------------------------
# Install kubectl first so we can read ${PROXMOX_BOOTSTRAP_CONFIG_SECRET_NAME} (config.yaml); merge overlays snapshot
# *_VERSION (and chart pins) from the in-cluster Secret before other CLIs, then re-check kubectl in case the Secret
# changed KUBECTL_VERSION.
ensure_kubectl
merge_proxmox_bootstrap_secrets_from_kind || true
bootstrap_sync_capi_controller_images_to_clusterctl_version
ensure_kubectl
ensure_kind
ensure_clusterctl
ensure_cilium_cli

# Add-on CLIs match what this script deploys via Argo CD (skip when --disable-argocd).
if is_true "$ARGOCD_ENABLED"; then
  ensure_argocd_cli
  if is_true "$KYVERNO_ENABLED"; then
    ensure_kyverno_cli
  fi
  if is_true "$CERT_MANAGER_ENABLED"; then
    ensure_cmctl
  fi
fi

require_cmd kind
require_cmd kubectl
require_cmd clusterctl
require_cmd cilium
if is_true "$ARGOCD_ENABLED"; then
  require_cmd argocd
  if is_true "$KYVERNO_ENABLED"; then
    require_cmd kyverno
  fi
  if is_true "$CERT_MANAGER_ENABLED"; then
    require_cmd cmctl
  fi
fi

maybe_interactive_select_kind_cluster
if ! is_true "${BOOTSTRAP_CAPI_MANIFEST_USER_SET:-false}"; then
  bootstrap_refresh_default_capi_manifest_path
fi

merge_proxmox_bootstrap_secrets_from_kind || true
# Re-push proxmox-bootstrap-config (config.yaml) from the process environment. merge_proxmox_bootstrap_secrets_from_kind
# (above) must run first so in-cluster k9s edits to config.yaml are applied to env, otherwise script defaults would
# overwrite the Secret. CLI flags that set *_EXPLICIT skip overlay for that key. Token material: CAPI/CSI in
# ${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME} + ${PROXMOX_BOOTSTRAP_CSI_SECRET_NAME} (or legacy), admin in ${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME}.
sync_bootstrap_config_to_kind
sync_proxmox_bootstrap_literal_credentials_to_kind

PHASE1_SKIP_HEAVY_MAINTENANCE=false
if is_true "$NO_DELETE_KIND"; then
  PHASE1_SKIP_HEAVY_MAINTENANCE=true
elif command -v kind >/dev/null 2>&1 && (kind get clusters 2>/dev/null | tr -d '\r' | contains_line "$KIND_CLUSTER_NAME"); then
  if ! is_true "$FORCE"; then
    PHASE1_SKIP_HEAVY_MAINTENANCE=true
  fi
fi

if is_true "$PHASE1_SKIP_HEAVY_MAINTENANCE"; then
  log "Existing kind cluster '${KIND_CLUSTER_NAME}' detected (or NO_DELETE_KIND) — skipping Docker package upgrade and bpg/proxmox provider install."
else
  log "Updating Docker via package manager..."
  if command -v apt-get >/dev/null 2>&1; then
    RUN_PRIVILEGED apt-get update -qq && RUN_PRIVILEGED apt-get install -y --only-upgrade docker-ce docker-ce-cli containerd.io
  elif command -v dnf >/dev/null 2>&1; then
    RUN_PRIVILEGED dnf upgrade -y docker-ce docker-ce-cli containerd.io
  elif command -v yum >/dev/null 2>&1; then
    RUN_PRIVILEGED yum update -y docker-ce docker-ce-cli containerd.io
  else
    warn "Unknown package manager — skipping Docker update."
  fi
  log "Docker version: $(docker --version)"
fi

# --- Terraform + bpg/proxmox provider -----------------------------------------
ensure_opentofu
require_cmd tofu
if is_true "$PHASE1_SKIP_HEAVY_MAINTENANCE"; then
  log "Skipping bpg/proxmox provider install (reuse path — cache from a previous run should already exist)."
else
  install_bpg_proxmox_provider
fi

# --- kind cluster config (ephemeral minimal config unless KIND_CONFIG / --kind-config is set) ---
bootstrap_ensure_kind_config

log "Phase 1 complete: all dependencies installed."

# =============================================================================
# PHASE 2: Bootstrap
# =============================================================================
log "Phase 2: Running bootstrap..."

# Config is loaded at startup and merged from kind; clusterctl on disk is not required (ephemeral file under TMPDIR for the CLI only).
# If the config secret is empty, CLI/env or explicit CLUSTERCTL_CFG/PROXMOX_ADMIN_CONFIG (legacy) must provide values.

# --- 0. Proxmox identity bootstrap (Terraform) --------------------------------
#
# If the CSI config or clusterctl config are missing, attempt to derive them
# from environment variables / existing config files first; fall back to
# Terraform to create the Proxmox users/tokens when necessary.
# When --recreate-proxmox-identities: replace tokens via Terraform (-replace) instead of apply-only; then post-capmox + post-manifest hooks re-sync Secrets and roll out controllers.
RECREATE_OPENTOFU_DONE=false
phase0_identity_bootstrap=false
if ! { _clusterctl_cfg_file_present || have_clusterctl_creds_in_env; }; then
  phase0_identity_bootstrap=true
fi
if [[ -n "${PROXMOX_CSI_CONFIG:-}" ]]; then
  [[ ! -f "$PROXMOX_CSI_CONFIG" ]] && phase0_identity_bootstrap=true
else
  [[ -z "${PROXMOX_CSI_TOKEN_ID:-}" || -z "${PROXMOX_CSI_TOKEN_SECRET:-}" ]] && phase0_identity_bootstrap=true
fi

if is_true "$phase0_identity_bootstrap"; then
  warn "Clusterctl API identity and/or CSI credentials are not satisfied from env or an explicit local clusterctl file — checking further."

  # With the central config secret, we no longer need to fall back to reading proxmox-admin.yaml or clusterctl.yaml here.
  # The config is loaded at startup. If it's the first run, the user must provide credentials via CLI or interactive prompt.
  refresh_derived_identity_token_ids

  write_clusterctl_config_if_missing
  write_csi_config_if_missing

  need_terraform=false
  if ! { _clusterctl_cfg_file_present || have_clusterctl_creds_in_env; }; then
    need_terraform=true
  fi
  if [[ -n "${PROXMOX_CSI_CONFIG:-}" && ! -f "$PROXMOX_CSI_CONFIG" && ( -z "$PROXMOX_CSI_TOKEN_ID" || -z "$PROXMOX_CSI_TOKEN_SECRET" ) ]]; then
    need_terraform=true
  fi
  if [[ -z "${PROXMOX_CSI_CONFIG:-}" && ( -z "$PROXMOX_CSI_TOKEN_ID" || -z "$PROXMOX_CSI_TOKEN_SECRET" ) ]]; then
    need_terraform=true
  fi

  if [[ "$need_terraform" == true ]]; then
    warn "CLI/env values are insufficient — running Terraform bootstrap for CAPI/CSI identities."

    if [[ -z "$PROXMOX_URL" || -z "$PROXMOX_ADMIN_USERNAME" || -z "$PROXMOX_ADMIN_TOKEN" ]]; then
      ensure_proxmox_admin_config
    fi

    missing_admin_cfg=()
    [[ -z "$PROXMOX_URL" ]]            && missing_admin_cfg+=(PROXMOX_URL)
    [[ -z "$PROXMOX_ADMIN_USERNAME" ]] && missing_admin_cfg+=(PROXMOX_ADMIN_USERNAME)
    [[ -z "$PROXMOX_ADMIN_TOKEN" ]]    && missing_admin_cfg+=(PROXMOX_ADMIN_TOKEN)

    if [[ ${#missing_admin_cfg[@]} -gt 0 ]]; then
      warn "Missing admin Proxmox configuration: ${missing_admin_cfg[*]}"
      die "Cannot run Terraform bootstrap without admin credentials (set env vars, add them to kind Secret ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}/${PROXMOX_BOOTSTRAP_ADMIN_SECRET_NAME}, or run interactively)."
    fi

    resolve_proxmox_region_and_node_from_admin_api
    resolve_available_cluster_set_id_for_roles || true
    check_proxmox_admin_api_connectivity

    if is_true "$RECREATE_PROXMOX_IDENTITIES"; then
      recreate_proxmox_identities_terraform
      RECREATE_OPENTOFU_DONE=true
    else
      apply_proxmox_identity_terraform
      generate_configs_from_terraform_outputs
    fi
  fi
fi

if is_true "$RECREATE_PROXMOX_IDENTITIES" && ! is_true "$RECREATE_OPENTOFU_DONE"; then
  if [[ -z "$PROXMOX_URL" || -z "$PROXMOX_ADMIN_USERNAME" || -z "$PROXMOX_ADMIN_TOKEN" ]]; then
    ensure_proxmox_admin_config
  fi
  resolve_proxmox_region_and_node_from_admin_api
  resolve_available_cluster_set_id_for_roles || true
  check_proxmox_admin_api_connectivity
  recreate_proxmox_identities_terraform
  RECREATE_OPENTOFU_DONE=true
fi

# --- 1. Ensure clusterctl credentials exist -----------------------------------
if ! { _clusterctl_cfg_file_present || have_clusterctl_creds_in_env; }; then
  warn "Proxmox clusterctl API identity is not in the environment and no explicit local CLUSTERCTL_CFG is set."
  if confirm "Enter Proxmox API values interactively now?"; then
    printf '\033[1;36m[?]\033[0m Proxmox VE URL (e.g. https://pve.example:8006): ' >&2
    read -r _input_url
    printf '\033[1;36m[?]\033[0m Proxmox API TokenID (e.g. capmox@pve!capi): ' >&2
    read -r _input_token
    printf '\033[1;36m[?]\033[0m Proxmox API Token secret (UUID): ' >&2
    read -rs _input_secret; echo >&2

    PROXMOX_URL="${_input_url}"
    PROXMOX_TOKEN="${_input_token}"
    PROXMOX_SECRET="${_input_secret}"
    sync_bootstrap_config_to_kind || true
    sync_proxmox_bootstrap_literal_credentials_to_kind || true
    log "Proxmox API identity updated in kind when the management cluster is reachable. clusterctl on disk is not used by default (temp file for CLI only)."
    unset _input_url _input_token _input_secret
  else
    warn "Skipping interactive creation. Set PROXMOX_URL, PROXMOX_TOKEN, and PROXMOX_SECRET, or add them to ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE} on kind, or set CLUSTERCTL_CFG to a local YAML you maintain."
    warn "Expected format:"
    cat >&2 <<'EXAMPLE'

  PROXMOX_URL: "https://pve.example:8006"
  PROXMOX_TOKEN: "capmox@pve!capi"
  PROXMOX_SECRET: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"

  To create a dedicated API token on your Proxmox node:
    pveum user add capmox@pve
    pveum aclmod / -user capmox@pve -role PVEVMAdmin
    pveum user token add capmox@pve capi -privsep 0

EXAMPLE
    printf '\033[1;33m[?]\033[0m Press ENTER once you have set env vars or kind Secrets, or a CLUSTERCTL_CFG file...' >&2
    read -r _
    merge_proxmox_bootstrap_secrets_from_kind || true
    if ! { _clusterctl_cfg_file_present || have_clusterctl_creds_in_env; }; then
      die "Proxmox API identity still unset: not in kind Secrets, not in the environment, and no usable CLUSTERCTL_CFG. Aborting."
    fi
    log "Continuing with Proxmox credentials from env, kind, or explicit CLUSTERCTL_CFG file."
  fi
fi
if ! is_true "$PROXMOX_BOOTSTRAP_KIND_SECRET_USED" && ! is_true "$PROXMOX_KIND_CAPMOX_CREDENTIALS_ACTIVE" && _clusterctl_cfg_file_present; then
  PROXMOX_URL="${PROXMOX_URL:-$(_get_yaml_value "$CLUSTERCTL_CFG" PROXMOX_URL)}"
  PROXMOX_TOKEN="${PROXMOX_TOKEN:-$(_get_yaml_value "$CLUSTERCTL_CFG" PROXMOX_TOKEN)}"
  PROXMOX_SECRET="${PROXMOX_SECRET:-$(_get_yaml_value "$CLUSTERCTL_CFG" PROXMOX_SECRET)}"
fi
PROXMOX_SECRET="$(normalize_proxmox_token_secret "$PROXMOX_SECRET" "$PROXMOX_TOKEN")"
validate_proxmox_token_secret "PROXMOX_SECRET" "$PROXMOX_SECRET"
refresh_derived_identity_token_ids
# Legacy env: TEMPLATE_VMID was the old name; only PROXMOX_TEMPLATE_ID is kept in shell state.
PROXMOX_TEMPLATE_ID="${PROXMOX_TEMPLATE_ID:-${TEMPLATE_VMID:-104}}"
unset TEMPLATE_VMID 2>/dev/null || true
ALLOWED_NODES="${ALLOWED_NODES:-${PROXMOX_NODE}}"

missing_cfg=()
[[ -z "$PROXMOX_URL" ]]    && missing_cfg+=(PROXMOX_URL)
[[ -z "$PROXMOX_TOKEN" ]]  && missing_cfg+=(PROXMOX_TOKEN)
[[ -z "$PROXMOX_SECRET" ]] && missing_cfg+=(PROXMOX_SECRET)

if [[ ${#missing_cfg[@]} -gt 0 ]]; then
  warn "Missing Proxmox configuration: ${missing_cfg[*]}"
  warn "Set them as environment variables, store them in kind Secrets in ${PROXMOX_BOOTSTRAP_SECRET_NAMESPACE}, or use an explicit local CLUSTERCTL_CFG (legacy)."
  die "Configure Proxmox credentials before running this script."
fi

log "Testing Proxmox API connectivity at $(pve_api_host_base_url) (clusterctl token)..."
http_code=$(curl -sk -o /dev/null -w "%{http_code}" \
  -H "Authorization: PVEAPIToken=${PROXMOX_TOKEN}=${PROXMOX_SECRET}" \
  "$(pve_api_host_base_url)/api2/json/version")

case "$http_code" in
  200) log "Proxmox API reachable (HTTP 200)." ;;
  401) die "Proxmox API returned 401 Unauthorized — check PROXMOX_TOKEN and PROXMOX_SECRET (kind: ${PROXMOX_BOOTSTRAP_CAPMOX_SECRET_NAME} or legacy proxmox-bootstrap-credentials, keys token/secret, capi_token_id/capi_token_secret); ensure PROXMOX_URL is the pve base (e.g. https://host:8006) not .../api2/json." ;;
  000) die "Could not reach Proxmox API at ${PROXMOX_URL} — check PROXMOX_URL and network connectivity." ;;
  *)   die "Proxmox API returned unexpected HTTP ${http_code} — verify PROXMOX_URL and credentials." ;;
esac

resolve_proxmox_region_and_node_from_clusterctl_api

bootstrap_sync_clusterctl_config_file

# --- 4. Check for existing kind clusters and optionally delete ----------------
KIND_CLUSTER_REUSED=false
log "Checking for existing kind clusters..."
if (kind get clusters 2>/dev/null | tr -d '\r' | contains_line "$KIND_CLUSTER_NAME"); then
  if is_true "$FORCE" && ! is_true "$NO_DELETE_KIND"; then
    log "Force mode: replacing kind cluster '${KIND_CLUSTER_NAME}'..."
    delete_workload_cluster_before_kind_deletion
    kind delete cluster --name "$KIND_CLUSTER_NAME"
    log "Cluster deleted."
    purge_stale_host_networking
  else
    if is_true "$FORCE" && is_true "$NO_DELETE_KIND"; then
      warn "NO_DELETE_KIND is set — keeping existing kind cluster despite --force."
    fi
    log "Reusing existing kind cluster '${KIND_CLUSTER_NAME}' (use --force to destroy and recreate; --no-delete-kind prevents deletion)."
    KIND_CLUSTER_REUSED=true
  fi
else
  log "No existing cluster found; purging any leftover networking state before fresh bootstrap."
  purge_stale_host_networking
fi

# --- 5. Resolve CAPMOX image tag ---------------------------------------------
log "Resolving CAPMOX image tag..."
CAPMOX_TAG=""

if [[ -n "${CAPMOX_VERSION:-}" ]]; then
  CAPMOX_TAG="$CAPMOX_VERSION"
  log "Using pinned CAPMOX version: ${CAPMOX_TAG}"
else
  log "Cloning ${CAPMOX_REPO} to determine latest stable tag..."
  rm -rf "$CAPMOX_BUILD_DIR"
  git clone --filter=blob:none "$CAPMOX_REPO" "$CAPMOX_BUILD_DIR"
  CAPMOX_TAG="$(git -C "$CAPMOX_BUILD_DIR" tag --list 'v*' \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' \
    | sort -V | tail -1 || true)"
  [[ -n "$CAPMOX_TAG" ]] || die "Could not determine a stable release tag from ${CAPMOX_REPO}"
  log "Latest stable tag detected: ${CAPMOX_TAG}"
fi

CAPMOX_IMAGE="${CAPMOX_IMAGE_REPO}:${CAPMOX_TAG}"

# --- 6. Create kind cluster --------------------------------------------------
if is_true "$KIND_CLUSTER_REUSED"; then
  log "Skipping kind cluster creation; reusing existing cluster '${KIND_CLUSTER_NAME}'."
else
  log "Creating kind cluster using ${KIND_CONFIG}..."
  kind create cluster --name "$KIND_CLUSTER_NAME" --config "$KIND_CONFIG"
fi

# `kind get clusters` can list a cluster even when the current kubeconfig has no
# kind-<name> context (new host, KUBECONFIG changed, or kubeconfig was deleted). Merge so kubectl --context works.
if (kind get clusters 2>/dev/null | tr -d '\r' | contains_line "$KIND_CLUSTER_NAME"); then
  if is_true "$KIND_CLUSTER_REUSED" \
    || ! (kubectl config get-contexts -o name 2>/dev/null | tr -d '\r' | contains_line "kind-${KIND_CLUSTER_NAME}"); then
    log "Merging kubeconfig for kind cluster '${KIND_CLUSTER_NAME}' (context kind-${KIND_CLUSTER_NAME})..."
    kind export kubeconfig --name "$KIND_CLUSTER_NAME" \
      || die "The kind cluster '${KIND_CLUSTER_NAME}' exists, but 'kind export kubeconfig' failed. Fix container runtime / kind, set KUBECONFIG, or run: kind export kubeconfig --name ${KIND_CLUSTER_NAME}"
  fi
fi

if is_true "$KIND_CLUSTER_REUSED"; then
  log "Reusing existing kind cluster — skipping arm64 image checks and kind-load (images already present from previous bootstrap)."
else
  log "Checking arm64 availability for all provider/CAPI images and loading into kind..."
  build_if_no_arm64 "$CAPMOX_IMAGE"            "$CAPMOX_REPO"    "$CAPMOX_TAG"         "$CAPMOX_BUILD_DIR"
  build_if_no_arm64 "$CAPI_CORE_IMAGE"         "$CAPI_CORE_REPO" "$CLUSTERCTL_VERSION" "./cluster-api"
  build_if_no_arm64 "$CAPI_BOOTSTRAP_IMAGE"    "$CAPI_CORE_REPO" "$CLUSTERCTL_VERSION" "./cluster-api"
  build_if_no_arm64 "$CAPI_CONTROLPLANE_IMAGE" "$CAPI_CORE_REPO" "$CLUSTERCTL_VERSION" "./cluster-api"
  build_if_no_arm64 "$IPAM_IMAGE"              "$IPAM_REPO"      "${IPAM_IMAGE##*:}"   "./cluster-api-ipam-provider-in-cluster"
fi
sync_bootstrap_config_to_kind
sync_proxmox_bootstrap_literal_credentials_to_kind

# --- 7. Management cluster CNI (kindnet, default) ----------------------------
# Using kind's default CNI (kindnet) on the management cluster for minimal
# overhead — Cilium is reserved for the workload cluster.
log "Using kind's default CNI (kindnet) on the management cluster; skipping Cilium install."

# --- 8. Initialize Cluster API -----------------------------------------------
install_metrics_server_on_kind_management_cluster

log "Initializing Cluster API (infrastructure=${INFRA_PROVIDER}, ipam=${IPAM_PROVIDER}, addon=helm)..."
EXP_CLUSTER_RESOURCE_SET="${EXP_CLUSTER_RESOURCE_SET:-false}" \
CLUSTER_TOPOLOGY="$CLUSTER_TOPOLOGY" \
EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION="$EXP_KUBEADM_BOOTSTRAP_FORMAT_IGNITION" \
clusterctl init \
  --config "$BOOTSTRAP_CLUSTERCTL_CONFIG_PATH" \
  --infrastructure "$INFRA_PROVIDER" \
  --ipam "$IPAM_PROVIDER" \
  --addon helm

log "Waiting for CAAPH (add-on provider Helm) to become ready, if present..."
if kubectl get deploy -n caaph-system 2>/dev/null | grep -q .; then
  for _d in $(kubectl get deploy -n caaph-system -o name 2>/dev/null); do
    kubectl wait -n caaph-system "$_d" --for=condition=Available --timeout=300s 2>/dev/null || true
  done
else
  warn "caaph-system not found after clusterctl init --addon helm — verify CAAPH; HelmChartProxy may fail without it."
fi

log "Waiting for core CAPI controllers to become ready..."
kubectl wait deployment capi-controller-manager \
  --namespace capi-system \
  --for=condition=Available \
  --timeout=300s
kubectl wait deployment capi-kubeadm-bootstrap-controller-manager \
  --namespace capi-kubeadm-bootstrap-system \
  --for=condition=Available \
  --timeout=300s
kubectl wait deployment capi-kubeadm-control-plane-controller-manager \
  --namespace capi-kubeadm-control-plane-system \
  --for=condition=Available \
  --timeout=300s

log "Waiting for CAPI webhook service endpoints..."
wait_for_service_endpoint capi-kubeadm-bootstrap-system capi-kubeadm-bootstrap-webhook-service 300
wait_for_service_endpoint capi-kubeadm-control-plane-system capi-kubeadm-control-plane-webhook-service 300

log "Waiting for Proxmox provider (capmox-controller-manager) to become ready..."
kubectl wait deployment capmox-controller-manager \
  --namespace capmox-system \
  --for=condition=Available \
  --timeout=300s

log "Waiting for CAPMOX mutating webhook endpoint (ProxmoxCluster apply)..."
wait_for_service_endpoint capmox-system capmox-webhook-service 300

recreate_identities_resync_and_rollout_capmox

# --- 9. Apply workload cluster manifest --------------------------------------
# Label the Cluster, then apply it before Cilium/Argo HelmChartProxy objects so a Cluster exists
# to select (CAAPH). Cilium: Cluster API add-on provider Helm (HelmChartProxy), not ClusterResourceSet.
# Immutability: changing pod/service CIDRs, name, or infra wiring requires deleting the existing Cluster first.
maybe_interactive_select_workload_cluster_from_management
try_fill_workload_manifest_inputs_from_management_cluster
# Re-apply proxmox-bootstrap-config so NODE_IP_RANGES (and other snapshot keys) beat live ProxmoxCluster backfill above.
merge_proxmox_bootstrap_secrets_from_kind || true
PROXMOX_TEMPLATE_ID="${PROXMOX_TEMPLATE_ID:-${TEMPLATE_VMID:-104}}"
unset TEMPLATE_VMID 2>/dev/null || true
capi_manifest_try_load_from_secret
generate_workload_manifest_if_missing
patch_capi_manifest_proxmox_csi_topology_labels "$CAPI_MANIFEST"
patch_capi_manifest_kubeadm_skip_kube_proxy_for_cilium "$CAPI_MANIFEST"
patch_capi_manifest_proxmox_machine_template_spec_revisions "$CAPI_MANIFEST"
discover_workload_cluster_identity "$CAPI_MANIFEST"
ensure_workload_cluster_label "$CAPI_MANIFEST" "$WORKLOAD_CLUSTER_NAME"
refresh_derived_cilium_cluster_id
patch_capi_cluster_caaph_helm_labels "$CAPI_MANIFEST"
capi_manifest_push_to_secret

if is_true "${BOOTSTRAP_CAPI_USE_SECRET:-false}"; then
  log "Applying workload manifest to management cluster (ephemeral file in this run; last pushed to Secret ${CAPI_MANIFEST_SECRET_NAMESPACE}/${CAPI_MANIFEST_SECRET_NAME})..."
else
  log "Applying CAPI manifest ${CAPI_MANIFEST}..."
fi
warn_regenerated_capi_manifest_immutable_risk
for attempt in 1 2 3; do
  if apply_workload_cluster_manifest_to_management_cluster "$CAPI_MANIFEST"; then
    break
  fi
  if [[ "$attempt" -eq 3 ]]; then
    die "Failed to apply ${CAPI_MANIFEST} after ${attempt} attempts."
  fi
  warn "Apply failed (attempt ${attempt}/3). Retrying in 10s while webhooks settle..."
  sleep 10
done

recreate_identities_workload_csi_secrets

apply_workload_cilium_helmchartproxy

wait_for_workload_cluster_ready

apply_workload_cilium_lbb_to_workload_if_enabled

# CAPI cluster metrics: deploy via your app-of-apps Git (CAAPH); kubectl install only if Argo is fully disabled.
if is_true "${ENABLE_WORKLOAD_METRICS_SERVER:-true}"; then
  if is_true "$ARGOCD_ENABLED" && is_true "${WORKLOAD_ARGOCD_ENABLED:-true}"; then
    log "workload metrics-server: deploy with your app-of-apps repo (WORKLOAD_APP_OF_APPS_GIT_URL); ENABLE_WORKLOAD_METRICS_SERVER is informational when Argo delivers apps from Git."
  else
    install_metrics_server_on_workload_cluster
  fi
fi

# Proxmox CSI config Secret on the workload (for Git/Helm to reference); required before CSI syncs from Git.
if is_true "$PROXMOX_CSI_ENABLED" && is_true "$ARGOCD_ENABLED" && is_true "${WORKLOAD_ARGOCD_ENABLED:-true}"; then
  load_csi_vars_from_config
  PROXMOX_CSI_URL="${PROXMOX_CSI_URL:-$(proxmox_api_json_url)}"
  if [[ -n "$PROXMOX_CSI_URL" && -n "$PROXMOX_CSI_TOKEN_ID" && -n "$PROXMOX_CSI_TOKEN_SECRET" && -n "$PROXMOX_REGION" ]]; then
    apply_proxmox_csi_config_secret_to_workload_cluster
  else
    warn "Proxmox CSI token material incomplete — push ${WORKLOAD_CLUSTER_NAME}-proxmox-csi-config to the workload yourself before syncing the CSI app."
  fi
fi

# Pre-install Secret argocd-redis on the workload (same bootstrap phase as CSI secrets) before CAAPH argocd-apps (operator path).
if is_true "$ARGOCD_ENABLED" && is_true "${WORKLOAD_ARGOCD_ENABLED:-true}"; then
  apply_workload_argocd_redis_secret_to_workload_cluster
fi

# --- 10. Argo CD: workload only (Argo CD Operator + CAAPH argocd-apps root app-of-apps). ---
# --disable-workload-argocd skips CAAPH Argo / app-of-apps on the workload.
if is_true "$ARGOCD_ENABLED"; then
  log "Argo CD on the workload: Argo CD Operator + ArgoCD CR, then CAAPH argocd-apps (root Application name ${WORKLOAD_CLUSTER_NAME}; see --workload-app-of-apps-git-*)."
  if is_true "${WORKLOAD_ARGOCD_ENABLED:-true}"; then
    caaph_apply_workload_argo_helm_proxies
    caaph_wait_workload_argocd_server
    caaph_log_workload_argo_apps_status
  fi
else
  warn "Argo CD disabled (--disable-argocd) — skipping CAAPH workload Argo and app-of-apps."
fi

log "Done. CAPI: 'kubectl get clusters -A' and 'clusterctl describe cluster <name>'. For workload apps, rely on Argo CD sync (this script does not wait for all add-ons to be Healthy)."
