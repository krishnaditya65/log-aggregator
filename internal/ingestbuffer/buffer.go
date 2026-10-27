package ingestbuffer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Buffer is the public entry point: a partitioned, replicated,
// backpressure-aware, tenant-fair commit-log buffer.
type Buffer struct {
	cfg        Config
	partitions []*partition
	router     *tenantRouter

	mu           sync.Mutex
	tokenBuckets map[string]*tokenBucket

	closed bool

	replicaHandles []*dirReplicator // for Close()
}

// New creates (or reopens, replaying existing WAL segments) a Buffer per
// cfg.
func New(cfg Config) (*Buffer, error) {
	if cfg.Partitions <= 0 {
		return nil, fmt.Errorf("ingestbuffer: Partitions must be > 0")
	}
	if cfg.ShardSize <= 0 || cfg.ShardSize > cfg.Partitions {
		cfg.ShardSize = cfg.Partitions
	}
	if cfg.MaxPartitionDepth <= 0 {
		cfg.MaxPartitionDepth = 10000
	}
	if cfg.TenantRateLimit <= 0 {
		cfg.TenantRateLimit = 1e9 // effectively unlimited
	}
	if cfg.TenantBurst <= 0 {
		cfg.TenantBurst = cfg.TenantRateLimit
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("ingestbuffer: mkdir %s: %w", cfg.Dir, err)
	}

	replicaDirs := cfg.ReplicaDirs
	if len(replicaDirs) == 0 {
		replicaDirs = []string{filepath.Join(cfg.Dir, "replica-0")}
	}

	b := &Buffer{
		cfg:          cfg,
		router:       newTenantRouter(cfg.ShardSize, cfg.Partitions),
		tokenBuckets: make(map[string]*tokenBucket),
	}

	for i := 0; i < cfg.Partitions; i++ {
		partDir := filepath.Join(cfg.Dir, fmt.Sprintf("partition-%d", i))
		if err := os.MkdirAll(partDir, 0o755); err != nil {
			return nil, err
		}
		wal, err := openWAL(filepath.Join(partDir, "wal.log"))
		if err != nil {
			return nil, err
		}
		var replicators []Replicator
		for _, rd := range replicaDirs {
			rep, err := newDirReplicator(filepath.Join(rd, fmt.Sprintf("partition-%d", i)), "replica.log")
			if err != nil {
				return nil, err
			}
			replicators = append(replicators, rep)
			b.replicaHandles = append(b.replicaHandles, rep)
		}
		b.partitions = append(b.partitions, newPartition(i, wal, replicators))
	}

	return b, nil
}

func (b *Buffer) tenantBucket(tenantID string) *tokenBucket {
	b.mu.Lock()
	defer b.mu.Unlock()
	tb, ok := b.tokenBuckets[tenantID]
	if !ok {
		tb = newTokenBucket(b.cfg.TenantRateLimit, b.cfg.TenantBurst)
		b.tokenBuckets[tenantID] = tb
	}
	return tb
}

// Publish publishes payload for tenantID with the given priority (an
// arbitrary caller-defined string; "debug" is treated specially by the
// backpressure shedding policy - see PriorityDebug).
//
// Order of operations:
//  1. Tenant token-bucket rate limit check -> ErrRateLimited.
//  2. Shuffle-shard partition selection for tenantID.
//  3. Backpressure check against that partition's backlog depth:
//     - debug priority over threshold -> dropped, ErrDropped.
//     - other priority over threshold -> ErrBackpressure (PolicyReject) or
//     block until it clears (PolicyBlock, subject to ctx).
//  4. WAL append (fsync) + replication to a quorum of replicas.
func (b *Buffer) Publish(ctx context.Context, tenantID, priority string, payload []byte) error {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return ErrClosed
	}

	if !b.tenantBucket(tenantID).Allow() {
		return ErrRateLimited
	}

	partIdx := b.router.partitionFor(tenantID)
	p := b.partitions[partIdx]

	for p.depth() >= int64(b.cfg.MaxPartitionDepth) {
		if priority == PriorityDebug {
			return ErrDropped
		}
		if b.cfg.Policy == PolicyReject {
			return ErrBackpressure
		}
		// PolicyBlock: wait for the backlog to drain, or ctx to end.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}

	_, err := p.publish(tenantID, priority, payload)
	return err
}

// Subscribe returns a channel delivering every message published to
// `partition` from `fromOffset` onward (replayed from the on-disk WAL),
// followed by live messages as they're published.
func (b *Buffer) Subscribe(partitionIdx int, fromOffset int64) (<-chan Message, error) {
	if partitionIdx < 0 || partitionIdx >= len(b.partitions) {
		return nil, ErrUnknownPartition
	}
	return b.partitions[partitionIdx].subscribe(fromOffset, b.cfg.SubscriberBuffer)
}

// Commit records that `offset` (and everything before it) has been fully
// processed for `partition`, advancing the backlog/backpressure watermark
// and forming the basis of consumer-offset tracking / replay resumption.
func (b *Buffer) Commit(partitionIdx int, offset int64) error {
	if partitionIdx < 0 || partitionIdx >= len(b.partitions) {
		return ErrUnknownPartition
	}
	b.partitions[partitionIdx].commit(offset)
	return nil
}

// CommittedOffset returns the last committed offset for a partition (-1 if
// none committed yet).
func (b *Buffer) CommittedOffset(partitionIdx int) (int64, error) {
	if partitionIdx < 0 || partitionIdx >= len(b.partitions) {
		return 0, ErrUnknownPartition
	}
	return b.partitions[partitionIdx].committed(), nil
}

// Depth returns the current backlog size for a partition.
func (b *Buffer) Depth(partitionIdx int) (int64, error) {
	if partitionIdx < 0 || partitionIdx >= len(b.partitions) {
		return 0, ErrUnknownPartition
	}
	return b.partitions[partitionIdx].depth(), nil
}

// PartitionFor exposes the shuffle-shard routing decision for a tenant,
// mainly for tests/observability.
func (b *Buffer) PartitionFor(tenantID string) int {
	return b.router.partitionFor(tenantID)
}

// Close closes all partitions (WALs + replicas) and live subscriber
// channels.
func (b *Buffer) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()

	var firstErr error
	for _, p := range b.partitions {
		if err := p.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, r := range b.replicaHandles {
		if err := r.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
