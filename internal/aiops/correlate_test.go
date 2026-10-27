package aiops

import (
	"strings"
	"testing"
	"time"
)

func TestCorrelatePicksNearestPrecedingDeploy(t *testing.T) {
	base := int64(1_700_000_000)
	window := 15 * time.Minute

	anomalies := []Anomaly{
		{Service: "payment-api", Metric: "p99_latency_ms", Time: base, Score: 4.2},
	}
	deploys := []Deployment{
		{Service: "payment-api", Time: base - 3600, Version: "v1.0.0"}, // far outside window
		{Service: "payment-api", Time: base - 300, Version: "v1.2.3"},  // 5 min before, in window
		{Service: "payment-api", Time: base - 120, Version: "v1.2.4"},  // 2 min before, nearest
		{Service: "other-service", Time: base - 60, Version: "v9.9.9"}, // different service
	}

	got := Correlate(anomalies, deploys, nil, window)
	if len(got) != 1 {
		t.Fatalf("expected 1 hypothesis, got %d: %+v", len(got), got)
	}
	h := got[0]
	if h.Service != "payment-api" {
		t.Fatalf("unexpected service: %s", h.Service)
	}
	if h.Confidence <= 0 {
		t.Fatalf("expected positive confidence, got %f", h.Confidence)
	}

	found := false
	for _, e := range h.Evidence {
		if strings.Contains(e, "v1.2.4") {
			found = true
		}
		if strings.Contains(e, "v1.0.0") || strings.Contains(e, "v1.2.3") {
			t.Fatalf("evidence should only cite the nearest preceding deploy (v1.2.4), got: %s", e)
		}
	}
	if !found {
		t.Fatalf("expected evidence to cite nearest deploy v1.2.4, got: %+v", h.Evidence)
	}
}

func TestCorrelateAttachesLogSpikeEvidence(t *testing.T) {
	base := int64(1_700_000_000)
	window := 10 * time.Minute

	anomalies := []Anomaly{
		{Service: "checkout", Metric: "error_rate", Time: base, Score: 3.1},
	}
	deploys := []Deployment{
		{Service: "checkout", Time: base - 120, Version: "v2.0.0"},
	}
	spikes := []LogSpike{
		{Service: "checkout", Time: base - 30, Count: 842, SampleMessages: []string{"panic: nil pointer dereference"}},
		{Service: "unrelated-svc", Time: base, Count: 5000}, // different service, should not match
	}

	got := Correlate(anomalies, deploys, spikes, window)
	if len(got) != 1 {
		t.Fatalf("expected 1 hypothesis, got %d", len(got))
	}
	h := got[0]

	var sawSpike, sawDeploy bool
	for _, e := range h.Evidence {
		if strings.Contains(e, "log spike") && strings.Contains(e, "842") {
			sawSpike = true
		}
		if strings.Contains(e, "v2.0.0") {
			sawDeploy = true
		}
		if strings.Contains(e, "unrelated-svc") {
			t.Fatalf("evidence should not include spike from unrelated service: %s", e)
		}
	}
	if !sawSpike {
		t.Fatalf("expected log spike evidence, got: %+v", h.Evidence)
	}
	if !sawDeploy {
		t.Fatalf("expected deploy evidence, got: %+v", h.Evidence)
	}

	// Confidence with corroborating spike should exceed a deploy-only case.
	deployOnly := Correlate(anomalies, deploys, nil, window)
	if len(deployOnly) != 1 {
		t.Fatalf("expected 1 deploy-only hypothesis, got %d", len(deployOnly))
	}
	if h.Confidence <= deployOnly[0].Confidence {
		t.Fatalf("expected spike-corroborated confidence (%f) > deploy-only confidence (%f)", h.Confidence, deployOnly[0].Confidence)
	}
}

func TestCorrelateIgnoresDeployAfterAnomaly(t *testing.T) {
	base := int64(1_700_000_000)
	anomalies := []Anomaly{{Service: "svc", Metric: "m", Time: base, Score: 1}}
	deploys := []Deployment{{Service: "svc", Time: base + 60, Version: "v1"}} // after anomaly

	got := Correlate(anomalies, deploys, nil, 15*time.Minute)
	if len(got) != 0 {
		t.Fatalf("expected no hypothesis for a deploy occurring after the anomaly, got %+v", got)
	}
}

func TestCorrelateIgnoresOutOfWindowSignals(t *testing.T) {
	base := int64(1_700_000_000)
	anomalies := []Anomaly{{Service: "svc", Metric: "m", Time: base, Score: 1}}
	deploys := []Deployment{{Service: "svc", Time: base - 3600, Version: "v1"}} // 1h before, window is 5m
	spikes := []LogSpike{{Service: "svc", Time: base - 3600, Count: 100}}

	got := Correlate(anomalies, deploys, spikes, 5*time.Minute)
	if len(got) != 0 {
		t.Fatalf("expected no hypothesis when all signals are outside the window, got %+v", got)
	}
}

func TestCorrelateRelatedServiceHeuristic(t *testing.T) {
	base := int64(1_700_000_000)
	anomalies := []Anomaly{{Service: "payment-api", Metric: "errors", Time: base, Score: 2}}
	spikes := []LogSpike{{Service: "payment-db", Time: base - 10, Count: 300}}

	got := Correlate(anomalies, nil, spikes, 10*time.Minute)
	if len(got) != 1 {
		t.Fatalf("expected related-service spike to produce a hypothesis, got %d", len(got))
	}
	joined := strings.Join(got[0].Evidence, " | ")
	if !strings.Contains(joined, "related service") {
		t.Fatalf("expected evidence to note related-service relation, got: %s", joined)
	}
}

func TestCorrelateSortedByConfidenceDescending(t *testing.T) {
	base := int64(1_700_000_000)
	anomalies := []Anomaly{
		{Service: "svc-a", Metric: "m", Time: base, Score: 1},
		{Service: "svc-b", Metric: "m", Time: base, Score: 1},
	}
	deploys := []Deployment{
		{Service: "svc-a", Time: base - 600, Version: "v1"}, // far from anomaly -> low confidence
		{Service: "svc-b", Time: base - 30, Version: "v1"},  // close to anomaly -> high confidence
	}

	got := Correlate(anomalies, deploys, nil, 15*time.Minute)
	if len(got) != 2 {
		t.Fatalf("expected 2 hypotheses, got %d", len(got))
	}
	if got[0].Confidence < got[1].Confidence {
		t.Fatalf("expected results sorted by descending confidence, got %+v", got)
	}
	if got[0].Service != "svc-b" {
		t.Fatalf("expected svc-b (closer deploy) to rank first, got %s", got[0].Service)
	}
}
