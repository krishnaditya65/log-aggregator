package alerting

import (
	"strings"
	"testing"
	"time"
)

func TestDeployCorrelatorPicksNearestPriorDeploy(t *testing.T) {
	c := NewDeployCorrelator(30 * time.Minute)

	c.AddDeployment(DeploymentEvent{Service: "checkout", Time: 1000, Version: "v1"})
	c.AddDeployment(DeploymentEvent{Service: "checkout", Time: 1500, Version: "v2"}) // nearest prior
	c.AddDeployment(DeploymentEvent{Service: "checkout", Time: 2200, Version: "v3"}) // after the alert, must be ignored
	c.AddDeployment(DeploymentEvent{Service: "billing", Time: 1800, Version: "b1"})  // different service

	alertTime := int64(2000)
	dep, ok := c.NearestPriorDeploy("checkout", alertTime)
	if !ok {
		t.Fatalf("expected a qualifying deployment to be found")
	}
	if dep.Version != "v2" {
		t.Fatalf("expected nearest prior deploy to be v2, got %s", dep.Version)
	}
}

func TestDeployCorrelatorRespectsLookbackWindow(t *testing.T) {
	c := NewDeployCorrelator(5 * time.Minute) // 300s

	c.AddDeployment(DeploymentEvent{Service: "checkout", Time: 1000, Version: "too-old"})

	_, ok := c.NearestPriorDeploy("checkout", 1000+600) // 600s later, outside 300s window
	if ok {
		t.Fatalf("expected no deployment found outside the lookback window")
	}

	_, ok = c.NearestPriorDeploy("checkout", 1000+200) // within window
	if !ok {
		t.Fatalf("expected deployment found within the lookback window")
	}
}

func TestDeployCorrelatorAnnotatesAlert(t *testing.T) {
	c := NewDeployCorrelator(30 * time.Minute)
	c.AddDeployment(DeploymentEvent{Service: "checkout", Time: 1000, Version: "v9"})

	alert := &Alert{
		Labels:      map[string]string{"service": "checkout"},
		Annotations: map[string]string{},
	}
	c.Correlate(alert, 1100)

	cause, ok := alert.Annotations["likely_cause_deploy"]
	if !ok {
		t.Fatalf("expected likely_cause_deploy annotation to be set")
	}
	if !strings.Contains(cause, "v9") {
		t.Fatalf("expected annotation to reference deployed version v9, got %q", cause)
	}
}

func TestDeployCorrelatorNoAnnotationWhenNoServiceLabel(t *testing.T) {
	c := NewDeployCorrelator(30 * time.Minute)
	c.AddDeployment(DeploymentEvent{Service: "checkout", Time: 1000, Version: "v9"})

	alert := &Alert{Labels: map[string]string{}, Annotations: map[string]string{}}
	c.Correlate(alert, 1100)

	if _, ok := alert.Annotations["likely_cause_deploy"]; ok {
		t.Fatalf("expected no annotation when alert has no service label")
	}
}

// TestEngineAttachesDeployCorrelation exercises the full engine tick path:
// a rule fires, and the engine should attach the deploy correlation
// annotation before dispatch.
func TestEngineAttachesDeployCorrelation(t *testing.T) {
	grouper := NewGrouper([]string{"alertname"}, 0, 1)
	notifier := &captureNotifier{}
	router := NewRouter(&Route{Receiver: "r"}, map[string]Notifier{"r": notifier})
	correlator := NewDeployCorrelator(10 * time.Minute)
	eng := NewEngine(grouper, router, nil, correlator)

	eng.AddDeployment(DeploymentEvent{Service: "checkout", Time: 900, Version: "v5"})

	rule := &Rule{
		ID:     "r1",
		Name:   "ErrorSpike",
		For:    0,
		Labels: map[string]string{"alertname": "ErrorSpike", "service": "checkout"},
		Evaluator: func(now int64) (float64, bool, error) {
			return 1, true, nil
		},
	}
	eng.AddRule(rule)

	groups := eng.Evaluate(1000)
	if len(groups) != 1 || len(groups[0].Alerts) != 1 {
		t.Fatalf("expected 1 group with 1 alert, got %+v", groups)
	}
	cause, ok := groups[0].Alerts[0].Annotations["likely_cause_deploy"]
	if !ok {
		t.Fatalf("expected likely_cause_deploy annotation on fired alert")
	}
	if !strings.Contains(cause, "v5") {
		t.Fatalf("expected annotation to mention v5, got %q", cause)
	}
}
