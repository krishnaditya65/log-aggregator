package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protowire"
)

// -- test helpers: minimal hand-rolled protobuf encoders mirroring the
// decoders in wire_decode.go, used to build synthetic OTLP request bytes
// without depending on generated proto types. --

func appendTag(b []byte, num protowire.Number, typ protowire.Type) []byte {
	return protowire.AppendTag(b, num, typ)
}

func appendString(b []byte, num protowire.Number, s string) []byte {
	b = appendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, []byte(s))
}

func appendBytesField(b []byte, num protowire.Number, v []byte) []byte {
	b = appendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func appendVarint(b []byte, num protowire.Number, v uint64) []byte {
	b = appendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendFixed64(b []byte, num protowire.Number, v uint64) []byte {
	b = appendTag(b, num, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, v)
}

func appendMessage(b []byte, num protowire.Number, msg []byte) []byte {
	b = appendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, msg)
}

func encodeKeyValue(key string, strVal string) []byte {
	var av []byte
	av = appendString(av, 1, strVal) // AnyValue.string_value
	var kv []byte
	kv = appendString(kv, 1, key)
	kv = appendMessage(kv, 2, av)
	return kv
}

func encodeLogRecord(nanos uint64, sevText, body string, traceID, spanID []byte) []byte {
	var lr []byte
	lr = appendFixed64(lr, 1, nanos)
	lr = appendVarint(lr, 2, 9) // severity_number = INFO
	lr = appendString(lr, 3, sevText)
	var av []byte
	av = appendString(av, 1, body)
	lr = appendMessage(lr, 5, av)
	lr = appendMessage(lr, 6, encodeKeyValue("k1", "v1"))
	if traceID != nil {
		lr = appendBytesField(lr, 9, traceID)
	}
	if spanID != nil {
		lr = appendBytesField(lr, 10, spanID)
	}
	return lr
}

func encodeExportLogsRequest(nanos uint64) []byte {
	// Resource{attributes: [service.name=test-svc]}
	resourceMsg := appendMessage(nil, 1, encodeKeyValue("service.name", "test-svc"))
	// ScopeLogs{log_records: [...]}
	scopeLogsMsg := appendMessage(nil, 2, encodeLogRecord(nanos, "INFO", "hello world", bytes.Repeat([]byte{0xAB}, 16), bytes.Repeat([]byte{0xCD}, 8)))
	// ResourceLogs{resource: resourceMsg, scope_logs: [scopeLogsMsg]}
	var resourceLogs []byte
	resourceLogs = appendMessage(resourceLogs, 1, resourceMsg)
	resourceLogs = appendMessage(resourceLogs, 2, scopeLogsMsg)
	// ExportLogsServiceRequest{resource_logs: [resourceLogs]}
	return appendMessage(nil, 1, resourceLogs)
}

func TestDecodeExportLogsServiceRequest_RoundTrip(t *testing.T) {
	nanos := uint64(time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC).UnixNano())
	req := encodeExportLogsRequest(nanos)

	logs, err := DecodeExportLogsServiceRequest(req)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log record, got %d", len(logs))
	}
	l := logs[0]
	if l.Body != "hello world" {
		t.Errorf("body = %q, want %q", l.Body, "hello world")
	}
	if l.SeverityText != "INFO" {
		t.Errorf("severity text = %q", l.SeverityText)
	}
	if l.SeverityNumber != 9 {
		t.Errorf("severity number = %d, want 9", l.SeverityNumber)
	}
	wantTrace := "abababababababababababababababab"[:32]
	if l.TraceID != wantTrace {
		t.Errorf("trace id = %q, want %q", l.TraceID, wantTrace)
	}
	if l.SpanID != "cdcdcdcdcdcdcdcd" {
		t.Errorf("span id = %q", l.SpanID)
	}
	if !l.Timestamp.Equal(time.Unix(0, int64(nanos)).UTC()) {
		t.Errorf("timestamp = %v", l.Timestamp)
	}
	if len(l.ResourceAttributes) != 1 || l.ResourceAttributes[0].Key != "service.name" || l.ResourceAttributes[0].Value != "test-svc" {
		t.Errorf("resource attrs = %+v", l.ResourceAttributes)
	}
	if len(l.Attributes) != 1 || l.Attributes[0].Key != "k1" || l.Attributes[0].Value != "v1" {
		t.Errorf("attrs = %+v", l.Attributes)
	}
}

// -- Metrics round trip --

func encodeNumberDataPoint(nanos uint64, val float64) []byte {
	var dp []byte
	dp = appendFixed64(dp, 3, nanos)                 // time_unix_nano
	dp = appendFixed64(dp, 4, math.Float64bits(val)) // as_double
	dp = appendMessage(dp, 7, encodeKeyValue("region", "us"))
	return dp
}

func encodeExportMetricsRequest(nanos uint64, val float64) []byte {
	gauge := appendMessage(nil, 1, encodeNumberDataPoint(nanos, val))
	var metric []byte
	metric = appendString(metric, 1, "cpu.util")
	metric = appendMessage(metric, 5, gauge)
	scopeMetrics := appendMessage(nil, 2, metric)
	resourceMetrics := appendMessage(nil, 2, scopeMetrics)
	return appendMessage(nil, 1, resourceMetrics)
}

func TestDecodeExportMetricsServiceRequest_RoundTrip(t *testing.T) {
	nanos := uint64(time.Now().UnixNano())
	req := encodeExportMetricsRequest(nanos, 42.5)
	metrics, err := DecodeExportMetricsServiceRequest(req)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(metrics))
	}
	m := metrics[0]
	if m.Name != "cpu.util" {
		t.Errorf("name = %q", m.Name)
	}
	if m.Value != 42.5 {
		t.Errorf("value = %v, want 42.5", m.Value)
	}
	if len(m.Labels) != 1 || m.Labels[0].Key != "region" {
		t.Errorf("labels = %+v", m.Labels)
	}
}

// -- Traces round trip --

func encodeSpan(traceID, spanID []byte, name string, start, end uint64) []byte {
	var sp []byte
	sp = appendBytesField(sp, 1, traceID)
	sp = appendBytesField(sp, 2, spanID)
	sp = appendString(sp, 5, name)
	sp = appendFixed64(sp, 7, start)
	sp = appendFixed64(sp, 8, end)
	var status []byte
	status = appendString(status, 2, "OK")
	status = appendVarint(status, 3, 1)
	sp = appendMessage(sp, 15, status)
	return sp
}

func encodeExportTraceRequest(traceID, spanID []byte, start, end uint64) []byte {
	scopeSpans := appendMessage(nil, 2, encodeSpan(traceID, spanID, "GET /", start, end))
	resourceSpans := appendMessage(nil, 2, scopeSpans)
	return appendMessage(nil, 1, resourceSpans)
}

func TestDecodeExportTraceServiceRequest_RoundTrip(t *testing.T) {
	traceID := bytes.Repeat([]byte{0x11}, 16)
	spanID := bytes.Repeat([]byte{0x22}, 8)
	start := uint64(1000)
	end := uint64(2500000) // 2.5ms later, in nanos
	req := encodeExportTraceRequest(traceID, spanID, start, end)

	spans, err := DecodeExportTraceServiceRequest(req)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	s := spans[0]
	if s.Name != "GET /" {
		t.Errorf("name = %q", s.Name)
	}
	if s.StatusCode != 1 || s.StatusMessage != "OK" {
		t.Errorf("status = %d/%q", s.StatusCode, s.StatusMessage)
	}
	if s.Duration != time.Duration(end-start) {
		t.Errorf("duration = %v, want %v", s.Duration, time.Duration(end-start))
	}
}

// -- JSON decode --

func TestDecodeExportLogsServiceRequestJSON(t *testing.T) {
	payload := map[string]any{
		"resourceLogs": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{"key": "service.name", "value": map[string]any{"stringValue": "svc"}},
					},
				},
				"scopeLogs": []map[string]any{
					{
						"logRecords": []map[string]any{
							{
								"timeUnixNano":   "1700000000000000000",
								"severityText":   "ERROR",
								"severityNumber": 17,
								"body":           map[string]any{"stringValue": "boom"},
							},
						},
					},
				},
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := DecodeExportLogsServiceRequestJSON(b)
	if err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if logs[0].Body != "boom" || logs[0].SeverityText != "ERROR" {
		t.Errorf("unexpected log: %+v", logs[0])
	}
	if len(logs[0].ResourceAttributes) != 1 || logs[0].ResourceAttributes[0].Value != "svc" {
		t.Errorf("resource attrs = %+v", logs[0].ResourceAttributes)
	}
}

// -- gRPC receiver end-to-end --

type memSink struct {
	mu      sync.Mutex
	logs    []Log
	metrics []Metric
	spans   []Span
}

func (s *memSink) IngestLog(l Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, l)
	return nil
}
func (s *memSink) IngestMetric(m Metric) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = append(s.metrics, m)
	return nil
}
func (s *memSink) IngestSpan(sp Span) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spans = append(s.spans, sp)
	return nil
}

func TestGRPCReceiver_ExportLogs(t *testing.T) {
	sink := &memSink{}
	r := NewReceiver()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()

	go func() {
		_ = r.Start(addr, sink)
	}()
	defer r.Stop()

	var conn *grpc.ClientConn
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := encodeExportLogsRequest(uint64(time.Now().UnixNano()))
	var reply rawMessage
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Retry the RPC briefly: the listener above may not have handed off to
	// the grpc.Server goroutine yet.
	deadline = time.Now().Add(5 * time.Second)
	for {
		err = conn.Invoke(ctx, logsServiceExportMethod, &rawMessage{data: req}, &reply, grpc.ForceCodec(rawCodec{}))
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Export RPC: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.logs) != 1 {
		t.Fatalf("expected 1 log ingested via gRPC, got %d", len(sink.logs))
	}
	if sink.logs[0].Body != "hello world" {
		t.Errorf("body = %q", sink.logs[0].Body)
	}
}

// -- OTLP/HTTP receiver end-to-end --

func TestHTTPReceiver_ExportLogsProtobuf(t *testing.T) {
	sink := &memSink{}
	r := NewReceiver()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	lis.Close()

	go func() { _ = r.StartHTTP(addr, sink) }()
	defer r.Stop()

	url := "http://" + addr + "/v1/logs"
	body := encodeExportLogsRequest(uint64(time.Now().UnixNano()))

	var resp *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err = http.Post(url, "application/x-protobuf", bytes.NewReader(body))
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.logs) != 1 {
		t.Fatalf("expected 1 log ingested via HTTP, got %d", len(sink.logs))
	}
}
