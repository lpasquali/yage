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
	"os"

	"github.com/lpasquali/yage/internal/bootstrap"
	"github.com/lpasquali/yage/internal/cli"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/pricing"

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
)

func main() {
	cfg := config.Load()
	cli.Parse(cfg, os.Args[1:])
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
	os.Exit(bootstrap.Run(cfg))
}
