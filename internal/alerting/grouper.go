package alerting

import (
	"sync"
	"time"
)

// Grouper batches firing alerts that share the same group-by label values
// and receiver into a single NotifiedGroup, similar to Alertmanager's
// grouping. A group is flushed (returned by Flush) once its age exceeds
// GroupWindow or its size reaches GroupSize, whichever comes first.
type Grouper struct {
	mu        sync.Mutex
	groupBy   []string
	window    time.Duration
	sizeLimit int
	groups    map[string]*group
}

type group struct {
	key      string
	receiver string
	created  time.Time
	alerts   []Alert
}

// NewGrouper builds a Grouper that groups alerts by the given label keys
// (in addition to receiver), flushing a group after `window` has elapsed
// since its first alert, or once it accumulates `sizeLimit` alerts -
// whichever happens first. sizeLimit <= 0 disables the size trigger.
func NewGrouper(groupBy []string, window time.Duration, sizeLimit int) *Grouper {
	return &Grouper{
		groupBy:   groupBy,
		window:    window,
		sizeLimit: sizeLimit,
		groups:    make(map[string]*group),
	}
}

// Add enqueues an alert into the appropriate group for the given receiver.
// It does not itself return anything - flushed batches are retrieved via
// Flush, which callers should invoke on a tick (timer-driven) as well as
// after every Add if immediate size-based flushing is desired.
func (g *Grouper) Add(a Alert, receiver string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	key := g.groupKey(a.Labels, receiver)
	grp, ok := g.groups[key]
	if !ok {
		grp = &group{key: key, receiver: receiver, created: a.StartsAt}
		if grp.created.IsZero() {
			grp.created = time.Now()
		}
		g.groups[key] = grp
	}
	grp.alerts = append(grp.alerts, a)
}

// Flush scans all pending groups and returns (removing from internal state)
// those whose window has elapsed or whose size threshold has been reached,
// as evaluated at time `now`.
func (g *Grouper) Flush(now time.Time) []NotifiedGroup {
	g.mu.Lock()
	defer g.mu.Unlock()

	var out []NotifiedGroup
	for key, grp := range g.groups {
		reason := ""
		if g.sizeLimit > 0 && len(grp.alerts) >= g.sizeLimit {
			reason = "size_threshold"
		} else if g.window > 0 && now.Sub(grp.created) >= g.window {
			reason = "window_elapsed"
		} else if g.window <= 0 {
			// No window configured: flush immediately (size-only / pass-through mode).
			reason = "window_elapsed"
		}
		if reason == "" {
			continue
		}
		out = append(out, NotifiedGroup{
			GroupKey: grp.key,
			Receiver: grp.receiver,
			Alerts:   append([]Alert(nil), grp.alerts...),
			Reason:   reason,
		})
		delete(g.groups, key)
	}
	return out
}

// Pending returns the number of alerts currently buffered (across all
// groups) waiting to be flushed. Useful for tests/metrics.
func (g *Grouper) Pending() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, grp := range g.groups {
		n += len(grp.alerts)
	}
	return n
}

func (g *Grouper) groupKey(labels map[string]string, receiver string) string {
	sub := make(map[string]string, len(g.groupBy)+1)
	for _, k := range g.groupBy {
		sub[k] = labels[k]
	}
	sub["__receiver__"] = receiver
	return labelsKey(sub)
}
