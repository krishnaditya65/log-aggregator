package tsdb

import "sort"

// chunkBuffer is the in-memory, mutable write buffer for a single time
// window ("chunk"). Once its time window closes or it grows past the
// configured size threshold, the Store flushes it to an immutable
// columnar segment file (see segment.go) and discards the buffer.
type chunkBuffer struct {
	start  int64 // inclusive chunk start, unix nanos
	end    int64 // exclusive chunk end, unix nanos
	series map[SeriesID][]Point
	points int // total point count across all series, for size-threshold flushing
}

func newChunkBuffer(start, end int64) *chunkBuffer {
	return &chunkBuffer{start: start, end: end, series: make(map[SeriesID][]Point)}
}

// add appends a point for the given series. Points are kept sorted lazily:
// callers typically append in time order, but add tolerates out-of-order
// insertion by re-sorting the affected series on flush.
func (c *chunkBuffer) add(id SeriesID, p Point) {
	c.series[id] = append(c.series[id], p)
	c.points++
}

// sortedSeries returns a copy of the buffer's series map with every
// series' points sorted ascending by time, ready for encoding.
func (c *chunkBuffer) sortedSeries() map[SeriesID][]Point {
	out := make(map[SeriesID][]Point, len(c.series))
	for id, pts := range c.series {
		cpy := make([]Point, len(pts))
		copy(cpy, pts)
		sort.Slice(cpy, func(i, j int) bool { return cpy[i].Time < cpy[j].Time })
		out[id] = cpy
	}
	return out
}
