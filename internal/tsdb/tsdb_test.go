package tsdb

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func tempStore(t *testing.T, opts Options) *Store {
	t.Helper()
	dir := t.TempDir()
	opts.Dir = dir
	s, err := NewStore(opts)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestAppendQueryRoundTrip verifies that points written via Append come
// back correctly (same values, in time order) via Query, both while still
// buffered in memory and after being flushed to an on-disk segment.
func TestAppendQueryRoundTrip(t *testing.T) {
	s := tempStore(t, Options{ChunkDuration: 2 * time.Hour})

	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).UnixNano()
	labels := map[string]string{"service": "api", "host": "h1"}

	for i := 0; i < 10; i++ {
		t2 := base + int64(i)*int64(time.Minute)
		if err := s.Append(labels, t2, float64(i)*1.5); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	// Query while still in the in-memory buffer.
	res, err := s.Query(map[string]string{"service": "api"}, base, base+int64(20)*int64(time.Minute))
	if err != nil {
		t.Fatalf("Query (buffered): %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 series, got %d", len(res))
	}
	if len(res[0].Points) != 10 {
		t.Fatalf("expected 10 points, got %d", len(res[0].Points))
	}
	for i, p := range res[0].Points {
		wantT := base + int64(i)*int64(time.Minute)
		wantV := float64(i) * 1.5
		if p.Time != wantT || p.Value != wantV {
			t.Fatalf("point %d: got (%d,%v) want (%d,%v)", i, p.Time, p.Value, wantT, wantV)
		}
	}

	// Now flush to disk and query again; results must match exactly.
	if err := s.FlushAll(); err != nil {
		t.Fatalf("FlushAll: %v", err)
	}
	res2, err := s.Query(map[string]string{"service": "api"}, base, base+int64(20)*int64(time.Minute))
	if err != nil {
		t.Fatalf("Query (flushed): %v", err)
	}
	if len(res2) != 1 || len(res2[0].Points) != 10 {
		t.Fatalf("post-flush query mismatch: %+v", res2)
	}
	for i, p := range res2[0].Points {
		wantT := base + int64(i)*int64(time.Minute)
		wantV := float64(i) * 1.5
		if p.Time != wantT || p.Value != wantV {
			t.Fatalf("post-flush point %d: got (%d,%v) want (%d,%v)", i, p.Time, p.Value, wantT, wantV)
		}
	}

	// Label mismatch should yield ErrNotFound.
	if _, err := s.Query(map[string]string{"service": "nope"}, base, base+int64(time.Hour)); err == nil {
		t.Fatalf("expected error for non-matching label")
	}

	// Regex matcher should also find it.
	res3, err := s.Query(map[string]string{"service": "~^a.*"}, base, base+int64(20)*int64(time.Minute))
	if err != nil || len(res3) != 1 {
		t.Fatalf("regex query failed: res=%v err=%v", res3, err)
	}
}

// TestCompactionMergesAndDedups writes overlapping data across two
// separate segments (including a duplicate timestamp) and verifies that
// RunOnce merges them into fewer segments and that querying afterward
// returns the deduplicated, correctly-ordered point set.
func TestCompactionMergesAndDedups(t *testing.T) {
	s := tempStore(t, Options{ChunkDuration: 2 * time.Hour})

	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).UnixNano()
	labels := map[string]string{"service": "worker"}

	// First segment: t=0,1,2 minutes.
	for i := 0; i < 3; i++ {
		if err := s.Append(labels, base+int64(i)*int64(time.Minute), float64(i)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := s.FlushAll(); err != nil {
		t.Fatalf("flush 1: %v", err)
	}

	// Second segment: t=2 (duplicate, different value -> should win as
	// "last written"), 3, 4 minutes.
	for i := 2; i < 5; i++ {
		v := float64(i) + 100 // distinguishable from first write
		if err := s.Append(labels, base+int64(i)*int64(time.Minute), v); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := s.FlushAll(); err != nil {
		t.Fatalf("flush 2: %v", err)
	}

	if got := len(s.segmentPaths()); got != 2 {
		t.Fatalf("expected 2 segments before compaction, got %d", got)
	}

	c := NewCompactor(s)
	c.MinSegmentsToMerge = 2
	c.MaxMergeSpan = 24 * time.Hour
	if err := c.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	segs := s.segmentPaths()
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment after compaction, got %d: %v", len(segs), segs)
	}

	res, err := s.Query(map[string]string{"service": "worker"}, base, base+int64(10)*int64(time.Minute))
	if err != nil {
		t.Fatalf("query after compaction: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 series after compaction, got %d", len(res))
	}
	pts := res[0].Points
	if len(pts) != 5 {
		t.Fatalf("expected 5 deduped points (0,1,2,3,4 minutes), got %d: %+v", len(pts), pts)
	}
	// Verify ascending order and the deduped value at t=2 (should be the
	// later write, 102).
	for i, p := range pts {
		wantT := base + int64(i)*int64(time.Minute)
		if p.Time != wantT {
			t.Fatalf("point %d time = %d, want %d", i, p.Time, wantT)
		}
	}
	if pts[2].Value != 102 {
		t.Fatalf("expected deduped point at t=2min to keep last-write value 102, got %v", pts[2].Value)
	}
}

// TestDocValueSkipping verifies that querying a narrow time range which
// only a subset of a segment's series/blocks can satisfy results in fewer
// blocks being decoded than the total number of blocks in the segment,
// proving the doc-value index is actually used to skip work.
func TestDocValueSkipping(t *testing.T) {
	s := tempStore(t, Options{ChunkDuration: 24 * time.Hour})

	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).UnixNano()

	// 20 distinct series, each with one point at a distinct, widely
	// spaced timestamp so their block time ranges don't overlap much.
	const n = 20
	for i := 0; i < n; i++ {
		labels := map[string]string{"series": fmt.Sprintf("s%02d", i)}
		ts := base + int64(i)*int64(time.Minute)
		if err := s.Append(labels, ts, float64(i)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := s.FlushAll(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	s.ResetDecodedBlocks()

	// Query a narrow window that only overlaps series index 5's point.
	target := base + 5*int64(time.Minute)
	res, err := s.Query(map[string]string{}, target, target)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected exactly 1 series to match the narrow window, got %d", len(res))
	}

	decoded := s.DecodedBlocks()
	if decoded != 1 {
		t.Fatalf("expected doc-value skipping to limit decoding to 1 block, got %d decoded blocks (out of %d total)", decoded, n)
	}
	if decoded >= int64(n) {
		t.Fatalf("doc-value skipping had no effect: decoded %d of %d blocks", decoded, n)
	}
}

// TestCardinalityLimiterRollup verifies that once a label name's distinct
// value count passes the configured threshold, further new values are
// rolled up to "_other_" instead of being admitted verbatim, and that the
// rollup is reflected both in the returned labels and in Store queries.
func TestCardinalityLimiterRollup(t *testing.T) {
	s := tempStore(t, Options{ChunkDuration: 2 * time.Hour, CardinalityThreshold: 3})

	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).UnixNano()

	// Push 10 distinct session_id values through Append; only the first 3
	// should be admitted verbatim, the rest rolled up to "_other_".
	for i := 0; i < 10; i++ {
		labels := map[string]string{
			"service":    "web",
			"session_id": fmt.Sprintf("sess-%d", i),
		}
		if err := s.Append(labels, base+int64(i)*int64(time.Second), float64(i)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	limiter := s.Limiter()
	if got := limiter.Cardinality("session_id"); got != 4 { // 3 real + "_other_"
		t.Fatalf("expected session_id cardinality to be capped at 4 (3 real + _other_), got %d", got)
	}
	if rollups := limiter.RollupCount("session_id"); rollups != 7 {
		t.Fatalf("expected 7 rollups (indices 3..9), got %d", rollups)
	}
	if total := limiter.TotalRollups(); total != 7 {
		t.Fatalf("expected TotalRollups() == 7, got %d", total)
	}

	// The "service" label never exceeded the threshold, so it must be
	// unaffected.
	if got := limiter.Cardinality("service"); got != 1 {
		t.Fatalf("expected service cardinality 1, got %d", got)
	}

	// Querying for the rolled-up bucket should find a series carrying
	// session_id="_other_".
	res, err := s.Query(map[string]string{"session_id": "_other_"}, base, base+int64(20)*int64(time.Second))
	if err != nil {
		t.Fatalf("query _other_: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("expected at least one series with rolled-up session_id")
	}
}

// TestRetentionExpiresSegments verifies that segments entirely older than
// the retention window are removed and that the expire hook fires for
// each of them.
func TestRetentionExpiresSegments(t *testing.T) {
	var expired []string
	dir := t.TempDir()
	s, err := NewStore(Options{
		Dir:           dir,
		ChunkDuration: time.Hour,
		Retention:     time.Hour,
		OnExpireSegment: func(path string, minTime, maxTime int64) {
			expired = append(expired, path)
		},
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer s.Close()

	oldT := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	newT := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).UnixNano()

	if err := s.Append(map[string]string{"x": "old"}, oldT, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushAll(); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(map[string]string{"x": "new"}, newT, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.FlushAll(); err != nil {
		t.Fatal(err)
	}

	if got := len(s.segmentPaths()); got != 2 {
		t.Fatalf("expected 2 segments before retention, got %d", got)
	}

	c := NewCompactor(s)
	if err := c.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	segs := s.segmentPaths()
	if len(segs) != 1 {
		t.Fatalf("expected 1 segment after retention, got %d: %v", len(segs), segs)
	}
	if len(expired) != 1 {
		t.Fatalf("expected OnExpireSegment to fire once, fired %d times", len(expired))
	}
	if _, err := os.Stat(expired[0]); !os.IsNotExist(err) {
		t.Fatalf("expected expired segment file to be deleted: %s", expired[0])
	}
}
