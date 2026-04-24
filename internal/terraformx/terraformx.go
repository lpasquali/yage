// Package terraformx will host the embedded Proxmox identity Terraform
// config + apply/state-rm logic. Stub only.
//
// Bash source map:
//   - install_bpg_proxmox_provider                          ~L2860-2880
//   - write_embedded_terraform_files                        ~L2881-3040
//   - apply_proxmox_identity_terraform                      ~L3040-3070
//   - recreate_proxmox_identities_terraform                 ~L3201-3283
//   - proxmox_identity_terraform_state_rm_all               ~L3141-3154
package terraformx

import "fmt"

func todo(desc string) error { return fmt.Errorf("not yet ported: %s", desc) }
