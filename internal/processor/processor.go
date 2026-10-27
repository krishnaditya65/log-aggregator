package processor

import (
	"log"

	"custom-log-aggregator/internal/alerts"
	"custom-log-aggregator/internal/queue"
	"custom-log-aggregator/internal/storage"
)

type Processor struct {
	q      *queue.FileQueue
	store  storage.Store
	alerts *alerts.Service
}

func New(q *queue.FileQueue, store storage.Store, alerts *alerts.Service) *Processor {
	return &Processor{q: q, store: store, alerts: alerts}
}

func (p *Processor) Run() {
	ch := p.q.Subscribe()
	for event := range ch {
		if event.Log != nil {
			if err := p.store.AppendLog(*event.Log); err != nil {
				log.Printf("processor: append log: %v", err)
			}
		}
		if event.Metric != nil {
			if err := p.store.AppendMetric(*event.Metric); err != nil {
				log.Printf("processor: append metric: %v", err)
				continue
			}
			for _, alert := range p.alerts.Evaluate(*event.Metric) {
				log.Printf("alert triggered: %+v", alert)
			}
		}
	}
}
