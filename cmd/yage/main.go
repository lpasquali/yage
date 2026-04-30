// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// yage is a Cluster API bootstrap orchestrator: provisions a
// kind-based CAPI management plane and brings up a workload cluster
// on the configured infrastructure provider, then layers in CNI,
// CSI, and an Argo CD app-of-apps.
//
// Internal structure is modular Go packages under internal/, one
// per concern.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/obs"
	"github.com/lpasquali/yage/internal/orchestrator"
	"github.com/lpasquali/yage/internal/platform/airgap"
	syskeyring "github.com/lpasquali/yage/internal/platform/keyring"
	"github.com/lpasquali/yage/internal/platform/secmem"
	"github.com/lpasquali/yage/internal/platform/shell"
	"github.com/lpasquali/yage/internal/pricing"
	"github.com/lpasquali/yage/internal/ui/cli"
	"github.com/lpasquali/yage/internal/ui/xapiri"

	// Provider registrations: importing each provider package runs
	// its init() which calls provider.Register. Add a new provider
	// by dropping a package under internal/provider/<name> and
	// importing it here.
	_ "github.com/lpasquali/yage/internal/provider/aws"
	_ "github.com/lpasquali/yage/internal/provider/azure"
	_ "github.com/lpasquali/yage/internal/provider/capd"
	_ "github.com/lpasquali/yage/internal/provider/digitalocean"
	_ "github.com/lpasquali/yage/internal/provider/gcp"
	_ "github.com/lpasquali/yage/internal/provider/hetzner"
	_ "github.com/lpasquali/yage/internal/provider/ibmcloud"
	_ "github.com/lpasquali/yage/internal/provider/linode"
	_ "github.com/lpasquali/yage/internal/provider/oci"
	_ "github.com/lpasquali/yage/internal/provider/openstack"
	_ "github.com/lpasquali/yage/internal/provider/proxmox"
	_ "github.com/lpasquali/yage/internal/provider/vsphere"

	// CSI driver registrations: importing each driver package runs
	// its init() which calls csi.Register. AWS-EBS, Azure-Disk,
	// GCP-PD, OpenStack-Cinder, and Proxmox-CSI are wired through
	// the §20 registry. The rest of the §20.1 matrix lands as
	// drivers are added.
	_ "github.com/lpasquali/yage/internal/csi/awsebs"
	_ "github.com/lpasquali/yage/internal/csi/azuredisk"
	_ "github.com/lpasquali/yage/internal/csi/cindercsi"
	_ "github.com/lpasquali/yage/internal/csi/gcppd"
	_ "github.com/lpasquali/yage/internal/csi/hcloud"
	_ "github.com/lpasquali/yage/internal/csi/proxmoxcsi"
	_ "github.com/lpasquali/yage/internal/csi/rookceph"
	_ "github.com/lpasquali/yage/internal/csi/vspherecsi"
)

func main() {
	if err := secmem.DisableDump(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: prctl PR_SET_DUMPABLE failed:", err)
	}
	cfg := config.Load()
	// Apply YAML config file before CLI flags so CLI wins over everything.
	if path := config.ConfigFilePath(os.Args[1:]); path != "" {
		if err := config.ApplyYAMLFile(cfg, path); err != nil {
			fmt.Fprintln(os.Stderr, "✗", err.Error())
			os.Exit(1)
		}
		cfg.ConfigFile = path
	}
	cli.Parse(cfg, os.Args[1:])

	// --clear-keyring: remove Proxmox credentials from the OS keychain and exit.
	if cfg.ClearKeyring {
		_ = syskeyring.Delete(syskeyring.KeyProxmoxCAPIToken)
		_ = syskeyring.Delete(syskeyring.KeyProxmoxCAPISecret)
		_ = syskeyring.Delete(syskeyring.KeyProxmoxAdminToken)
		fmt.Fprintln(os.Stdout, "Proxmox credentials removed from keyring.")
		os.Exit(0)
	}

	config.ClearCredentialEnvVars()

	// Airgap completion (§17 / §21.4): install the operator's CA
	// bundle on the Go HTTP transport + child-process env, register
	// the kind --image override, and stash the Helm-repo mirror so
	// the rewriter (called next) can sweep cfg.*ChartRepoURL onto
	// it. Apply errors only when the CA bundle is set but
	// unreadable / unparseable — fatal.
	if err := airgap.Apply(cfg.InternalCABundle, cfg.HelmRepoMirror, cfg.NodeImage); err != nil {
		fmt.Fprintln(os.Stderr, "✗", err.Error())
		os.Exit(1)
	}
	airgap.RewriteConfigChartURLs(cfg)
	shell.SetKindNodeImage(cfg.NodeImage)

	// §16 c2: read cfg.Cost.Credentials + per-provider state from the
	// per-config bootstrap-config Secret (<ConfigName>-bootstrap-config
	// in yage-system). Best-effort: first run and offline modes are
	// silent no-ops.
	//
	// Three branches correspond to the three supported modes:
	//   • KindClusterName set + ConfigNameExplicit (--config-name passed)
	//     → deterministic get: exactly one Secret targeted.
	//   • KindClusterName set, ConfigName defaulted from WorkloadClusterName
	//     → huh picker across all Secrets on that kind cluster so the user
	//       can choose among profiles / drafts for the same workload.
	//   • Neither set → scan every running kind cluster for labeled Secrets
	//     and let the user pick across all of them.
	switch {
	case cfg.KindClusterName != "" && cfg.ConfigNameExplicit:
		_ = kindsync.MergeBootstrapConfigFromKind(cfg)
	case cfg.KindClusterName != "":
		kindsync.MergeBootstrapConfigFromKindCluster(cfg)
	default:
		kindsync.MergeBootstrapConfigFromFirstKindCluster(cfg)
	}

	// Hand cost-estimation credentials + currency preferences to
	// the pricing package once at startup. Values come from
	// Secret/yage-system/bootstrap-config (when kind is reachable)
	// and env vars via config.Load. See docs/abstraction-plan.md
	// §16.
	pricing.SetCredentials(pricing.Credentials{
		AWSAccessKeyID:     cfg.Cost.Credentials.AWSAccessKeyID,
		AWSSecretAccessKey: cfg.Cost.Credentials.AWSSecretAccessKey,
		GCPAPIKey:          cfg.Cost.Credentials.GCPAPIKey,
		HetznerToken:       cfg.Cost.Credentials.HetznerToken,
		DigitalOceanToken:  cfg.Cost.Credentials.DigitalOceanToken,
		IBMCloudAPIKey:     cfg.Cost.Credentials.IBMCloudAPIKey,
	})
	pricing.SetCurrency(pricing.Currency{
		DisplayCurrency:    cfg.Cost.Currency.DisplayCurrency,
		DataCenterLocation: cfg.Cost.Currency.DataCenterLocation,
	})
	pricing.SetAirgapped(cfg.Airgapped)

	// --data-center-location: fill empty Region / Location fields on
	// every registered provider using the country centroid. Runs
	// before any orchestrator phase so subsequent code (cost-compare,
	// preflight, real provisioning) sees the resolved regions. No-op
	// when the flag isn't set.
	for _, ln := range xapiri.ApplyDataCenterLocationDefaults(cfg) {
		fmt.Fprintf(os.Stderr, "  %s\n", ln)
	}

	// Airgapped completeness checks: every public-internet path
	// the orchestrator hits has an opt-in mirror knob. Warn on
	// missing ones rather than failing — operators sometimes pull
	// charts via a sidecar proxy without a yage-side rewrite.
	// See §17 follow-up + §21.4.
	if cfg.Airgapped {
		if cfg.ImageRegistryMirror == "" {
			fmt.Fprintln(os.Stderr, "⚠ --airgapped is set but --image-registry-mirror is empty; clusterctl will try to pull CAPI provider images from public registries and likely fail. Set --image-registry-mirror <host/path> or YAGE_IMAGE_REGISTRY_MIRROR.")
		}
		if cfg.HelmRepoMirror == "" {
			fmt.Fprintln(os.Stderr, "⚠ --airgapped is set but --helm-repo-mirror is empty; Helm chart pulls (Cilium, Argo, kyverno, …) will hit the public chart repos and likely fail. Set --helm-repo-mirror <url> or YAGE_HELM_REPO_MIRROR.")
		}
		if cfg.InternalCABundle == "" {
			fmt.Fprintln(os.Stderr, "⚠ --airgapped is set but --internal-ca-bundle is empty; HTTPS calls to your internal mirror/registry may fail TLS verification if it's behind a private CA. Set --internal-ca-bundle <path> or YAGE_INTERNAL_CA_BUNDLE.")
		}
	}

	// Pre-orchestrator escape hatches — neither needs InfraProvider:
	//   --xapiri   walks the user through on-prem/cloud and sets it
	//   --print-pricing-setup is informational, not a deploy
	if cfg.Xapiri {
		if rc := xapiri.Run(os.Stdout, cfg); rc != 0 || !cfg.XapiriDeployNow {
			os.Exit(rc)
		}
		// User answered "deploy now? y" — fall through to orchestrator.Run.
	}
	if cfg.PrintPricingSetup != "" {
		switch cfg.PrintPricingSetup {
		case "all":
			for _, v := range []string{"aws", "azure", "gcp", "hetzner",
				"digitalocean", "linode", "oci", "ibmcloud"} {
				pricing.PrintOnboardingForce(os.Stdout, v)
			}
		default:
			pricing.PrintOnboardingForce(os.Stdout, cfg.PrintPricingSetup)
		}
		return
	}
	if cfg.PrintCommand != "" {
		mode := cli.SensitiveAsEnv
		switch cfg.PrintCommand {
		case "raw":
			mode = cli.SensitiveRaw
		case "masked":
			mode = cli.SensitiveMasked
		}
		fmt.Fprintln(os.Stdout, cli.RenderCommand(cfg, mode))
		return
	}

	// Wire the optional OTEL gRPC tracing backend (§24).
	// When --trace-endpoint is set, spans are exported to any OTEL-compatible
	// collector (Jaeger, Zipkin, Datadog, …). Without it the global tracer
	// is the zero-overhead NoopTracer and no OTEL code runs at runtime.
	if cfg.TraceEndpoint != "" {
		ctx := context.Background()
		tp, shutdown := obs.NewGRPCTracerProvider(
			ctx,
			cfg.TraceEndpoint,
			"yage",
			"dev",
			cfg.WorkloadClusterName,
		)
		if tp != nil {
			obs.SetTracer(obs.NewOTELTracer(tp, "yage"))
			defer shutdown(ctx)
		}
	}

	// The user must opt into a provider explicitly — or run --xapiri
	// to pick one through the TUI.
	if cfg.InfraProvider == "" {
		fmt.Fprintln(os.Stderr, "✗ no infrastructure provider selected.")
		fmt.Fprintln(os.Stderr, "  Pick one with --infra-provider <name> or INFRA_PROVIDER=<name>")
		fmt.Fprintln(os.Stderr, "  (proxmox, aws, azure, gcp, hetzner, openstack, vsphere, docker,")
		fmt.Fprintln(os.Stderr, "   digitalocean, linode, oci, ibmcloud), or run `yage --xapiri`")
		fmt.Fprintln(os.Stderr, "  to be walked through on-prem vs cloud and pick interactively.")
		os.Exit(2)
	}

	runID := obs.NewRunID()
	ctx := obs.WithRunID(context.Background(), runID)
	os.Exit(orchestrator.Run(ctx, cfg))
}