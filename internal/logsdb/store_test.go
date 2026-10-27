package logsdb

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func newTestStore(t *testing.T, cfg Config) *Store {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	s, err := NewStore(cfg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustAppend(t *testing.T, s *Store, d LogDoc) {
	t.Helper()
	if err := s.Append(d); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func TestKeywordSearchRoundTrip(t *testing.T) {
	s := newTestStore(t, Config{MaxBufferedDocs: 1000, BlockSize: 4})

	mustAppend(t, s, LogDoc{Host: "h1", Time: 1, Message: "connection refused by upstream"})
	mustAppend(t, s, LogDoc{Host: "h2", Time: 2, Message: "request completed successfully"})
	mustAppend(t, s, LogDoc{Host: "h1", Time: 3, Message: "retrying connection to upstream"})

	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	res, err := s.Search(Query{Keyword: "connection"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 docs matching 'connection', got %d: %+v", len(res), res)
	}
	for _, d := range res {
		if d.Host != "h1" {
			t.Errorf("expected host h1, got %s", d.Host)
		}
	}

	res, err = s.Search(Query{Keyword: "successfully"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Host != "h2" {
		t.Fatalf("expected 1 doc on h2, got %+v", res)
	}

	res, err = s.Search(Query{Keyword: "nonexistentterm"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("expected 0 docs, got %d", len(res))
	}
}

func TestTagAndFieldFilters(t *testing.T) {
	s := newTestStore(t, Config{MaxBufferedDocs: 1000, BlockSize: 4})

	mustAppend(t, s, LogDoc{Host: "h1", Time: 1, Message: "boot sequence started",
		Tags:   map[string]string{"environment": "production", "region": "us-east"},
		Fields: map[string]any{"status_code": 200}})
	mustAppend(t, s, LogDoc{Host: "h2", Time: 2, Message: "boot sequence started",
		Tags:   map[string]string{"environment": "staging", "region": "us-east"},
		Fields: map[string]any{"status_code": 500}})
	mustAppend(t, s, LogDoc{Host: "h3", Time: 3, Message: "boot sequence started",
		Tags:   map[string]string{"environment": "production", "region": "eu-west"},
		Fields: map[string]any{"status_code": 200}})

	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	res, err := s.Search(Query{Tags: map[string]string{"environment": "production"}})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 production docs, got %d", len(res))
	}

	res, err = s.Search(Query{
		Tags:   map[string]string{"environment": "production"},
		Fields: map[string]string{"status_code": "200"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 docs for production+200, got %d", len(res))
	}

	res, err = s.Search(Query{
		Tags: map[string]string{"environment": "production", "region": "eu-west"},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Host != "h3" {
		t.Fatalf("expected h3 only, got %+v", res)
	}
}

func TestRegexAndWildcardSearch(t *testing.T) {
	s := newTestStore(t, Config{MaxBufferedDocs: 1000, BlockSize: 4})

	mustAppend(t, s, LogDoc{Host: "h1", Time: 1, Message: "user login failed for admin"})
	mustAppend(t, s, LogDoc{Host: "h2", Time: 2, Message: "user login succeeded for guest"})
	mustAppend(t, s, LogDoc{Host: "h3", Time: 3, Message: "disk usage at 92 percent"})

	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Pure regex, no indexable term -> full scan fallback.
	res, err := s.Search(Query{Regex: `login (failed|succeeded)`})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 regex matches, got %d: %+v", len(res), res)
	}

	// Wildcard, no indexable term -> full scan fallback, case-insensitive.
	res, err = s.Search(Query{Wildcard: "*DISK USAGE*"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Host != "h3" {
		t.Fatalf("expected h3 only, got %+v", res)
	}

	// Keyword narrows the candidate set, regex further filters it.
	res, err = s.Search(Query{Keyword: "login", Regex: `failed`})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Host != "h1" {
		t.Fatalf("expected h1 only, got %+v", res)
	}
}

func TestSearchAcrossBufferedAndFlushed(t *testing.T) {
	s := newTestStore(t, Config{MaxBufferedDocs: 1000, BlockSize: 4})

	mustAppend(t, s, LogDoc{Host: "h1", Time: 1, Message: "flushed marker event"})
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	mustAppend(t, s, LogDoc{Host: "h2", Time: 2, Message: "buffered marker event"})

	res, err := s.Search(Query{Keyword: "marker"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected marker to match both flushed and buffered docs, got %d", len(res))
	}
}

func TestSegmentSortedByHostThenTime(t *testing.T) {
	dir := t.TempDir()
	docs := []LogDoc{
		{Host: "zeta", Time: 5, Message: "m1"},
		{Host: "alpha", Time: 9, Message: "m2"},
		{Host: "alpha", Time: 3, Message: "m3"},
		{Host: "beta", Time: 1, Message: "m4"},
		{Host: "alpha", Time: 1, Message: "m5"},
	}

	seg, err := writeSegment(dir, "test.seg", docs, 2)
	if err != nil {
		t.Fatalf("writeSegment: %v", err)
	}

	got, err := seg.allDocs()
	if err != nil {
		t.Fatalf("allDocs: %v", err)
	}
	if len(got) != len(docs) {
		t.Fatalf("expected %d docs, got %d", len(docs), len(got))
	}

	if !sort.SliceIsSorted(got, func(i, j int) bool {
		if got[i].Host != got[j].Host {
			return got[i].Host < got[j].Host
		}
		return got[i].Time < got[j].Time
	}) {
		t.Fatalf("segment docs not sorted by (Host, Time): %+v", got)
	}

	// Spot-check the expected exact order.
	wantHosts := []string{"alpha", "alpha", "alpha", "beta", "zeta"}
	for i, h := range wantHosts {
		if got[i].Host != h {
			t.Errorf("position %d: expected host %s, got %s", i, h, got[i].Host)
		}
	}
	if got[0].Time != 1 || got[1].Time != 3 || got[2].Time != 9 {
		t.Errorf("alpha docs not sorted by time: %+v", got[:3])
	}
}

func TestCompressionStatsAreSane(t *testing.T) {
	s := newTestStore(t, Config{MaxBufferedDocs: 1000, BlockSize: 64})

	// Highly repetitive, same-host log lines: exactly what LogsDB-style
	// host+time sorting is meant to exploit for compression.
	for i := 0; i < 500; i++ {
		mustAppend(t, s, LogDoc{
			Host:    "web-01",
			Time:    int64(i),
			Message: "GET /health 200 OK healthcheck passed all systems nominal",
			Tags:    map[string]string{"environment": "production"},
		})
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	stats := s.CompressionStats()
	if len(stats) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(stats))
	}
	st := stats[0]
	if st.DocCount != 500 {
		t.Fatalf("expected 500 docs, got %d", st.DocCount)
	}
	if st.CompressedBytes >= st.RawBytes {
		t.Fatalf("expected compression to shrink repetitive data: raw=%d compressed=%d", st.RawBytes, st.CompressedBytes)
	}
	if st.Ratio <= 1.0 {
		t.Fatalf("expected compression ratio > 1, got %f", st.Ratio)
	}
	t.Logf("compression: raw=%d compressed=%d ratio=%.2fx", st.RawBytes, st.CompressedBytes, st.Ratio)
}

func TestRetentionExpiresOldSegments(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, Config{Dir: dir, MaxBufferedDocs: 1000, BlockSize: 4, Retention: time.Hour})

	oldTime := time.Now().Add(-2 * time.Hour).UnixNano()
	newTime := time.Now().UnixNano()

	mustAppend(t, s, LogDoc{Host: "old-host", Time: oldTime, Message: "ancient event"})
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	mustAppend(t, s, LogDoc{Host: "new-host", Time: newTime, Message: "recent event"})
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := len(s.CompressionStats()); got != 2 {
		t.Fatalf("expected 2 segments before retention, got %d", got)
	}

	var expiredPaths []string
	s.OnExpireSegment(func(path string, minTime, maxTime int64) {
		expiredPaths = append(expiredPaths, path)
	})

	if err := s.RunRetention(time.Now()); err != nil {
		t.Fatalf("RunRetention: %v", err)
	}

	if len(expiredPaths) != 1 {
		t.Fatalf("expected 1 expired segment, got %d: %v", len(expiredPaths), expiredPaths)
	}

	stats := s.CompressionStats()
	if len(stats) != 1 {
		t.Fatalf("expected 1 remaining segment, got %d", len(stats))
	}

	res, err := s.Search(Query{Keyword: "ancient"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("expected expired segment's docs to be gone, got %+v", res)
	}

	res, err = s.Search(Query{Keyword: "recent"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected recent doc to remain, got %+v", res)
	}
}

func TestReopenStoreLoadsExistingSegments(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(Config{Dir: dir, MaxBufferedDocs: 1000, BlockSize: 4})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	mustAppend(t, s1, LogDoc{Host: "h1", Time: 1, Message: "persisted across restarts"})
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := NewStore(Config{Dir: dir, MaxBufferedDocs: 1000, BlockSize: 4})
	if err != nil {
		t.Fatalf("NewStore (reopen): %v", err)
	}
	defer s2.Close()

	res, err := s2.Search(Query{Keyword: "persisted"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 doc from reopened store, got %d", len(res))
	}
}

func TestAppendGeneratesIDAndTime(t *testing.T) {
	s := newTestStore(t, Config{MaxBufferedDocs: 1000, BlockSize: 4})
	d := LogDoc{Host: "h1", Message: "no id or time set"}
	mustAppend(t, s, d)
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	res, err := s.Search(Query{Keyword: "id"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(res))
	}
	if res[0].ID == "" {
		t.Fatal("expected auto-generated ID")
	}
	if res[0].Time == 0 {
		t.Fatal("expected auto-generated Time")
	}
}

func TestConcurrentAppendAndSearch(t *testing.T) {
	s := newTestStore(t, Config{MaxBufferedDocs: 50, BlockSize: 8})

	done := make(chan struct{})
	var wg int
	errs := make(chan error, 20)

	writer := func(id int) {
		for i := 0; i < 100; i++ {
			err := s.Append(LogDoc{
				Host:    fmt.Sprintf("host-%d", id%3),
				Time:    int64(i),
				Message: fmt.Sprintf("worker %d iteration %d event", id, i),
			})
			if err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}
	reader := func() {
		for i := 0; i < 100; i++ {
			if _, err := s.Search(Query{Keyword: "event"}); err != nil {
				errs <- err
				return
			}
		}
		errs <- nil
	}

	for i := 0; i < 5; i++ {
		wg++
		go writer(i)
	}
	for i := 0; i < 5; i++ {
		wg++
		go reader()
	}
	go func() {
		for i := 0; i < wg; i++ {
			<-errs
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for concurrent workers")
	}

	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	res, err := s.Search(Query{Keyword: "event"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 500 {
		t.Fatalf("expected 500 total events, got %d", len(res))
	}
}

func TestGlobToRegexEscaping(t *testing.T) {
	re := globToRegex("err.or*?")
	// '.' should be escaped literally, '*' -> '.*', '?' -> '.'.
	want := `^err\.or.*.$`
	if re != want {
		t.Fatalf("globToRegex mismatch: got %q want %q", re, want)
	}
}

func TestSegmentFilePathsAreDeterministic(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, Config{Dir: dir, MaxBufferedDocs: 1, BlockSize: 4})
	mustAppend(t, s, LogDoc{Host: "h1", Time: 1, Message: "auto flush one"})
	stats := s.CompressionStats()
	if len(stats) != 1 {
		t.Fatalf("expected auto-flush after hitting MaxBufferedDocs, got %d segments", len(stats))
	}
	if filepath.Dir(stats[0].Path) != dir {
		t.Fatalf("expected segment in %s, got %s", dir, stats[0].Path)
	}
}
