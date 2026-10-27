package alerting

import (
	"testing"
	"time"
)

func TestSilencerSuppressesMatchingActiveAlert(t *testing.T) {
	s := NewSilencer()
	now := time.Unix(10_000, 0)

	s.Add(Silence{
		ID:        "sil-1",
		Matchers:  map[string]string{"service": "checkout", "env": "prod"},
		StartsAt:  now.Add(-time.Minute),
		EndsAt:    now.Add(time.Hour),
		CreatedBy: "oncall",
		Comment:   "planned maintenance",
	})

	silencedLabels := map[string]string{"service": "checkout", "env": "prod", "pod": "abc"}
	if !s.IsSilenced(now, silencedLabels) {
		t.Fatalf("expected labels matching silence to be suppressed")
	}

	nonMatching := map[string]string{"service": "billing", "env": "prod"}
	if s.IsSilenced(now, nonMatching) {
		t.Fatalf("expected non-matching labels to not be silenced")
	}
}

func TestSilenceAutoExpires(t *testing.T) {
	s := NewSilencer()
	start := time.Unix(20_000, 0)

	s.Add(Silence{
		ID:       "sil-2",
		Matchers: map[string]string{"service": "checkout"},
		StartsAt: start,
		EndsAt:   start.Add(10 * time.Minute),
	})

	labels := map[string]string{"service": "checkout"}

	// Active during the window.
	if !s.IsSilenced(start.Add(5*time.Minute), labels) {
		t.Fatalf("expected silence to be active mid-window")
	}

	// Expired after EndsAt - should no longer suppress, without needing
	// explicit removal.
	if s.IsSilenced(start.Add(11*time.Minute), labels) {
		t.Fatalf("expected silence to have auto-expired after EndsAt")
	}

	// Before StartsAt it should also not be active yet.
	if s.IsSilenced(start.Add(-time.Minute), labels) {
		t.Fatalf("expected silence to not be active before StartsAt")
	}
}

func TestSilencerExpireOldPrunesState(t *testing.T) {
	s := NewSilencer()
	start := time.Unix(30_000, 0)
	s.Add(Silence{ID: "sil-3", Matchers: map[string]string{"a": "b"}, StartsAt: start, EndsAt: start.Add(time.Minute)})

	removed := s.ExpireOld(start.Add(2 * time.Minute))
	if removed != 1 {
		t.Fatalf("expected 1 silence removed, got %d", removed)
	}
	if len(s.Active(start.Add(2*time.Minute))) != 0 {
		t.Fatalf("expected no active silences after expiry pruning")
	}
}

func TestEngineSuppressesFiringAlertDuringSilence(t *testing.T) {
	grouper := NewGrouper([]string{"alertname"}, 0, 1)
	notifier := &captureNotifier{}
	router := NewRouter(&Route{Receiver: "r"}, map[string]Notifier{"r": notifier})
	silencer := NewSilencer()
	eng := NewEngine(grouper, router, silencer, nil)

	rule := &Rule{
		ID:     "r1",
		Name:   "HighLatency",
		For:    0,
		Labels: map[string]string{"alertname": "HighLatency", "service": "checkout"},
		Evaluator: func(now int64) (float64, bool, error) {
			return 999, true, nil
		},
	}
	eng.AddRule(rule)

	silencer.Add(Silence{
		ID:       "maint",
		Matchers: map[string]string{"service": "checkout"},
		StartsAt: time.Unix(0, 0),
		EndsAt:   time.Unix(100_000, 0),
	})

	groups := eng.Evaluate(50_000)
	if len(groups) != 0 {
		t.Fatalf("expected alert to be suppressed by active silence, got %d groups", len(groups))
	}

	// After the silence window, the same rule should fire.
	groups = eng.Evaluate(200_000)
	if len(groups) != 1 {
		t.Fatalf("expected alert to fire once silence expired, got %d groups", len(groups))
	}
}
