// Package ingestgateway implements otlp.Sink by forwarding translated
// domain objects to cmd/aggregator's internal, unauthenticated,
// service-to-service HTTP endpoints (POST /internal/logs, /internal/metrics,
// /internal/traces), rather than publishing directly onto a local
// ingestbuffer.Buffer the way internal/platform's old platformSink did. This
// is the component that lets cmd/ingest-gateway run as its own binary/
// deployable while cmd/aggregator's ingest buffer, stream processor, and
// storage engines stay untouched.
package ingestgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"custom-log-aggregator/internal/domain"
	"custom-log-aggregator/internal/otlp"
	"custom-log-aggregator/internal/util"
)

// ForwardSink implements otlp.Sink. It translates OTLP's neutral
// representation into domain types - exactly like internal/platform's old
// platformSink did (severity mapping, tenant/service attribute extraction,
// ID generation, priority selection is all unchanged) - but instead of
// calling into a local Platform.publish, it POSTs the result to
// AggregatorURL's /internal/{logs,metrics,traces} endpoints. Network errors
// are returned to the OTLP caller (fail closed) rather than dropped.
type ForwardSink struct {
	AggregatorURL string
	Client        *http.Client
}

// NewForwardSink builds a ForwardSink posting to aggregatorURL (e.g.
// "http://aggregator:8080").
func NewForwardSink(aggregatorURL string) *ForwardSink {
	return &ForwardSink{AggregatorURL: aggregatorURL, Client: http.DefaultClient}
}

func attrString(kvs []otlp.KeyValue, key string) string {
	for _, kv := range kvs {
		if kv.Key == key {
			if s, ok := kv.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

func attrsToTags(kvs []otlp.KeyValue) map[string]string {
	if len(kvs) == 0 {
		return nil
	}
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		out[kv.Key] = fmt.Sprint(kv.Value)
	}
	return out
}

// internalLogRequest mirrors internal/api.internalLogRequest - it carries
// priority alongside the domain.LogEntry since priority (derived here from
// OTLP's SeverityNumber) isn't a field on domain.LogEntry itself.
type internalLogRequest struct {
	Entry    domain.LogEntry `json:"entry"`
	Priority string          `json:"priority"`
}

func (s *ForwardSink) IngestLog(l otlp.Log) error {
	tenant := attrString(l.ResourceAttributes, "tenant.id")
	service := attrString(l.ResourceAttributes, "service.name")
	entry := domain.LogEntry{
		ID:       util.NewID("log"),
		Time:     l.Timestamp,
		Service:  service,
		Level:    l.SeverityText,
		Message:  l.Body,
		TraceID:  l.TraceID,
		SpanID:   l.SpanID,
		TenantID: tenant,
		Tags:     attrsToTags(l.Attributes),
	}
	priority := "info"
	if l.SeverityNumber > 0 && l.SeverityNumber < 9 { // OTLP DEBUG range
		priority = "debug"
	}
	return s.post("/internal/logs", internalLogRequest{Entry: entry, Priority: priority})
}

func (s *ForwardSink) IngestMetric(m otlp.Metric) error {
	tenant := attrString(m.ResourceAttributes, "tenant.id")
	service := attrString(m.ResourceAttributes, "service.name")
	point := domain.MetricPoint{
		ID:       util.NewID("metric"),
		Name:     m.Name,
		Time:     m.Timestamp,
		Service:  service,
		Value:    m.Value,
		Unit:     m.Unit,
		TenantID: tenant,
		Tags:     attrsToTags(m.Labels),
	}
	return s.post("/internal/metrics", point)
}

func (s *ForwardSink) IngestSpan(sp otlp.Span) error {
	tenant := attrString(sp.ResourceAttributes, "tenant.id")
	service := attrString(sp.ResourceAttributes, "service.name")
	status := "ok"
	if sp.StatusCode != 0 {
		status = "error"
	}
	entry := domain.SpanEntry{
		TraceID:      sp.TraceID,
		SpanID:       sp.SpanID,
		ParentSpanID: sp.ParentSpanID,
		Service:      service,
		Name:         sp.Name,
		StartTime:    sp.StartTime,
		EndTime:      sp.EndTime,
		Status:       status,
		TenantID:     tenant,
		Tags:         attrsToTags(sp.Attributes),
	}
	return s.post("/internal/traces", entry)
}

// post JSON-encodes body and POSTs it to path on AggregatorURL, failing
// closed: any network error, or non-2xx response, is returned to the
// caller (the OTLP receiver) instead of being logged and dropped.
func (s *ForwardSink) post(path string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("ingestgateway: encode %s: %w", path, err)
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.AggregatorURL+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("ingestgateway: build request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ingestgateway: forward to aggregator %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ingestgateway: aggregator %s returned %s", path, resp.Status)
	}
	return nil
}

var _ otlp.Sink = (*ForwardSink)(nil)
