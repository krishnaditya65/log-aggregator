package main

import "time"

// Record is the JSON payload shape forwarded to the aggregator's ingest
// endpoint. It doubles as a log line record and a metric sample record,
// distinguished by Type.
type Record struct {
	Type      string            `json:"type"` // "log" or "metric"
	Timestamp time.Time         `json:"timestamp"`
	Source    string            `json:"source,omitempty"`
	Line      string            `json:"line,omitempty"`
	Metric    string            `json:"metric,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64           `json:"value,omitempty"`
}
