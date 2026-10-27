package ingestbuffer

import (
	"hash/fnv"
	"math/rand"
	"sync"
	"sync/atomic"
)

// shuffleShard implements deterministic shuffle-sharding, the same idea
// used by Cortex/Mimir/Loki to give each tenant a private-ish, isolated
// pseudo-random subset of a shared partition pool:
//
//  1. Hash the tenant ID (FNV-1a, 64-bit) to get a stable per-tenant seed.
//  2. Seed a PRNG with that hash and take a random permutation of
//     [0, totalPartitions).
//  3. The tenant's shard is the first `shardSize` elements of that
//     permutation.
//
// Because the seed is derived purely from the tenant ID, the same tenant
// always maps to the same shard (deterministic, no coordination needed).
// Two different tenants' shards overlap only partially (statistically,
// shardSize/totalPartitions fraction of overlap in expectation) rather
// than either (a) fully overlapping - one shared queue, in which a noisy
// tenant starves everyone - or (b) not overlapping at all - one dedicated
// queue per tenant, which doesn't scale and wastes partitions when there
// are far more tenants than partitions. With shuffle sharding, a noisy
// tenant can only ever degrade the small set of tenants that happen to
// share one of its shardSize partitions, and the probability that two
// particular tenants fully collide (share every partition) shrinks
// combinatorially as totalPartitions grows relative to shardSize.
func shuffleShard(tenantID string, shardSize, totalPartitions int) []int {
	if totalPartitions <= 0 {
		return nil
	}
	if shardSize <= 0 || shardSize > totalPartitions {
		shardSize = totalPartitions
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(tenantID))
	seed := int64(h.Sum64())
	rng := rand.New(rand.NewSource(seed))
	perm := rng.Perm(totalPartitions)
	shard := make([]int, shardSize)
	copy(shard, perm[:shardSize])
	return shard
}

// tenantRouter picks, for each Publish call, which partition within a
// tenant's shuffle shard to use, round-robining across the shard so a
// single tenant's traffic still spreads across its isolated partition set
// instead of hammering just one.
type tenantRouter struct {
	shardSize, totalPartitions int

	mu       sync.Mutex
	shards   map[string][]int
	counters map[string]*uint64
}

func newTenantRouter(shardSize, totalPartitions int) *tenantRouter {
	return &tenantRouter{
		shardSize:       shardSize,
		totalPartitions: totalPartitions,
		shards:          make(map[string][]int),
		counters:        make(map[string]*uint64),
	}
}

// partitionFor returns the partition a given Publish call for tenantID
// should land on: tenantID's shuffle shard, round-robined.
func (t *tenantRouter) partitionFor(tenantID string) int {
	t.mu.Lock()
	shard, ok := t.shards[tenantID]
	if !ok {
		shard = shuffleShard(tenantID, t.shardSize, t.totalPartitions)
		t.shards[tenantID] = shard
		var c uint64
		t.counters[tenantID] = &c
	}
	counter := t.counters[tenantID]
	t.mu.Unlock()

	if len(shard) == 0 {
		return 0
	}
	n := atomic.AddUint64(counter, 1)
	return shard[int(n)%len(shard)]
}
