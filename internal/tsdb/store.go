package tsdb

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options configures a Store.
type Options struct {
	// Dir is the directory segments, the label index and compaction
	// output are persisted to. It is created if missing.
	Dir string

	// ChunkDuration is the width of each in-memory write chunk before it
	// is eligible to be flushed to a segment (e.g. 2h). Defaults to 2h.
	ChunkDuration time.Duration

	// MaxChunkPoints, if > 0, forces a chunk flush once it holds this
	// many points total, regardless of whether its time window has
	// closed (bounds memory use under high write volume).
	MaxChunkPoints int

	// CardinalityThreshold is the max distinct values per label name
	// before new values are rolled up. Defaults to a high value
	// (effectively disabled) if <= 0.
	CardinalityThreshold int

	// Retention is how long a segment's data is kept before it is
	// expired (handed to OnExpireSegment, if set, then deleted). Zero
	// disables retention-based deletion.
	Retention time.Duration

	// OnExpireSegment, if set, is invoked with a segment's path and time
	// range immediately before that segment is deleted for retention,
	// giving a cold-storage/archival system a chance to pick it up.
	OnExpireSegment func(segmentPath string, minTime, maxTime int64)
}

func (o *Options) setDefaults() {
	if o.ChunkDuration <= 0 {
		o.ChunkDuration = 2 * time.Hour
	}
	if o.CardinalityThreshold <= 0 {
		o.CardinalityThreshold = 1 << 30
	}
}

// Store is the main entry point of the tsdb package: it wires together the
// in-memory write buffer (chunked by time), immutable on-disk segments, the
// label inverted index, the cardinality limiter and the background
// compactor.
//
// All exported methods are safe for concurrent use.
type Store struct {
	mu   sync.RWMutex
	opts Options

	index   *LabelIndex
	limiter *CardinalityLimiter

	// active holds one write buffer per open chunk, keyed by chunk start
	// time (unix nanos). Most workloads have exactly one open chunk, but
	// slightly-out-of-order writes near a boundary can touch two.
	active map[int64]*chunkBuffer

	// segments lists on-disk segment file paths, oldest first.
	segments []string
	readers  map[string]*segmentReader

	decoded *counter // decoded-block counter, exposed for observability/tests
	seq     uint64   // monotonically increasing counter for unique filenames

	closed bool
}

// NewStore opens (or creates) a store rooted at opts.Dir.
func NewStore(opts Options) (*Store, error) {
	opts.setDefaults()
	if opts.Dir == "" {
		return nil, fmt.Errorf("tsdb: Options.Dir is required")
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("tsdb: create dir: %w", err)
	}

	idx, err := LoadLabelIndex(indexPath(opts.Dir))
	if err != nil {
		return nil, fmt.Errorf("tsdb: load index: %w", err)
	}

	entries, err := os.ReadDir(opts.Dir)
	if err != nil {
		return nil, err
	}
	var segments []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".seg") {
			continue
		}
		segments = append(segments, filepath.Join(opts.Dir, e.Name()))
	}
	sort.Strings(segments)

	return &Store{
		opts:     opts,
		index:    idx,
		limiter:  NewCardinalityLimiter(opts.CardinalityThreshold),
		active:   make(map[int64]*chunkBuffer),
		segments: segments,
		readers:  make(map[string]*segmentReader),
		decoded:  &counter{},
	}, nil
}

func indexPath(dir string) string { return filepath.Join(dir, "index.gob") }

// Append records a single sample. labels are passed through the store's
// CardinalityLimiter before being written, so a high-cardinality label may
// be generalized to "_other_" (see CardinalityLimiter).
func (s *Store) Append(labels map[string]string, t int64, v float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("tsdb: store is closed")
	}

	admitted := s.limiter.Admit(labels)
	id := seriesIDFromLabels(admitted)
	s.index.Add(id, admitted)

	dur := s.opts.ChunkDuration.Nanoseconds()
	start := (t / dur) * dur
	if t < 0 && t%dur != 0 {
		start -= dur // floor division for negative timestamps
	}
	end := start + dur

	cb, ok := s.active[start]
	if !ok {
		cb = newChunkBuffer(start, end)
		s.active[start] = cb
	}
	cb.add(id, Point{Time: t, Value: v})

	// Flush this chunk if it has grown past the size threshold.
	if s.opts.MaxChunkPoints > 0 && cb.points >= s.opts.MaxChunkPoints {
		if err := s.flushChunkLocked(start); err != nil {
			return err
		}
	}

	// Opportunistically flush any chunk whose window has fully closed
	// relative to the latest timestamp we've observed (i.e. it is more
	// than one chunk width in the past).
	for cs := range s.active {
		if cs != start && cs+dur <= t {
			if err := s.flushChunkLocked(cs); err != nil {
				return err
			}
		}
	}

	return nil
}

// flushChunkLocked writes the chunk starting at `start` to a new segment
// file and removes it from the active set. Caller must hold s.mu.
func (s *Store) flushChunkLocked(start int64) error {
	cb, ok := s.active[start]
	if !ok || cb.points == 0 {
		delete(s.active, start)
		return nil
	}
	delete(s.active, start)

	s.seq++
	name := fmt.Sprintf("seg-%020d-%06d.seg", start, s.seq)
	path := filepath.Join(s.opts.Dir, name)

	if err := writeSegment(path, cb.sortedSeries()); err != nil {
		return fmt.Errorf("tsdb: flush chunk: %w", err)
	}
	s.segments = append(s.segments, path)
	sort.Strings(s.segments)
	return nil
}

// FlushAll flushes every currently open in-memory chunk to disk,
// regardless of whether its time window has closed. Useful before Close or
// in tests that want deterministic on-disk state.
func (s *Store) FlushAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	starts := make([]int64, 0, len(s.active))
	for start := range s.active {
		starts = append(starts, start)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	for _, start := range starts {
		if err := s.flushChunkLocked(start); err != nil {
			return err
		}
	}
	return nil
}

// Query returns all series matching the given label matchers that have at
// least one point in [start, end] (inclusive), restricted to that range.
// Matchers map a label name to either an exact value, or "~<regexp>" for a
// regex match against the label's value. An empty matcher map matches
// every series.
func (s *Store) Query(matchers map[string]string, start, end int64) ([]Series, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	candidates := s.index.Resolve(matchers)
	if len(candidates) == 0 {
		return nil, &ErrNotFound{Matchers: matchers}
	}

	results := make(map[SeriesID]*Series, len(candidates))
	get := func(id SeriesID) *Series {
		if series, ok := results[id]; ok {
			return series
		}
		labels, _ := s.index.Labels(id)
		series := &Series{Labels: labels}
		results[id] = series
		return series
	}

	wantSet := make(map[SeriesID]struct{}, len(candidates))
	for _, id := range candidates {
		wantSet[id] = struct{}{}
	}

	// 1. Active (unflushed) in-memory chunks.
	for _, cb := range s.active {
		if cb.end <= start || cb.start > end {
			continue
		}
		for id := range wantSet {
			pts, ok := cb.series[id]
			if !ok {
				continue
			}
			series := get(id)
			for _, p := range pts {
				if p.Time >= start && p.Time <= end {
					series.Points = append(series.Points, p)
				}
			}
		}
	}

	// 2. On-disk segments, using the per-segment doc-value block index to
	// skip decoding series/time ranges that cannot match.
	for _, path := range s.segments {
		r, err := s.readerLocked(path)
		if err != nil {
			return nil, err
		}
		if r.header.MaxTime < start || r.header.MinTime > end {
			continue // whole segment out of range: skip without touching the index scan cost further
		}
		for _, entry := range r.index {
			if _, wanted := wantSet[entry.SeriesID]; !wanted {
				continue
			}
			// Doc-value skip: this series' block in this segment
			// cannot contain any point in [start,end].
			if entry.MaxTime < start || entry.MinTime > end {
				continue
			}
			pts, err := r.readBlock(entry)
			if err != nil {
				return nil, err
			}
			series := get(entry.SeriesID)
			for _, p := range pts {
				if p.Time >= start && p.Time <= end {
					series.Points = append(series.Points, p)
				}
			}
		}
	}

	out := make([]Series, 0, len(results))
	for _, series := range results {
		if len(series.Points) == 0 {
			continue
		}
		sort.Slice(series.Points, func(i, j int) bool { return series.Points[i].Time < series.Points[j].Time })
		out = append(out, *series)
	}
	return out, nil
}

// readerLocked returns a cached segmentReader for path, opening it if
// necessary. Caller must hold at least s.mu (read lock sufficient since
// readers map access here is itself further guarded... note: to keep
// implementation simple and correct we upgrade lazily under a dedicated
// lock).
func (s *Store) readerLocked(path string) (*segmentReader, error) {
	// s.mu is held (R or W) by the caller for the segments slice; the
	// readers cache has its own fine-grained lock so concurrent readers
	// don't serialize on the store-wide lock while opening files.
	return s.readerCache(path)
}

var readerCacheMu sync.Mutex

func (s *Store) readerCache(path string) (*segmentReader, error) {
	readerCacheMu.Lock()
	defer readerCacheMu.Unlock()
	if r, ok := s.readers[path]; ok {
		return r, nil
	}
	r, err := openSegmentReader(path, s.decoded)
	if err != nil {
		return nil, err
	}
	s.readers[path] = r
	return r, nil
}

// invalidateReader drops a cached reader, e.g. after compaction replaces a
// segment file.
func (s *Store) invalidateReader(path string) {
	readerCacheMu.Lock()
	defer readerCacheMu.Unlock()
	delete(s.readers, path)
}

// DecodedBlocks returns the running count of series blocks that have been
// decoded from disk, across all segments and queries. It is monotonically
// increasing and mainly useful to verify doc-value skipping is effective.
func (s *Store) DecodedBlocks() int64 { return s.decoded.Value() }

// ResetDecodedBlocks zeroes the decoded-block counter (test helper).
func (s *Store) ResetDecodedBlocks() { s.decoded.Reset() }

// Limiter exposes the store's cardinality limiter for inspection/metrics.
func (s *Store) Limiter() *CardinalityLimiter { return s.limiter }

// Index exposes the store's label index for inspection/metrics.
func (s *Store) Index() *LabelIndex { return s.index }

// segmentPaths returns a snapshot of the current segment file list.
func (s *Store) segmentPaths() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.segments))
	copy(out, s.segments)
	return out
}

// replaceSegmentsLocked atomically swaps a set of old segment paths for a
// single new merged one in the store's bookkeeping. Caller must hold s.mu
// (write lock).
func (s *Store) replaceSegmentsLocked(oldPaths []string, newPath string) {
	oldSet := make(map[string]struct{}, len(oldPaths))
	for _, p := range oldPaths {
		oldSet[p] = struct{}{}
	}
	kept := s.segments[:0:0]
	for _, p := range s.segments {
		if _, drop := oldSet[p]; drop {
			continue
		}
		kept = append(kept, p)
	}
	if newPath != "" {
		kept = append(kept, newPath)
	}
	s.segments = kept
	sort.Strings(s.segments)
	for _, p := range oldPaths {
		s.invalidateReader(p)
	}
}

// removeSegmentLocked drops a segment from bookkeeping (used by retention).
// Caller must hold s.mu (write lock).
func (s *Store) removeSegmentLocked(path string) {
	out := s.segments[:0]
	for _, p := range s.segments {
		if p != path {
			out = append(out, p)
		}
	}
	s.segments = out
	s.invalidateReader(path)
}

// Close flushes all pending in-memory data to disk and persists the label
// index. The store must not be used after Close returns.
func (s *Store) Close() error {
	if err := s.FlushAll(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.index.Save(indexPath(s.opts.Dir))
}

// nextSeq returns a fresh monotonically increasing sequence number, used by
// the compactor to name merged segment files uniquely.
func (s *Store) nextSeq() uint64 { return atomic.AddUint64(&s.seq, 1) }

// parseSegmentTimeRange extracts (minTime, maxTime) from a segment's own
// header without needing the filename, by opening it via the reader cache.
func (s *Store) segmentTimeRange(path string) (min, max int64, err error) {
	r, err := s.readerCache(path)
	if err != nil {
		return 0, 0, err
	}
	return r.header.MinTime, r.header.MaxTime, nil
}
