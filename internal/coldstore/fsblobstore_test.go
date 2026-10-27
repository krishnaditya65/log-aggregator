package coldstore

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestFSBlobstorePutGetList(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFSBlobstore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	keys := []string{
		"svc/2026/01/15/segment-1-100.col",
		"svc/2026/01/15/segment-101-200.col",
		"svc/2026/01/16/segment-201-300.col",
		"other/2026/01/15/segment-1-1.col",
	}
	for _, k := range keys {
		if err := store.Put(ctx, k, bytes.NewReader([]byte("data-for-"+k))); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}

	rc, err := store.Get(ctx, keys[0])
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "data-for-"+keys[0] {
		t.Fatalf("unexpected data: %s", data)
	}

	listed, err := store.List(ctx, "svc/2026/01/15/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 keys under svc/2026/01/15/, got %d: %v", len(listed), listed)
	}

	all, err := store.List(ctx, "svc/")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 keys under svc/, got %d: %v", len(all), all)
	}

	// Get on a missing key should error.
	if _, err := store.Get(ctx, "does/not/exist"); err == nil {
		t.Fatal("expected error getting missing key")
	}
}
