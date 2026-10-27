// Command alerting-service runs rule-based alert evaluation standalone,
// decoupled from cmd/aggregator's ingest/write path: rules, "for"-duration
// gating, Alertmanager-style grouping and routing (Slack/PagerDuty/generic
// webhook), maintenance silences, and deployment-correlated root-cause
// hints (internal/alerting.Engine). It is a periodic read-only consumer of
// cmd/aggregator's TSDB, queried over HTTP via
// POST /internal/metrics/query_range - it does not ingest or store
// anything itself.
//
// Statistical anomaly detection (ObserveMetric/AddAnomalyDetector), which
// feeds the AIOps spike/anomaly feed synchronously from every ingested
// point, is a different, tightly-coupled-by-design concern and stays in
// cmd/aggregator (internal/platform/consumer.go) - it is not part of this
// service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"custom-log-aggregator/internal/alerting"
	"custom-log-aggregator/internal/alertingsvc"
)

func main() {
	addr := env("ALERTING_SERVICE_ADDR", ":8083")
	aggregatorURL := env("AGGREGATOR_URL", "http://localhost:8080")

	// Same grouper/router/silencer/correlator construction as
	// internal/platform/platform.go used to build in-process, before rule-
	// based alerting moved out of cmd/aggregator.
	deployCorrelate := alerting.NewDeployCorrelator(30 * time.Minute)
	grouper := alerting.NewGrouper([]string{"service"}, 30*time.Second, 25)
	root := &alerting.Route{Receiver: "default"}
	receivers := map[string]alerting.Notifier{"default": noopNotifier{}}
	if url := os.Getenv("CLA_SLACK_WEBHOOK_URL"); url != "" {
		receivers["slack"] = alerting.NewSlackNotifier(url)
		root = &alerting.Route{Children: []*alerting.Route{{Receiver: "slack", Continue: true}}}
	}
	if key := os.Getenv("CLA_PAGERDUTY_ROUTING_KEY"); key != "" {
		receivers["pagerduty"] = alerting.NewPagerDutyNotifier(key, "custom-log-aggregator")
		root.Children = append(root.Children, &alerting.Route{
			Matchers: map[string]string{"severity": "critical"},
			Receiver: "pagerduty",
		})
	}
	router := alerting.NewRouter(root, receivers)
	engine := alerting.NewEngine(grouper, router, alerting.NewSilencer(), deployCorrelate)

	srv := alertingsvc.NewServer(engine, aggregatorURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	alertingsvc.StartTicker(ctx, engine, 15*time.Second)

	log.Printf("alerting-service listening on %s (querying aggregator TSDB at %s)", addr, aggregatorURL)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}

// noopNotifier is the default receiver when no Slack/PagerDuty webhook is
// configured: it logs instead of silently dropping notifications.
type noopNotifier struct{}

func (noopNotifier) Notify(_ context.Context, g alerting.NotifiedGroup) error {
	log.Printf("alert (no route configured): %s", g.Summary())
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
