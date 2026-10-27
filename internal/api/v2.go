package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"custom-log-aggregator/internal/aiops"
	"custom-log-aggregator/internal/coldstore"
	"custom-log-aggregator/internal/domain"
	"custom-log-aggregator/internal/profiling"
	"custom-log-aggregator/internal/querylang/metricsql"
	"custom-log-aggregator/internal/querylang/pipeql"
	"custom-log-aggregator/internal/rbac"
	"custom-log-aggregator/internal/util"
)

// routesV2 registers the "advanced stack" surface: OTLP-fed storage
// engines (tsdb/logsdb), the metricsql/pipeql query languages, alerting
// v2, tracing, semantic search, AIOps, continuous profiling, and cold
// storage archival - everything in Platform. Only mounted when
// cfg.Platform is non-nil (see routes()).
func (s *Server) routesV2() {
	s.mux.Handle("POST /v2/logs", s.v2(rbac.Permission{Action: "logs:write", Resource: "*"}, s.v2IngestLogs))
	s.mux.Handle("POST /v2/metrics", s.v2(rbac.Permission{Action: "metrics:write", Resource: "*"}, s.v2IngestMetrics))
	s.mux.Handle("POST /v2/traces", s.v2(rbac.Permission{Action: "logs:write", Resource: "*"}, s.v2IngestTraces))

	s.mux.Handle("GET /v2/logs/search", s.v2(rbac.Permission{Action: "logs:read", Resource: "*"}, s.v2SearchLogs))
	s.mux.Handle("GET /v2/logs/semantic", s.v2(rbac.Permission{Action: "logs:read", Resource: "*"}, s.v2SemanticSearch))
	s.mux.Handle("GET /v2/metrics/query", s.v2(rbac.Permission{Action: "metrics:read", Resource: "*"}, s.v2QueryMetrics))
	s.mux.Handle("GET /v2/traces/{id}", s.v2(rbac.Permission{Action: "logs:read", Resource: "*"}, s.v2GetTrace))

	s.mux.Handle("GET /v2/alerts/rules", s.v2(rbac.Permission{Action: "alerts:read", Resource: "*"}, s.v2ListAlertRules))
	s.mux.Handle("POST /v2/alerts/rules", s.v2(rbac.Permission{Action: "alerts:write", Resource: "*"}, s.v2CreateAlertRule))
	s.mux.Handle("POST /v2/alerts/silences", s.v2(rbac.Permission{Action: "alerts:write", Resource: "*"}, s.v2CreateSilence))
	s.mux.Handle("POST /v2/deployments", s.v2(rbac.Permission{Action: "alerts:write", Resource: "*"}, s.v2RecordDeployment))

	s.mux.Handle("GET /v2/aiops/rootcause", s.v2(rbac.Permission{Action: "metrics:read", Resource: "*"}, s.v2RootCause))

	s.mux.Handle("POST /v2/profiling/capture", s.v2(rbac.Permission{Action: "admin:*", Resource: "*"}, s.v2CaptureProfile))
	s.mux.Handle("GET /v2/profiling/flamegraph", s.v2(rbac.Permission{Action: "admin:*", Resource: "*"}, s.v2Flamegraph))

	s.mux.Handle("POST /v2/archive/ingest", s.v2(rbac.Permission{Action: "admin:*", Resource: "*"}, s.v2ArchiveIngest))
	s.mux.Handle("GET /v2/archive/query", s.v2(rbac.Permission{Action: "admin:*", Resource: "*"}, s.v2ArchiveQuery))

	s.mux.Handle("POST /v2/auth/token", http.HandlerFunc(s.v2IssueToken)) // unauthenticated: this *is* the login endpoint
	s.mux.Handle("GET /v2/stats", s.v2(rbac.Permission{Action: "metrics:read", Resource: "*"}, s.v2Stats))

	// Internal, service-to-service only: lets cmd/alerting-service's rule
	// Evaluator closures query this aggregator's TSDB over the network
	// (it no longer runs in the same process). Deliberately not RBAC-gated
	// like the public /v2/metrics/query - it's not reachable unless an
	// operator routes to it, same trust boundary as the rest of the
	// internal docker/k8s network.
	s.mux.Handle("POST /internal/metrics/query_range", http.HandlerFunc(s.internalQueryRange))

	// Internal, service-to-service only: the network target cmd/ingest-
	// gateway's forwardSink posts normalized domain objects to, now that
	// the OTLP receiver no longer runs in this process. Same trust
	// boundary/no-RBAC pattern as /internal/metrics/query_range above -
	// not reachable unless an operator routes to it.
	s.mux.Handle("POST /internal/logs", http.HandlerFunc(s.internalIngestLogs))
	s.mux.Handle("POST /internal/metrics", http.HandlerFunc(s.internalIngestMetrics))
	s.mux.Handle("POST /internal/traces", http.HandlerFunc(s.internalIngestTraces))
}

// internalIngestLogs/Metrics/Traces accept a single already-normalized
// domain object (as opposed to /v2/logs etc, which accept a JSON array and
// apply defaulting) and call the exact same Platform.Publish* methods, so
// cmd/ingest-gateway's forwardSink writes through the identical buffer/
// stream-processor pipeline the in-process OTLP sink used to.

// internalLogRequest carries the priority alongside the domain.LogEntry
// because priority (derived by the caller from OTLP's SeverityNumber, not
// carried on domain.LogEntry itself) is part of the OTLP-to-domain
// translation logic that must stay exactly as it was in the old in-process
// platformSink - it must not be recomputed here from Level text.
type internalLogRequest struct {
	Entry    domain.LogEntry `json:"entry"`
	Priority string          `json:"priority"`
}

func (s *Server) internalIngestLogs(w http.ResponseWriter, r *http.Request) {
	var req internalLogRequest
	if err := decodeBody(r.Body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	priority := req.Priority
	if priority == "" {
		priority = "info"
	}
	if err := s.cfg.Platform.PublishLog(req.Entry, priority); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"accepted": 1})
}

func (s *Server) internalIngestMetrics(w http.ResponseWriter, r *http.Request) {
	var point domain.MetricPoint
	if err := decodeBody(r.Body, &point); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.cfg.Platform.PublishMetric(point); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"accepted": 1})
}

func (s *Server) internalIngestTraces(w http.ResponseWriter, r *http.Request) {
	var span domain.SpanEntry
	if err := decodeBody(r.Body, &span); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.cfg.Platform.PublishSpan(span); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"accepted": 1})
}

func (s *Server) internalQueryRange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Matchers map[string]string `json:"matchers"`
		Start    int64             `json:"start"`
		End      int64             `json:"end"`
	}
	if err := decodeBody(r.Body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	series, err := s.cfg.Platform.MetricsDataSource().QueryRange(req.Matchers, req.Start, req.End)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, series)
}

// v2 wraps a v2 handler with RBAC auth+authz when cfg.RBACEnabled, and is
// a no-op passthrough otherwise (dev mode).
func (s *Server) v2(perm rbac.Permission, fn http.HandlerFunc) http.Handler {
	var h http.Handler = fn
	if s.cfg.RBACEnabled {
		h = rbac.RequirePermission(s.cfg.Platform.RBAC, perm)(h)
		h = rbac.Middleware(s.cfg.Platform.RBAC, s.cfg.Platform.RBACSecret)(h)
	}
	return h
}

func (s *Server) v2IngestLogs(w http.ResponseWriter, r *http.Request) {
	var payload []domain.LogEntry
	if err := decodeBody(r.Body, &payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := s.cfg.Platform
	for _, entry := range payload {
		if entry.Time.IsZero() {
			entry.Time = time.Now().UTC()
		}
		if entry.ID == "" {
			entry.ID = util.NewID("log")
		}
		priority := "info"
		if strings.EqualFold(entry.Level, "debug") {
			priority = "debug"
		}
		if err := s.cfg.Platform.PublishLog(entry, priority); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	_ = p
	writeJSON(w, map[string]any{"accepted": len(payload)})
}

func (s *Server) v2IngestMetrics(w http.ResponseWriter, r *http.Request) {
	var payload []domain.MetricPoint
	if err := decodeBody(r.Body, &payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, point := range payload {
		if point.Time.IsZero() {
			point.Time = time.Now().UTC()
		}
		if point.ID == "" {
			point.ID = util.NewID("metric")
		}
		if err := s.cfg.Platform.PublishMetric(point); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	writeJSON(w, map[string]any{"accepted": len(payload)})
}

func (s *Server) v2IngestTraces(w http.ResponseWriter, r *http.Request) {
	var payload []domain.SpanEntry
	if err := decodeBody(r.Body, &payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, span := range payload {
		if err := s.cfg.Platform.PublishSpan(span); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	writeJSON(w, map[string]any{"accepted": len(payload)})
}

func (s *Server) v2SearchLogs(w http.ResponseWriter, r *http.Request) {
	pipeline := r.URL.Query().Get("pipeline")
	if pipeline == "" {
		http.Error(w, "missing ?pipeline=", http.StatusBadRequest)
		return
	}
	start, end := timeRangeParams(r)
	result, err := pipeql.Run(pipeline, s.cfg.Platform.LogSource(), start, end)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (s *Server) v2SemanticSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	k, _ := strconv.Atoi(r.URL.Query().Get("k"))
	if k <= 0 {
		k = 10
	}
	results := s.cfg.Platform.Vector.SemanticSearch(q, k)
	out := make([]map[string]any, 0, len(results))
	for _, res := range results {
		text, _ := s.cfg.Platform.Vector.Text(res.ID)
		out = append(out, map[string]any{"id": res.ID, "score": res.Score, "text": text})
	}
	writeJSON(w, out)
}

func (s *Server) v2QueryMetrics(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	if query == "" {
		http.Error(w, "missing ?query=", http.StatusBadRequest)
		return
	}
	evalTime := time.Now().Unix()
	if t := r.URL.Query().Get("time"); t != "" {
		if parsed, err := strconv.ParseInt(t, 10, 64); err == nil {
			evalTime = parsed
		}
	}
	expr, err := metricsql.Parse(metricsql.Lex(query))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	series, err := metricsql.Eval(expr, s.cfg.Platform.MetricsDataSource(), evalTime)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, series)
}

func (s *Server) v2GetTrace(w http.ResponseWriter, r *http.Request) {
	traceID := r.PathValue("id")
	trace, ok := s.cfg.Platform.GetTrace(traceID)
	if !ok {
		http.Error(w, "trace not found or not yet complete", http.StatusNotFound)
		return
	}
	writeJSON(w, trace)
}

// Rule-based alerting (rules, silences, deployment-correlation) has moved
// to the standalone cmd/alerting-service binary (see internal/alertingsvc)
// - internal/alerting.Engine's grouper/router/silencer/correlator no longer
// run in-process here. These handlers proxy through to alerting-service,
// pointed at by cfg.AlertingServiceURL, preserving the exact request and
// response JSON shapes external callers of /v2/alerts/* and /v2/deployments
// already depend on: the move is transparent to them.

func (s *Server) v2ListAlertRules(w http.ResponseWriter, r *http.Request) {
	s.proxyAlertingService(w, r, http.MethodGet, "/v2/alerts/rules")
}

func (s *Server) v2CreateAlertRule(w http.ResponseWriter, r *http.Request) {
	s.proxyAlertingService(w, r, http.MethodPost, "/v2/alerts/rules")
}

func (s *Server) v2CreateSilence(w http.ResponseWriter, r *http.Request) {
	s.proxyAlertingService(w, r, http.MethodPost, "/v2/alerts/silences")
}

// v2RecordDeployment records a deployment for both the standalone alerting
// service's deploy-correlated root-cause hints (proxied over HTTP, since
// rule-based alerting no longer runs in-process) and this aggregator's own
// AIOps root-cause feed (p.aiopsFeed, an unrelated aggregator-local concern
// that stays exactly as it was). It fails closed: if alerting-service is
// unreachable or errors, no local state is recorded and the same error
// status is returned to the caller rather than silently succeeding.
func (s *Server) v2RecordDeployment(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var evt struct {
		Service string    `json:"service"`
		Version string    `json:"version"`
		Time    time.Time `json:"time"`
	}
	if err := json.Unmarshal(body, &evt); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if evt.Time.IsZero() {
		evt.Time = time.Now().UTC()
	}
	// Re-marshal with the now-defaulted time so alerting-service and the
	// local aiopsFeed record the exact same timestamp.
	normalized, err := json.Marshal(evt)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !s.forwardToAlertingService(w, r, http.MethodPost, "/v2/deployments", normalized) {
		return
	}
	s.cfg.Platform.AddDeploymentEvent(evt.Service, evt.Version, evt.Time)
	writeJSON(w, map[string]any{"ok": true})
}

// proxyAlertingService forwards the incoming request's body and query
// string to alerting-service and copies its response status/body straight
// back, so cmd/aggregator's own /v2/alerts/* handlers stay byte-for-byte
// compatible with what alerting-service returns.
func (s *Server) proxyAlertingService(w http.ResponseWriter, r *http.Request, method, path string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	url := s.cfg.AlertingServiceURL + path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), method, url, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Fail closed: never report success when alerting-service could
		// not be reached.
		http.Error(w, fmt.Sprintf("alerting service unreachable: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// forwardToAlertingService is like proxyAlertingService but for callers
// (v2RecordDeployment) that need to inspect/mutate the payload before
// forwarding and still perform local work afterwards - only on success. It
// reports whether the forwarded call succeeded (2xx); on failure it has
// already written the error response to w and the caller must return
// immediately without touching local state, so the request fails closed.
func (s *Server) forwardToAlertingService(w http.ResponseWriter, r *http.Request, method, path string, body []byte) bool {
	url := s.cfg.AlertingServiceURL + path
	req, err := http.NewRequestWithContext(r.Context(), method, url, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("alerting service unreachable: %v", err), http.StatusBadGateway)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		http.Error(w, string(respBody), resp.StatusCode)
		return false
	}
	io.Copy(io.Discard, resp.Body)
	return true
}

func (s *Server) v2RootCause(w http.ResponseWriter, r *http.Request) {
	window := 10 * time.Minute
	if raw := r.URL.Query().Get("window"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			window = d
		}
	}
	hyps := s.cfg.Platform.RootCauseAnalysis(window)
	if hyps == nil {
		hyps = []aiops.RootCauseHypothesis{}
	}
	writeJSON(w, hyps)
}

func (s *Server) v2CaptureProfile(w http.ResponseWriter, r *http.Request) {
	duration := 5 * time.Second
	if raw := r.URL.Query().Get("duration"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			duration = d
		}
	}
	prof, err := s.cfg.Platform.Profiler.Capture(r.Context(), duration)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"service": prof.Service, "host": prof.Host, "time": prof.Time, "bytes": len(prof.Data)})
}

func (s *Server) v2Flamegraph(w http.ResponseWriter, r *http.Request) {
	t, err := strconv.ParseInt(r.URL.Query().Get("time"), 10, 64)
	if err != nil {
		http.Error(w, "invalid or missing ?time=", http.StatusBadRequest)
		return
	}
	prof, ok := s.cfg.Platform.Profiler.Get(t)
	if !ok {
		http.Error(w, "profile not found", http.StatusNotFound)
		return
	}
	root, err := profiling.BuildFlameGraph(prof.Data, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, root)
}

func (s *Server) v2ArchiveIngest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := decodeBody(r.Body, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	key := fmt.Sprintf("adhoc-%s", util.NewID("batch"))
	if err := s.cfg.Platform.Archive.ArchiveSegment(r.Context(), key, body.Rows); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"archived": len(body.Rows), "key": key})
}

// archiveOpAliases maps the symbolic operators accepted on the wire
// (?predicates=col:>=:100) to the "eq"/"ne"/"gt"/"gte"/"lt"/"lte" strings
// coldstore.Predicate.Op actually compares against.
var archiveOpAliases = map[string]string{
	"=": "eq", "==": "eq", "eq": "eq",
	"!=": "ne", "ne": "ne",
	">": "gt", "gt": "gt",
	">=": "gte", "gte": "gte",
	"<": "lt", "lt": "lt",
	"<=": "lte", "lte": "lte",
}

func (s *Server) v2ArchiveQuery(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	var predicates []coldstore.Predicate
	for _, raw := range strings.Split(r.URL.Query().Get("predicates"), ",") {
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, ":", 3)
		if len(parts) != 3 {
			continue
		}
		op, ok := archiveOpAliases[parts[1]]
		if !ok {
			http.Error(w, fmt.Sprintf("unknown predicate operator %q (want one of =, !=, >, >=, <, <=)", parts[1]), http.StatusBadRequest)
			return
		}
		// Predicate values arrive as strings from the query param; try
		// numeric first since most archived columns are numeric, falling
		// back to the raw string (coldstore.valueMatches type-switches on
		// the row's actual column type, not the predicate's).
		var value any = parts[2]
		if f, err := strconv.ParseFloat(parts[2], 64); err == nil {
			value = f
		}
		predicates = append(predicates, coldstore.Predicate{Column: parts[0], Op: op, Value: value})
	}
	rows, err := s.cfg.Platform.Archive.QueryPushdown(r.Context(), prefix, predicates)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rows)
}

func (s *Server) v2IssueToken(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.RBACEnabled {
		http.Error(w, "RBAC disabled on this deployment (CLA_RBAC_ENABLED=false)", http.StatusNotFound)
		return
	}
	var req struct {
		UserID   string `json:"user_id"`
		TenantID string `json:"tenant_id"`
		TTLSecs  int64  `json:"ttl_seconds"`
	}
	if err := decodeBody(r.Body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.TTLSecs <= 0 {
		req.TTLSecs = 3600
	}
	token, err := rbac.IssueToken(s.cfg.Platform.RBACSecret, req.UserID, req.TenantID, time.Duration(req.TTLSecs)*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"token": token, "expires_in": req.TTLSecs})
}

func (s *Server) v2Stats(w http.ResponseWriter, r *http.Request) {
	p := s.cfg.Platform
	writeJSON(w, map[string]any{
		"tsdb_decoded_blocks":    p.TSDB.DecodedBlocks(),
		"logsdb_segment_stats":   p.LogsDB.CompressionStats(),
		"vector_index_size":      p.Vector.Len(),
		"tracing_pending_traces": p.Traces.Pending(),
	})
}

func timeRangeParams(r *http.Request) (start, end int64) {
	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			start = t.Unix()
		}
	}
	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			end = t.Unix()
		}
	}
	if end == 0 {
		end = time.Now().Unix()
	}
	return start, end
}
