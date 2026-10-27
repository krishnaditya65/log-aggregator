package logsdb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"custom-log-aggregator/internal/util"
)

// Config controls Store behavior. Zero-value fields fall back to sane
// defaults in NewStore.
type Config struct {
	// Dir is where segment files are written. Created if missing.
	Dir string

	// MaxBufferedDocs is how many docs accumulate in memory before an
	// automatic flush to a new sorted, compressed segment. Default 1000.
	MaxBufferedDocs int

	// BlockSize is how many docs go in each compressed block within a
	// segment. Default 128.
	BlockSize int

	// Retention is how long a segment is kept after its newest document's
	// Time before it becomes eligible for expiry via RunRetention. Zero
	// disables retention (segments are kept forever).
	Retention time.Duration
}

// Store is the LogsDB entry point: buffered, sorted, compressed, indexed
// log storage with keyword/regex/wildcard/tag search.
type Store struct {
	cfg Config

	mu       sync.RWMutex // guards buffer, segments, closed
	buffer   []LogDoc
	segments []*segment
	closed   bool
	nextSeg  int

	expireMu sync.Mutex
	onExpire func(path string, minTime, maxTime int64)
}

// NewStore opens (or creates) a LogsDB store rooted at cfg.Dir.
func NewStore(cfg Config) (*Store, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("logsdb: Dir is required")
	}
	if cfg.MaxBufferedDocs <= 0 {
		cfg.MaxBufferedDocs = 1000
	}
	if cfg.BlockSize <= 0 {
		cfg.BlockSize = 128
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, err
	}

	s := &Store{cfg: cfg}

	// Load any pre-existing segments (e.g. from a prior process).
	entries, err := os.ReadDir(cfg.Dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".seg" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		seg, err := openSegment(filepath.Join(cfg.Dir, n))
		if err != nil {
			return nil, fmt.Errorf("logsdb: loading segment %s: %w", n, err)
		}
		s.segments = append(s.segments, seg)
		s.nextSeg++
	}

	return s, nil
}

// OnExpireSegment registers a hook invoked just before a segment is removed
// by RunRetention, so a cold-storage archiver can copy it off first. Only
// one hook is kept; calling again replaces the previous one.
func (s *Store) OnExpireSegment(fn func(path string, minTime, maxTime int64)) {
	s.expireMu.Lock()
	s.onExpire = fn
	s.expireMu.Unlock()
}

// Append buffers a document, assigning an ID and default Time if unset,
// and triggers an automatic flush once the buffer reaches MaxBufferedDocs.
func (s *Store) Append(doc LogDoc) error {
	if doc.ID == "" {
		doc.ID = util.NewID("log")
	}
	if doc.Time == 0 {
		doc.Time = time.Now().UnixNano()
	}
	doc = doc.clone()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("logsdb: store is closed")
	}
	s.buffer = append(s.buffer, doc)
	needFlush := len(s.buffer) >= s.cfg.MaxBufferedDocs
	s.mu.Unlock()

	if needFlush {
		return s.Flush()
	}
	return nil
}

// Flush forces any buffered docs to be sorted (Host, Time), compressed, and
// written out as a new immutable segment.
func (s *Store) Flush() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("logsdb: store is closed")
	}
	if len(s.buffer) == 0 {
		s.mu.Unlock()
		return nil
	}
	docs := s.buffer
	s.buffer = nil
	name := fmt.Sprintf("seg-%08d.seg", s.nextSeg)
	s.nextSeg++
	s.mu.Unlock()

	seg, err := writeSegment(s.cfg.Dir, name, docs, s.cfg.BlockSize)
	if err != nil {
		// Put the docs back so they aren't lost on a transient error.
		s.mu.Lock()
		s.buffer = append(docs, s.buffer...)
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.segments = append(s.segments, seg)
	s.mu.Unlock()
	return nil
}

// Search evaluates q against all flushed segments plus any still-buffered
// docs. When q has an indexable clause (Keyword/Tags/Fields), each
// segment's inverted index narrows the candidate set to O(matching docs)
// before any doc bytes are read; pure Regex/Wildcard queries fall back to a
// full segment scan since there is no term to look up.
func (s *Store) Search(q Query) ([]LogDoc, error) {
	if err := q.compile(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	segs := make([]*segment, len(s.segments))
	copy(segs, s.segments)
	buffered := make([]LogDoc, len(s.buffer))
	copy(buffered, s.buffer)
	s.mu.RUnlock()

	var out []LogDoc

	for _, seg := range segs {
		docs, err := seg.search(q)
		if err != nil {
			return nil, err
		}
		out = append(out, docs...)
		if q.Limit > 0 && len(out) >= q.Limit {
			return out[:q.Limit], nil
		}
	}

	// The buffer isn't indexed yet (it's not sorted/flushed), so it's
	// always a plain linear scan.
	for _, d := range buffered {
		if q.matchesPostFilter(d) {
			out = append(out, d)
			if q.Limit > 0 && len(out) >= q.Limit {
				return out[:q.Limit], nil
			}
		}
	}

	return out, nil
}

// search runs q against one segment using its inverted index where
// possible.
func (s *segment) search(q Query) ([]LogDoc, error) {
	var candidates []int32
	haveCandidates := false

	intersect := func(postings []int32) {
		if !haveCandidates {
			candidates = postings
			haveCandidates = true
			return
		}
		candidates = intersectSorted(candidates, postings)
	}

	if q.Keyword != "" {
		term := "msg:" + normalizeKeyword(q.Keyword)
		intersect(s.footer.Index[term])
	}
	for k, v := range q.Tags {
		term := "tag:" + k + "=" + v
		intersect(s.footer.Index[term])
	}
	for k, v := range q.Fields {
		term := "field:" + k + "=" + v
		intersect(s.footer.Index[term])
	}

	if !haveCandidates {
		// No indexable clause: only legitimate for regex/wildcard-only
		// queries, which must fall back to a full segment scan.
		all, err := s.allDocs()
		if err != nil {
			return nil, err
		}
		var out []LogDoc
		for _, d := range all {
			if q.matchesPostFilter(d) {
				out = append(out, d)
			}
		}
		return out, nil
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	docs, err := s.docsByOrdinals(candidates)
	if err != nil {
		return nil, err
	}
	var out []LogDoc
	for _, d := range docs {
		if q.matchesPostFilter(d) {
			out = append(out, d)
		}
	}
	return out, nil
}

func normalizeKeyword(kw string) string {
	toks := tokenize(kw)
	if len(toks) == 0 {
		return kw
	}
	return toks[0]
}

// intersectSorted intersects two already-sorted int32 slices. Postings are
// built in increasing ordinal order at segment-write time (see
// writeSegment), so no sort is needed here -- which also matters for
// thread-safety, since postings slices are shared, read-only, and reused
// across concurrent Search calls; sorting in place would race.
func intersectSorted(a, b []int32) []int32 {
	var out []int32
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return out
}

// CompressionStats reports raw-vs-compressed bytes per flushed segment,
// demonstrating the benefit of host+time sorting before compression.
func (s *Store) CompressionStats() []SegmentStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SegmentStats, 0, len(s.segments))
	for _, seg := range s.segments {
		out = append(out, seg.stats())
	}
	return out
}

// RunRetention removes (and reports via OnExpireSegment) segments whose
// newest document is older than cfg.Retention relative to now. It is a
// no-op if Retention is zero. Safe to call periodically from a caller-owned
// ticker, or directly from tests for determinism.
func (s *Store) RunRetention(now time.Time) error {
	if s.cfg.Retention <= 0 {
		return nil
	}
	cutoff := now.Add(-s.cfg.Retention).UnixNano()

	s.mu.Lock()
	var keep []*segment
	var expired []*segment
	for _, seg := range s.segments {
		if seg.footer.MaxTime < cutoff {
			expired = append(expired, seg)
		} else {
			keep = append(keep, seg)
		}
	}
	s.segments = keep
	s.mu.Unlock()

	s.expireMu.Lock()
	hook := s.onExpire
	s.expireMu.Unlock()

	for _, seg := range expired {
		if hook != nil {
			hook(seg.path, seg.footer.MinTime, seg.footer.MaxTime)
		}
		if err := os.Remove(seg.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Close flushes any buffered docs and marks the store closed to further
// writes.
func (s *Store) Close() error {
	if err := s.Flush(); err != nil {
		return err
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}
