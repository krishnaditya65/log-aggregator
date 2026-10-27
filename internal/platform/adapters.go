package platform

import (
	"strings"
	"time"

	"custom-log-aggregator/internal/logsdb"
	"custom-log-aggregator/internal/querylang/metricsql"
	"custom-log-aggregator/internal/querylang/pipeql"
	"custom-log-aggregator/internal/tsdb"
)

// MetricsDataSource exposes the tsdb store as a metricsql.DataSource so
// callers (the v2 API, the alerting engine) can evaluate PromQL-like
// expressions against live data.
func (p *Platform) MetricsDataSource() metricsql.DataSource {
	return metricsqlDataSource{store: p.TSDB}
}

// LogSource exposes the logsdb store as a pipeql.LogSource so callers can
// run pipelined log queries against it.
func (p *Platform) LogSource() pipeql.LogSource {
	return pipeqlLogSource{store: p.LogsDB}
}

// metricsqlDataSource adapts tsdb.Store to metricsql.DataSource so
// /v2/metrics/query can evaluate PromQL-like expressions against it.
type metricsqlDataSource struct {
	store *tsdb.Store
}

func (d metricsqlDataSource) QueryRange(matchers map[string]string, start, end int64) ([]metricsql.Series, error) {
	series, err := d.store.Query(matchers, start*int64(time.Second), end*int64(time.Second))
	if err != nil {
		return nil, err
	}
	out := make([]metricsql.Series, 0, len(series))
	for _, s := range series {
		points := make([]metricsql.Point, len(s.Points))
		for i, pt := range s.Points {
			points[i] = metricsql.Point{T: pt.Time / int64(time.Second), V: pt.Value}
		}
		out = append(out, metricsql.Series{Labels: s.Labels, Points: points})
	}
	return out, nil
}

// pipeqlLogSource adapts logsdb.Store to pipeql.LogSource so
// /v2/logs/search can evaluate pipelined queries against it.
type pipeqlLogSource struct {
	store *logsdb.Store
}

func (d pipeqlLogSource) Search(filters map[string]string, keywords []string, start, end int64) ([]pipeql.LogRecord, error) {
	q := logsdb.Query{Tags: filters}
	if len(keywords) > 0 {
		// logsdb.Query.Keyword is matched as a single exact token against
		// the inverted index (see logsdb's tokenizer), so a multi-word
		// keyword phrase like `search "otlp hello"` can't be passed
		// through verbatim - it would never match any single token.
		// Instead index-narrow on the phrase's first word (cheap, always
		// correct to AND against - every doc containing the full phrase
		// necessarily contains this token) and verify the complete
		// phrase(s) via the substring post-filter below, which checks
		// *every* keyword including this first one, not just the rest.
		if first := firstToken(keywords[0]); first != "" {
			q.Keyword = first
		}
	}
	docs, err := d.store.Search(q)
	if err != nil {
		return nil, err
	}
	out := make([]pipeql.LogRecord, 0, len(docs))
	for _, doc := range docs {
		sec := doc.Time / int64(time.Second)
		if start != 0 && sec < start {
			continue
		}
		if end != 0 && sec > end {
			continue
		}
		if !allKeywordsPresent(doc.Message, keywords) {
			continue
		}
		fields := map[string]any{}
		for k, v := range doc.Fields {
			fields[k] = v
		}
		fields["trace_id"] = doc.TraceID
		fields["span_id"] = doc.SpanID
		fields["id"] = doc.ID
		out = append(out, pipeql.LogRecord{
			Time:    sec,
			Host:    doc.Host,
			Service: doc.Service,
			Level:   doc.Level,
			Message: doc.Message,
			Fields:  fields,
		})
	}
	return out, nil
}

// allKeywordsPresent verifies every keyword phrase (not just the ones
// after the first) is present in message as a substring, case-insensitive.
// The indexed logsdb.Query.Keyword only narrows candidates by the first
// *word* of keywords[0] (see Search above), so the full phrase still needs
// verifying here even for keywords[0].
func allKeywordsPresent(message string, keywords []string) bool {
	for _, kw := range keywords {
		if !containsFold(message, kw) {
			return false
		}
	}
	return true
}

// firstToken returns the first whitespace-delimited word of s, lowercased
// to match logsdb's tokenizer.
func firstToken(s string) string {
	s = strings.TrimSpace(s)
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			return strings.ToLower(s[:i])
		}
	}
	return strings.ToLower(s)
}

func containsFold(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	hl, nl := len(haystack), len(needle)
	if nl > hl {
		return false
	}
	toLower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + 32
		}
		return b
	}
	for i := 0; i+nl <= hl; i++ {
		match := true
		for j := 0; j < nl; j++ {
			if toLower(haystack[i+j]) != toLower(needle[j]) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
