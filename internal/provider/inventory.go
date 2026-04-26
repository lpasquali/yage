package provider

// Inventory is the cloud-correct picture of "what's there + what's
// free" returned by Provider.Inventory in a single round-trip. It
// replaces the older split between HostCapacity (Total) and
// ExistingUsage (Used) — those two quantities were always queried
// together at every call site, and on non-flat-pool clouds the
// arithmetic Available = Total − Used is wrong (per-family quotas,
// count-based limits, multi-level hierarchies don't compose with
// flat subtraction).
//
// Available is computed by the provider from its own quota model,
// NOT by the caller. On flat-pool clouds (Proxmox, OpenStack
// per-project) Available = Total − Used; on AWS/GCP/Azure/Hetzner/
// vSphere the provider returns ErrNotApplicable instead because
// preflight isn't expressible as flat ResourceTotals.
//
// Hosts is the typed list of compute hosts / AZs / zones the
// totals were aggregated over (Proxmox: cluster nodes; vSphere:
// ESXi hosts; AWS/GCP/Azure: AZs in the active region). Empty for
// providers where the concept doesn't apply, or when the provider
// returned ErrNotApplicable.
//
// Notes is the escape hatch for providers that have something to
// say but can't express it as Total/Used/Available — e.g. Hetzner:
// "3 of 10 servers used", or "quota raise pending". Notes are
// human-display-only; the orchestrator never parses them.
type Inventory struct {
	Total     ResourceTotals // host hardware totals (informational)
	Used      ResourceTotals // running workload (informational, drives plan output)
	Available ResourceTotals // cloud-correct headroom — what preflight checks
	Hosts     []string       // typed compute-host / AZ list
	Notes     []string       // provider advisories (human-display only)
}

// ResourceTotals is a flat CPU + memory + storage triple. Storage
// is reported as both an aggregate (StorageGiB) and an optional
// per-class breakdown (StorageByClass) — empty/nil when the
// provider has a single backend.
//
// StorageByClass keys come from the cloud's storage-class taxonomy:
//
//	AWS:        gp3 / io2 / standard / sc1
//	GCP:        pd-balanced / pd-ssd / pd-standard
//	OpenStack:  Cinder backends (fast / slow / archive)
//	vSphere:    Datastores
//	Proxmox:    storage pool names from /api2/json/storage
type ResourceTotals struct {
	Cores          int
	MemoryMiB      int64
	StorageGiB     int64
	StorageByClass map[string]int64
}
