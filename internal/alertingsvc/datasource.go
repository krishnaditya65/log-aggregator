// Package alertingsvc implements the standalone alerting-service binary's
// HTTP API and wiring: rule-based alert evaluation, grouping, routing,
// silencing and deploy-correlation (internal/alerting.Engine), decoupled
// from the aggregator process. It is the exact same rule-evaluation logic
// that used to run in-process inside cmd/aggregator (see
// internal/platform/alerts_wire.go and alert_ticker.go, now removed) - the
// only thing that changed is that a Rule's Evaluator closure now fetches
// its TSDB data over the network (httpDataSource below) instead of through
// an in-process *tsdb.Store pointer.
package alertingsvc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"custom-log-aggregator/internal/querylang/metricsql"
)

// httpDataSource adapts the aggregator's /internal/metrics/query_range
// endpoint to metricsql.DataSource, so alert rule Evaluator closures built
// by this service can evaluate metricsql expressions against live TSDB
// data without an in-process *tsdb.Store - mirroring
// internal/platform/adapters.go's metricsqlDataSource, just backed by an
// HTTP call instead of a local pointer, since alerting-service now runs as
// a separate process from the aggregator that owns the TSDB.
type httpDataSource struct {
	baseURL string
	client  *http.Client
}

func newHTTPDataSource(baseURL string) *httpDataSource {
	return &httpDataSource{baseURL: baseURL, client: http.DefaultClient}
}

// queryRangeRequest is the request body shape expected by the aggregator's
// POST /internal/metrics/query_range endpoint.
type queryRangeRequest struct {
	Matchers map[string]string `json:"matchers"`
	Start    int64             `json:"start"`
	End      int64             `json:"end"`
}

// QueryRange implements metricsql.DataSource.
func (d *httpDataSource) QueryRange(matchers map[string]string, start, end int64) ([]metricsql.Series, error) {
	body, err := json.Marshal(queryRangeRequest{Matchers: matchers, Start: start, End: end})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, d.baseURL+"/internal/metrics/query_range", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("alertingsvc: query aggregator: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alertingsvc: aggregator query_range returned status %d", resp.StatusCode)
	}
	var series []metricsql.Series
	if err := json.NewDecoder(resp.Body).Decode(&series); err != nil {
		return nil, fmt.Errorf("alertingsvc: decode query_range response: %w", err)
	}
	return series, nil
}
