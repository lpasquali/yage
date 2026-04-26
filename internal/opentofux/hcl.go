package opentofux

// IdentityHCL is the HCL body written to ${state_dir}/proxmox-identity.tf
// by WriteEmbeddedFiles. Verbatim copy of the bash heredoc
// (yage.sh L2885-L3037).
const IdentityHCL = `# Plugin: bpg/proxmox

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
`

// BPGProviderHCL is the single-line main.tf written to a scratch
// directory by install_bpg_proxmox_provider so that `tofu init` warms the
// plugin cache. Matches the bash heredoc in install_bpg_proxmox_provider
// (L2866-L2874).
const BPGProviderHCL = `terraform {
  required_providers {
    proxmox = {
      source = "bpg/proxmox"
    }
  }
}
`
