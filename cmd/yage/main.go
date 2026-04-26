// yage is the Go port of yage.sh.
//
// Goal: identical CLI surface (same flags, same env vars, same exit codes,
// same log output format). Internal structure is modular Go packages under
// internal/, one per concern.
//
// The port is incremental. Today:
//   - CLI parse matches bash parse_options() 1:1
//   - Usage output matches bash usage() (embedded from the .sh header)
//   - Dependency installers (ensure_*) are ported
//   - Top-level orchestration is stubbed with pointers back to bash line ranges
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
