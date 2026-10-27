package platform

import (
	"context"
	"sync"
	"time"

	"custom-log-aggregator/internal/tracing"
)

// traceArchive retains completed (tail-sampled-in) traces so /v2/traces/{id}
// can look them up after SpanStore has flushed them out of its own
// in-flight buffer.
type traceArchive struct {
	mu    sync.RWMutex
	order []string
	byID  map[string][]tracing.Span
}

const traceArchiveCap = 2000

func newTraceArchive() *traceArchive {
	return &traceArchive{byID: make(map[string][]tracing.Span)}
}

func (a *traceArchive) put(traceID string, spans []tracing.Span) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.byID[traceID]; !exists {
		a.order = append(a.order, traceID)
		if len(a.order) > traceArchiveCap {
			evict := a.order[0]
			a.order = a.order[1:]
			delete(a.byID, evict)
		}
	}
	a.byID[traceID] = spans
}

func (a *traceArchive) get(traceID string) ([]tracing.Span, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	spans, ok := a.byID[traceID]
	return spans, ok
}

// wireTraceArchive hooks SpanStore.OnComplete to run the tail sampler and
// retain sampled-in traces (plus attach exemplars for slow ones), and
// starts a periodic Sweep so traces whose root span never explicitly
// closes still get reaped and evaluated.
func (p *Platform) wireTraceArchive(ctx context.Context) {
	archive := newTraceArchive()
	p.traceArchive = archive
	p.Traces.OnComplete = func(traceID string, spans []tracing.Span) {
		if !p.Sampler.Decide(spans) {
			return
		}
		archive.put(traceID, spans)
	}

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.Traces.Sweep()
			}
		}
	}()
}

// GetTrace looks up a previously archived (tail-sampled-in) trace by ID.
func (p *Platform) GetTrace(traceID string) ([]tracing.Span, bool) {
	if p.traceArchive == nil {
		return nil, false
	}
	return p.traceArchive.get(traceID)
}
