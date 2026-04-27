// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package config

// ArgoCDConfig groups all Argo CD configuration.
// The env var and CLI flag names are unchanged — only the Go field paths moved.
type ArgoCDConfig struct {
	// Version drives both the argocd CLI release tag and the
	// ArgoCD CR spec.version; the two are kept in lockstep upstream.
	Version string
	// OperatorVersion is the Argo CD Operator chart/image version.
	OperatorVersion string
	// Enabled controls whether Argo CD is installed on the workload cluster.
	Enabled bool
	// ServerInsecure controls whether the Argo CD server runs in insecure mode.
	ServerInsecure string
	// DisableOperatorManagedIngress disables the Argo CD Operator's
	// managed Ingress resource when set to "true".
	DisableOperatorManagedIngress string
	// PrometheusEnabled controls whether Prometheus monitoring is enabled
	// for the Argo CD Operator's ArgoCD CR.
	PrometheusEnabled string
	// MonitoringEnabled controls whether monitoring is enabled for the
	// Argo CD Operator's ArgoCD CR.
	MonitoringEnabled string
	// PrintAccessStandalone, when true, makes the program print the Argo CD
	// access info for the workload cluster and exit.
	PrintAccessStandalone bool
	// PrintAccessTarget selects which cluster to print access info for
	// ("workload" is the only supported value).
	PrintAccessTarget string
	// PortForwardStandalone, when true, makes the program run a kubectl
	// port-forward for the workload Argo CD server and block.
	PortForwardStandalone bool
	// PortForwardTarget selects which cluster to port-forward
	// ("workload" is the only supported value).
	PortForwardTarget string
	// PortForwardPort is the local port for the Argo CD port-forward.
	PortForwardPort string

	// WorkloadEnabled controls whether Argo CD is enabled on the workload
	// cluster (i.e. whether the CAAPH argocd-apps HelmChartProxy is applied).
	WorkloadEnabled bool
	// WorkloadNamespace is the Kubernetes namespace Argo CD is installed
	// into on the workload cluster.
	WorkloadNamespace string

	// AppOfAppsGitURL is the Git repository URL for the workload app-of-apps.
	AppOfAppsGitURL string
	// AppOfAppsGitPath is the path within the app-of-apps repository.
	AppOfAppsGitPath string
	// AppOfAppsGitRef is the Git ref (branch, tag, commit) for the app-of-apps.
	AppOfAppsGitRef string

	// PostsyncHooksEnabled controls whether Argo CD workload post-sync hooks
	// are enabled.
	PostsyncHooksEnabled bool
	// PostsyncHooksGitURL is the Git repository URL for the post-sync hooks.
	PostsyncHooksGitURL string
	// PostsyncHooksGitPath is the path within the post-sync hooks repository.
	PostsyncHooksGitPath string
	// PostsyncHooksGitRef is the Git ref for the post-sync hooks.
	PostsyncHooksGitRef string
	// PostsyncHooksKubectlImg is the kubectl image used in the post-sync hook Jobs.
	PostsyncHooksKubectlImg string
}
