package otlp

import (
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// field is a single decoded top-level protobuf wire field: its number, wire
// type, and the raw bytes of its value (for BytesType, the length-delimited
// payload; for Varint/Fixed32/Fixed64, the raw fixed-width or varint bytes
// are already consumed into `val` numeric forms via helpers below).
type field struct {
	num protowire.Number
	typ protowire.Type
	raw []byte // for BytesType: the payload. for others: unused, see decode helpers below.
	u64 uint64 // decoded varint/fixed value for Varint/Fixed32/Fixed64 types
}

// walkFields parses `b` as a sequence of top-level protobuf fields and
// invokes fn for each one. It returns an error if the buffer is malformed.
// Unknown/unsupported field numbers are simply reported to fn, which may
// ignore them - this is the whole point of a minimal decoder: we only act
// on the field numbers we understand and silently skip the rest, which
// keeps us forward-compatible with OTLP messages that carry extra fields.
func walkFields(b []byte, fn func(f field) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return fmt.Errorf("otlp: invalid tag: %w", protowire.ParseError(n))
		}
		b = b[n:]
		f := field{num: num, typ: typ}
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return fmt.Errorf("otlp: invalid varint: %w", protowire.ParseError(n))
			}
			f.u64 = v
			b = b[n:]
		case protowire.Fixed32Type:
			v, n := protowire.ConsumeFixed32(b)
			if n < 0 {
				return fmt.Errorf("otlp: invalid fixed32: %w", protowire.ParseError(n))
			}
			f.u64 = uint64(v)
			b = b[n:]
		case protowire.Fixed64Type:
			v, n := protowire.ConsumeFixed64(b)
			if n < 0 {
				return fmt.Errorf("otlp: invalid fixed64: %w", protowire.ParseError(n))
			}
			f.u64 = v
			b = b[n:]
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return fmt.Errorf("otlp: invalid bytes: %w", protowire.ParseError(n))
			}
			f.raw = v
			b = b[n:]
		case protowire.StartGroupType:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return fmt.Errorf("otlp: invalid group: %w", protowire.ParseError(n))
			}
			b = b[n:]
			continue // groups are deprecated/unused in OTLP; skip entirely
		default:
			return fmt.Errorf("otlp: unsupported wire type %d", typ)
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return nil
}

func fixed64Time(nanos uint64) time.Time {
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(nanos)).UTC()
}

// -- KeyValue / AnyValue / Resource / InstrumentationScope -----------------

// decodeAnyValue decodes an OTLP `AnyValue` message into a Go any.
func decodeAnyValue(b []byte) (any, error) {
	var out any
	err := walkFields(b, func(f field) error {
		switch f.num {
		case 1: // string_value
			out = string(f.raw)
		case 2: // bool_value
			out = f.u64 != 0
		case 3: // int_value
			out = int64(f.u64)
		case 4: // double_value
			out = math.Float64frombits(f.u64)
		case 7: // bytes_value
			out = append([]byte(nil), f.raw...)
			// 5 array_value, 6 kvlist_value: nested composites, not needed for
			// the neutral representation - skipped.
		}
		return nil
	})
	return out, err
}

// decodeKeyValue decodes an OTLP `KeyValue` message.
func decodeKeyValue(b []byte) (KeyValue, error) {
	var kv KeyValue
	err := walkFields(b, func(f field) error {
		switch f.num {
		case 1: // key
			kv.Key = string(f.raw)
		case 2: // value (AnyValue)
			v, err := decodeAnyValue(f.raw)
			if err != nil {
				return err
			}
			kv.Value = v
		}
		return nil
	})
	return kv, err
}

// decodeAttributes decodes a repeated KeyValue field's raw bytes, appending
// each entry to attrs.
func decodeAttributes(attrs *[]KeyValue, raw []byte) error {
	kv, err := decodeKeyValue(raw)
	if err != nil {
		return err
	}
	*attrs = append(*attrs, kv)
	return nil
}

// decodeResource decodes an OTLP `Resource` message.
func decodeResource(b []byte) (Resource, error) {
	var r Resource
	err := walkFields(b, func(f field) error {
		if f.num == 1 { // attributes
			return decodeAttributes(&r.Attributes, f.raw)
		}
		return nil
	})
	return r, err
}

func hexID(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return hex.EncodeToString(b)
}

// -- Logs --------------------------------------------------------------

// DecodeExportLogsServiceRequest decodes an ExportLogsServiceRequest message
// and returns each contained LogRecord flattened with its resource
// attributes attached.
func DecodeExportLogsServiceRequest(b []byte) ([]Log, error) {
	var logs []Log
	err := walkFields(b, func(f field) error {
		if f.num != 1 { // resource_logs
			return nil
		}
		return decodeResourceLogs(f.raw, &logs)
	})
	return logs, err
}

func decodeResourceLogs(b []byte, out *[]Log) error {
	var resAttrs []KeyValue
	var scopeLogs [][]byte
	if err := walkFields(b, func(f field) error {
		switch f.num {
		case 1: // resource
			res, err := decodeResource(f.raw)
			if err != nil {
				return err
			}
			resAttrs = res.Attributes
		case 2: // scope_logs
			scopeLogs = append(scopeLogs, f.raw)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, sl := range scopeLogs {
		if err := decodeScopeLogs(sl, resAttrs, out); err != nil {
			return err
		}
	}
	return nil
}

func decodeScopeLogs(b []byte, resAttrs []KeyValue, out *[]Log) error {
	return walkFields(b, func(f field) error {
		if f.num != 2 { // log_records
			return nil
		}
		lr, err := decodeLogRecord(f.raw, resAttrs)
		if err != nil {
			return err
		}
		*out = append(*out, lr)
		return nil
	})
}

func decodeLogRecord(b []byte, resAttrs []KeyValue) (Log, error) {
	lr := Log{ResourceAttributes: resAttrs}
	err := walkFields(b, func(f field) error {
		switch f.num {
		case 1: // time_unix_nano
			lr.Timestamp = fixed64Time(f.u64)
		case 11: // observed_time_unix_nano
			lr.ObservedTimestamp = fixed64Time(f.u64)
		case 2: // severity_number
			lr.SeverityNumber = int32(f.u64)
		case 3: // severity_text
			lr.SeverityText = string(f.raw)
		case 5: // body (AnyValue)
			v, err := decodeAnyValue(f.raw)
			if err != nil {
				return err
			}
			if s, ok := v.(string); ok {
				lr.Body = s
			} else if v != nil {
				lr.Body = fmt.Sprint(v)
			}
		case 6: // attributes
			return decodeAttributes(&lr.Attributes, f.raw)
		case 9: // trace_id
			lr.TraceID = hexID(f.raw)
		case 10: // span_id
			lr.SpanID = hexID(f.raw)
		}
		return nil
	})
	return lr, err
}

// -- Metrics -------------------------------------------------------------

// DecodeExportMetricsServiceRequest decodes an ExportMetricsServiceRequest,
// flattening Gauge and Sum data points into individual Metric values with
// resource attributes attached. Histogram/ExponentialHistogram/Summary
// points are currently skipped (not represented in the neutral Metric type).
func DecodeExportMetricsServiceRequest(b []byte) ([]Metric, error) {
	var out []Metric
	err := walkFields(b, func(f field) error {
		if f.num != 1 { // resource_metrics
			return nil
		}
		return decodeResourceMetrics(f.raw, &out)
	})
	return out, err
}

func decodeResourceMetrics(b []byte, out *[]Metric) error {
	var resAttrs []KeyValue
	var scopeMetrics [][]byte
	if err := walkFields(b, func(f field) error {
		switch f.num {
		case 1:
			res, err := decodeResource(f.raw)
			if err != nil {
				return err
			}
			resAttrs = res.Attributes
		case 2:
			scopeMetrics = append(scopeMetrics, f.raw)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, sm := range scopeMetrics {
		if err := decodeScopeMetrics(sm, resAttrs, out); err != nil {
			return err
		}
	}
	return nil
}

func decodeScopeMetrics(b []byte, resAttrs []KeyValue, out *[]Metric) error {
	return walkFields(b, func(f field) error {
		if f.num != 2 { // metrics
			return nil
		}
		return decodeMetric(f.raw, resAttrs, out)
	})
}

func decodeMetric(b []byte, resAttrs []KeyValue, out *[]Metric) error {
	var name, desc, unit string
	var gaugePoints, sumPoints [][]byte
	var isMonotonic bool
	if err := walkFields(b, func(f field) error {
		switch f.num {
		case 1:
			name = string(f.raw)
		case 2:
			desc = string(f.raw)
		case 3:
			unit = string(f.raw)
		case 5: // gauge
			return walkFields(f.raw, func(gf field) error {
				if gf.num == 1 {
					gaugePoints = append(gaugePoints, gf.raw)
				}
				return nil
			})
		case 7: // sum
			return walkFields(f.raw, func(sf field) error {
				switch sf.num {
				case 1:
					sumPoints = append(sumPoints, sf.raw)
				case 3: // is_monotonic
					isMonotonic = sf.u64 != 0
				}
				return nil
			})
		}
		return nil
	}); err != nil {
		return err
	}
	for _, dp := range gaugePoints {
		m, err := decodeNumberDataPoint(dp, resAttrs)
		if err != nil {
			return err
		}
		m.Name, m.Description, m.Unit = name, desc, unit
		*out = append(*out, m)
	}
	for _, dp := range sumPoints {
		m, err := decodeNumberDataPoint(dp, resAttrs)
		if err != nil {
			return err
		}
		m.Name, m.Description, m.Unit = name, desc, unit
		m.IsMonotonicSum = isMonotonic
		*out = append(*out, m)
	}
	return nil
}

func decodeNumberDataPoint(b []byte, resAttrs []KeyValue) (Metric, error) {
	m := Metric{ResourceAttributes: resAttrs}
	err := walkFields(b, func(f field) error {
		switch f.num {
		case 3: // time_unix_nano
			m.Timestamp = fixed64Time(f.u64)
		case 4: // as_double
			m.Value = math.Float64frombits(f.u64)
		case 6: // as_int (sfixed64)
			m.Value = float64(int64(f.u64))
		case 7: // attributes
			return decodeAttributes(&m.Labels, f.raw)
		}
		return nil
	})
	return m, err
}

// -- Traces ----------------------------------------------------------------

// DecodeExportTraceServiceRequest decodes an ExportTraceServiceRequest into
// flattened Spans with resource attributes attached.
func DecodeExportTraceServiceRequest(b []byte) ([]Span, error) {
	var out []Span
	err := walkFields(b, func(f field) error {
		if f.num != 1 { // resource_spans
			return nil
		}
		return decodeResourceSpans(f.raw, &out)
	})
	return out, err
}

func decodeResourceSpans(b []byte, out *[]Span) error {
	var resAttrs []KeyValue
	var scopeSpans [][]byte
	if err := walkFields(b, func(f field) error {
		switch f.num {
		case 1:
			res, err := decodeResource(f.raw)
			if err != nil {
				return err
			}
			resAttrs = res.Attributes
		case 2:
			scopeSpans = append(scopeSpans, f.raw)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, ss := range scopeSpans {
		if err := decodeScopeSpans(ss, resAttrs, out); err != nil {
			return err
		}
	}
	return nil
}

func decodeScopeSpans(b []byte, resAttrs []KeyValue, out *[]Span) error {
	return walkFields(b, func(f field) error {
		if f.num != 2 { // spans
			return nil
		}
		sp, err := decodeSpan(f.raw, resAttrs)
		if err != nil {
			return err
		}
		*out = append(*out, sp)
		return nil
	})
}

func decodeSpan(b []byte, resAttrs []KeyValue) (Span, error) {
	sp := Span{ResourceAttributes: resAttrs}
	err := walkFields(b, func(f field) error {
		switch f.num {
		case 1: // trace_id
			sp.TraceID = hexID(f.raw)
		case 2: // span_id
			sp.SpanID = hexID(f.raw)
		case 4: // parent_span_id
			sp.ParentSpanID = hexID(f.raw)
		case 5: // name
			sp.Name = string(f.raw)
		case 6: // kind
			sp.Kind = int32(f.u64)
		case 7: // start_time_unix_nano
			sp.StartTime = fixed64Time(f.u64)
		case 8: // end_time_unix_nano
			sp.EndTime = fixed64Time(f.u64)
		case 9: // attributes
			return decodeAttributes(&sp.Attributes, f.raw)
		case 15: // status
			return walkFields(f.raw, func(stf field) error {
				switch stf.num {
				case 2:
					sp.StatusMessage = string(stf.raw)
				case 3:
					sp.StatusCode = int32(stf.u64)
				}
				return nil
			})
		}
		return nil
	})
	if err == nil && !sp.StartTime.IsZero() && !sp.EndTime.IsZero() {
		sp.Duration = sp.EndTime.Sub(sp.StartTime)
	}
	return sp, err
}
