package otlp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// httpServer wraps the net/http.Server used to serve OTLP/HTTP.
type httpServer struct {
	srv *http.Server
}

func (h *httpServer) stop() {
	if h.srv != nil {
		_ = h.srv.Shutdown(context.Background())
	}
}

// StartHTTP starts the OTLP/HTTP receiver, listening on addr and serving
// POST /v1/logs, /v1/metrics, /v1/traces. It accepts either
// Content-Type: application/x-protobuf (or application/protobuf) - the
// standard OTLP protobuf-over-HTTP encoding - or application/json. It
// blocks until the server stops (via r.Stop / ctx-based Shutdown) or fails.
func (r *Receiver) StartHTTP(addr string, sink Sink) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/logs", otlpHTTPHandler(sink, DecodeExportLogsServiceRequest, DecodeExportLogsServiceRequestJSON, ingestLogs))
	mux.HandleFunc("/v1/metrics", otlpHTTPHandler(sink, DecodeExportMetricsServiceRequest, DecodeExportMetricsServiceRequestJSON, ingestMetrics))
	mux.HandleFunc("/v1/traces", otlpHTTPHandler(sink, DecodeExportTraceServiceRequest, DecodeExportTraceServiceRequestJSON, ingestSpans))

	r.httpServer = &httpServer{srv: &http.Server{Addr: addr, Handler: mux}}
	err := r.httpServer.srv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func ingestLogs(sink Sink, logs []Log) error {
	for _, l := range logs {
		if err := sink.IngestLog(l); err != nil {
			return err
		}
	}
	return nil
}

func ingestMetrics(sink Sink, metrics []Metric) error {
	for _, m := range metrics {
		if err := sink.IngestMetric(m); err != nil {
			return err
		}
	}
	return nil
}

func ingestSpans(sink Sink, spans []Span) error {
	for _, s := range spans {
		if err := sink.IngestSpan(s); err != nil {
			return err
		}
	}
	return nil
}

// otlpHTTPHandler builds an http.HandlerFunc that decodes the request body
// with decodePB (application/x-protobuf, application/protobuf, or no
// Content-Type, per the OTLP/HTTP spec's protobuf default) or decodeJSON
// (application/json), then applies ingest to hand records to sink.
func otlpHTTPHandler[T any](sink Sink, decodePB func([]byte) (T, error), decodeJSON func([]byte) (T, error), ingest func(Sink, T) error) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, 64<<20))
		if err != nil {
			http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
			return
		}
		defer req.Body.Close()

		ct := req.Header.Get("Content-Type")
		var recs T
		if strings.Contains(ct, "json") {
			recs, err = decodeJSON(body)
		} else {
			// application/x-protobuf, application/protobuf, or unset:
			// OTLP/HTTP defaults to protobuf.
			recs, err = decodePB(body)
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("decode: %v", err), http.StatusBadRequest)
			return
		}
		if err := ingest(sink, recs); err != nil {
			http.Error(w, fmt.Sprintf("ingest: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		if strings.Contains(ct, "json") {
			_, _ = w.Write([]byte("{}"))
		}
		// protobuf response body: empty ExportXServiceResponse serializes
		// to zero bytes, so nothing further to write.
	}
}
