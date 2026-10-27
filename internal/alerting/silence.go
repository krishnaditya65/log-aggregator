package alerting

import (
	"sync"
	"time"
)

// Silencer holds a set of maintenance Silences and reports whether a given
// alert should be suppressed. Silences are matched by exact-match on the
// Matchers subset against the alert's labels (an alert matches a silence
// iff every matcher key/value pair in the silence is present and equal in
// the alert's labels). Expired silences (EndsAt in the past) are simply
// treated as inactive; ExpireOld can be called periodically to reclaim
// memory.
type Silencer struct {
	mu       sync.Mutex
	silences map[string]Silence
}

// NewSilencer builds an empty Silencer.
func NewSilencer() *Silencer {
	return &Silencer{silences: make(map[string]Silence)}
}

// Add registers (or replaces) a silence.
func (s *Silencer) Add(sil Silence) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.silences[sil.ID] = sil
}

// Remove deletes a silence by ID (e.g. to cancel it early).
func (s *Silencer) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.silences, id)
}

// IsSilenced reports whether labels are currently suppressed by any active
// silence at time now.
func (s *Silencer) IsSilenced(now time.Time, labels map[string]string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sil := range s.silences {
		if !sil.active(now) {
			continue
		}
		if matchesSubset(sil.Matchers, labels) {
			return true
		}
	}
	return false
}

// Active returns the currently active silences at time now (does not
// mutate state).
func (s *Silencer) Active(now time.Time) []Silence {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Silence
	for _, sil := range s.silences {
		if sil.active(now) {
			out = append(out, sil)
		}
	}
	return out
}

// ExpireOld removes silences whose EndsAt is before now, freeing memory.
// Silencer works correctly without ever calling this (IsSilenced already
// ignores expired silences), it's just housekeeping.
func (s *Silencer) ExpireOld(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, sil := range s.silences {
		if now.After(sil.EndsAt) {
			delete(s.silences, id)
			removed++
		}
	}
	return removed
}

func matchesSubset(matchers, labels map[string]string) bool {
	if len(matchers) == 0 {
		// An empty matcher set matches nothing by convention here - a
		// silence must target something specific.
		return false
	}
	for k, v := range matchers {
		if labels[k] != v {
			return false
		}
	}
	return true
}
