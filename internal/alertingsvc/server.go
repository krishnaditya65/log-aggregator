package alertingsvc

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"custom-log-aggregator/internal/alerting"
	"custom-log-aggregator/internal/querylang/metricsql"
	"custom-log-aggregator/internal/util"
)

// Server exposes the HTTP API that internal/api/v2.go (cmd/aggregator)
// proxies to: POST/GET /v2/alerts/rules, POST /v2/alerts/silences, and
// POST /v2/deployments. Request/response JSON shapes are unchanged from
// when these were handled directly by internal/api/v2.go against
// s.cfg.Platform.Alerting - this is a transparent move, not a breaking API
// change for whatever calls cmd/aggregator's /v2/alerts/* today.
type Server struct {
	engine     *alerting.Engine
	dataSource metricsql.DataSource
	mux        *http.ServeMux

	mu    sync.Mutex
	rules map[string]RuleView
}

// NewServer builds a Server around an already-wired alerting.Engine
// (grouper/router/silencer/correlator - see cmd/alerting-service/main.go)
// and the aggregator base URL used to satisfy rule Evaluator closures'
// TSDB queries over HTTP.
func NewServer(engine *alerting.Engine, aggregatorURL string) *Server {
	s := &Server{
		engine:     engine,
		dataSource: newHTTPDataSource(aggregatorURL),
		rules:      make(map[string]RuleView),
		mux:        http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	s.mux.Handle("GET /v2/alerts/rules", http.HandlerFunc(s.listRules))
	s.mux.Handle("POST /v2/alerts/rules", http.HandlerFunc(s.createRule))
	s.mux.Handle("POST /v2/alerts/silences", http.HandlerFunc(s.createSilence))
	s.mux.Handle("POST /v2/deployments", http.HandlerFunc(s.recordDeployment))
}

// RuleView is what GET /v2/alerts/rules returns - the alerting.Engine
// itself doesn't expose a lister (rule state lives behind the injected
// Evaluator closure), so, same as before the move, this service keeps its
// own registry of installed rule specs alongside the engine.
type RuleView struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Expr     string            `json:"expr"`
	For      time.Duration     `json:"for"`
	Severity string            `json:"severity"`
	Labels   map[string]string `json:"labels,omitempty"`
}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	out := make([]RuleView, 0, len(s.rules))
	for _, rv := range s.rules {
		out = append(out, rv)
	}
	s.mu.Unlock()
	writeJSON(w, out)
}

// createRuleRequest is the same request shape v2CreateAlertRule used to
// decode directly.
type createRuleRequest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Expr        string            `json:"expr"`
	ForSeconds  int64             `json:"for_seconds"`
	Severity    string            `json:"severity"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

// createRule compiles spec.Expr once with metricsql and installs a rule on
// the alerting engine whose Evaluator re-evaluates that parsed expression
// against live TSDB data (fetched over HTTP from the aggregator via
// s.dataSource) on every tick. This is the same logic that used to live in
// internal/platform/alerts_wire.go's AddMetricAlertRule, just relocated
// here with a network-backed DataSource instead of an in-process one.
func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	var spec createRuleRequest
	if err := decodeBody(r.Body, &spec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if spec.ID == "" {
		spec.ID = util.NewID("rule")
	}
	expr, err := metricsql.Parse(metricsql.Lex(spec.Expr))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ds := s.dataSource
	evaluator := func(now int64) (float64, bool, error) {
		series, err := metricsql.Eval(expr, ds, now)
		if err != nil {
			return 0, false, err
		}
		if len(series) == 0 {
			return 0, false, nil
		}
		var v float64
		if n := len(series[0].Points); n > 0 {
			v = series[0].Points[n-1].V
		}
		return v, true, nil
	}
	rule := &alerting.Rule{
		ID:          spec.ID,
		Name:        spec.Name,
		Evaluator:   evaluator,
		For:         time.Duration(spec.ForSeconds) * time.Second,
		Labels:      spec.Labels,
		Annotations: spec.Annotations,
		Severity:    spec.Severity,
	}
	s.engine.AddRule(rule)

	rv := RuleView{
		ID: rule.ID, Name: rule.Name, Expr: spec.Expr, For: rule.For, Severity: rule.Severity, Labels: rule.Labels,
	}
	s.mu.Lock()
	s.rules[rule.ID] = rv
	s.mu.Unlock()

	writeJSON(w, rv)
}

func (s *Server) createSilence(w http.ResponseWriter, r *http.Request) {
	var sil alerting.Silence
	if err := decodeBody(r.Body, &sil); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.engine.AddSilence(sil)
	writeJSON(w, sil)
}

func (s *Server) recordDeployment(w http.ResponseWriter, r *http.Request) {
	var evt struct {
		Service string    `json:"service"`
		Version string    `json:"version"`
		Time    time.Time `json:"time"`
	}
	if err := decodeBody(r.Body, &evt); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if evt.Time.IsZero() {
		evt.Time = time.Now().UTC()
	}
	s.engine.AddDeployment(alerting.DeploymentEvent{Service: evt.Service, Time: evt.Time.Unix(), Version: evt.Version})
	writeJSON(w, map[string]any{"ok": true})
}

func decodeBody(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
