// Package proxmox will host Proxmox API helpers + identity/token derivation
// logic. Stub only.
//
// Bash source map:
//   - derive_proxmox_identity_suffix                         ~L1227-1242
//   - proxmox_user_id_with_suffix / refresh_* / derive_cilium_* ~L1244-1463
//   - resolve_available_cluster_set_id_for_roles             ~L1320-1463
//   - proxmox_api_json_url / pve_api_host_base_url           ~L3316-3330
//   - normalize_proxmox_token_secret / validate_*            ~L3333-3400
package proxmox

import "fmt"

func todo(desc string) error { return fmt.Errorf("not yet ported: %s", desc) }
