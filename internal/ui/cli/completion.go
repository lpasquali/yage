// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// topLevelFlags is the canonical list of long flags yage accepts. Source
// of truth: the switch in parse.go. Keep alphabetized for diff sanity.
var topLevelFlags = []string{
	"--admin-token",
	"--admin-username",
	"--airgapped",
	"--allow-resource-overcommit",
	"--allowed-nodes",
	"--argocd-app-version",
	"--argocd-disable-operator-ingress",
	"--argocd-port-forward",
	"--argocd-print-access",
	"--argocd-server-insecure",
	"--argocd-workload-postsync-hooks-git-path",
	"--argocd-workload-postsync-hooks-git-ref",
	"--argocd-workload-postsync-hooks-git-url",
	"--aws-alb-count",
	"--aws-cloudwatch-logs-gb",
	"--aws-data-transfer-gb",
	"--aws-fargate-pod-count",
	"--aws-fargate-pod-cpu",
	"--aws-fargate-pod-memory-gib",
	"--aws-mode",
	"--aws-nat-gateway-count",
	"--aws-nlb-count",
	"--aws-overhead-tier",
	"--azure-client-id",
	"--azure-control-plane-machine-type",
	"--azure-identity-model",
	"--azure-location",
	"--azure-mode",
	"--azure-node-machine-type",
	"--azure-overhead-tier",
	"--azure-resource-group",
	"--azure-subnet-name",
	"--azure-subscription-id",
	"--azure-tenant-id",
	"--azure-vnet-name",
	"--bootstrap-config-file",
	"--bootstrap-mode",
	"--bridge",
	"--budget-usd-month",
	"--build-all",
	"--capi-manifest",
	"--capi-proxmox-machine-template-spec-rev-skip",
	"--capi-token-prefix",
	"--capi-user-id",
	"--cert-manager-version",
	"--cilium-gateway-api",
	"--cilium-hubble",
	"--cilium-hubble-ui",
	"--cilium-ingress",
	"--cilium-ipam-cluster-pool-ipv4",
	"--cilium-ipam-cluster-pool-ipv4-mask-size",
	"--cilium-kube-proxy-replacement",
	"--cilium-lb-ipam",
	"--cilium-lb-ipam-pool-cidr",
	"--cilium-lb-ipam-pool-name",
	"--cilium-lb-ipam-pool-start",
	"--cilium-lb-ipam-pool-stop",
	"--cilium-wait-duration",
	"--cloudinit-storage",
	"--cluster-set-id",
	"--cluster-topology",
	"--cnpg-version",
	"--completion",
	"--control-plane-boot-volume-device",
	"--control-plane-boot-volume-size",
	"--control-plane-count",
	"--control-plane-endpoint-ip",
	"--control-plane-endpoint-port",
	"--control-plane-memory-mib",
	"--control-plane-num-cores",
	"--control-plane-num-sockets",
	"--cost-compare",
	"--allowed-providers",
	"--skip-providers",
	"--stop-before-workload",
	"--crossplane-version",
	"--csi-default-class",
	"--csi-driver",
	"--csi-fstype",
	"--csi-insecure",
	"--csi-reclaim-policy",
	"--csi-storage",
	"--csi-storage-class",
	"--csi-token-id",
	"--csi-token-prefix",
	"--csi-token-secret",
	"--csi-url",
	"--csi-user-id",
	"--data-center-location",
	"--disable-argocd",
	"--disable-argocd-workload-postsync-hooks",
	"--disable-cert-manager",
	"--disable-cnpg",
	"--disable-crossplane",
	"--disable-kyverno",
	"--disable-metrics-server",
	"--disable-proxmox-csi",
	"--disable-proxmox-csi-smoketest",
	"--disable-victoriametrics",
	"--disable-workload-argocd",
	"--disable-workload-metrics-server",
	"--dns-servers",
	"--dry-run",
	"--exp-cluster-resource-set",
	"--exp-kubeadm-bootstrap-format-ignition",
	"--force",
	"--gateway",
	"--gcp-control-plane-machine-type",
	"--gcp-identity-model",
	"--gcp-image-family",
	"--gcp-mode",
	"--gcp-network-name",
	"--gcp-node-machine-type",
	"--gcp-overhead-tier",
	"--gcp-project",
	"--gcp-region",
	"--hardware-cost-usd",
	"--hardware-kwh-rate-usd",
	"--hardware-support-usd-month",
	"--hardware-useful-life-years",
	"--hardware-watts",
	"--helm-repo-mirror",
	"--help",
	"--hetzner-control-plane-machine-type",
	"--hetzner-location",
	"--hetzner-node-machine-type",
	"--hetzner-overhead-tier",
	"--image-registry-mirror",
	"--infra-provider",
	"--infrastructure-provider",
	"--internal-ca-bundle",
	"--ip-prefix",
	"--kind-backup",
	"--kind-cluster-name",
	"--kind-config",
	"--kind-restore",
	"--kyverno-version",
	"--memory-adjustment",
	"--mgmt-capi-manifest",
	"--mgmt-cilium-cluster-id",
	"--mgmt-cluster-name",
	"--mgmt-cluster-namespace",
	"--mgmt-control-plane-boot-volume-device",
	"--mgmt-control-plane-boot-volume-size",
	"--mgmt-control-plane-endpoint-ip",
	"--mgmt-control-plane-endpoint-port",
	"--mgmt-control-plane-machine-count",
	"--mgmt-control-plane-memory-mib",
	"--mgmt-control-plane-num-cores",
	"--mgmt-control-plane-num-sockets",
	"--mgmt-control-plane-template-id",
	"--mgmt-k8s-version",
	"--mgmt-node-ip-ranges",
	"--mgmt-proxmox-pool",
	"--mgmt-worker-machine-count",
	"--mgmt-worker-template-id",
	"--no-delete-kind",
	"--no-pivot",
	"--node",
	"--node-image",
	"--node-ip-ranges",
	"--openstack-cloud",
	"--openstack-control-plane-flavor",
	"--openstack-dns-nameservers",
	"--openstack-failure-domain",
	"--openstack-image-name",
	"--openstack-project-name",
	"--openstack-region",
	"--openstack-ssh-key-name",
	"--openstack-worker-flavor",
	"--overcommit-tolerance-pct",
	"--persist-local-secrets",
	"--pivot",
	"--pivot-dry-run",
	"--pivot-keep-kind",
	"--pivot-verify-timeout",
	"--print-pricing-setup",
	"--proxmox-bootstrap-admin-secret",
	"--proxmox-csi-version",
	"--proxmox-pool",
	"--proxmox-secret",
	"--proxmox-token",
	"--proxmox-url",
	"--purge",
	"--recreate-proxmox-identities",
	"--recreate-proxmox-identities-scope",
	"--recreate-proxmox-identities-state-rm",
	"--region",
	"--regenerate-capi-manifest",
	"--resource-budget-fraction",
	"--system-apps-cpu-millicores",
	"--system-apps-memory-mib",
	"--template-id",
	"--template-vmid",
	"--victoriametrics-version",
	"--vsphere-datacenter",
	"--vsphere-datastore",
	"--vsphere-folder",
	"--vsphere-network",
	"--vsphere-password",
	"--vsphere-resource-pool",
	"--vsphere-server",
	"--vsphere-template",
	"--vsphere-tls-thumbprint",
	"--vsphere-username",
	"--worker-boot-volume-device",
	"--worker-boot-volume-size",
	"--worker-count",
	"--worker-memory-mib",
	"--worker-num-cores",
	"--worker-num-sockets",
	"--workload-app-of-apps-git-path",
	"--workload-app-of-apps-git-ref",
	"--workload-app-of-apps-git-url",
	"--workload-cilium-cluster-id",
	"--workload-cluster-name",
	"--workload-cluster-namespace",
	"--workload-control-plane-template-id",
	"--workload-gitops-mode",
	"--workload-k8s-version",
	"--workload-rollout",
	"--workload-rollout-no-wait",
	"--workload-worker-template-id",
	"--xapiri",
}

// PrintShellCompletion writes a completion script for the named shell
// to w. Bash is the only fully wired shell today; zsh and fish print a
// not-yet-implemented notice and return without error so the caller can
// exit(0). Unknown shells return an error.
func PrintShellCompletion(w io.Writer, shell string) error {
	if w == nil {
		w = os.Stdout
	}
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "bash":
		PrintBashCompletion(w)
		return nil
	case "zsh", "fish":
		fmt.Fprintf(w, "# yage %s completion is not yet implemented; bash is the only wired shell today.\n", shell)
		return nil
	default:
		return fmt.Errorf("unsupported shell %q (want: bash, zsh, fish)", shell)
	}
}

// PrintBashCompletion writes a bash completion script for `yage` to w.
// Source the output (e.g. `source <(yage --completion bash)`) to enable
// completion in the current shell.
func PrintBashCompletion(w io.Writer) {
	flags := strings.Join(topLevelFlags, " ")
	topics := strings.Join(HelpTopics(), " ")

	const tmpl = `# bash completion for yage — source it via:
#     source <(yage --completion bash)
# or drop it into /etc/bash_completion.d/yage.

_yage() {
    local cur prev words cword
    _init_completion -n = 2>/dev/null || {
        cur="${COMP_WORDS[COMP_CWORD]}"
        prev="${COMP_WORDS[COMP_CWORD-1]}"
    }

    # Value completions keyed off the previous token.
    case "${prev}" in
        --help|-h)
            COMPREPLY=( $(compgen -W "%[2]s" -- "${cur}") )
            return 0
            ;;
        --completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
            return 0
            ;;
        --infra-provider|--infrastructure-provider)
            COMPREPLY=( $(compgen -W "aws azure gcp hetzner digitalocean linode oci ibmcloud proxmox openstack vsphere docker" -- "${cur}") )
            return 0
            ;;
        --skip-providers|--allowed-providers)
            # Comma-separated. Bash completion of comma-separated values
            # is awkward; we just suggest the registry names so the user
            # can build the list one provider at a time.
            COMPREPLY=( $(compgen -W "aws azure gcp hetzner digitalocean linode oci ibmcloud proxmox openstack vsphere docker" -- "${cur}") )
            return 0
            ;;
        --bootstrap-mode)
            COMPREPLY=( $(compgen -W "kubeadm k3s" -- "${cur}") )
            return 0
            ;;
        --aws-mode)
            COMPREPLY=( $(compgen -W "unmanaged eks eks-fargate" -- "${cur}") )
            return 0
            ;;
        --azure-mode)
            COMPREPLY=( $(compgen -W "unmanaged aks" -- "${cur}") )
            return 0
            ;;
        --gcp-mode)
            COMPREPLY=( $(compgen -W "unmanaged gke" -- "${cur}") )
            return 0
            ;;
        --aws-overhead-tier|--azure-overhead-tier|--gcp-overhead-tier|--hetzner-overhead-tier)
            COMPREPLY=( $(compgen -W "dev prod enterprise" -- "${cur}") )
            return 0
            ;;
        --print-pricing-setup)
            COMPREPLY=( $(compgen -W "aws azure gcp hetzner ibmcloud all" -- "${cur}") )
            return 0
            ;;
        --workload-rollout)
            COMPREPLY=( $(compgen -W "argocd capi all" -- "${cur}") )
            return 0
            ;;
        --azure-identity-model)
            COMPREPLY=( $(compgen -W "service-principal managed-identity workload-identity" -- "${cur}") )
            return 0
            ;;
        --gcp-identity-model)
            COMPREPLY=( $(compgen -W "service-account adc workload-identity" -- "${cur}") )
            return 0
            ;;
        --argocd-print-access|--argocd-port-forward)
            COMPREPLY=( $(compgen -W "workload" -- "${cur}") )
            return 0
            ;;
        --kind-config|--internal-ca-bundle|--bootstrap-config-file|--capi-manifest|--mgmt-capi-manifest|--kind-backup|--kind-restore)
            COMPREPLY=( $(compgen -f -- "${cur}") )
            return 0
            ;;
    esac

    # Default: complete with the long-flag list.
    if [[ "${cur}" == -* ]]; then
        COMPREPLY=( $(compgen -W "%[1]s -h -f -p -b -u -t -r -n" -- "${cur}") )
        return 0
    fi

    COMPREPLY=()
    return 0
}

complete -F _yage yage
`
	fmt.Fprintf(w, tmpl, flags, topics)
}
