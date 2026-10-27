package tracing

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewTraceAndSpanID(t *testing.T) {
	tid, err := NewTraceID()
	if err != nil {
		t.Fatalf("NewTraceID: %v", err)
	}
	if len(tid) != 32 {
		t.Fatalf("expected 32 hex chars (128 bits), got %d: %q", len(tid), tid)
	}
	sid, err := NewSpanID()
	if err != nil {
		t.Fatalf("NewSpanID: %v", err)
	}
	if len(sid) != 16 {
		t.Fatalf("expected 16 hex chars (64 bits), got %d: %q", len(sid), sid)
	}

	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := NewTraceID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate trace id generated: %s", id)
		}
		seen[id] = true
	}
}

func TestSpanStoreIdleCompletion(t *testing.T) {
	store := NewSpanStore(20 * time.Millisecond)
	var mu sync.Mutex
	var completedTraces []string
	store.OnComplete = func(traceID string, spans []Span) {
		mu.Lock()
		completedTraces = append(completedTraces, traceID)
		mu.Unlock()
	}

	now := time.Now().UnixNano()
	store.Add(Span{TraceID: "t1", SpanID: "root", StartTime: now, EndTime: 0})
	store.Add(Span{TraceID: "t1", SpanID: "child", ParentSpanID: "root", StartTime: now, EndTime: now + 100})

	if store.Pending() != 1 {
		t.Fatalf("expected 1 pending trace, got %d", store.Pending())
	}

	time.Sleep(30 * time.Millisecond)
	completed := store.Sweep()
	if _, ok := completed["t1"]; !ok {
		t.Fatalf("expected t1 to be swept as complete")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(completedTraces) != 1 || completedTraces[0] != "t1" {
		t.Fatalf("expected OnComplete called for t1, got %v", completedTraces)
	}
	if store.Pending() != 0 {
		t.Fatalf("expected 0 pending after sweep, got %d", store.Pending())
	}
}

func TestSpanStoreRootClosedSignal(t *testing.T) {
	store := NewSpanStore(time.Hour) // long idle so only explicit close matters
	now := time.Now().UnixNano()

	store.Add(Span{TraceID: "t2", SpanID: "root", StartTime: now, EndTime: 0})
	if store.Pending() != 1 {
		t.Fatalf("expected pending trace before close")
	}

	// Root span with EndTime set (closed) should auto-complete on Add.
	completed := false
	store.OnComplete = func(traceID string, spans []Span) {
		if traceID == "t2" {
			completed = true
		}
	}
	// Re-add same root span but with EndTime, simulating close event.
	store.Add(Span{TraceID: "t2", SpanID: "root", StartTime: now, EndTime: now + 500})

	if !completed {
		t.Fatalf("expected trace t2 to complete on root span close")
	}
	if store.Pending() != 0 {
		t.Fatalf("expected 0 pending after root close, got %d", store.Pending())
	}
}

func TestSpanStoreExplicitCloseRoot(t *testing.T) {
	store := NewSpanStore(time.Hour)
	now := time.Now().UnixNano()
	store.Add(Span{TraceID: "t3", SpanID: "root", StartTime: now})
	store.Add(Span{TraceID: "t3", SpanID: "child", ParentSpanID: "root", StartTime: now, EndTime: now + 10})

	var gotSpans []Span
	store.OnComplete = func(traceID string, spans []Span) {
		gotSpans = spans
	}
	store.CloseRoot("t3")

	if len(gotSpans) != 2 {
		t.Fatalf("expected 2 spans on explicit close, got %d", len(gotSpans))
	}
	if store.Pending() != 0 {
		t.Fatalf("expected trace removed after CloseRoot")
	}
}

func mkTrace(rootDur time.Duration, hasError bool, traceID string) []Span {
	start := time.Now().UnixNano()
	end := start + int64(rootDur)
	status := "ok"
	if hasError {
		status = "error"
	}
	return []Span{
		{TraceID: traceID, SpanID: "root", StartTime: start, EndTime: end, Status: "ok"},
		{TraceID: traceID, SpanID: "child", ParentSpanID: "root", StartTime: start, EndTime: end, Status: status},
	}
}

func TestTailSamplerAlwaysKeepsErrors(t *testing.T) {
	ts := NewTailSampler(time.Second)
	ts.HealthySampleRate = 0.0 // ensure only error/latency rules can keep
	for i := 0; i < 20; i++ {
		trace := mkTrace(10*time.Millisecond, true, randID(t, i))
		if !ts.Decide(trace) {
			t.Fatalf("expected error trace to always be kept")
		}
	}
}

func TestTailSamplerAlwaysKeepsSlow(t *testing.T) {
	ts := NewTailSampler(100 * time.Millisecond)
	ts.HealthySampleRate = 0.0
	for i := 0; i < 20; i++ {
		trace := mkTrace(200*time.Millisecond, false, randID(t, i))
		if !ts.Decide(trace) {
			t.Fatalf("expected slow trace to always be kept")
		}
	}
}

func TestTailSamplerHealthySampledAtRate(t *testing.T) {
	ts := NewTailSampler(time.Second)
	ts.HealthySampleRate = 0.10

	const trials = 5000
	kept := 0
	for i := 0; i < trials; i++ {
		trace := mkTrace(1*time.Millisecond, false, randID(t, i))
		if ts.Decide(trace) {
			kept++
		}
	}
	rate := float64(kept) / float64(trials)
	// Allow generous tolerance since this is a hash-based approximation.
	if rate < 0.05 || rate > 0.16 {
		t.Fatalf("expected sample rate near 0.10, got %.4f (%d/%d)", rate, kept, trials)
	}
}

func TestTailSamplerDeterministicPerTraceID(t *testing.T) {
	ts := NewTailSampler(time.Second)
	ts.HealthySampleRate = 0.5
	trace := mkTrace(1*time.Millisecond, false, "fixed-trace-id-123")
	first := ts.Decide(trace)
	for i := 0; i < 10; i++ {
		if ts.Decide(trace) != first {
			t.Fatalf("expected deterministic decision for same trace ID")
		}
	}
}

func randID(t *testing.T, i int) string {
	id, err := NewTraceID()
	if err != nil {
		t.Fatal(err)
	}
	return id + strings.Repeat("x", i%3)
}

func TestExemplarStoreAttachQuery(t *testing.T) {
	es := NewExemplarStore()
	es.Attach("http.latency", Exemplar{TraceID: "a", Value: 100, Time: 1})
	es.Attach("http.latency", Exemplar{TraceID: "b", Value: 500, Time: 2})
	es.Attach("http.latency", Exemplar{TraceID: "c", Value: 50, Time: 3})
	es.Attach("other.metric", Exemplar{TraceID: "d", Value: 999, Time: 4})

	got := es.Query("http.latency", 100)
	if len(got) != 2 {
		t.Fatalf("expected 2 exemplars >= 100, got %d", len(got))
	}
	if got[0].TraceID != "a" || got[1].TraceID != "b" {
		t.Fatalf("expected sorted by time [a,b], got %v", got)
	}

	none := es.Query("nonexistent", 0)
	if len(none) != 0 {
		t.Fatalf("expected 0 results for unknown series")
	}
}

func TestInjectContext(t *testing.T) {
	fields := map[string]any{"msg": "hello", "level": "info"}
	out := InjectContext(fields, "trace123", "span456")

	if out["trace_id"] != "trace123" || out["span_id"] != "span456" {
		t.Fatalf("expected trace/span ids injected, got %v", out)
	}
	if out["msg"] != "hello" {
		t.Fatalf("expected original fields preserved")
	}
	// Original map must not be mutated.
	if _, ok := fields["trace_id"]; ok {
		t.Fatalf("expected original map to be unmodified")
	}
}
