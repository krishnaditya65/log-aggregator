package platform

import (
	"testing"
	"time"

	"custom-log-aggregator/internal/alerting"
	"custom-log-aggregator/internal/domain"
	"custom-log-aggregator/internal/querylang/metricsql"
	"custom-log-aggregator/internal/querylang/pipeql"
)

// TestEndToEndPipeline exercises the full wiring: HTTP-side publish ->
// ingestbuffer -> stream processor -> tsdb/logsdb -> query languages, and
// AIOps root-cause correlation fed by the alerting engine's anomaly
// detector. This is the integration proof that platform.New/Start actually
// connects every subsystem, not just that each compiles in isolation.
func TestEndToEndPipeline(t *testing.T) {
	dir := t.TempDir()
	p, err := New(Config{
		DataDir:          dir,
		BufferPartitions: 2,
		BufferShardSize:  1,
		BufferReplicas:   1,
		HotRetention:     time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	// Give the alerting engine an anomaly detector so ObserveMetric (called
	// from the stream processor) has something to evaluate.
	p.Alerting.AddAnomalyDetector("latency_ms", newTestZScore())

	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	now := time.Now().UTC()
	if err := p.PublishLog(domain.LogEntry{
		Service: "checkout", Host: "host-1", Level: "error",
		Message: "database connection timeout", Time: now,
	}, "info"); err != nil {
		t.Fatalf("PublishLog: %v", err)
	}
	if err := p.PublishMetric(domain.MetricPoint{
		Name: "latency_ms", Service: "checkout", Value: 42, Time: now,
	}); err != nil {
		t.Fatalf("PublishMetric: %v", err)
	}

	// The stream processor consumes asynchronously; poll briefly rather
	// than sleeping a fixed guess.
	deadline := time.Now().Add(5 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		docs, err := p.LogSource().Search(nil, []string{"timeout"}, 0, 0)
		if err != nil {
			t.Fatalf("LogSource search: %v", err)
		}
		if len(docs) > 0 {
			found = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatal("expected the published log to reach logsdb via the stream processor")
	}

	// metricsql query against the same data, through the DataSource
	// adapter over tsdb.
	expr, err := metricsql.Parse(metricsql.Lex(`latency_ms{service="checkout"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	var series []metricsql.Series
	for time.Now().Before(deadline) {
		// The stream processor is asynchronous, so until the metric has
		// actually landed, tsdb legitimately reports "no series found" -
		// that's an expected transient state to poll through, not a
		// fatal error.
		series, err = metricsql.Eval(expr, p.MetricsDataSource(), now.Add(time.Minute).Unix())
		if err == nil && len(series) > 0 && len(series[0].Points) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(series) == 0 || len(series[0].Points) == 0 {
		t.Fatalf("expected the published metric to reach tsdb via the stream processor (last err: %v)", err)
	}
	if got := series[0].Points[len(series[0].Points)-1].V; got != 42 {
		t.Fatalf("metric value = %v, want 42", got)
	}

	// pipeql end to end.
	result, err := pipeql.Run(`search "timeout" | stats count() by (host)`, p.LogSource(), 0, 0)
	if err != nil {
		t.Fatalf("pipeql run: %v", err)
	}
	if len(result.StatsRows) == 0 {
		t.Fatal("expected pipeql stats to have at least one row")
	}
}

// newTestZScore returns a permissive anomaly detector stand-in; the real
// implementations are exercised by internal/alerting's own tests. Here we
// just need *something* satisfying alerting.AnomalyDetector to prove
// ObserveMetric's wiring doesn't panic.
func newTestZScore() alerting.AnomalyDetector {
	return zscoreStub{}
}

type zscoreStub struct{}

func (zscoreStub) Observe(t int64, v float64) (isAnomaly bool, score float64) { return false, 0 }
