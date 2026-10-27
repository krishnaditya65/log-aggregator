package tsdb

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"
)

// Compactor is a background process that periodically merges small, closed
// segment files whose time ranges overlap or are adjacent into larger
// segments (deduplicating any overlapping points), and enforces retention
// by deleting (or handing off, via Store's OnExpireSegment hook) segments
// older than the configured retention window.
type Compactor struct {
	store *Store

	// MaxMergeSpan bounds how wide a merged segment's time range may be;
	// segments are grouped for merging only while the running span stays
	// within this bound. Defaults to 4x the store's chunk duration.
	MaxMergeSpan time.Duration

	// MinSegmentsToMerge is the minimum group size worth merging.
	// Defaults to 2.
	MinSegmentsToMerge int

	cancel context.CancelFunc
	done   chan struct{}
}

// NewCompactor creates a compactor bound to store.
func NewCompactor(store *Store) *Compactor {
	return &Compactor{
		store:              store,
		MaxMergeSpan:       4 * store.opts.ChunkDuration,
		MinSegmentsToMerge: 2,
	}
}

// segMeta is lightweight metadata about a segment used for grouping.
type segMeta struct {
	path    string
	minTime int64
	maxTime int64
}

// RunOnce performs a single compaction + retention pass. It is safe to
// call concurrently with Store.Append/Query (segment files are immutable;
// swaps are done under the store's lock).
func (c *Compactor) RunOnce(ctx context.Context) error {
	if err := c.compactOnce(ctx); err != nil {
		return err
	}
	return c.applyRetention(ctx)
}

func (c *Compactor) compactOnce(ctx context.Context) error {
	paths := c.store.segmentPaths()
	if len(paths) < 2 {
		return nil
	}

	metas := make([]segMeta, 0, len(paths))
	for _, p := range paths {
		min, max, err := c.store.segmentTimeRange(p)
		if err != nil {
			// Segment may have been removed concurrently by a prior
			// compaction/retention pass; skip it.
			continue
		}
		metas = append(metas, segMeta{path: p, minTime: min, maxTime: max})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].minTime < metas[j].minTime })

	minGroup := c.MinSegmentsToMerge
	if minGroup < 2 {
		minGroup = 2
	}
	maxSpan := c.MaxMergeSpan
	if maxSpan <= 0 {
		maxSpan = 4 * c.store.opts.ChunkDuration
	}

	var group []segMeta
	flushGroup := func() error {
		if len(group) < minGroup {
			group = nil
			return nil
		}
		if err := c.mergeGroup(group); err != nil {
			return err
		}
		group = nil
		return nil
	}

	for _, m := range metas {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if len(group) == 0 {
			group = append(group, m)
			continue
		}
		last := group[len(group)-1]
		spanIfAdded := maxInt64(m.maxTime, group[0].minTime) - minInt64(group[0].minTime, m.minTime)
		// Adjacent-or-overlapping test: next segment's min time must not
		// leave a gap beyond the store's chunk duration past the running
		// group's current max time, and the total span must stay bounded.
		adjacent := m.minTime <= last.maxTime+c.store.opts.ChunkDuration.Nanoseconds()
		if adjacent && spanIfAdded <= maxSpan.Nanoseconds() {
			group = append(group, m)
			continue
		}
		if err := flushGroup(); err != nil {
			return err
		}
		group = append(group, m)
	}
	return flushGroup()
}

// mergeGroup reads every series/point from the given segments, dedups
// points sharing the same (series, timestamp), and writes a single new
// segment covering the merged time range, then atomically swaps it into
// the store in place of the originals.
func (c *Compactor) mergeGroup(group []segMeta) error {
	merged := make(map[SeriesID]map[int64]float64)

	for _, m := range group {
		r, err := c.store.readerCache(m.path)
		if err != nil {
			return fmt.Errorf("tsdb: compactor open %s: %w", m.path, err)
		}
		for _, entry := range r.index {
			pts, err := r.readBlock(entry)
			if err != nil {
				return fmt.Errorf("tsdb: compactor read block %s: %w", m.path, err)
			}
			byTime, ok := merged[entry.SeriesID]
			if !ok {
				byTime = make(map[int64]float64, len(pts))
				merged[entry.SeriesID] = byTime
			}
			for _, p := range pts {
				// Later-read segments overwrite earlier ones for the
				// same timestamp; group is sorted by minTime so this
				// keeps the most recent write, a reasonable last-write-
				// wins dedup policy.
				byTime[p.Time] = p.Value
			}
		}
	}

	if len(merged) == 0 {
		return nil
	}

	seriesOut := make(map[SeriesID][]Point, len(merged))
	for id, byTime := range merged {
		pts := make([]Point, 0, len(byTime))
		for t, v := range byTime {
			pts = append(pts, Point{Time: t, Value: v})
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].Time < pts[j].Time })
		seriesOut[id] = pts
	}

	c.store.mu.Lock()
	c.store.seq++
	seq := c.store.seq
	c.store.mu.Unlock()

	minTime := group[0].minTime
	for _, m := range group[1:] {
		minTime = minInt64(minTime, m.minTime)
	}
	// Sequence number guarantees uniqueness even if minTime matches one
	// of the segments being replaced.
	name := fmt.Sprintf("seg-%020d-%06d.seg", minTime, seq)
	newPath := c.store.opts.Dir + string(os.PathSeparator) + name

	if err := writeSegment(newPath, seriesOut); err != nil {
		return fmt.Errorf("tsdb: compactor write merged segment: %w", err)
	}

	oldPaths := make([]string, len(group))
	for i, m := range group {
		oldPaths[i] = m.path
	}

	c.store.mu.Lock()
	c.store.replaceSegmentsLocked(oldPaths, newPath)
	c.store.mu.Unlock()

	for _, p := range oldPaths {
		if p == newPath {
			continue
		}
		_ = os.Remove(p)
	}
	return nil
}

// applyRetention deletes (after invoking OnExpireSegment, if set) any
// segment whose entire time range is older than the configured retention
// window relative to the newest data point observed in the store.
func (c *Compactor) applyRetention(ctx context.Context) error {
	if c.store.opts.Retention <= 0 {
		return nil
	}
	paths := c.store.segmentPaths()
	if len(paths) == 0 {
		return nil
	}

	var globalMax int64
	metas := make([]segMeta, 0, len(paths))
	for _, p := range paths {
		min, max, err := c.store.segmentTimeRange(p)
		if err != nil {
			continue
		}
		metas = append(metas, segMeta{path: p, minTime: min, maxTime: max})
		if max > globalMax {
			globalMax = max
		}
	}

	cutoff := globalMax - c.store.opts.Retention.Nanoseconds()
	for _, m := range metas {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if m.maxTime >= cutoff {
			continue
		}
		if c.store.opts.OnExpireSegment != nil {
			c.store.opts.OnExpireSegment(m.path, m.minTime, m.maxTime)
		}
		// Note: label-index entries for series that only existed in the
		// expired segment are left in place; Query simply returns no
		// points for them since no segment/chunk carries their data
		// anymore. A full GC of orphaned index entries would require
		// scanning all remaining segments and is left as a future
		// optimization.
		c.store.mu.Lock()
		c.store.removeSegmentLocked(m.path)
		c.store.mu.Unlock()
		if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("tsdb: retention remove %s: %w", m.path, err)
		}
	}
	return nil
}

// Start launches a background goroutine that calls RunOnce every interval
// until the returned context is canceled or Stop is called. It returns
// immediately.
func (c *Compactor) Start(ctx context.Context, interval time.Duration) {
	ctx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.done = make(chan struct{})

	go func() {
		defer close(c.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.RunOnce(ctx)
			}
		}
	}()
}

// Stop cancels a compaction loop started with Start and waits for it to
// exit.
func (c *Compactor) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.done != nil {
		<-c.done
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
