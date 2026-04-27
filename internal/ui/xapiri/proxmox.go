// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package xapiri

// proxmox.go — structured credential + network steps for the Proxmox
// on-prem fork.  Replaces the reflection-driven step6_providerDetails
// for this provider, which was too noisy (it asked for every empty
// string field regardless of relevance) and lacked the managed-vs-BYO
// credential fork.
//
// Two credential modes:
//
//	managed  — operator provides admin username + token; yage runs
//	           OpenTofu to create CAPI (CAPMOX) and CSI identities.
//	           The orchestrator's existing phase0IdentityBootstrap
//	           logic fires automatically when Token/Secret/CSITokenID/
//	           CSITokenSecret are all empty.
//
//	byo      — operator provides the CAPMOX token (URL + Token +
//	           Secret) and CSI token (CSITokenID + CSITokenSecret)
//	           directly; the orchestrator's HaveClusterctlCredsInEnv()
//	           returns true and OpenTofu is skipped.

import "fmt"

// credentialMode is the local enum for the two Proxmox identity flows.
type credentialMode int

const (
	credManaged credentialMode = iota
	credBYO
)

// step6_proxmox collects the Proxmox-specific settings: core API
// fields, then a managed-vs-BYO credential fork.
func (s *state) step6_proxmox() error {
	s.r.section("proxmox settings")

	// --- Core fields (always asked) ---
	s.cfg.Providers.Proxmox.URL = s.r.promptString(
		"Proxmox URL (e.g. https://pve.example.com:8006)",
		s.cfg.Providers.Proxmox.URL)

	s.cfg.Providers.Proxmox.Node = s.r.promptString(
		"Proxmox node name",
		s.cfg.Providers.Proxmox.Node)

	s.cfg.Providers.Proxmox.Region = s.r.promptString(
		"region / datacenter label (used in CAPI topology tags)",
		s.cfg.Providers.Proxmox.Region)

	s.cfg.Providers.Proxmox.Bridge = s.r.promptString(
		"VM network bridge",
		orStr(s.cfg.Providers.Proxmox.Bridge, "vmbr0"))

	s.cfg.Providers.Proxmox.Pool = s.r.promptString(
		"Proxmox pool (empty = no pool)",
		s.cfg.Providers.Proxmox.Pool)

	s.cfg.Providers.Proxmox.CloudinitStorage = s.r.promptString(
		"cloud-init storage name",
		orStr(s.cfg.Providers.Proxmox.CloudinitStorage, "local"))

	s.cfg.Providers.Proxmox.TemplateID = s.r.promptString(
		"VM template ID",
		orStr(s.cfg.Providers.Proxmox.TemplateID, "104"))

	// --- Credential mode ---
	s.r.info("")
	mode := s.promptCredMode()

	switch mode {
	case credManaged:
		return s.step6_proxmox_managed()
	case credBYO:
		return s.step6_proxmox_byo()
	}
	return nil
}

const (
	choiceManaged = "managed     — provide admin creds; yage creates CAPI/CSI tokens via OpenTofu"
	choiceBYO     = "bring-your-own — provide existing CAPI and CSI tokens directly"
)

// promptCredMode asks the operator which credential flow to use.
// Defaults to managed when no BYO tokens are already set.
func (s *state) promptCredMode() credentialMode {
	hasBYO := s.cfg.Providers.Proxmox.CAPIToken != "" &&
		s.cfg.Providers.Proxmox.CAPISecret != "" &&
		s.cfg.Providers.Proxmox.CSITokenID != "" &&
		s.cfg.Providers.Proxmox.CSITokenSecret != ""
	cur := choiceManaged
	if hasBYO {
		cur = choiceBYO
	}
	pick := s.r.promptChoice("credential mode:", []string{choiceManaged, choiceBYO}, cur)
	if pick == choiceBYO {
		return credBYO
	}
	return credManaged
}

// step6_proxmox_managed collects admin credentials for the OpenTofu
// identity bootstrap path.  Token/Secret/CSITokenID/CSITokenSecret
// are intentionally left empty so the orchestrator's
// phase0IdentityBootstrap logic fires and creates them.
func (s *state) step6_proxmox_managed() error {
	s.r.info("managed mode — yage will run OpenTofu to create CAPI and CSI identities.")
	s.cfg.Providers.Proxmox.AdminUsername = s.r.promptString(
		"admin API username",
		orStr(s.cfg.Providers.Proxmox.AdminUsername, "root@pam!capi-bootstrap"))

	token, err := s.r.promptSecretHidden("admin API token", s.cfg.Providers.Proxmox.AdminToken)
	if err != nil {
		return fmt.Errorf("reading admin token: %w", err)
	}
	s.cfg.Providers.Proxmox.AdminToken = token
	// Clear BYO fields so the orchestrator doesn't accidentally see
	// a partial set and skip OpenTofu.
	s.cfg.Providers.Proxmox.CAPIToken = ""
	s.cfg.Providers.Proxmox.CAPISecret = ""
	s.cfg.Providers.Proxmox.CSITokenID = ""
	s.cfg.Providers.Proxmox.CSITokenSecret = ""
	return nil
}

// step6_proxmox_byo collects the CAPMOX and CSI tokens the operator
// has already created on Proxmox.  When Token + Secret + CSITokenID +
// CSITokenSecret are all non-empty, HaveClusterctlCredsInEnv() returns
// true and the orchestrator skips the OpenTofu identity phase entirely.
func (s *state) step6_proxmox_byo() error {
	s.r.info("bring-your-own mode — OpenTofu identity bootstrap will be skipped.")
	s.r.info("Provide the existing Proxmox API tokens for CAPMOX and CSI.")

	s.cfg.Providers.Proxmox.CAPIToken = s.r.promptString(
		"CAPMOX token ID (e.g. capmox@pve!capmox-token)",
		s.cfg.Providers.Proxmox.CAPIToken)

	secret, err := s.r.promptSecretHidden("CAPMOX token secret", s.cfg.Providers.Proxmox.CAPISecret)
	if err != nil {
		return fmt.Errorf("reading CAPMOX token secret: %w", err)
	}
	s.cfg.Providers.Proxmox.CAPISecret = secret

	s.cfg.Providers.Proxmox.CSITokenID = s.r.promptString(
		"CSI token ID (e.g. csi@pve!csi-token)",
		s.cfg.Providers.Proxmox.CSITokenID)

	csiSecret, err := s.r.promptSecretHidden("CSI token secret", s.cfg.Providers.Proxmox.CSITokenSecret)
	if err != nil {
		return fmt.Errorf("reading CSI token secret: %w", err)
	}
	s.cfg.Providers.Proxmox.CSITokenSecret = csiSecret
	// Clear admin creds — they're not needed when BYO tokens are supplied.
	s.cfg.Providers.Proxmox.AdminToken = ""
	return nil
}

// step6_5_proxmox_network collects the network settings for all Proxmox
// clusters: workload VIP/range/gateway/DNS, SSH keys, cluster names, and —
// when pivot is enabled — the management cluster's VIP and node IP range.
func (s *state) step6_5_proxmox_network() error {
	s.r.section("workload cluster — network")

	s.cfg.ControlPlaneEndpointIP = s.r.promptString(
		"control-plane VIP (must be outside the node IP range)",
		orStr(s.cfg.ControlPlaneEndpointIP, "192.168.0.20"))

	s.cfg.NodeIPRanges = s.r.promptString(
		"node IP range (e.g. 192.168.0.21-192.168.0.30)",
		orStr(s.cfg.NodeIPRanges, "192.168.0.21-192.168.0.30"))

	s.cfg.Gateway = s.r.promptString(
		"default gateway",
		orStr(s.cfg.Gateway, "192.168.0.1"))

	s.cfg.IPPrefix = s.r.promptString(
		"subnet prefix length (e.g. 24)",
		orStr(s.cfg.IPPrefix, "24"))

	s.cfg.DNSServers = s.r.promptString(
		"DNS servers (comma-separated)",
		orStr(s.cfg.DNSServers, "8.8.8.8,8.8.4.4"))

	s.cfg.VMSSHKeys = s.r.promptSSHKeys(s.cfg.VMSSHKeys)

	s.cfg.WorkloadClusterName = s.r.promptString(
		"workload cluster name",
		orStr(s.cfg.WorkloadClusterName, "capi-quickstart"))

	// Management cluster network — only needed when pivot is enabled.
	// These are the two inputs renderManagementManifest requires that
	// have no sensible default (the VIP and node range must be distinct
	// from the workload cluster's ranges).
	if s.cfg.Pivot.Enabled {
		s.r.section("management cluster — network")
		s.r.info("The management cluster needs its own VIP and node IP range,")
		s.r.info("separate from the workload cluster's ranges above.")

		s.cfg.Mgmt.ControlPlaneEndpointIP = s.r.promptString(
			"management cluster VIP",
			orStr(s.cfg.Mgmt.ControlPlaneEndpointIP, "192.168.0.30"))

		s.cfg.Mgmt.NodeIPRanges = s.r.promptString(
			"management node IP range (e.g. 192.168.0.31-192.168.0.32)",
			orStr(s.cfg.Mgmt.NodeIPRanges, "192.168.0.31-192.168.0.32"))

		s.cfg.Mgmt.ClusterName = s.r.promptString(
			"management cluster name",
			orStr(s.cfg.Mgmt.ClusterName, "capi-management"))

		s.cfg.Providers.Proxmox.Mgmt.Pool = s.r.promptString(
			"management cluster pool (empty = no pool)",
			orStr(s.cfg.Providers.Proxmox.Mgmt.Pool, s.cfg.Mgmt.ClusterName))
	}

	return nil
}

// orStr returns s when non-empty, else def.
func orStr(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
