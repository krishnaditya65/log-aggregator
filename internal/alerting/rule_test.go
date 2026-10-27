package alerting

import (
	"context"
	"testing"
	"time"
)

// TestRuleForDurationGating verifies that a rule only fires once its
// breached condition has held continuously for at least rule.For.
func TestRuleForDurationGating(t *testing.T) {
	breachedFrom := int64(1000)
	value := 0.0

	rule := &Rule{
		ID:   "r1",
		Name: "high-cpu",
		For:  30 * time.Second,
	}

	rule.Evaluator = func(now int64) (float64, bool, error) {
		return value, now >= breachedFrom, nil
	}

	grouper := NewGrouper([]string{"alertname"}, 0, 1) // flush immediately, size 1
	router := NewRouter(&Route{Receiver: "default"}, map[string]Notifier{"default": &captureNotifier{}})
	eng := NewEngine(grouper, router, nil, nil)
	eng.AddRule(rule)

	// Not yet breached.
	groups := eng.Evaluate(999)
	if len(groups) != 0 {
		t.Fatalf("expected no groups before breach, got %d", len(groups))
	}

	// Breach starts at t=1000, but hasn't held for 30s yet.
	groups = eng.Evaluate(1000)
	if len(groups) != 0 {
		t.Fatalf("expected no groups immediately at breach start, got %d", len(groups))
	}
	if eng.IsFiring("r1") {
		t.Fatalf("rule should not be firing yet")
	}

	groups = eng.Evaluate(1015)
	if len(groups) != 0 {
		t.Fatalf("expected no groups mid-breach (15s < 30s for), got %d", len(groups))
	}

	// Now the breach has held for exactly 30s.
	groups = eng.Evaluate(1030)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group once for-duration elapsed, got %d", len(groups))
	}
	if !eng.IsFiring("r1") {
		t.Fatalf("rule should be firing")
	}
	if len(groups[0].Alerts) != 1 || groups[0].Alerts[0].RuleID != "r1" {
		t.Fatalf("unexpected group contents: %+v", groups[0])
	}

	// Condition clears - breach state resets.
	breachedFrom = 999999
	groups = eng.Evaluate(1031)
	if len(groups) != 0 {
		t.Fatalf("expected no groups after condition clears, got %d", len(groups))
	}
	if eng.IsFiring("r1") {
		t.Fatalf("rule should no longer be firing after condition clears")
	}
}

// captureNotifier is a no-op Notifier used where dispatch mechanics aren't
// under test.
type captureNotifier struct {
	notified []NotifiedGroup
}

func (c *captureNotifier) Notify(_ context.Context, g NotifiedGroup) error {
	c.notified = append(c.notified, g)
	return nil
}
