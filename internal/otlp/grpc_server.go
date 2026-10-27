package otlp

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/encoding"
	"google.golang.org/grpc/status"
)

// Full gRPC method names per the OTLP spec
// (opentelemetry-proto/opentelemetry/proto/collector/{logs,metrics,trace}/v1/*.proto).
const (
	logsServiceExportMethod    = "/opentelemetry.proto.collector.logs.v1.LogsService/Export"
	metricsServiceExportMethod = "/opentelemetry.proto.collector.metrics.v1.MetricsService/Export"
	traceServiceExportMethod   = "/opentelemetry.proto.collector.trace.v1.TraceService/Export"
)

// rawMessage is a pass-through payload used with rawCodec below: it lets us
// run a standard *grpc.Server without depending on any generated
// (*.pb.go) proto message types. The server hands us the exact bytes off
// the wire and we decode them ourselves via wire_decode.go.
type rawMessage struct{ data []byte }

// rawCodec implements google.golang.org/grpc/encoding.Codec by doing an
// identity pass-through of raw bytes instead of marshaling/unmarshaling a
// proto.Message. It is installed per-server via grpc.ForceServerCodec, so
// it does NOT need to be (and must not be) registered globally - that would
// clobber the real "proto" codec used by any other gRPC servers/clients
// running in the same process.
type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) {
	m, ok := v.(*rawMessage)
	if !ok {
		return nil, fmt.Errorf("otlp: rawCodec.Marshal: unexpected type %T", v)
	}
	return m.data, nil
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	m, ok := v.(*rawMessage)
	if !ok {
		return fmt.Errorf("otlp: rawCodec.Unmarshal: unexpected type %T", v)
	}
	m.data = append([]byte(nil), data...)
	return nil
}

func (rawCodec) Name() string { return "proto" }

var _ encoding.Codec = rawCodec{}

// Receiver hosts the OTLP gRPC Export services (logs/metrics/traces) and,
// optionally, the OTLP/HTTP receiver (see http_server.go).
type Receiver struct {
	grpcServer *grpc.Server
	httpServer *httpServer
}

// NewReceiver constructs an (unstarted) Receiver.
func NewReceiver() *Receiver {
	return &Receiver{}
}

// Start starts the OTLP gRPC receiver, listening on addr, dispatching
// decoded records to sink. It blocks until the listener fails or the
// server is stopped via Stop.
func (r *Receiver) Start(addr string, sink Sink) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("otlp: listen %s: %w", addr, err)
	}
	r.grpcServer = grpc.NewServer(grpc.ForceServerCodec(rawCodec{}))
	desc := &grpc.ServiceDesc{
		ServiceName: "opentelemetry.proto.collector.logs.v1.LogsService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "Export", Handler: exportHandler(sink, decodeAndIngestLogs)},
		},
		Streams:  []grpc.StreamDesc{},
		Metadata: "logs.proto",
	}
	metricsDesc := &grpc.ServiceDesc{
		ServiceName: "opentelemetry.proto.collector.metrics.v1.MetricsService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "Export", Handler: exportHandler(sink, decodeAndIngestMetrics)},
		},
		Metadata: "metrics.proto",
	}
	traceDesc := &grpc.ServiceDesc{
		ServiceName: "opentelemetry.proto.collector.trace.v1.TraceService",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "Export", Handler: exportHandler(sink, decodeAndIngestSpans)},
		},
		Metadata: "trace.proto",
	}
	r.grpcServer.RegisterService(desc, struct{}{})
	r.grpcServer.RegisterService(metricsDesc, struct{}{})
	r.grpcServer.RegisterService(traceDesc, struct{}{})

	return r.grpcServer.Serve(lis)
}

// Stop gracefully stops the gRPC receiver (and the HTTP receiver, if
// started via StartHTTP).
func (r *Receiver) Stop() {
	if r.grpcServer != nil {
		r.grpcServer.GracefulStop()
	}
	if r.httpServer != nil {
		r.httpServer.stop()
	}
}

// exportHandler builds a grpc.MethodHandler that decodes the raw request
// bytes with decode, ingests each record into sink, and replies with an
// empty (zero-byte) OTLP *ExportXServiceResponse, which is a valid empty
// proto3 message and signals success to any standards-compliant OTLP
// exporter.
func exportHandler(sink Sink, decode func(sink Sink, body []byte) error) grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := new(rawMessage)
		if err := dec(in); err != nil {
			return nil, err
		}
		handle := func(ctx context.Context, req any) (any, error) {
			raw := req.(*rawMessage)
			if err := decode(sink, raw.data); err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "otlp: decode: %v", err)
			}
			return &rawMessage{data: nil}, nil
		}
		if interceptor == nil {
			return handle(ctx, in)
		}
		info := &grpc.UnaryServerInfo{FullMethod: logsServiceExportMethod}
		return interceptor(ctx, in, info, handle)
	}
}

func decodeAndIngestLogs(sink Sink, body []byte) error {
	logs, err := DecodeExportLogsServiceRequest(body)
	if err != nil {
		return err
	}
	for _, l := range logs {
		if err := sink.IngestLog(l); err != nil {
			return err
		}
	}
	return nil
}

func decodeAndIngestMetrics(sink Sink, body []byte) error {
	metrics, err := DecodeExportMetricsServiceRequest(body)
	if err != nil {
		return err
	}
	for _, m := range metrics {
		if err := sink.IngestMetric(m); err != nil {
			return err
		}
	}
	return nil
}

func decodeAndIngestSpans(sink Sink, body []byte) error {
	spans, err := DecodeExportTraceServiceRequest(body)
	if err != nil {
		return err
	}
	for _, s := range spans {
		if err := sink.IngestSpan(s); err != nil {
			return err
		}
	}
	return nil
}
