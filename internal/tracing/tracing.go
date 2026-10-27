// Package tracing implements a lightweight distributed-tracing model:
// span representation, ID generation, a buffering span store that detects
// trace completion, tail-based sampling, and trace-to-metric exemplars.
//
// This is a pure userspace library. Real eBPF-level context propagation
// (e.g. auto-injecting trace/span IDs into syscalls or socket buffers at
// the kernel level) lives in internal/ebpf, which is owned by a different
// task. InjectContext below is the userspace-library equivalent: it stamps
// trace/span identifiers onto structured log fields so logs, traces and
// metrics can be correlated in the aggregator's own pipeline.
package tracing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"sort"
	"sync"
	"time"
)

// Span represents a single unit of work within a distributed trace.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Service      string
	Name         string
	StartTime    int64 // unix nanos
	EndTime      int64 // unix nanos
	Status       string
	Attributes   map[string]any
	Tags         map[string]string
}

// Duration returns the span's wall-clock duration.
func (s Span) Duration() time.Duration {
	if s.EndTime <= s.StartTime {
		return 0
	}
	return time.Duration(s.EndTime - s.StartTime)
}

// IsRoot reports whether the span has no parent.
func (s Span) IsRoot() bool {
	return s.ParentSpanID == ""
}

// NewTraceID generates a random 128-bit trace ID, hex-encoded (32 chars).
func NewTraceID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("tracing: generate trace id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewSpanID generates a random 64-bit span ID, hex-encoded (16 chars).
func NewSpanID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("tracing: generate span id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------------
// SpanStore: buffers spans per trace until the trace is judged complete.
// ---------------------------------------------------------------------

type traceBuffer struct {
	spans      []Span
	lastUpdate time.Time
	closed     bool // root span explicitly signaled closed
}

// SpanStore buffers spans grouped by TraceID until a trace is deemed
// complete: either no new spans have arrived within IdleTimeout, or the
// root span was explicitly closed via CloseRoot.
type SpanStore struct {
	mu          sync.Mutex
	traces      map[string]*traceBuffer
	IdleTimeout time.Duration
	// OnComplete, if set, is invoked (outside the lock) whenever a trace
	// is flushed via Sweep or CloseRoot.
	OnComplete func(traceID string, spans []Span)
}

// NewSpanStore constructs a SpanStore with the given idle timeout used to
// decide when a trace with no root-close signal should be flushed.
func NewSpanStore(idleTimeout time.Duration) *SpanStore {
	return &SpanStore{
		traces:      make(map[string]*traceBuffer),
		IdleTimeout: idleTimeout,
	}
}

// Add buffers a span under its trace ID.
func (s *SpanStore) Add(sp Span) {
	s.mu.Lock()
	tb, ok := s.traces[sp.TraceID]
	if !ok {
		tb = &traceBuffer{}
		s.traces[sp.TraceID] = tb
	}
	tb.spans = append(tb.spans, sp)
	tb.lastUpdate = time.Now()
	if sp.IsRoot() && sp.EndTime != 0 {
		tb.closed = true
	}
	var (
		complete bool
		spans    []Span
	)
	if tb.closed {
		complete = true
		spans = append([]Span(nil), tb.spans...)
		delete(s.traces, sp.TraceID)
	}
	cb := s.OnComplete
	s.mu.Unlock()

	if complete && cb != nil {
		cb(sp.TraceID, spans)
	}
}

// CloseRoot explicitly signals that the root span for traceID has closed,
// forcing immediate completion regardless of idle timeout.
func (s *SpanStore) CloseRoot(traceID string) {
	s.mu.Lock()
	tb, ok := s.traces[traceID]
	if !ok {
		s.mu.Unlock()
		return
	}
	spans := append([]Span(nil), tb.spans...)
	delete(s.traces, traceID)
	cb := s.OnComplete
	s.mu.Unlock()

	if cb != nil {
		cb(traceID, spans)
	}
}

// Sweep flushes any traces that have been idle longer than IdleTimeout,
// returning the completed traces (also invoking OnComplete for each).
// Call this periodically (e.g. from a ticker) to reap traces whose root
// span never explicitly closes.
func (s *SpanStore) Sweep() map[string][]Span {
	now := time.Now()
	s.mu.Lock()
	completed := make(map[string][]Span)
	for id, tb := range s.traces {
		if now.Sub(tb.lastUpdate) >= s.IdleTimeout {
			completed[id] = append([]Span(nil), tb.spans...)
			delete(s.traces, id)
		}
	}
	cb := s.OnComplete
	s.mu.Unlock()

	if cb != nil {
		for id, spans := range completed {
			cb(id, spans)
		}
	}
	return completed
}

// Pending returns the number of traces currently buffered.
func (s *SpanStore) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.traces)
}

// ---------------------------------------------------------------------
// Tail-based sampling
// ---------------------------------------------------------------------

// TailSampler decides, after a trace is complete, whether to keep it.
type TailSampler struct {
	// LatencyThreshold: traces whose root-span duration exceeds this are
	// always kept.
	LatencyThreshold time.Duration
	// HealthySampleRate is the fixed probability (0..1) applied to traces
	// that are neither erroring nor slow.
	HealthySampleRate float64
}

// NewTailSampler builds a TailSampler with the given latency threshold and
// a default 1% sampling rate for healthy traces.
func NewTailSampler(latencyThreshold time.Duration) *TailSampler {
	return &TailSampler{
		LatencyThreshold:  latencyThreshold,
		HealthySampleRate: 0.01,
	}
}

// rootSpan finds the root span (ParentSpanID == "") in the trace, falling
// back to the span with the earliest start time if no explicit root exists.
func rootSpan(trace []Span) (Span, bool) {
	if len(trace) == 0 {
		return Span{}, false
	}
	for _, sp := range trace {
		if sp.IsRoot() {
			return sp, true
		}
	}
	best := trace[0]
	for _, sp := range trace[1:] {
		if sp.StartTime < best.StartTime {
			best = sp
		}
	}
	return best, true
}

// traceDuration computes total trace duration as span-covering
// [min(start), max(end)] across all spans, which for a well-formed trace
// equals the root span's duration.
func traceDuration(trace []Span) time.Duration {
	if len(trace) == 0 {
		return 0
	}
	minStart := trace[0].StartTime
	maxEnd := trace[0].EndTime
	for _, sp := range trace[1:] {
		if sp.StartTime < minStart {
			minStart = sp.StartTime
		}
		if sp.EndTime > maxEnd {
			maxEnd = sp.EndTime
		}
	}
	if maxEnd <= minStart {
		return 0
	}
	return time.Duration(maxEnd - minStart)
}

// Decide reports whether the trace should be kept. Rules, in order:
//  1. Any span with Status == "error" -> always keep.
//  2. Root-span (trace) duration exceeds LatencyThreshold -> always keep.
//  3. Otherwise, deterministically sample at HealthySampleRate based on a
//     hash of the trace ID, so the same trace ID always yields the same
//     decision (consistent sampling across collector replicas).
func (t *TailSampler) Decide(trace []Span) bool {
	if len(trace) == 0 {
		return false
	}
	for _, sp := range trace {
		if sp.Status == "error" {
			return true
		}
	}
	if t.LatencyThreshold > 0 && traceDuration(trace) > t.LatencyThreshold {
		return true
	}
	root, _ := rootSpan(trace)
	return deterministicSample(root.TraceID, t.HealthySampleRate)
}

// deterministicSample hashes traceID to a value in [0,1) and compares
// against rate, giving a stable decision for a given trace ID.
func deterministicSample(traceID string, rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(traceID))
	sum := h.Sum64()
	frac := float64(sum) / float64(^uint64(0))
	return frac < rate
}

// ---------------------------------------------------------------------
// Trace-to-metric exemplars
// ---------------------------------------------------------------------

// Exemplar links a metric observation back to the trace that produced it.
type Exemplar struct {
	TraceID string
	Value   float64
	Time    int64 // unix nanos
	Labels  map[string]string
}

// ExemplarStore attaches exemplars to named metric series and supports
// querying by minimum value (e.g. "show me exemplars for latency > 500ms").
type ExemplarStore struct {
	mu     sync.Mutex
	series map[string][]Exemplar
}

// NewExemplarStore constructs an empty ExemplarStore.
func NewExemplarStore() *ExemplarStore {
	return &ExemplarStore{series: make(map[string][]Exemplar)}
}

// Attach records an exemplar for the given metric series.
func (e *ExemplarStore) Attach(metricSeries string, ex Exemplar) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.series[metricSeries] = append(e.series[metricSeries], ex)
}

// Query returns exemplars for metricSeries with Value >= minValue, sorted
// by Time ascending.
func (e *ExemplarStore) Query(metricSeries string, minValue float64) []Exemplar {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Exemplar
	for _, ex := range e.series[metricSeries] {
		if ex.Value >= minValue {
			out = append(out, ex)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time < out[j].Time })
	return out
}

// ---------------------------------------------------------------------
// Trace-log correlation
// ---------------------------------------------------------------------

// InjectContext returns a copy of logFields with trace_id/span_id stamped
// in, so log lines emitted while a span is active can be correlated back
// to the originating trace. This is the userspace-library equivalent of
// eBPF-level context propagation (see internal/ebpf, owned separately),
// which injects context at the kernel/socket layer without app changes;
// this helper is for call sites that already have a logger and want to
// enrich fields explicitly before emission.
func InjectContext(logFields map[string]any, traceID, spanID string) map[string]any {
	out := make(map[string]any, len(logFields)+2)
	for k, v := range logFields {
		out[k] = v
	}
	out["trace_id"] = traceID
	out["span_id"] = spanID
	return out
}
