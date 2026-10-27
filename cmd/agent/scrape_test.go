package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const samplePromText = `# HELP http_requests_total The total number of HTTP requests.
# TYPE http_requests_total counter
http_requests_total{method="post",code="200"} 1027
http_requests_total{method="post",code="400"} 3
# TYPE go_gc_duration_seconds summary
go_gc_duration_seconds{quantile="0"} 4.9351e-05
go_gc_duration_seconds{quantile="1"} 0.000188101
go_gc_duration_seconds_sum 0.0356818
go_gc_duration_seconds_count 62

process_cpu_seconds_total 1.23
plain_no_labels_metric 42
`

func TestParsePrometheusText(t *testing.T) {
	samples, err := ParsePrometheusText([]byte(samplePromText))
	if err != nil {
		t.Fatalf("ParsePrometheusText: %v", err)
	}

	if len(samples) != 8 {
		t.Fatalf("expected 8 samples, got %d: %+v", len(samples), samples)
	}

	first := samples[0]
	if first.Name != "http_requests_total" {
		t.Errorf("expected first metric name http_requests_total, got %s", first.Name)
	}
	if first.Value != 1027 {
		t.Errorf("expected value 1027, got %v", first.Value)
	}
	if first.Labels["method"] != "post" || first.Labels["code"] != "200" {
		t.Errorf("unexpected labels: %+v", first.Labels)
	}

	second := samples[1]
	if second.Labels["code"] != "400" {
		t.Errorf("expected second sample code=400, got %+v", second.Labels)
	}

	// Scientific notation value.
	found := false
	for _, s := range samples {
		if s.Name == "go_gc_duration_seconds" && s.Labels["quantile"] == "0" {
			found = true
			if s.Value != 4.9351e-05 {
				t.Errorf("expected scientific notation parsed correctly, got %v", s.Value)
			}
		}
	}
	if !found {
		t.Fatal("expected to find go_gc_duration_seconds{quantile=\"0\"} sample")
	}

	// Plain metric with no labels.
	plainFound := false
	for _, s := range samples {
		if s.Name == "plain_no_labels_metric" {
			plainFound = true
			if s.Value != 42 {
				t.Errorf("expected 42, got %v", s.Value)
			}
			if len(s.Labels) != 0 {
				t.Errorf("expected no labels, got %+v", s.Labels)
			}
		}
	}
	if !plainFound {
		t.Fatal("expected to find plain_no_labels_metric sample")
	}

	// Comments and blank lines must be skipped, not produce samples.
	for _, s := range samples {
		if s.Name == "" {
			t.Errorf("unexpected empty-name sample (comment/blank line leaked through): %+v", s)
		}
	}
}

func TestScrapeURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte("up 1\nfoo_bar{env=\"prod\"} 3.5\n"))
	}))
	defer srv.Close()

	samples, err := ScrapeURL(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("ScrapeURL: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("expected 2 samples, got %d: %+v", len(samples), samples)
	}
	if samples[0].Name != "up" || samples[0].Value != 1 {
		t.Errorf("unexpected first sample: %+v", samples[0])
	}
	if samples[1].Name != "foo_bar" || samples[1].Labels["env"] != "prod" || samples[1].Value != 3.5 {
		t.Errorf("unexpected second sample: %+v", samples[1])
	}
}
