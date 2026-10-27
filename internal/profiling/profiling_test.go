package profiling

import (
	"context"
	"testing"
	"time"
)

func TestProfilerCaptureProducesNonEmptyPprofData(t *testing.T) {
	p := NewProfiler("aggregator", "test-host", 10)

	prof, err := p.Capture(context.Background(), 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(prof.Data) == 0 {
		t.Fatal("expected non-empty pprof data")
	}
	if prof.Service != "aggregator" || prof.Host != "test-host" {
		t.Fatalf("unexpected tags: %+v", prof)
	}
	if prof.Format != "pprof" {
		t.Fatalf("unexpected format: %s", prof.Format)
	}
	if prof.Time == 0 {
		t.Fatal("expected non-zero capture time")
	}

	// Round-trip through the flamegraph parser.
	root, err := BuildFlameGraph(prof.Data, 1)
	if err != nil {
		t.Fatalf("BuildFlameGraph: %v", err)
	}
	if root.Name != "root" {
		t.Fatalf("expected root node named 'root', got %q", root.Name)
	}
	if root.TotalNS < 0 {
		t.Fatalf("expected non-negative TotalNS, got %d", root.TotalNS)
	}
	// Sanity: TotalNS of root must equal sum of immediate children's TotalNS
	// (all samples flow through exactly one top-level frame or accrue to
	// root's own SelfNS).
	var childSum int64
	for _, c := range root.Children {
		childSum += c.TotalNS
	}
	if childSum > root.TotalNS {
		t.Fatalf("children TotalNS sum %d exceeds root TotalNS %d", childSum, root.TotalNS)
	}
	assertSaneTree(t, root)
}

// assertSaneTree checks the invariant TotalNS >= SelfNS and TotalNS >= sum
// of children's TotalNS (equality expected, but >= tolerates any rounding
// paths) recursively.
func assertSaneTree(t *testing.T, n *FlameNode) {
	t.Helper()
	if n.TotalNS < n.SelfNS {
		t.Fatalf("node %q: TotalNS %d < SelfNS %d", n.Name, n.TotalNS, n.SelfNS)
	}
	var sum int64
	for _, c := range n.Children {
		sum += c.TotalNS
		assertSaneTree(t, c)
	}
	if sum > n.TotalNS {
		t.Fatalf("node %q: children sum %d exceeds TotalNS %d", n.Name, sum, n.TotalNS)
	}
}

func TestProfilerListAndGet(t *testing.T) {
	p := NewProfiler("svc", "host1", 2)

	p1, err := p.Capture(context.Background(), 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Capture 1: %v", err)
	}
	time.Sleep(1100 * time.Millisecond) // ensure distinct unix-second timestamps
	p2, err := p.Capture(context.Background(), 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Capture 2: %v", err)
	}

	list := p.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 retained profiles, got %d", len(list))
	}

	if got, ok := p.Get(p1.Time); !ok || got.Time != p1.Time {
		t.Fatalf("Get(p1.Time) failed: ok=%v got=%+v", ok, got)
	}
	if got, ok := p.Get(p2.Time); !ok || got.Time != p2.Time {
		t.Fatalf("Get(p2.Time) failed: ok=%v got=%+v", ok, got)
	}
	if _, ok := p.Get(1); ok {
		t.Fatal("expected Get(1) to miss")
	}
}

func TestProfilerEviction(t *testing.T) {
	p := NewProfiler("svc", "host1", 1)
	if _, err := p.Capture(context.Background(), 10*time.Millisecond); err != nil {
		t.Fatalf("Capture 1: %v", err)
	}
	if _, err := p.Capture(context.Background(), 10*time.Millisecond); err != nil {
		t.Fatalf("Capture 2: %v", err)
	}
	if len(p.List()) != 1 {
		t.Fatalf("expected eviction to cap at 1, got %d", len(p.List()))
	}
}

func TestCaptureRespectsCanceledContext(t *testing.T) {
	p := NewProfiler("svc", "host1", 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := p.Capture(ctx, 10*time.Millisecond); err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestBuildFlameGraphRejectsGarbage(t *testing.T) {
	if _, err := BuildFlameGraph([]byte("not a gzip stream"), 1); err == nil {
		t.Fatal("expected error decoding garbage input")
	}
}
