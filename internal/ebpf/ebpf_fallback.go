//go:build !linux

package ebpf

import (
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"
)

// Collector is the pluggable source of REDEvents on non-Linux platforms
// (and can also be used on Linux by callers that want a synthetic source
// for testing without CAP_BPF). A real eBPF-backed Collector would live in
// ebpf_linux.go; this file only defines the interface + a working
// approximation used by tests and by any caller running off-Linux.
type Collector interface {
	// Collect returns a batch of observed (or synthesized) RED events.
	// Implementations may block briefly but should not block forever.
	Collect() []REDEvent
}

// SyntheticCollector is a real, deterministic (given a seeded source)
// userspace approximation of network RED-event capture. It does not touch
// the kernel in any way; it exists so the rest of the system can be built
// and tested against the ebpf.Collector contract on platforms without
// eBPF. It is driven by a caller-supplied feed of "connections" rather
// than actually sniffing sockets, making it fully deterministic/testable.
type SyntheticCollector struct {
	mu    sync.Mutex
	queue []REDEvent
}

// NewSyntheticCollector returns an empty collector; use Push to enqueue
// events (e.g. from an instrumented HTTP client/server middleware) for
// later draining via Collect.
func NewSyntheticCollector() *SyntheticCollector {
	return &SyntheticCollector{}
}

// Push enqueues a RED event to be returned by a subsequent Collect call.
func (s *SyntheticCollector) Push(evt REDEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, evt)
}

// Collect drains and returns all queued events.
func (s *SyntheticCollector) Collect() []REDEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queue
	s.queue = nil
	return out
}

// Recorder wraps a Collector and provides a simple polling loop that
// accumulates events into an in-memory ring, standing in for what would
// otherwise be a perf-event-array reader goroutine on Linux.
type Recorder struct {
	collector Collector
	mu        sync.Mutex
	events    []REDEvent
	maxEvents int
}

// NewRecorder builds a Recorder around the given Collector, retaining at
// most maxEvents most-recent events.
func NewRecorder(c Collector, maxEvents int) *Recorder {
	if maxEvents <= 0 {
		maxEvents = 1000
	}
	return &Recorder{collector: c, maxEvents: maxEvents}
}

// Poll pulls the next batch from the collector and appends it to the
// recorder's ring, evicting the oldest events if over capacity.
func (r *Recorder) Poll() int {
	batch := r.collector.Collect()
	if len(batch) == 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, batch...)
	if over := len(r.events) - r.maxEvents; over > 0 {
		r.events = r.events[over:]
	}
	return len(batch)
}

// Run polls the collector on the given interval until ctx-like stop
// channel is closed (kept dependency-free: caller passes a done channel).
func (r *Recorder) Run(interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			r.Poll()
		}
	}
}

// Events returns a copy of the currently retained events.
func (r *Recorder) Events() []REDEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]REDEvent, len(r.events))
	copy(out, r.events)
	return out
}

// randUint32 returns a cryptographically-sourced but otherwise meaningless
// 32-bit value, used only to synthesize plausible-looking addresses/ports
// in helper constructors below (NewLoopbackEvent). Not used for anything
// security sensitive.
func randUint32() uint32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}

// NewLoopbackEvent builds a plausible REDEvent for 127.0.0.1 traffic,
// useful in tests/demos that want realistic-looking but synthetic data
// without depending on actual kernel capture.
func NewLoopbackEvent(pid uint32, comm string, srcPort, dstPort uint16, bytes uint64, dur time.Duration, isErr bool) REDEvent {
	loopback := binary.BigEndian.Uint32([]byte{127, 0, 0, 1})
	return REDEvent{
		PID:        pid,
		Comm:       comm,
		SrcAddr:    loopback,
		DstAddr:    loopback,
		SrcPort:    srcPort,
		DstPort:    dstPort,
		Bytes:      bytes,
		DurationNS: uint64(dur.Nanoseconds()),
		IsError:    isErr,
	}
}

// fallbackTLS is the non-Linux TLSVisibility implementation: there is no
// way to uprobe SSL_write/SSL_read without eBPF, so every method reports
// ErrUnsupportedPlatform. It is still a real, usable value satisfying the
// interface (as opposed to a nil/panic stub) so calling code can treat
// "no TLS visibility on this platform" as an ordinary error path.
type fallbackTLS struct{}

// NewTLSVisibility returns the platform's TLSVisibility implementation.
// On non-Linux platforms this always reports ErrUnsupportedPlatform from
// OnPlaintext.
func NewTLSVisibility() TLSVisibility {
	return fallbackTLS{}
}

func (fallbackTLS) OnPlaintext(fn func(direction string, data []byte)) error {
	_ = fn
	return ErrUnsupportedPlatform
}
