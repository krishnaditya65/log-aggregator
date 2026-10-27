package alerts

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"custom-log-aggregator/internal/domain"
)

type Service struct {
	mu    sync.RWMutex
	rules []domain.AlertRule
	path  string
}

func NewService(path string) (*Service, error) {
	s := &Service{path: path}
	_ = s.load()
	return s, nil
}

func (s *Service) List() []domain.AlertRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.AlertRule, len(s.rules))
	copy(out, s.rules)
	return out
}

func (s *Service) Upsert(rule domain.AlertRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rule.Enabled = true
	updated := false
	for i := range s.rules {
		if s.rules[i].ID == rule.ID {
			s.rules[i] = rule
			updated = true
		}
	}
	if !updated {
		s.rules = append(s.rules, rule)
	}
	return s.save()
}

func (s *Service) Evaluate(point domain.MetricPoint) []domain.AlertEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var events []domain.AlertEvent
	for _, rule := range s.rules {
		if !rule.Enabled || rule.Metric != point.Name {
			continue
		}
		triggered := false
		switch rule.Operator {
		case "gt":
			triggered = point.Value > rule.Threshold
		case "gte":
			triggered = point.Value >= rule.Threshold
		case "lt":
			triggered = point.Value < rule.Threshold
		case "lte":
			triggered = point.Value <= rule.Threshold
		}
		if triggered {
			events = append(events, domain.AlertEvent{
				RuleID:    rule.ID,
				Metric:    point.Name,
				Value:     point.Value,
				Triggered: time.Now().UTC(),
				Message:   "threshold crossed",
			})
		}
	}
	return events
}

func (s *Service) save() error {
	if s.path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(s.rules, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o644)
}

func (s *Service) load() error {
	if s.path == "" {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	return json.Unmarshal(raw, &s.rules)
}
