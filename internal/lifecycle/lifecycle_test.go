package lifecycle

import (
	"context"
	"testing"
	"time"
)

func TestRunOnceDecisionMatrix(t *testing.T) {
	const now = int64(1_000_000)

	segments := []SegmentMeta{
		{Path: "hot-seg", Service: "checkout", MaxTime: now - int64(time.Hour/time.Second)},           // 1h old: hot
		{Path: "warm-seg", Service: "checkout", MaxTime: now - int64(6*time.Hour/time.Second)},        // 6h old: warm
		{Path: "expired-seg", Service: "checkout", MaxTime: now - int64(48*time.Hour/time.Second)},    // 48h old: expired
		{Path: "unmatched-seg", Service: "unmanaged", MaxTime: now - int64(48*time.Hour/time.Second)}, // no matching policy
	}

	var archivedPaths []string
	var deletedPaths []string

	lister := func() []SegmentMeta { return segments }
	archive := func(path string) error { archivedPaths = append(archivedPaths, path); return nil }
	del := func(path string) error { deletedPaths = append(deletedPaths, path); return nil }

	m := NewManager(lister, archive, del)
	m.RegisterPolicy(Policy{
		Name:           "checkout-default",
		Selector:       map[string]string{"service": "checkout"},
		HotRetention:   2 * time.Hour,
		WarmRetention:  22 * time.Hour,
		TotalRetention: 24 * time.Hour,
	})

	moved, deleted, err := m.RunOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if moved != 1 {
		t.Errorf("expected 1 archived segment, got %d", moved)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted segment, got %d", deleted)
	}
	if len(archivedPaths) != 1 || archivedPaths[0] != "warm-seg" {
		t.Errorf("expected warm-seg archived, got %v", archivedPaths)
	}
	if len(deletedPaths) != 1 || deletedPaths[0] != "expired-seg" {
		t.Errorf("expected expired-seg deleted, got %v", deletedPaths)
	}

	// hot-seg and unmatched-seg must never appear in either list.
	for _, p := range append(append([]string{}, archivedPaths...), deletedPaths...) {
		if p == "hot-seg" {
			t.Errorf("hot-seg should be untouched, but was acted on")
		}
		if p == "unmatched-seg" {
			t.Errorf("unmatched-seg has no matching policy and should be skipped")
		}
	}
}

func TestWarmSegmentArchivedExactlyOnce(t *testing.T) {
	const now = int64(1_000_000)
	segments := []SegmentMeta{
		{Path: "warm-seg", Service: "checkout", MaxTime: now - int64(6*time.Hour/time.Second)},
	}

	var archiveCalls int
	lister := func() []SegmentMeta { return segments }
	archive := func(path string) error { archiveCalls++; return nil }
	del := func(path string) error { return nil }

	m := NewManager(lister, archive, del)
	m.RegisterPolicy(Policy{
		Selector:       map[string]string{"service": "checkout"},
		HotRetention:   2 * time.Hour,
		TotalRetention: 24 * time.Hour,
	})

	for i := 0; i < 3; i++ {
		if _, _, err := m.RunOnce(context.Background(), now); err != nil {
			t.Fatalf("RunOnce iteration %d: %v", i, err)
		}
	}

	if archiveCalls != 1 {
		t.Fatalf("expected archive callback to fire exactly once across repeated ticks, got %d calls", archiveCalls)
	}
}

func TestFirstMatchWinsPrecedence(t *testing.T) {
	const now = int64(1_000_000)
	segments := []SegmentMeta{
		// Old enough to be "expired" under the narrow policy, but only
		// "warm" under the broad catch-all policy that comes second.
		{Path: "seg-a", Service: "checkout", Tenant: "acme", MaxTime: now - int64(30*time.Hour/time.Second)},
	}

	var archivedPaths, deletedPaths []string
	lister := func() []SegmentMeta { return segments }
	archive := func(path string) error { archivedPaths = append(archivedPaths, path); return nil }
	del := func(path string) error { deletedPaths = append(deletedPaths, path); return nil }

	m := NewManager(lister, archive, del)
	// First-registered, narrower selector: matches (service=checkout,
	// tenant=acme) and would delete this segment.
	m.RegisterPolicy(Policy{
		Name:           "checkout-acme-aggressive",
		Selector:       map[string]string{"service": "checkout", "tenant": "acme"},
		HotRetention:   time.Hour,
		TotalRetention: 24 * time.Hour,
	})
	// Second-registered, catch-all for the service: would only archive,
	// not delete, at this age. Should never be consulted for seg-a.
	m.RegisterPolicy(Policy{
		Name:           "checkout-catch-all",
		Selector:       map[string]string{"service": "checkout"},
		HotRetention:   time.Hour,
		TotalRetention: 100 * time.Hour,
	})

	moved, deleted, err := m.RunOnce(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 || moved != 0 {
		t.Fatalf("expected first (narrower) policy to win and delete seg-a, got moved=%d deleted=%d", moved, deleted)
	}
	if len(deletedPaths) != 1 || deletedPaths[0] != "seg-a" {
		t.Fatalf("expected seg-a deleted under first-match policy, got archived=%v deleted=%v", archivedPaths, deletedPaths)
	}
}

func TestNonMatchingSelectorSkipped(t *testing.T) {
	const now = int64(1_000_000)
	segments := []SegmentMeta{
		{Path: "orphan", Service: "billing", MaxTime: now - int64(100*time.Hour/time.Second)},
	}
	lister := func() []SegmentMeta { return segments }
	var calls int
	archive := func(path string) error { calls++; return nil }
	del := func(path string) error { calls++; return nil }

	m := NewManager(lister, archive, del)
	m.RegisterPolicy(Policy{
		Selector:       map[string]string{"service": "checkout"},
		HotRetention:   time.Hour,
		TotalRetention: 24 * time.Hour,
	})

	moved, deleted, err := m.RunOnce(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 0 || deleted != 0 || calls != 0 {
		t.Fatalf("expected segment with no matching policy to be fully skipped, got moved=%d deleted=%d calls=%d", moved, deleted, calls)
	}
}

func TestHotSegmentUntouched(t *testing.T) {
	const now = int64(1_000_000)
	segments := []SegmentMeta{
		{Path: "fresh", Service: "checkout", MaxTime: now - 60}, // 1 minute old
	}
	lister := func() []SegmentMeta { return segments }
	var calls int
	archive := func(path string) error { calls++; return nil }
	del := func(path string) error { calls++; return nil }

	m := NewManager(lister, archive, del)
	m.RegisterPolicy(Policy{
		Selector:       map[string]string{"service": "checkout"},
		HotRetention:   2 * time.Hour,
		TotalRetention: 24 * time.Hour,
	})

	moved, deleted, err := m.RunOnce(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 0 || deleted != 0 || calls != 0 {
		t.Fatalf("expected hot segment untouched, got moved=%d deleted=%d calls=%d", moved, deleted, calls)
	}
}
