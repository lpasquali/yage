// yage is a Cluster API bootstrap orchestrator: provisions a kind-based
// CAPI management plane and brings up a workload cluster on the configured
// infrastructure provider, then layers in CNI, CSI, and an Argo CD app-of-apps.
//
// Internal structure is modular Go packages under internal/, one per concern.
// Started life as a Go port of a bash script; the bash source is no longer
// tracked here, but historical provenance comments still cite the original
// line ranges where useful.
package main

import (
	"fmt"
	"os"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/orchestrator"
	"github.com/lpasquali/yage/internal/ui/cli"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"
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
	// its init() which calls csi.Register. Phase F (scoped) ships
	// AWS-EBS, Azure-Disk, GCP-PD; the rest of the §20.1 matrix
	// lands in follow-ups.
	_ "github.com/lpasquali/yage/internal/csi/awsebs"
	_ "github.com/lpasquali/yage/internal/csi/azuredisk"
	_ "github.com/lpasquali/yage/internal/csi/gcppd"
)

func main() {
	cfg := config.Load()
	cli.Parse(cfg, os.Args[1:])

	// §16 c2: read cfg.Cost.Credentials + per-provider state from
	// Secret/yage-system/bootstrap-config when the kind cluster is
	// reachable. Best-effort: if the Secret doesn't exist (first
	// run) or the kind cluster isn't up yet, the call is a silent
	// no-op and cfg keeps what config.Load got from env.
	//
	// Fill-empty-only semantics: env values that were explicitly
	// set survive the merge.
	_ = kindsync.MergeBootstrapConfigFromKind(cfg)

	// Hand cost-estimation credentials + currency preferences to the
	// pricing package once at startup. After Phase D ships, these
	// values come from Secret/yage-system/bootstrap-config; today they
	// come from env vars via config.Load. See docs/abstraction-plan.md §16.
	pricing.SetCredentials(pricing.Credentials{
		GCPAPIKey:         cfg.Cost.Credentials.GCPAPIKey,
		HetznerToken:      cfg.Cost.Credentials.HetznerToken,
		DigitalOceanToken: cfg.Cost.Credentials.DigitalOceanToken,
		IBMCloudAPIKey:    cfg.Cost.Credentials.IBMCloudAPIKey,
	})
	pricing.SetCurrency(pricing.Currency{
		DisplayCurrency: cfg.Cost.Currency.DisplayCurrency,
		EURUSDOverride:  cfg.Cost.Currency.EURUSDOverride,
	})
	pricing.SetAirgapped(cfg.Airgapped)

	// One-liner when the active infrastructure provider was
	// silently defaulted (no INFRA_PROVIDER env, no --infra-provider
	// flag). yage was Proxmox-only originally; in the multi-cloud
	// era this default is a vestige that surprises new users. See
	// docs/abstraction-plan.md §18.
	if cfg.InfraProviderDefaulted {
		fmt.Fprintln(os.Stderr, "ℹ INFRA_PROVIDER not set — defaulting to 'proxmox'. Pick one explicitly with --infra-provider <name> (proxmox, aws, azure, gcp, hetzner, openstack, vsphere, capd, …) or set INFRA_PROVIDER=<name>.")
	}

	// Warn when --airgapped is set without an internal image
	// mirror — clusterctl will then try to pull CAPI provider
	// images from the public registries and fail. See §17 follow-up.
	if cfg.Airgapped && cfg.ImageRegistryMirror == "" {
		fmt.Fprintln(os.Stderr, "⚠ --airgapped is set but --image-registry-mirror is empty; clusterctl will try to pull CAPI provider images from public registries and likely fail. Set --image-registry-mirror <host/path> or YAGE_IMAGE_REGISTRY_MIRROR.")
	}

	if cfg.Xapiri {
		os.Exit(xapiri.Run(os.Stdout, cfg))
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
	os.Exit(orchestrator.Run(cfg))
}
