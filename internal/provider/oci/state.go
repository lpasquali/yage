// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package oci

// State-handoff hooks for Oracle Cloud Infrastructure (CAPOCI).
//
// See docs/abstraction-plan.md §11 + §14.D.
// OCI authentication uses a private key file on disk — only the path
// is tracked here (never the key material). The CAPOCI controller
// reads the key from the path/Secret it is configured with at deploy
// time. TenancyOCID and UserOCID are non-secret identity pointers and
// are safe to round-trip through the kind Secret.

import (
	"github.com/lpasquali/yage/internal/config"
)

// KindSyncFields persists the OCI-specific configuration the next
// yage run needs to reconstruct the active cluster. No private key
// material is included — only the path reference and non-secret OCIDs.
func (p *Provider) KindSyncFields(cfg *config.Config) map[string]string {
	out := map[string]string{}
	add := func(k, v string) {
		if v != "" {
			out[k] = v
		}
	}
	add("region", cfg.Providers.OCI.Region)
	add("control_plane_shape", cfg.Providers.OCI.ControlPlaneShape)
	add("node_shape", cfg.Providers.OCI.NodeShape)
	add("tenancy_ocid", cfg.Providers.OCI.TenancyOCID)
	add("user_ocid", cfg.Providers.OCI.UserOCID)
	add("fingerprint", cfg.Providers.OCI.Fingerprint)
	add("compartment_ocid", cfg.Providers.OCI.CompartmentOCID)
	add("image_id", cfg.Providers.OCI.ImageID)
	add("private_key_path", cfg.Providers.OCI.PrivateKeyPath)
	add("overhead_tier", cfg.Providers.OCI.OverheadTier)
	return out
}

// TemplateVars returns the OCI-specific clusterctl manifest
// substitution map.
func (p *Provider) TemplateVars(cfg *config.Config) map[string]string {
	return map[string]string{
		"OCI_REGION":           orDefault(cfg.Providers.OCI.Region, "us-ashburn-1"),
		"OCI_CP_SHAPE":         orDefault(cfg.Providers.OCI.ControlPlaneShape, "VM.Standard.E4.Flex"),
		"OCI_WORKER_SHAPE":     orDefault(cfg.Providers.OCI.NodeShape, "VM.Standard.E4.Flex"),
		"OCI_TENANCY_OCID":     cfg.Providers.OCI.TenancyOCID,
		"OCI_USER_OCID":        cfg.Providers.OCI.UserOCID,
		"OCI_COMPARTMENT_OCID": cfg.Providers.OCI.CompartmentOCID,
		"OCI_IMAGE_ID":         cfg.Providers.OCI.ImageID,
	}
}

// AbsorbConfigYAML is the reverse of KindSyncFields: reads the
// lowercase bare-key map the yage-system/bootstrap-config Secret
// schema writes (orchestrator strips the "oci." prefix before
// dispatching) and fills empty cfg fields with non-empty values.
func (p *Provider) AbsorbConfigYAML(cfg *config.Config, kv map[string]string) bool {
	assigned := false
	assign := func(cur *string, v string) {
		if *cur == "" && v != "" {
			*cur = v
			assigned = true
		}
	}
	for k, v := range kv {
		switch k {
		case "region":
			assign(&cfg.Providers.OCI.Region, v)
		case "control_plane_shape":
			assign(&cfg.Providers.OCI.ControlPlaneShape, v)
		case "node_shape":
			assign(&cfg.Providers.OCI.NodeShape, v)
		case "tenancy_ocid":
			assign(&cfg.Providers.OCI.TenancyOCID, v)
		case "user_ocid":
			assign(&cfg.Providers.OCI.UserOCID, v)
		case "fingerprint":
			assign(&cfg.Providers.OCI.Fingerprint, v)
		case "compartment_ocid":
			assign(&cfg.Providers.OCI.CompartmentOCID, v)
		case "image_id":
			assign(&cfg.Providers.OCI.ImageID, v)
		case "private_key_path":
			assign(&cfg.Providers.OCI.PrivateKeyPath, v)
		case "overhead_tier":
			assign(&cfg.Providers.OCI.OverheadTier, v)
		}
	}
	return assigned
}
