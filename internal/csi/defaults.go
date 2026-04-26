// defaults.go — per-provider default driver list (§20.5).
//
// The full §20.1 matrix names 12 provider-default drivers + 3 cross-
// provider opt-ins + 4 virt-platform extras. Phase F (this commit,
// scoped) ships AWS-EBS, Azure-Disk, and GCP-PD; the remaining
// drivers land in follow-ups. Until those drivers register
// themselves, DefaultsFor() returns the driver names this commit
// promises — orchestrator calls Selector() which silently drops any
// name that isn't in the registry yet, so unimplemented entries are
// safe stubs.
package csi

// DefaultsFor returns the list of CSI driver names yage installs by
// default for the given infrastructure provider. Empty result means
// "no default — operator picks". The ordering matters: when
// cfg.CSI.DefaultClass is empty, the first installable driver in
// the slice supplies the default StorageClass.
//
// Per the user's Phase F scope decision, the table below only lists
// names this commit ships. Adding hetzner/digitalocean/linode/etc.
// is a follow-up commit — same shape, drop a new package under
// internal/csi/<name>/ and add an entry here.
func DefaultsFor(provider string) []string {
	switch provider {
	case "aws":
		return []string{"aws-ebs"}
	case "azure":
		return []string{"azure-disk"}
	case "gcp":
		return []string{"gcp-pd"}
	default:
		return nil
	}
}
