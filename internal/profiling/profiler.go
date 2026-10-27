package profiling

import (
	"bytes"
	"context"
	"fmt"
	"runtime/pprof"
	"sync"
	"time"
)

// Profile is a captured CPU profile, tagged with the service/host that
// produced it and the raw pprof-format (gzip'd protobuf) bytes.
type Profile struct {
	Service string
	Host    string
	Time    int64  // unix seconds, capture start
	Format  string // always "pprof" today; kept for forward-compat
	Data    []byte
}

// Profiler periodically (or on demand) captures short CPU profiling
// windows via runtime/pprof and retains them in memory, most-recent last.
type Profiler struct {
	service string
	host    string

	mu       sync.Mutex
	profiles []Profile
	max      int

	// captureFn allows tests to swap in a fast/deterministic stand-in for
	// runtime/pprof.StartCPUProfile+StopCPUProfile.
	captureFn func(duration time.Duration) ([]byte, error)
}

// NewProfiler builds a Profiler tagging captures with the given service
// and host names, retaining at most maxProfiles in memory (oldest
// evicted first).
func NewProfiler(service, host string, maxProfiles int) *Profiler {
	if maxProfiles <= 0 {
		maxProfiles = 50
	}
	p := &Profiler{service: service, host: host, max: maxProfiles}
	p.captureFn = p.captureCPUProfile
	return p
}

// captureCPUProfile runs a real runtime/pprof CPU profile for `duration`
// and returns the raw gzip'd-protobuf bytes it writes.
func (p *Profiler) captureCPUProfile(duration time.Duration) ([]byte, error) {
	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		return nil, fmt.Errorf("profiling: start cpu profile: %w", err)
	}
	// Busy-ish wait for the sampling window so the profiler has samples to
	// collect; runtime/pprof samples at ~100Hz by default regardless of
	// whether the goroutine calling StartCPUProfile itself is busy, since
	// it profiles the whole process, but a pure time.Sleep can still yield
	// too few samples on an otherwise-idle process. We do a small amount
	// of real work on another goroutine to guarantee non-trivial samples
	// exist, in addition to sleeping out the window on this goroutine.
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(duration)
		x := 0
		for time.Now().Before(deadline) {
			for i := 0; i < 100000; i++ {
				x += i * i
			}
		}
		_ = x
	}()
	<-done
	pprof.StopCPUProfile()
	return buf.Bytes(), nil
}

// Capture runs a CPU profile for the given duration and stores the
// resulting Profile, also returning it to the caller. Respects ctx
// cancellation before starting (but the underlying pprof capture itself
// runs to completion once started, matching runtime/pprof semantics).
func (p *Profiler) Capture(ctx context.Context, duration time.Duration) (Profile, error) {
	if err := ctx.Err(); err != nil {
		return Profile{}, err
	}
	if duration <= 0 {
		duration = 100 * time.Millisecond
	}
	start := time.Now()
	data, err := p.captureFn(duration)
	if err != nil {
		return Profile{}, err
	}
	prof := Profile{
		Service: p.service,
		Host:    p.host,
		Time:    start.Unix(),
		Format:  "pprof",
		Data:    data,
	}

	p.mu.Lock()
	p.profiles = append(p.profiles, prof)
	if over := len(p.profiles) - p.max; over > 0 {
		p.profiles = p.profiles[over:]
	}
	p.mu.Unlock()

	return prof, nil
}

// List returns all retained profiles, oldest first.
func (p *Profiler) List() []Profile {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Profile, len(p.profiles))
	copy(out, p.profiles)
	return out
}

// Get returns the profile captured at the given unix-second timestamp, if
// retained, along with whether it was found.
func (p *Profiler) Get(t int64) (Profile, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, prof := range p.profiles {
		if prof.Time == t {
			return prof, true
		}
	}
	return Profile{}, false
}
