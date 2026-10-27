// Package ingestbuffer implements a Kafka-like, partitioned, append-only,
// on-disk commit-log buffer sitting between HTTP/OTLP ingestion and
// downstream processing/storage. It provides:
//
//   - Partitioned append-only log segments with per-partition sequential
//     offsets and consumer offset commit/replay (wal.go, partition.go).
//   - Backpressure: Publish rejects (or, with PolicyBlock, blocks) once a
//     partition's unconsumed backlog crosses a configured depth, shedding
//     low-priority ("debug") traffic first (buffer.go).
//   - Tenant-aware fair queuing: a per-tenant token-bucket rate limiter
//     plus shuffle-shard partition assignment so no single tenant can
//     starve others (tokenbucket.go, shuffleshard.go).
//   - HA WAL semantics: fsync-on-write WAL entries, replicated to a
//     pluggable Replicator, requiring a quorum of replica acks before
//     Publish returns success (wal.go, replicator.go).
package ingestbuffer

import "time"

// Priority levels understood by the backpressure shedding policy.
// Any other string is treated as a normal/high priority (never shed).
const (
	PriorityDebug = "debug"
)

// Message is a single record delivered to a Subscribe channel.
type Message struct {
	Partition int
	Offset    int64
	TenantID  string
	Priority  string
	Payload   []byte
	Timestamp time.Time
}

// BackpressurePolicy controls what Publish does when a partition's backlog
// (latest offset - last committed offset) reaches MaxPartitionDepth.
type BackpressurePolicy int

const (
	// PolicyReject makes Publish return ErrBackpressure immediately for
	// non-debug-priority messages once a partition is over threshold.
	// debug-priority messages are always dropped (ErrDropped) rather than
	// rejected, regardless of policy - shedding low-value traffic is
	// preferred over pushing the error back to the producer.
	PolicyReject BackpressurePolicy = iota

	// PolicyBlock makes Publish block (subject to ctx cancellation) until
	// the partition's backlog drops back under threshold, for non-debug
	// messages. debug-priority messages are still dropped immediately.
	PolicyBlock
)

// Config configures a Buffer.
type Config struct {
	// Dir is the root directory for on-disk WAL segments (one
	// sub-directory per partition).
	Dir string

	// Partitions is the total number of partitions (virtual queues) in the
	// buffer. Tenants are shuffle-sharded across a subset of these.
	Partitions int

	// ShardSize is how many of the Partitions each tenant is assigned to
	// (its "shuffle shard"). Must be <= Partitions. A typical value is
	// small (e.g. 2-4) so that a noisy tenant can only ever contend with
	// the other tenants sharing its shard, not the whole fleet.
	ShardSize int

	// MaxPartitionDepth is the backlog (unconsumed message count) at which
	// a partition is considered under backpressure.
	MaxPartitionDepth int

	// Policy controls Publish behavior once a partition is over
	// MaxPartitionDepth (for non-debug priority messages).
	Policy BackpressurePolicy

	// ReplicaDirs simulates N replica "AZs": each is a local directory
	// that receives a durable copy of every WAL entry. Publish acks only
	// once a quorum (majority, including the primary WAL) have durably
	// written the entry. If empty, defaults to a single in-process replica
	// under Dir/replica-0 (quorum size 1).
	ReplicaDirs []string

	// TenantRateLimit is the sustained publish rate (messages/sec) allowed
	// per tenant.
	TenantRateLimit float64

	// TenantBurst is the token-bucket burst capacity per tenant.
	TenantBurst float64

	// SubscriberBuffer is the channel buffer size used for live delivery
	// to Subscribe callers.
	SubscriberBuffer int
}
