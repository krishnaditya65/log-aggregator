package coldstore

import (
	"path/filepath"
	"testing"
)

func TestColumnarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.col")

	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	rows := []map[string]any{
		{"service": "checkout", "level": "info", "latency_ms": 12.5, "ok": true},
		{"service": "checkout", "level": "error", "latency_ms": 900.0, "ok": false},
		{"service": "checkout", "level": "info", "latency_ms": 15.0, "ok": true},
	}
	if err := w.WriteBatch(rows); err != nil {
		t.Fatalf("WriteBatch: %v", err)
	}
	footer, err := w.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if footer.TotalRows != 3 {
		t.Fatalf("expected 3 total rows, got %d", footer.TotalRows)
	}
	if len(footer.RowGroups) != 1 {
		t.Fatalf("expected 1 row group, got %d", len(footer.RowGroups))
	}

	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	got, err := r.Filter(nil)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 rows back, got %d", len(got))
	}
	for i, row := range got {
		if row["service"] != rows[i]["service"] {
			t.Errorf("row %d service mismatch: got %v want %v", i, row["service"], rows[i]["service"])
		}
		if row["latency_ms"] != rows[i]["latency_ms"] {
			t.Errorf("row %d latency_ms mismatch: got %v want %v", i, row["latency_ms"], rows[i]["latency_ms"])
		}
		if row["ok"] != rows[i]["ok"] {
			t.Errorf("row %d ok mismatch: got %v want %v", i, row["ok"], rows[i]["ok"])
		}
	}

	// Exact-match predicate.
	matches, err := r.Filter([]Predicate{{Column: "level", Op: "eq", Value: "error"}})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(matches) != 1 || matches[0]["latency_ms"] != 900.0 {
		t.Fatalf("expected exactly one error row with latency 900, got %+v", matches)
	}
}

func TestDictionaryEncodingLowCardinality(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dict.col")

	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]map[string]any, 0, 100)
	for i := 0; i < 100; i++ {
		level := "info"
		if i%10 == 0 {
			level = "error"
		}
		rows = append(rows, map[string]any{"level": level, "n": float64(i)})
	}
	if err := w.WriteBatch(rows); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := r.Filter([]Predicate{{Column: "level", Op: "eq", Value: "error"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("expected 10 error rows, got %d", len(got))
	}
}

// TestRowGroupSkipping proves that Reader.Filter uses footer min/max
// statistics to skip whole row groups without decoding them, for a
// selective predicate.
func TestRowGroupSkipping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skip.col")

	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}

	// 10 row groups, each with a disjoint numeric range for column "v":
	// group i covers [i*100, i*100+9].
	const numGroups = 10
	for g := 0; g < numGroups; g++ {
		var batch []map[string]any
		for j := 0; j < 5; j++ {
			batch = append(batch, map[string]any{
				"v":     float64(g*100 + j),
				"group": float64(g),
			})
		}
		if err := w.WriteBatch(batch); err != nil {
			t.Fatal(err)
		}
	}
	footer, err := w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(footer.RowGroups) != numGroups {
		t.Fatalf("expected %d row groups, got %d", numGroups, len(footer.RowGroups))
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// This predicate can only match row group 3 (values 300-304).
	got, err := r.Filter([]Predicate{{Column: "v", Op: "eq", Value: 302.0}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 matching row, got %d: %+v", len(got), got)
	}
	if got[0]["group"] != 3.0 {
		t.Fatalf("expected match from group 3, got %+v", got[0])
	}

	if r.DecodedRowGroups != 1 {
		t.Errorf("expected exactly 1 row group decoded, got %d", r.DecodedRowGroups)
	}
	if r.SkippedRowGroups != numGroups-1 {
		t.Errorf("expected %d row groups skipped, got %d", numGroups-1, r.SkippedRowGroups)
	}

	// A range predicate spanning groups 5..7 should decode exactly 3.
	got2, err := r.Filter([]Predicate{
		{Column: "v", Op: "gte", Value: 500.0},
		{Column: "v", Op: "lte", Value: 704.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.DecodedRowGroups != 3 {
		t.Errorf("expected 3 row groups decoded for range query, got %d", r.DecodedRowGroups)
	}
	if len(got2) != 15 {
		t.Errorf("expected 15 matching rows (3 groups x 5 rows), got %d", len(got2))
	}
}

func TestFilterNoPredicatesDecodesEverything(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "all.col")
	w, _ := NewWriter(path)
	for g := 0; g < 4; g++ {
		w.WriteBatch([]map[string]any{{"v": float64(g)}})
	}
	w.Close()

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := r.Filter(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 rows, got %d", len(got))
	}
	if r.DecodedRowGroups != 4 || r.SkippedRowGroups != 0 {
		t.Fatalf("expected all 4 row groups decoded with no predicates, got decoded=%d skipped=%d", r.DecodedRowGroups, r.SkippedRowGroups)
	}
}
