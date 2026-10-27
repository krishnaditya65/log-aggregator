package alerting

import (
	"strconv"
	"testing"
	"time"
)

func TestGrouperBatchesBySizeThreshold(t *testing.T) {
	g := NewGrouper([]string{"alertname", "service"}, time.Hour, 5)

	base := time.Unix(1000, 0)
	for i := 0; i < 4; i++ {
		g.Add(Alert{
			Name:     "HighErrorRate",
			Labels:   map[string]string{"alertname": "HighErrorRate", "service": "checkout", "pod": "pod-" + strconv.Itoa(i)},
			StartsAt: base,
		}, "team-a")
	}

	// Not yet at size threshold (5) and window (1h) hasn't elapsed.
	groups := g.Flush(base.Add(time.Second))
	if len(groups) != 0 {
		t.Fatalf("expected no flush before size threshold, got %d groups", len(groups))
	}

	// 50 alerts from "the same incident" collapsing into one notification -
	// simulate by adding up to the threshold.
	for i := 4; i < 50; i++ {
		g.Add(Alert{
			Name:     "HighErrorRate",
			Labels:   map[string]string{"alertname": "HighErrorRate", "service": "checkout", "pod": "pod-" + strconv.Itoa(i)},
			StartsAt: base,
		}, "team-a")
	}

	groups = g.Flush(base.Add(2 * time.Second))
	if len(groups) != 1 {
		t.Fatalf("expected exactly 1 flushed group, got %d", len(groups))
	}
	if len(groups[0].Alerts) != 50 {
		t.Fatalf("expected 50 alerts in the single group, got %d", len(groups[0].Alerts))
	}
	if groups[0].Reason != "size_threshold" {
		t.Fatalf("expected size_threshold reason, got %q", groups[0].Reason)
	}
}

func TestGrouperFlushesOnWindowElapsed(t *testing.T) {
	g := NewGrouper([]string{"alertname"}, 10*time.Second, 1000)
	base := time.Unix(2000, 0)

	g.Add(Alert{Name: "A", Labels: map[string]string{"alertname": "A"}, StartsAt: base}, "r")

	// Before window elapses: nothing.
	groups := g.Flush(base.Add(5 * time.Second))
	if len(groups) != 0 {
		t.Fatalf("expected no flush before window elapsed, got %d", len(groups))
	}

	// After window elapses: flush.
	groups = g.Flush(base.Add(11 * time.Second))
	if len(groups) != 1 {
		t.Fatalf("expected 1 group after window elapsed, got %d", len(groups))
	}
	if groups[0].Reason != "window_elapsed" {
		t.Fatalf("expected window_elapsed reason, got %q", groups[0].Reason)
	}
}

func TestGrouperSeparatesDifferentGroupKeys(t *testing.T) {
	g := NewGrouper([]string{"service"}, time.Hour, 2)
	base := time.Unix(3000, 0)

	g.Add(Alert{Labels: map[string]string{"service": "checkout"}, StartsAt: base}, "r")
	g.Add(Alert{Labels: map[string]string{"service": "billing"}, StartsAt: base}, "r")
	g.Add(Alert{Labels: map[string]string{"service": "checkout"}, StartsAt: base}, "r")

	groups := g.Flush(base.Add(time.Millisecond))
	// checkout hits the size threshold of 2; billing has only 1 and hasn't
	// hit the window yet.
	if len(groups) != 1 {
		t.Fatalf("expected 1 flushed group (checkout), got %d", len(groups))
	}
	if len(groups[0].Alerts) != 2 {
		t.Fatalf("expected 2 alerts in checkout group, got %d", len(groups[0].Alerts))
	}
	for _, a := range groups[0].Alerts {
		if a.Labels["service"] != "checkout" {
			t.Fatalf("group leaked a different service's alert: %+v", a)
		}
	}
}
