package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MetricSample is one parsed Prometheus text-format sample.
type MetricSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

var labelPairRe = regexp.MustCompile(`([a-zA-Z_:][a-zA-Z0-9_:]*)\s*=\s*"((?:[^"\\]|\\.)*)"`)

// ParsePrometheusText parses a minimal subset of the Prometheus text
// exposition format: lines of the form
//
//	metric_name{label="value",other="v2"} 123.45
//	metric_name 123.45
//
// `# TYPE` / `# HELP` (and any other `#`-prefixed) comment lines and
// blank lines are skipped.
func ParsePrometheusText(data []byte) ([]MetricSample, error) {
	var samples []MetricSample
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.LastIndex(line, " ")
		if idx < 0 {
			continue // malformed, no value
		}
		metricPart := strings.TrimSpace(line[:idx])
		valuePart := strings.TrimSpace(line[idx+1:])

		value, err := strconv.ParseFloat(valuePart, 64)
		if err != nil {
			continue // skip malformed lines rather than fail the whole scrape
		}

		name := metricPart
		var labels map[string]string
		if b := strings.IndexByte(metricPart, '{'); b >= 0 {
			name = metricPart[:b]
			end := strings.LastIndexByte(metricPart, '}')
			if end > b {
				labelStr := metricPart[b+1 : end]
				labels = parseLabels(labelStr)
			}
		}

		samples = append(samples, MetricSample{Name: name, Labels: labels, Value: value})
	}
	if err := scanner.Err(); err != nil {
		return samples, err
	}
	return samples, nil
}

func parseLabels(s string) map[string]string {
	labels := map[string]string{}
	matches := labelPairRe.FindAllStringSubmatch(s, -1)
	for _, m := range matches {
		key := m[1]
		val := strings.ReplaceAll(m[2], `\"`, `"`)
		val = strings.ReplaceAll(val, `\n`, "\n")
		val = strings.ReplaceAll(val, `\\`, `\`)
		labels[key] = val
	}
	return labels
}

// ScrapeURL fetches url and parses it as Prometheus text format.
func ScrapeURL(ctx context.Context, client *http.Client, url string) ([]MetricSample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("scrape %s: unexpected status %s", url, resp.Status)
	}
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	return ParsePrometheusText(buf.Bytes())
}

// samplesToRecords converts scraped samples into forwardable Records.
func samplesToRecords(source string, samples []MetricSample) []Record {
	now := time.Now()
	out := make([]Record, 0, len(samples))
	for _, s := range samples {
		out = append(out, Record{
			Type:      "metric",
			Timestamp: now,
			Source:    source,
			Metric:    s.Name,
			Labels:    s.Labels,
			Value:     s.Value,
		})
	}
	return out
}
