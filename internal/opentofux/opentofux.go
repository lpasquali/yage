// Package opentofux will host the embedded Proxmox identity HCL config +
// apply/state-rm/recreate logic. State files are OpenTofu-format (== legacy
// Terraform format, read/written by both tools unchanged) and the binary
// invoked is `tofu`.
//
// Bash source map (bootstrap-capi.sh):
//   - install_bpg_proxmox_provider                          ~L2860-2879
//   - write_embedded_terraform_files                        ~L2881-3040
//   - apply_proxmox_identity_terraform                      ~L3040-3070
//   - recreate_proxmox_identities_terraform                 ~L3201-3283
//   - proxmox_identity_terraform_state_rm_all               ~L3141-3152
//   - destroy_proxmox_identity_terraform_state              ~L7497-7575
//
// The on-disk state directory is ~/.bootstrap-capi/proxmox-identity-terraform/
// and the state file inside it is the conventional terraform.tfstate (the
// same filename OpenTofu uses by default).
package opentofux

import "fmt"

func todo(desc string) error { return fmt.Errorf("not yet ported: %s", desc) }
