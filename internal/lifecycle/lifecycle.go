// Package lifecycle implements a dynamic data-retention daemon: policies
// select segments by tenant/service and move them hot -> warm (archived
// to cold storage) -> deleted as they age, based on injected callbacks so
// the actual archive/delete mechanics (e.g. coldstore.Archiver) stay
// decoupled from the scheduling logic here.
package lifecycle

import (
	"context"
	"sync"
	"time"
)

// Policy describes a retention rule. Selector matches segments by
// key/value (e.g. {"service": "checkout"} or {"tenant": "acme"}); an
// empty Selector matches every segment. Policies are evaluated in
// registration order and the first whose Selector matches a segment
// wins ("first-match-wins" precedence) — later policies are not
// consulted for that segment.
//
// A segment younger than HotRetention is left alone ("hot"). Once its
// age reaches HotRetention (and it is still younger than
// TotalRetention) it is "warm": archived to cold storage exactly once.
// Once its age reaches TotalRetention it is expired and deleted.
// WarmRetention documents the intended duration of the warm phase
// (informational / for validation) but the authoritative expiry cutoff
// is always TotalRetention.
type Policy struct {
	Name           string
	Selector       map[string]string
	HotRetention   time.Duration
	WarmRetention  time.Duration
	TotalRetention time.Duration
}

// SegmentMeta describes one on-disk segment as reported by a Lister.
type SegmentMeta struct {
	Path    string
	Service string
	Tenant  string
	MinTime int64 // unix seconds
	MaxTime int64 // unix seconds
}

// Lister enumerates the segments currently known to the system (hot
// tier and any tracked warm segments). It is called once per RunOnce.
type Lister func() []SegmentMeta

// ArchiveFunc moves a segment's data into cold storage. It is called at
// most once per segment while that segment remains warm (Manager tracks
// which segments it has already archived so repeated ticks do not
// re-archive a segment the Lister has not yet removed).
type ArchiveFunc func(segmentPath string) error

// DeleteFunc permanently removes a segment (or its cold-storage object)
// identified by key, once it is past TotalRetention.
type DeleteFunc func(key string) error

// Manager runs registered policies against segments produced by a
// Lister, invoking Archive/Delete callbacks as segments age.
type Manager struct {
	lister  Lister
	archive ArchiveFunc
	delete  DeleteFunc

	mu       sync.Mutex
	policies []Policy

	archived map[string]bool
	deleted  map[string]bool
}

// NewManager creates a Manager. archive is invoked when a segment
// crosses its policy's HotRetention threshold; delete is invoked when a
// segment crosses its policy's TotalRetention threshold.
func NewManager(lister Lister, archive ArchiveFunc, delete DeleteFunc) *Manager {
	return &Manager{
		lister:   lister,
		archive:  archive,
		delete:   delete,
		archived: map[string]bool{},
		deleted:  map[string]bool{},
	}
}

// RegisterPolicy adds a policy. Policies are matched in registration
// order; the first matching policy for a segment wins.
func (m *Manager) RegisterPolicy(p Policy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policies = append(m.policies, p)
}

// matchPolicy returns the first registered policy whose Selector
// matches seg, or nil if none match.
func (m *Manager) matchPolicy(seg SegmentMeta) *Policy {
	fields := map[string]string{
		"service": seg.Service,
		"tenant":  seg.Tenant,
	}
	for i := range m.policies {
		p := &m.policies[i]
		if selectorMatches(p.Selector, fields) {
			return p
		}
	}
	return nil
}

func selectorMatches(selector, fields map[string]string) bool {
	for k, want := range selector {
		if want == "" || want == "*" {
			continue
		}
		got, ok := fields[k]
		if !ok || got != want {
			return false
		}
	}
	return true
}

// RunOnce scans all segments once (as of the supplied now, unix
// seconds), archiving segments that have crossed HotRetention (but not
// yet TotalRetention) and deleting segments that have crossed
// TotalRetention. Segments whose age falls below HotRetention are left
// untouched. Segments matched by no policy are skipped. Returns the
// number of segments moved (archived) and deleted in this run.
func (m *Manager) RunOnce(ctx context.Context, now int64) (moved, deleted int, err error) {
	segments := m.lister()

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, seg := range segments {
		select {
		case <-ctx.Done():
			return moved, deleted, ctx.Err()
		default:
		}

		policy := m.matchPolicy(seg)
		if policy == nil {
			continue // non-matching selector: skip
		}

		age := time.Duration(now-seg.MaxTime) * time.Second

		switch {
		case age >= policy.TotalRetention:
			if m.deleted[seg.Path] {
				continue
			}
			if err := m.delete(seg.Path); err != nil {
				return moved, deleted, err
			}
			m.deleted[seg.Path] = true
			deleted++

		case age >= policy.HotRetention:
			if m.archived[seg.Path] {
				continue // archived exactly once
			}
			if err := m.archive(seg.Path); err != nil {
				return moved, deleted, err
			}
			m.archived[seg.Path] = true
			moved++

		default:
			// still hot: untouched
		}
	}

	return moved, deleted, nil
}

// Start runs RunOnce every tickInterval until ctx is cancelled.
func (m *Manager) Start(ctx context.Context, tickInterval time.Duration) {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _, _ = m.RunOnce(ctx, time.Now().Unix())
		}
	}
}
