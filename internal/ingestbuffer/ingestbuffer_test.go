package ingestbuffer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestBuffer(t *testing.T, cfg Config) *Buffer {
	t.Helper()
	dir := t.TempDir()
	cfg.Dir = dir
	if cfg.Partitions == 0 {
		cfg.Partitions = 4
	}
	b, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestPublishSubscribeReplay(t *testing.T) {
	b := newTestBuffer(t, Config{Partitions: 1, ShardSize: 1, MaxPartitionDepth: 1000})

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := b.Publish(ctx, "tenantA", "info", []byte(fmt.Sprintf("msg-%d", i))); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	part := b.PartitionFor("tenantA")

	// Live subscribe from the start should get all 5 (via replay path).
	ch, err := b.Subscribe(part, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	for i := 0; i < 5; i++ {
		select {
		case msg := <-ch:
			want := fmt.Sprintf("msg-%d", i)
			if string(msg.Payload) != want {
				t.Errorf("msg %d payload = %q, want %q", i, msg.Payload, want)
			}
			if msg.Offset != int64(i) {
				t.Errorf("msg %d offset = %d, want %d", i, msg.Offset, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for message %d", i)
		}
	}

	// Replay from offset 3 should only get the last two.
	ch2, err := b.Subscribe(part, 3)
	if err != nil {
		t.Fatalf("subscribe from 3: %v", err)
	}
	for i := 3; i < 5; i++ {
		select {
		case msg := <-ch2:
			if msg.Offset != int64(i) {
				t.Errorf("replay offset = %d, want %d", msg.Offset, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for replayed message %d", i)
		}
	}
}

func TestCommitAdvancesOffsetAndDepth(t *testing.T) {
	b := newTestBuffer(t, Config{Partitions: 1, ShardSize: 1, MaxPartitionDepth: 1000})
	ctx := context.Background()
	part := b.PartitionFor("tenantA")

	for i := 0; i < 3; i++ {
		if err := b.Publish(ctx, "tenantA", "info", []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	depth, _ := b.Depth(part)
	if depth != 3 {
		t.Fatalf("depth = %d, want 3", depth)
	}
	if err := b.Commit(part, 1); err != nil {
		t.Fatal(err)
	}
	depth, _ = b.Depth(part)
	if depth != 1 {
		t.Fatalf("depth after commit = %d, want 1", depth)
	}
	committed, _ := b.CommittedOffset(part)
	if committed != 1 {
		t.Fatalf("committed offset = %d, want 1", committed)
	}
}

func TestBackpressureDropsDebugFirst(t *testing.T) {
	b := newTestBuffer(t, Config{
		Partitions:        1,
		ShardSize:         1,
		MaxPartitionDepth: 2,
		Policy:            PolicyReject,
	})
	ctx := context.Background()

	// Fill the partition to threshold.
	for i := 0; i < 2; i++ {
		if err := b.Publish(ctx, "tenantA", "info", []byte("fill")); err != nil {
			t.Fatalf("fill publish %d: %v", i, err)
		}
	}

	// Now over threshold: a debug message should be dropped (ErrDropped),
	// not merely rejected.
	if err := b.Publish(ctx, "tenantA", PriorityDebug, []byte("debug-msg")); err != ErrDropped {
		t.Fatalf("expected ErrDropped for debug over threshold, got %v", err)
	}

	// A higher-priority message should get ErrBackpressure under
	// PolicyReject, not be silently dropped.
	if err := b.Publish(ctx, "tenantA", "critical", []byte("important")); err != ErrBackpressure {
		t.Fatalf("expected ErrBackpressure for non-debug over threshold, got %v", err)
	}

	part := b.PartitionFor("tenantA")
	depth, _ := b.Depth(part)
	if depth != 2 {
		t.Fatalf("depth = %d, want 2 (debug dropped, critical rejected, neither written)", depth)
	}
}

func TestBackpressureBlockPolicyUnblocksAfterCommit(t *testing.T) {
	b := newTestBuffer(t, Config{
		Partitions:        1,
		ShardSize:         1,
		MaxPartitionDepth: 1,
		Policy:            PolicyBlock,
	})
	ctx := context.Background()
	part := b.PartitionFor("tenantA")

	if err := b.Publish(ctx, "tenantA", "info", []byte("first")); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- b.Publish(ctx, "tenantA", "info", []byte("second"))
	}()

	select {
	case <-done:
		t.Fatalf("Publish returned before backlog drained")
	case <-time.After(100 * time.Millisecond):
		// expected: still blocked
	}

	if err := b.Commit(part, 0); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("blocked publish returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("blocked publish never unblocked after commit")
	}
}

func TestTokenBucketRateLimitsNoisyTenantNotOthers(t *testing.T) {
	b := newTestBuffer(t, Config{
		Partitions:        8,
		ShardSize:         8,
		MaxPartitionDepth: 100000,
		TenantRateLimit:   5, // 5 msgs/sec
		TenantBurst:       5,
	})
	ctx := context.Background()

	noisyAllowed := 0
	for i := 0; i < 50; i++ {
		if err := b.Publish(ctx, "noisy-tenant", "info", []byte("spam")); err == nil {
			noisyAllowed++
		} else if err != ErrRateLimited {
			t.Fatalf("unexpected error from noisy tenant: %v", err)
		}
	}
	if noisyAllowed > 10 {
		t.Fatalf("noisy tenant got %d/50 publishes through instantly, rate limiting isn't working", noisyAllowed)
	}

	// A quiet tenant publishing a couple of messages should sail through
	// unaffected by the noisy tenant's exhausted bucket.
	for i := 0; i < 3; i++ {
		if err := b.Publish(ctx, "quiet-tenant", "info", []byte("hi")); err != nil {
			t.Fatalf("quiet tenant publish %d failed: %v", i, err)
		}
	}
}

func TestShuffleShardIsDeterministicAndBounded(t *testing.T) {
	const totalPartitions = 32
	const shardSize = 4

	shardA1 := shuffleShard("tenant-a", shardSize, totalPartitions)
	shardA2 := shuffleShard("tenant-a", shardSize, totalPartitions)
	if fmt.Sprint(shardA1) != fmt.Sprint(shardA2) {
		t.Fatalf("shuffleShard not deterministic: %v vs %v", shardA1, shardA2)
	}
	if len(shardA1) != shardSize {
		t.Fatalf("shard size = %d, want %d", len(shardA1), shardSize)
	}
	seen := map[int]bool{}
	for _, p := range shardA1 {
		if p < 0 || p >= totalPartitions {
			t.Fatalf("shard partition %d out of range [0,%d)", p, totalPartitions)
		}
		if seen[p] {
			t.Fatalf("shard has duplicate partition %d", p)
		}
		seen[p] = true
	}

	shardB := shuffleShard("tenant-b", shardSize, totalPartitions)
	if fmt.Sprint(shardA1) == fmt.Sprint(shardB) {
		t.Logf("tenant-a and tenant-b happened to get identical shards (statistically rare but not impossible)")
	}
}

func TestWALQuorumRequiresNReplicas(t *testing.T) {
	dir := t.TempDir()
	replicaDirs := []string{
		filepath.Join(dir, "az1"),
		filepath.Join(dir, "az2"),
	}
	b, err := New(Config{
		Dir:               dir,
		Partitions:        1,
		ShardSize:         1,
		MaxPartitionDepth: 1000,
		ReplicaDirs:       replicaDirs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer b.Close()

	// total replicas = 1 (primary WAL) + 2 (az1, az2) = 3, quorum = 2.
	if err := b.Publish(context.Background(), "tenantA", "info", []byte("hello")); err != nil {
		t.Fatalf("publish with healthy replicas: %v", err)
	}

	for _, rd := range replicaDirs {
		entries, err := os.ReadDir(filepath.Join(rd, "partition-0"))
		if err != nil {
			t.Fatalf("reading replica dir %s: %v", rd, err)
		}
		if len(entries) == 0 {
			t.Fatalf("replica dir %s has no durable copy", rd)
		}
	}
}

func TestPublishRejectsOnClosedBuffer(t *testing.T) {
	b := newTestBuffer(t, Config{Partitions: 1})
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(context.Background(), "t", "info", []byte("x")); err != ErrClosed {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestWALSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Dir: dir, Partitions: 1, ShardSize: 1, MaxPartitionDepth: 1000}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := b.Publish(ctx, "tenantA", "info", []byte(fmt.Sprintf("m%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	b2, err := New(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer b2.Close()

	ch, err := b2.Subscribe(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case msg := <-ch:
			if string(msg.Payload) != fmt.Sprintf("m%d", i) {
				t.Errorf("replayed after reopen: got %q, want m%d", msg.Payload, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out replaying after reopen at %d", i)
		}
	}
}
