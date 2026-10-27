package domain

import "time"

type EntryType string

const (
	EntryLog    EntryType = "log"
	EntryMetric EntryType = "metric"
)

type LogEntry struct {
	ID       string            `json:"id"`
	Time     time.Time         `json:"time"`
	Service  string            `json:"service"`
	Host     string            `json:"host,omitempty"`
	Level    string            `json:"level,omitempty"`
	Message  string            `json:"message"`
	TraceID  string            `json:"trace_id,omitempty"`
	SpanID   string            `json:"span_id,omitempty"`
	TenantID string            `json:"tenant_id,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	Fields   map[string]any    `json:"fields,omitempty"`
	Raw      string            `json:"raw,omitempty"`
}

type MetricPoint struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Time     time.Time         `json:"time"`
	Service  string            `json:"service"`
	Value    float64           `json:"value"`
	Unit     string            `json:"unit,omitempty"`
	TenantID string            `json:"tenant_id,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	Exemplar string            `json:"exemplar,omitempty"`
}

// SpanEntry is a distributed-tracing span as ingested/queried through the
// v2 API. Mirrors internal/tracing.Span but kept independent so domain
// stays the shared wire-format contract instead of a dependency magnet.
type SpanEntry struct {
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Service      string            `json:"service"`
	Name         string            `json:"name"`
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time"`
	Status       string            `json:"status,omitempty"`
	TenantID     string            `json:"tenant_id,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	Attributes   map[string]any    `json:"attributes,omitempty"`
}

type AlertRule struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Metric     string            `json:"metric"`
	Threshold  float64           `json:"threshold"`
	Window     time.Duration     `json:"window"`
	Operator   string            `json:"operator"`
	WebhookURL string            `json:"webhook_url,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
	Enabled    bool              `json:"enabled"`
}

type AlertEvent struct {
	RuleID    string    `json:"rule_id"`
	Metric    string    `json:"metric"`
	Value     float64   `json:"value"`
	Triggered time.Time `json:"triggered"`
	Message   string    `json:"message"`
}
