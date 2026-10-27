package queue

import "custom-log-aggregator/internal/domain"

type Envelope struct {
	Log    *domain.LogEntry
	Metric *domain.MetricPoint
}

type Queue interface {
	EnqueueLog(domain.LogEntry) error
	EnqueueMetric(domain.MetricPoint) error
	Subscribe() <-chan Envelope
}
