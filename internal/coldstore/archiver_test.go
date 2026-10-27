package coldstore

import (
	"context"
	"testing"
)

func TestArchiverArchiveAndQueryPushdown(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFSBlobstore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := &Archiver{Store: store, Service: "checkout"}
	ctx := context.Background()

	// Segment 1: timestamps 1000-1004
	seg1 := []map[string]any{
		{"timestamp": int64(1000), "level": "info", "msg": "a"},
		{"timestamp": int64(1004), "level": "error", "msg": "b"},
	}
	if err := a.ArchiveSegment(ctx, "/hot/seg1.log", seg1); err != nil {
		t.Fatalf("ArchiveSegment seg1: %v", err)
	}

	// Segment 2: timestamps 2000-2004, far away in time.
	seg2 := []map[string]any{
		{"timestamp": int64(2000), "level": "info", "msg": "c"},
		{"timestamp": int64(2004), "level": "error", "msg": "d"},
	}
	if err := a.ArchiveSegment(ctx, "/hot/seg2.log", seg2); err != nil {
		t.Fatalf("ArchiveSegment seg2: %v", err)
	}

	keys, err := store.List(ctx, "checkout/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 archived segment objects, got %d: %v", len(keys), keys)
	}

	// A predicate that only overlaps segment 1's time range.
	got, err := a.QueryPushdown(ctx, "checkout/", []Predicate{
		{Column: "timestamp", Op: "lte", Value: 1004.0},
	})
	if err != nil {
		t.Fatalf("QueryPushdown: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows from segment 1, got %d: %+v", len(got), got)
	}
	for _, row := range got {
		if row["msg"] != "a" && row["msg"] != "b" {
			t.Errorf("unexpected row leaked from segment 2: %+v", row)
		}
	}

	// A predicate on "level" further narrows within the matched segment.
	got2, err := a.QueryPushdown(ctx, "checkout/", []Predicate{
		{Column: "level", Op: "eq", Value: "error"},
	})
	if err != nil {
		t.Fatalf("QueryPushdown: %v", err)
	}
	if len(got2) != 2 {
		t.Fatalf("expected 2 error rows across both segments, got %d: %+v", len(got2), got2)
	}
}
