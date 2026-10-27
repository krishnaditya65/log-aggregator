package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlackNotifierPayloadShape(t *testing.T) {
	var captured slackPayload
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("failed to decode slack payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := NewSlackNotifier(srv.URL)
	group := NotifiedGroup{
		GroupKey: "gk1",
		Receiver: "slack-team",
		Alerts: []Alert{
			{Name: "HighCPU", Severity: "critical", Value: 99.5, Annotations: map[string]string{"likely_cause_deploy": "checkout deployed version v42 30s before alert"}},
		},
	}

	if err := notifier.Notify(context.Background(), group); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if gotContentType != "application/json" {
		t.Fatalf("expected application/json content type, got %q", gotContentType)
	}
	if captured.Text == "" {
		t.Fatalf("expected non-empty top-level text field")
	}
	if len(captured.Blocks) == 0 {
		t.Fatalf("expected non-empty blocks field")
	}
	found := false
	for _, b := range captured.Blocks {
		if b.Text != nil && strings.Contains(b.Text.Text, "HighCPU") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a block referencing alert name HighCPU, got blocks: %+v", captured.Blocks)
	}
}

func TestPagerDutyNotifierPayloadShape(t *testing.T) {
	var captured map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("failed to decode pagerduty payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := NewPagerDutyNotifier("routing-key-123", "log-aggregator")
	notifier.Endpoint = srv.URL

	group := NotifiedGroup{
		GroupKey: "gk2",
		Alerts: []Alert{
			{Name: "DiskFull", Severity: "critical", Value: 1.0},
		},
	}

	if err := notifier.Notify(context.Background(), group); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if captured["routing_key"] != "routing-key-123" {
		t.Fatalf("expected routing_key to be set, got %v", captured["routing_key"])
	}
	if captured["event_action"] != "trigger" {
		t.Fatalf("expected event_action=trigger, got %v", captured["event_action"])
	}
	payload, ok := captured["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected payload object, got %T: %v", captured["payload"], captured["payload"])
	}
	if payload["summary"] == "" || payload["summary"] == nil {
		t.Fatalf("expected non-empty payload.summary")
	}
	if payload["severity"] != "critical" {
		t.Fatalf("expected payload.severity=critical, got %v", payload["severity"])
	}
	if payload["source"] != "log-aggregator" {
		t.Fatalf("expected payload.source=log-aggregator, got %v", payload["source"])
	}
}

func TestNotifierRetriesOnFailureThenSucceeds(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	notifier := NewWebhookNotifier(srv.URL)
	notifier.Retry = RetryConfig{MaxAttempts: 5, BaseDelay: time.Millisecond}

	err := notifier.Notify(context.Background(), NotifiedGroup{GroupKey: "gk3"})
	if err != nil {
		t.Fatalf("expected eventual success after retries, got error: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("expected exactly 3 attempts, got %d", attempts)
	}
}

func TestNotifierGivesUpAfterMaxAttempts(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	notifier := NewWebhookNotifier(srv.URL)
	notifier.Retry = RetryConfig{MaxAttempts: 3, BaseDelay: time.Millisecond}

	err := notifier.Notify(context.Background(), NotifiedGroup{GroupKey: "gk4"})
	if err == nil {
		t.Fatalf("expected error after exhausting retries")
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Fatalf("expected exactly 3 attempts (MaxAttempts), got %d", attempts)
	}
}

// TestRoutingTreeMatcherPrecedence verifies deepest-match-wins semantics: a
// child route whose matchers are a superset match of the parent's should
// override the parent's receiver, while alerts not matching any child fall
// back to the parent (and ultimately the root/default).
func TestRoutingTreeMatcherPrecedence(t *testing.T) {
	root := &Route{
		Matchers: map[string]string{}, // default route matches everything
		Receiver: "default-receiver",
		Children: []*Route{
			{
				Matchers: map[string]string{"team": "payments"},
				Receiver: "payments-receiver",
				Children: []*Route{
					{
						Matchers: map[string]string{"team": "payments", "severity": "critical"},
						Receiver: "payments-pagerduty",
					},
				},
			},
			{
				Matchers: map[string]string{"team": "infra"},
				Receiver: "infra-receiver",
			},
		},
	}
	router := NewRouter(root, nil)

	cases := []struct {
		name     string
		labels   map[string]string
		expected []string
	}{
		{"no team matches default", map[string]string{"team": "unknown"}, []string{"default-receiver"}},
		{"payments team, non-critical", map[string]string{"team": "payments", "severity": "warning"}, []string{"payments-receiver"}},
		{"payments team critical goes to deepest match", map[string]string{"team": "payments", "severity": "critical"}, []string{"payments-pagerduty"}},
		{"infra team", map[string]string{"team": "infra"}, []string{"infra-receiver"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := router.MatchReceivers(c.labels)
			if len(got) != len(c.expected) {
				t.Fatalf("expected %v, got %v", c.expected, got)
			}
			for i := range got {
				if got[i] != c.expected[i] {
					t.Fatalf("expected %v, got %v", c.expected, got)
				}
			}
		})
	}
}

func TestRouterDispatchSendsToMatchedReceiver(t *testing.T) {
	slack := &captureNotifier{}
	router := NewRouter(&Route{Receiver: "team-a"}, map[string]Notifier{"team-a": slack})

	group := NotifiedGroup{
		GroupKey: "gk5",
		Alerts:   []Alert{{Name: "X", Labels: map[string]string{"team": "a"}}},
	}
	if err := router.Dispatch(context.Background(), group); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slack.notified) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(slack.notified))
	}
}
