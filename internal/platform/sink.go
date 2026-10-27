package platform

import (
	"context"
	"encoding/json"
	"fmt"

	"custom-log-aggregator/internal/domain"
)

// envelope is the wire format placed on the ingestbuffer: exactly one of
// Log/Metric/Span is set. Both the HTTP v2 ingestion handlers and the OTLP
// receiver funnel through this same shape so there is a single stream
// processor (consumer.go) for every ingestion path, matching the
// architecture's Ingestion -> Buffer -> Stream Processor -> Storage flow.
type envelope struct {
	Kind   string              `json:"kind"` // "log" | "metric" | "span"
	Log    *domain.LogEntry    `json:"log,omitempty"`
	Metric *domain.MetricPoint `json:"metric,omitempty"`
	Span   *domain.SpanEntry   `json:"span,omitempty"`
}

// PublishLog is the HTTP-ingestion-side entry point onto the same
// buffer -> stream-processor pipeline OTLP uses, so both paths write
// through identical parsing/storage/alerting logic.
func (p *Platform) PublishLog(entry domain.LogEntry, priority string) error {
	return p.publish(context.Background(), entry.TenantID, priority, envelope{Kind: "log", Log: &entry})
}

// PublishMetric is the HTTP-ingestion-side entry point for metrics.
func (p *Platform) PublishMetric(point domain.MetricPoint) error {
	return p.publish(context.Background(), point.TenantID, "info", envelope{Kind: "metric", Metric: &point})
}

// PublishSpan is the HTTP-ingestion-side entry point for spans.
func (p *Platform) PublishSpan(span domain.SpanEntry) error {
	return p.publish(context.Background(), span.TenantID, "info", envelope{Kind: "span", Span: &span})
}

// publish serializes env and publishes it to the buffer, shuffle-sharded
// and rate-limited by tenantID.
func (p *Platform) publish(ctx context.Context, tenantID, priority string, env envelope) error {
	if tenantID == "" {
		tenantID = "default"
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("platform: encode envelope: %w", err)
	}
	return p.Buffer.Publish(ctx, tenantID, priority, raw)
}
