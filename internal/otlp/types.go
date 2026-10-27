// Package otlp implements a native OpenTelemetry Protocol (OTLP) ingestion
// path: a gRPC receiver (collector.{logs,metrics,trace}.v1.*Service/Export)
// and an OTLP/HTTP receiver (protobuf or JSON over POST /v1/{logs,metrics,traces}),
// both decoding onto a small neutral internal representation and handing
// records off to a pluggable Sink.
//
// Decode approach: this package does NOT depend on generated
// go.opentelemetry.io/proto/otlp Go types (that module is not vendored in
// this environment). Instead it hand-decodes the wire-format subset of the
// OTLP ExportLogsServiceRequest / ExportMetricsServiceRequest /
// ExportTraceServiceRequest messages that we care about, using the
// low-level google.golang.org/protobuf/encoding/protowire helpers (field
// tag/varint/length-delimited parsing primitives with no schema/codegen
// dependency). See wire_decode.go.
package otlp

import "time"

// KeyValue is a single resource/log/span attribute.
type KeyValue struct {
	Key   string
	Value any // string, bool, int64, float64, []byte, or nil
}

// Resource carries resource-level attributes common to a batch of
// telemetry (service.name, host, etc).
type Resource struct {
	Attributes []KeyValue
}

// Log is the neutral internal representation of an OTLP LogRecord plus its
// enclosing Resource attributes.
type Log struct {
	Timestamp          time.Time
	ObservedTimestamp  time.Time
	SeverityNumber     int32
	SeverityText       string
	Body               string
	Attributes         []KeyValue
	ResourceAttributes []KeyValue
	TraceID            string // hex-encoded
	SpanID             string // hex-encoded
}

// Metric is the neutral internal representation of a single OTLP metric
// data point (Gauge or Sum are supported; one Metric value per data point).
type Metric struct {
	Name               string
	Description        string
	Unit               string
	Timestamp          time.Time
	Value              float64
	IsMonotonicSum     bool
	Labels             []KeyValue
	ResourceAttributes []KeyValue
}

// Span is the neutral internal representation of an OTLP Span.
type Span struct {
	TraceID            string // hex-encoded
	SpanID             string // hex-encoded
	ParentSpanID       string // hex-encoded
	Name               string
	Kind               int32
	StartTime          time.Time
	EndTime            time.Time
	Duration           time.Duration
	StatusCode         int32
	StatusMessage      string
	Attributes         []KeyValue
	ResourceAttributes []KeyValue
}

// Sink decouples the OTLP receiver from the storage layer. The integrator
// implements this against the real storage/queue.
type Sink interface {
	IngestLog(Log) error
	IngestMetric(Metric) error
	IngestSpan(Span) error
}
