// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cost

// managed.go — managed-services capability matrix per vendor.
//
// Each registered cluster need (Postgres, message queue, object
// storage, in-memory cache) is satisfied either by the vendor's
// SaaS offering OR by yage provisioning the substitute in-cluster.
// Per-provider cost.go consults this matrix when assembling the
// bill: capable vendors get a managed-service line item priced
// against the catalog; gap vendors get a forecast line for the
// extra worker capacity (and, where relevant, persistent volumes)
// that the in-cluster substitute will consume.
//
// When the active vendor offers managed Postgres AND the operator
// hasn't disabled it (cfg.UseManagedPostgres = true, the default),
// the orchestrator skips the CloudNativePG helm install and the
// workload uses the SaaS database directly.

// ManagedService is the abstract slot a workload needs filled.
type ManagedService string

const (
	// MSPostgres is a Postgres-compatible relational database.
	// SaaS examples: AWS RDS / Aurora, Azure Database for PostgreSQL,
	// GCP Cloud SQL, DigitalOcean Managed Databases, OCI Autonomous DB,
	// IBM Cloud Databases for PostgreSQL.
	// In-cluster substitute: CloudNativePG operator + a worker pod.
	MSPostgres ManagedService = "postgres"

	// MSMessageQueue is an at-least-once durable queue.
	// SaaS examples: AWS SQS, Azure Service Bus, GCP Pub/Sub,
	// OCI Streaming, IBM MQ.
	// In-cluster substitute: NATS JetStream or RabbitMQ.
	MSMessageQueue ManagedService = "mq"

	// MSObjectStore is an S3-compatible bucket service.
	// SaaS examples: AWS S3, Azure Blob, GCP GCS, DigitalOcean Spaces,
	// Linode Object Storage, OCI Object Storage, IBM Cloud Object Storage.
	// In-cluster substitute: MinIO with a Cloud Volume backing.
	MSObjectStore ManagedService = "objstore"

	// MSCache is a key-value in-memory cache.
	// SaaS examples: AWS ElastiCache, Azure Cache for Redis,
	// GCP Memorystore.
	// In-cluster substitute: Redis or Memcached pod (RAM-pinned).
	MSCache ManagedService = "cache"
)

// vendorOffers lists the SaaS services each registered vendor
// exposes with priced catalog entries yage can read live. Entries
// reflect public availability — operator IAM may further restrict
// what's actually usable. Empty entries mean "no SaaS for this slot
// on this vendor; forecast the in-cluster substitute instead."
var vendorOffers = map[string]map[ManagedService]bool{
	"aws": {
		MSPostgres:     true, // RDS / Aurora
		MSMessageQueue: true, // SQS
		MSObjectStore:  true, // S3
		MSCache:        true, // ElastiCache
	},
	"azure": {
		MSPostgres:     true, // Azure Database for PostgreSQL
		MSMessageQueue: true, // Service Bus
		MSObjectStore:  true, // Blob Storage
		MSCache:        true, // Azure Cache for Redis
	},
	"gcp": {
		MSPostgres:     true, // Cloud SQL
		MSMessageQueue: true, // Pub/Sub
		MSObjectStore:  true, // GCS
		MSCache:        true, // Memorystore
	},
	"digitalocean": {
		MSPostgres:     true,  // DO Managed Databases
		MSMessageQueue: false, // no first-party MQ
		MSObjectStore:  true,  // Spaces
		MSCache:        true,  // Managed Redis
	},
	"linode": {
		MSPostgres:     true,  // Linode Managed Databases (Postgres)
		MSMessageQueue: false,
		MSObjectStore:  true,  // Linode Object Storage
		MSCache:        false,
	},
	"oci": {
		MSPostgres:     true, // OCI Database for PostgreSQL / Autonomous
		MSMessageQueue: true, // OCI Streaming
		MSObjectStore:  true, // OCI Object Storage
		MSCache:        true, // OCI Cache (Redis)
	},
	"ibmcloud": {
		MSPostgres:     true, // IBM Cloud Databases for PostgreSQL
		MSMessageQueue: true, // IBM MQ
		MSObjectStore:  true, // IBM Cloud Object Storage
		MSCache:        true, // IBM Cloud Databases for Redis
	},
	"hetzner": {
		MSPostgres:     false, // no managed PG
		MSMessageQueue: false,
		MSObjectStore:  false, // Storage Box is S3-adjacent, not equivalent
		MSCache:        false,
	},
	"proxmox":   {}, // self-hosted: every service is in-cluster
	"vsphere":   {}, // same
	"openstack": {}, // operator-dependent — assume in-cluster for the cost model
	"docker":    {}, // ephemeral test
}

// VendorOffersManaged reports whether vendor has a SaaS managed
// version of svc. Unknown vendors fall back to "no" so the cost
// model errs toward forecasting in-cluster substitutes.
func VendorOffersManaged(vendor string, svc ManagedService) bool {
	if m, ok := vendorOffers[vendor]; ok {
		return m[svc]
	}
	return false
}

// InClusterFootprint is the resource cost of running an in-cluster
// substitute when the vendor doesn't offer the SaaS equivalent.
// Sized to the small-to-medium-prod end of typical workloads;
// operators with heavier needs override via cfg.Workload.* fields.
type InClusterFootprint struct {
	// CPUMillicores is the steady-state CPU reservation the pod
	// requests. Used to grow the worker fleet when capacity-pressed.
	CPUMillicores int
	// MemoryMiB is the steady-state memory reservation.
	MemoryMiB int
	// PersistentGB is the size of the persistent volume the pod
	// claims (0 when the substitute is RAM-only, e.g. cache).
	PersistentGB int
	// Notes is a short human-readable hint surfaced in the bill
	// breakdown ("MinIO single-node, 4 vCPU / 8 GiB / 500 GiB volume").
	Notes string
}

// SubstituteFootprint returns the in-cluster footprint yage uses
// to forecast worker capacity + persistent volume cost when the
// active vendor lacks svc. Defaults match the platform-add-on tier
// the workload-shape step seeds.
func SubstituteFootprint(svc ManagedService) InClusterFootprint {
	switch svc {
	case MSPostgres:
		return InClusterFootprint{
			CPUMillicores: 2000,
			MemoryMiB:     4096,
			PersistentGB:  100,
			Notes:         "CloudNativePG single-instance (cnpg operator); cluster role 'primary'",
		}
	case MSMessageQueue:
		return InClusterFootprint{
			CPUMillicores: 1000,
			MemoryMiB:     2048,
			PersistentGB:  20,
			Notes:         "NATS JetStream single-replica (file-backed)",
		}
	case MSObjectStore:
		return InClusterFootprint{
			CPUMillicores: 1000,
			MemoryMiB:     2048,
			PersistentGB:  500,
			Notes:         "MinIO single-node (S3-compatible)",
		}
	case MSCache:
		return InClusterFootprint{
			CPUMillicores: 500,
			MemoryMiB:     2048,
			PersistentGB:  0,
			Notes:         "Redis (in-memory; no persistent volume)",
		}
	}
	return InClusterFootprint{}
}
