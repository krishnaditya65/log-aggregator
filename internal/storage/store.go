package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"custom-log-aggregator/internal/domain"
)

type Store interface {
	AppendLog(domain.LogEntry) error
	AppendMetric(domain.MetricPoint) error
	SearchLogs(query string, tags map[string]string, limit int) ([]domain.LogEntry, error)
	QueryMetrics(metric string, start, end time.Time, tags map[string]string) ([]domain.MetricPoint, error)
	Series(metric string, start, end time.Time, tags map[string]string) ([]domain.MetricPoint, error)
	Snapshot() Snapshot
}

type Snapshot struct {
	LogCount    int `json:"log_count"`
	MetricCount int `json:"metric_count"`
}

type DiskStore struct {
	mu         sync.RWMutex
	logs       []domain.LogEntry
	metrics    []domain.MetricPoint
	index      map[string][]int
	seen       map[string]struct{}
	dir        string
	logPath    string
	metricPath string
}

func NewDiskStore(dir string) (*DiskStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &DiskStore{
		index:      make(map[string][]int),
		seen:       make(map[string]struct{}),
		dir:        dir,
		logPath:    filepath.Join(dir, "logs.jsonl"),
		metricPath: filepath.Join(dir, "metrics.jsonl"),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *DiskStore) AppendLog(entry domain.LogEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.ID != "" {
		if _, ok := s.seen["log:"+entry.ID]; ok {
			return nil
		}
		s.seen["log:"+entry.ID] = struct{}{}
	}
	s.logs = append(s.logs, entry)
	idx := len(s.logs) - 1
	for _, token := range tokens(entry.Message) {
		s.index[token] = append(s.index[token], idx)
	}
	for k, v := range entry.Tags {
		s.index[k+"="+v] = append(s.index[k+"="+v], idx)
	}
	if err := appendJSONL(s.logPath, entry); err != nil {
		return err
	}
	return nil
}

func (s *DiskStore) AppendMetric(point domain.MetricPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if point.ID != "" {
		if _, ok := s.seen["metric:"+point.ID]; ok {
			return nil
		}
		s.seen["metric:"+point.ID] = struct{}{}
	}
	s.metrics = append(s.metrics, point)
	if err := appendJSONL(s.metricPath, point); err != nil {
		return err
	}
	return nil
}

func (s *DiskStore) SearchLogs(query string, tags map[string]string, limit int) ([]domain.LogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 50
	}
	seen := map[int]struct{}{}
	var out []domain.LogEntry
	for _, idx := range s.index[strings.ToLower(query)] {
		if _, ok := seen[idx]; ok {
			continue
		}
		entry := s.logs[idx]
		if matchTags(entry.Tags, tags) {
			seen[idx] = struct{}{}
			out = append(out, entry)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	if len(out) == 0 {
		for i := len(s.logs) - 1; i >= 0 && len(out) < limit; i-- {
			entry := s.logs[i]
			if strings.Contains(strings.ToLower(entry.Message), strings.ToLower(query)) && matchTags(entry.Tags, tags) {
				out = append(out, entry)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

func (s *DiskStore) QueryMetrics(metric string, start, end time.Time, tags map[string]string) ([]domain.MetricPoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.MetricPoint
	for _, p := range s.metrics {
		if metric != "" && p.Name != metric {
			continue
		}
		if !start.IsZero() && p.Time.Before(start) {
			continue
		}
		if !end.IsZero() && p.Time.After(end) {
			continue
		}
		if !matchTags(p.Tags, tags) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *DiskStore) Series(metric string, start, end time.Time, tags map[string]string) ([]domain.MetricPoint, error) {
	return s.QueryMetrics(metric, start, end, tags)
}

func (s *DiskStore) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{LogCount: len(s.logs), MetricCount: len(s.metrics)}
}

func tokens(text string) []string {
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return r < 'a' || r > 'z' && (r < '0' || r > '9')
	})
	return parts
}

func matchTags(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func appendJSONL(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(v)
}

func (s *DiskStore) load() error {
	if err := s.loadLogs(); err != nil {
		return err
	}
	if err := s.loadMetrics(); err != nil {
		return err
	}
	return nil
}

func (s *DiskStore) loadLogs() error {
	f, err := os.Open(s.logPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry domain.LogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("load log: %w", err)
		}
		s.logs = append(s.logs, entry)
		if entry.ID != "" {
			s.seen["log:"+entry.ID] = struct{}{}
		}
		idx := len(s.logs) - 1
		for _, token := range tokens(entry.Message) {
			s.index[token] = append(s.index[token], idx)
		}
		for k, v := range entry.Tags {
			s.index[k+"="+v] = append(s.index[k+"="+v], idx)
		}
	}
	return scanner.Err()
}

func (s *DiskStore) loadMetrics() error {
	f, err := os.Open(s.metricPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var point domain.MetricPoint
		if err := json.Unmarshal(scanner.Bytes(), &point); err != nil {
			return fmt.Errorf("load metric: %w", err)
		}
		s.metrics = append(s.metrics, point)
		if point.ID != "" {
			s.seen["metric:"+point.ID] = struct{}{}
		}
	}
	return scanner.Err()
}
