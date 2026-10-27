// Package logsdb implements a small embedded full-text log search engine,
// modeled loosely on Elasticsearch's "logsdb" index mode: documents are
// buffered in memory and, on flush, physically re-sorted by (Host, Time)
// before being written to a compressed, block-structured segment file with
// an accompanying inverted index for fast keyword/tag lookups.
package logsdb

// LogDoc is a single log record indexed by LogsDB.
type LogDoc struct {
	ID      string
	Time    int64 // unix nanoseconds
	Host    string
	Service string
	Level   string
	Message string
	TraceID string
	SpanID  string
	Tags    map[string]string
	Fields  map[string]any
	Raw     string
}

// clone returns a deep-enough copy of the doc so callers mutating maps they
// passed in (or results they got back) cannot corrupt store-internal state.
func (d LogDoc) clone() LogDoc {
	out := d
	if d.Tags != nil {
		out.Tags = make(map[string]string, len(d.Tags))
		for k, v := range d.Tags {
			out.Tags[k] = v
		}
	}
	if d.Fields != nil {
		out.Fields = make(map[string]any, len(d.Fields))
		for k, v := range d.Fields {
			out.Fields[k] = v
		}
	}
	return out
}
