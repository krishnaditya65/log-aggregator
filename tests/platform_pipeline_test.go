package tests

import (
	"bytes"
	"context"
	"testing"
	"time"

	"custom-log-aggregator/internal/coldstore"
	"custom-log-aggregator/internal/domain"
	"custom-log-aggregator/internal/platform"
)

func TestPlatformPublishesLogsAndMetricsThroughTheLocalPipeline(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	p, err := platform.New(platform.Config{
		DataDir:          t.TempDir(),
		BufferPartitions: 2,
		BufferShardSize:  1,
		BufferReplicas:   1,
		HotRetention:     time.Hour,
	})
	if err != nil {
		t.Fatalf("platform.New: %v", err)
	}
	defer p.Close()

	if err := p.Start(); err != nil {
		t.Fatalf("platform.Start: %v", err)
	}

	if err := p.PublishLog(domain.LogEntry{
		ID:       "log-integration-1",
		Time:     now,
		Service:  "checkout",
		Host:     "node-a",
		Level:    "error",
		Message:  "database connection timeout",
		TenantID: "tenant-a",
		Tags:     map[string]string{"environment": "test"},
	}, "critical"); err != nil {
		t.Fatalf("PublishLog: %v", err)
	}
	if err := p.PublishMetric(domain.MetricPoint{
		ID:       "metric-integration-1",
		Name:     "checkout_latency_ms",
		Time:     now,
		Service:  "checkout",
		Value:    42.5,
		TenantID: "tenant-a",
		Tags:     map[string]string{"environment": "test"},
	}); err != nil {
		t.Fatalf("PublishMetric: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		logs, logErr := p.LogSource().Search(map[string]string{"environment": "test"}, []string{"database connection timeout"}, 0, 0)
		series, metricErr := p.MetricsDataSource().QueryRange(
			map[string]string{"__name__": "checkout_latency_ms", "service": "checkout"},
			now.Add(-time.Minute).Unix(),
			now.Add(time.Minute).Unix(),
		)
		if logErr != nil {
			t.Fatalf("log query error: %v", logErr)
		}
		// Before the consumer processes the metric, QueryRange returns the
		// store's expected no-series error. Keep polling through that state.
		if metricErr == nil && len(logs) == 1 && logs[0].Message == "database connection timeout" && len(series) == 1 && len(series[0].Points) == 1 && series[0].Points[0].V == 42.5 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("published log and metric did not reach their local query stores before timeout")
}

func TestLocalColdstoreRoundTripAndPrefixIsolation(t *testing.T) {
	store, err := coldstore.DefaultBlobstore(t.TempDir())
	if err != nil {
		t.Fatalf("DefaultBlobstore: %v", err)
	}
	ctx := context.Background()

	if err := store.Put(ctx, "tenant-a/segment-1", bytes.NewReader([]byte("segment-a"))); err != nil {
		t.Fatalf("Put tenant-a: %v", err)
	}
	if err := store.Put(ctx, "tenant-b/segment-1", bytes.NewReader([]byte("segment-b"))); err != nil {
		t.Fatalf("Put tenant-b: %v", err)
	}

	reader, err := store.Get(ctx, "tenant-a/segment-1")
	if err != nil {
		t.Fatalf("Get tenant-a: %v", err)
	}
	data := make([]byte, len("segment-a"))
	if _, err := reader.Read(data); err != nil {
		reader.Close()
		t.Fatalf("Read tenant-a: %v", err)
	}
	reader.Close()
	if string(data) != "segment-a" {
		t.Fatalf("round-trip data = %q, want %q", data, "segment-a")
	}

	keys, err := store.List(ctx, "tenant-a/")
	if err != nil {
		t.Fatalf("List tenant-a: %v", err)
	}
	if len(keys) != 1 || keys[0] != "tenant-a/segment-1" {
		t.Fatalf("tenant-a prefix returned %v", keys)
	}
	if _, err := store.Get(ctx, "../../definitely-missing/segment-1"); err == nil {
		t.Fatal("expected missing sanitized path to return an error")
	}
}
