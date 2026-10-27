package otlp

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

// This file implements OTLP/HTTP JSON decoding. It targets the shape
// produced by protojson for the OTLP proto messages: fixed64/int64 fields
// are encoded as JSON strings, bytes fields (trace_id/span_id/bytes_value)
// as base64 strings, and message field names are lowerCamelCase.
//
// It is a convenience alternative to the protobuf body (POST .../v1/logs
// with Content-Type: application/json instead of application/x-protobuf),
// reusing the same Sink path as the protobuf/gRPC decoders.

type jsonAnyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
	BytesValue  *string  `json:"bytesValue,omitempty"`
}

func (v jsonAnyValue) toAny() any {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.BoolValue != nil:
		return *v.BoolValue
	case v.IntValue != nil:
		n, _ := strconv.ParseInt(*v.IntValue, 10, 64)
		return n
	case v.DoubleValue != nil:
		return *v.DoubleValue
	case v.BytesValue != nil:
		b, err := base64.StdEncoding.DecodeString(*v.BytesValue)
		if err != nil {
			return *v.BytesValue
		}
		return b
	}
	return nil
}

type jsonKeyValue struct {
	Key   string       `json:"key"`
	Value jsonAnyValue `json:"value"`
}

func toAttrs(kvs []jsonKeyValue) []KeyValue {
	out := make([]KeyValue, 0, len(kvs))
	for _, kv := range kvs {
		out = append(out, KeyValue{Key: kv.Key, Value: kv.Value.toAny()})
	}
	return out
}

type jsonResource struct {
	Attributes []jsonKeyValue `json:"attributes"`
}

// decodeIDField accepts either base64 (protojson-standard) or hex (common
// in hand-written test payloads / some SDKs) and returns hex, matching the
// convention used by the protobuf decoder (wire_decode.go: hexID).
func decodeIDField(s string) string {
	if s == "" {
		return ""
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && looksLikeID(b) {
		return hex.EncodeToString(b)
	}
	if _, err := hex.DecodeString(s); err == nil {
		return s
	}
	return s
}

// looksLikeID is a light heuristic: OTLP trace ids are 16 bytes, span ids 8
// bytes. Reject decodes that produce some other length, likely meaning the
// base64 decode fired on a hex string by accident.
func looksLikeID(b []byte) bool {
	return len(b) == 16 || len(b) == 8
}

type jsonLogRecord struct {
	TimeUnixNano         string         `json:"timeUnixNano"`
	ObservedTimeUnixNano string         `json:"observedTimeUnixNano"`
	SeverityNumber       int32          `json:"severityNumber"`
	SeverityText         string         `json:"severityText"`
	Body                 jsonAnyValue   `json:"body"`
	Attributes           []jsonKeyValue `json:"attributes"`
	TraceID              string         `json:"traceId"`
	SpanID               string         `json:"spanId"`
}

type jsonScopeLogs struct {
	LogRecords []jsonLogRecord `json:"logRecords"`
}

type jsonResourceLogs struct {
	Resource  jsonResource    `json:"resource"`
	ScopeLogs []jsonScopeLogs `json:"scopeLogs"`
}

type jsonExportLogsServiceRequest struct {
	ResourceLogs []jsonResourceLogs `json:"resourceLogs"`
}

func nanosToTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return fixed64Time(uint64(n))
}

// DecodeExportLogsServiceRequestJSON decodes the JSON form of
// ExportLogsServiceRequest.
func DecodeExportLogsServiceRequestJSON(b []byte) ([]Log, error) {
	var req jsonExportLogsServiceRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	var out []Log
	for _, rl := range req.ResourceLogs {
		resAttrs := toAttrs(rl.Resource.Attributes)
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				body := ""
				if v := lr.Body.toAny(); v != nil {
					if s, ok := v.(string); ok {
						body = s
					}
				}
				out = append(out, Log{
					Timestamp:          nanosToTime(lr.TimeUnixNano),
					ObservedTimestamp:  nanosToTime(lr.ObservedTimeUnixNano),
					SeverityNumber:     lr.SeverityNumber,
					SeverityText:       lr.SeverityText,
					Body:               body,
					Attributes:         toAttrs(lr.Attributes),
					ResourceAttributes: resAttrs,
					TraceID:            decodeIDField(lr.TraceID),
					SpanID:             decodeIDField(lr.SpanID),
				})
			}
		}
	}
	return out, nil
}

type jsonNumberDataPoint struct {
	TimeUnixNano string         `json:"timeUnixNano"`
	AsDouble     *float64       `json:"asDouble,omitempty"`
	AsInt        *string        `json:"asInt,omitempty"`
	Attributes   []jsonKeyValue `json:"attributes"`
}

func (p jsonNumberDataPoint) value() float64 {
	if p.AsDouble != nil {
		return *p.AsDouble
	}
	if p.AsInt != nil {
		n, _ := strconv.ParseInt(*p.AsInt, 10, 64)
		return float64(n)
	}
	return 0
}

type jsonGauge struct {
	DataPoints []jsonNumberDataPoint `json:"dataPoints"`
}

type jsonSum struct {
	DataPoints  []jsonNumberDataPoint `json:"dataPoints"`
	IsMonotonic bool                  `json:"isMonotonic"`
}

type jsonMetric struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Unit        string     `json:"unit"`
	Gauge       *jsonGauge `json:"gauge,omitempty"`
	Sum         *jsonSum   `json:"sum,omitempty"`
}

type jsonScopeMetrics struct {
	Metrics []jsonMetric `json:"metrics"`
}

type jsonResourceMetrics struct {
	Resource     jsonResource       `json:"resource"`
	ScopeMetrics []jsonScopeMetrics `json:"scopeMetrics"`
}

type jsonExportMetricsServiceRequest struct {
	ResourceMetrics []jsonResourceMetrics `json:"resourceMetrics"`
}

// DecodeExportMetricsServiceRequestJSON decodes the JSON form of
// ExportMetricsServiceRequest.
func DecodeExportMetricsServiceRequestJSON(b []byte) ([]Metric, error) {
	var req jsonExportMetricsServiceRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	var out []Metric
	for _, rm := range req.ResourceMetrics {
		resAttrs := toAttrs(rm.Resource.Attributes)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				base := Metric{Name: m.Name, Description: m.Description, Unit: m.Unit, ResourceAttributes: resAttrs}
				if m.Gauge != nil {
					for _, dp := range m.Gauge.DataPoints {
						metric := base
						metric.Timestamp = nanosToTime(dp.TimeUnixNano)
						metric.Value = dp.value()
						metric.Labels = toAttrs(dp.Attributes)
						out = append(out, metric)
					}
				}
				if m.Sum != nil {
					for _, dp := range m.Sum.DataPoints {
						metric := base
						metric.Timestamp = nanosToTime(dp.TimeUnixNano)
						metric.Value = dp.value()
						metric.Labels = toAttrs(dp.Attributes)
						metric.IsMonotonicSum = m.Sum.IsMonotonic
						out = append(out, metric)
					}
				}
			}
		}
	}
	return out, nil
}

type jsonStatus struct {
	Message string `json:"message"`
	Code    int32  `json:"code"`
}

type jsonSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId"`
	Name              string         `json:"name"`
	Kind              int32          `json:"kind"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []jsonKeyValue `json:"attributes"`
	Status            jsonStatus     `json:"status"`
}

type jsonScopeSpans struct {
	Spans []jsonSpan `json:"spans"`
}

type jsonResourceSpans struct {
	Resource   jsonResource     `json:"resource"`
	ScopeSpans []jsonScopeSpans `json:"scopeSpans"`
}

type jsonExportTraceServiceRequest struct {
	ResourceSpans []jsonResourceSpans `json:"resourceSpans"`
}

// DecodeExportTraceServiceRequestJSON decodes the JSON form of
// ExportTraceServiceRequest.
func DecodeExportTraceServiceRequestJSON(b []byte) ([]Span, error) {
	var req jsonExportTraceServiceRequest
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	var out []Span
	for _, rs := range req.ResourceSpans {
		resAttrs := toAttrs(rs.Resource.Attributes)
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				start := nanosToTime(s.StartTimeUnixNano)
				end := nanosToTime(s.EndTimeUnixNano)
				var dur time.Duration
				if !start.IsZero() && !end.IsZero() {
					dur = end.Sub(start)
				}
				out = append(out, Span{
					TraceID:            decodeIDField(s.TraceID),
					SpanID:             decodeIDField(s.SpanID),
					ParentSpanID:       decodeIDField(s.ParentSpanID),
					Name:               s.Name,
					Kind:               s.Kind,
					StartTime:          start,
					EndTime:            end,
					Duration:           dur,
					StatusCode:         s.Status.Code,
					StatusMessage:      s.Status.Message,
					Attributes:         toAttrs(s.Attributes),
					ResourceAttributes: resAttrs,
				})
			}
		}
	}
	return out, nil
}
