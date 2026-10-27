package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"custom-log-aggregator/internal/alerts"
	"custom-log-aggregator/internal/domain"
	"custom-log-aggregator/internal/platform"
	"custom-log-aggregator/internal/queue"
	"custom-log-aggregator/internal/storage"
	"custom-log-aggregator/internal/util"
)

type Config struct {
	Addr            string
	Queue           *queue.FileQueue
	Store           storage.Store
	Alerts          *alerts.Service
	DashboardOrigin string

	// Platform, if set, enables the v2 API surface (advanced storage
	// engines, query languages, RBAC, alerting v2, tracing, AIOps,
	// vector search, profiling, cold storage). Nil disables all /v2/*
	// routes so the server can still run in "basic" mode.
	Platform *platform.Platform

	// RBACEnabled gates whether /v2/* routes require an Authorization
	// bearer token. When false (the default, matching the out-of-the-box
	// docker-compose experience), v2 routes are open.
	RBACEnabled bool

	// AlertingServiceURL is the base URL of the standalone
	// cmd/alerting-service binary that now owns rule-based alerting
	// (rules, silences, deployment-correlation). /v2/alerts/* and
	// /v2/deployments proxy to it over HTTP instead of calling
	// Platform.Alerting directly. Required for those routes to work; if
	// unset, calls to them fail closed with an error instead of silently
	// succeeding.
	AlertingServiceURL string
}

type Server struct {
	cfg Config
	mux *http.ServeMux
}

func NewServer(cfg Config) *http.Server {
	s := &Server{cfg: cfg, mux: http.NewServeMux()}
	s.routes()
	return &http.Server{
		Addr:    cfg.Addr,
		Handler: s.cors(s.mux),
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	s.mux.HandleFunc("/v1/ingest/logs", s.ingestLogs)
	s.mux.HandleFunc("/v1/ingest/metrics", s.ingestMetrics)
	s.mux.HandleFunc("/v1/logs", s.searchLogs)
	s.mux.HandleFunc("/v1/metrics", s.getMetrics)
	s.mux.HandleFunc("/v1/alerts", s.alertsHandler)
	s.mux.HandleFunc("/v1/stats", s.stats)

	if s.cfg.Platform != nil {
		s.routesV2()
	}
}

func (s *Server) ListenAndServe() error {
	return (&http.Server{Addr: s.cfg.Addr, Handler: s.cors(s.mux)}).ListenAndServe()
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := s.cfg.DashboardOrigin
		if origin == "" {
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ingestLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload []domain.LogEntry
	if err := decodeBody(r.Body, &payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, entry := range payload {
		if entry.Time.IsZero() {
			entry.Time = time.Now().UTC()
		}
		if entry.ID == "" {
			entry.ID = util.NewID("log")
		}
		if err := s.cfg.Queue.EnqueueLog(entry); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{"accepted": len(payload)})
}

func (s *Server) ingestMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
		if err := s.cfg.Queue.EnqueueMetric(point); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{"accepted": len(payload)})
}

func (s *Server) searchLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	tags := parseTags(r.URL.Query().Get("tag"))
	items, err := s.cfg.Store.SearchLogs(q, tags, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
}

func (s *Server) getMetrics(w http.ResponseWriter, r *http.Request) {
	metric := r.URL.Query().Get("name")
	tags := parseTags(r.URL.Query().Get("tag"))
	start, _ := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	end, _ := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
	items, err := s.cfg.Store.QueryMetrics(metric, start, end, tags)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, items)
}

func (s *Server) alertsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.cfg.Alerts.List())
	case http.MethodPost:
		var rule domain.AlertRule
		if err := decodeBody(r.Body, &rule); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.cfg.Alerts.Upsert(rule); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, rule)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.cfg.Store.Snapshot())
}

func decodeBody(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func parseTags(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			out[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return out
}
