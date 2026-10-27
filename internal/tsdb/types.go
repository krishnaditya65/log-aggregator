// Package tsdb implements a small embedded time-series storage engine
// inspired by Prometheus TSDB / InfluxDB / Elasticsearch doc-values.
//
// Data flows: Append -> in-memory write buffer (chunked by time) -> flushed
// to immutable columnar segment files on disk once a chunk closes -> a
// background compactor periodically merges small segments and applies
// retention. Reads go through Store.Query, which resolves label matchers
// via an inverted index and then uses each segment's per-series block
// index ("doc values") to skip decoding blocks that cannot match the
// requested time range.
package tsdb

import (
	"fmt"
	"sort"
	"strings"
)

// Point is a single time-series sample.
type Point struct {
	// Time is a unix timestamp in nanoseconds.
	Time int64
	// Value is the sample value.
	Value float64
}

// Series is a named time series: a set of labels plus its samples.
type Series struct {
	Labels map[string]string
	Points []Point
}

// SeriesID is a canonical, order-independent string identifier for a
// label set: "name1=value1,name2=value2,..." with names sorted.
type SeriesID string

// seriesIDFromLabels builds a canonical SeriesID from a label map.
func seriesIDFromLabels(labels map[string]string) SeriesID {
	if len(labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(labels))
	for k := range labels {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(n)
		b.WriteByte('=')
		b.WriteString(labels[n])
	}
	return SeriesID(b.String())
}

// labelsFromSeriesID parses a canonical SeriesID back into a label map.
// It is the inverse of seriesIDFromLabels for well-formed IDs.
func labelsFromSeriesID(id SeriesID) map[string]string {
	s := string(id)
	if s == "" {
		return map[string]string{}
	}
	parts := strings.Split(s, ",")
	labels := make(map[string]string, len(parts))
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		labels[kv[0]] = kv[1]
	}
	return labels
}

// matchesLabels reports whether the given label set satisfies all matchers.
// Matchers whose value starts with "~" are treated as regular expressions
// applied to the label value (e.g. "~^prod-.*$"); all others require exact
// equality. This is a pure, allocation-light helper used both by the
// inverted index resolution and as a final verification step.
func matchesLabels(labels map[string]string, matchers map[string]string) bool {
	for name, want := range matchers {
		got, ok := labels[name]
		if !ok {
			return false
		}
		if strings.HasPrefix(want, "~") {
			re, err := compileCached(want[1:])
			if err != nil || !re.MatchString(got) {
				return false
			}
			continue
		}
		if got != want {
			return false
		}
	}
	return true
}

// ErrNotFound is returned when a query matches no series.
type ErrNotFound struct {
	Matchers map[string]string
}

func (e *ErrNotFound) Error() string {
	return fmt.Sprintf("tsdb: no series found for matchers %v", e.Matchers)
}
