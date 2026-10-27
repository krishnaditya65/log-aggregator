package platform

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"custom-log-aggregator/internal/aiops"
	"custom-log-aggregator/internal/domain"
	"custom-log-aggregator/internal/ingestbuffer"
	"custom-log-aggregator/internal/logsdb"
	"custom-log-aggregator/internal/tracing"
)

// startConsumer is the "Stream Processing (Aggregator)" layer: it drains
// every ingestbuffer partition, parses each envelope, and fans it out to
// every storage/analysis engine that cares (tsdb, logsdb, tracing,
// semantic vector index, alerting anomaly detectors, AIOps feed).
func (p *Platform) startConsumer(ctx context.Context) {
	for i := 0; i < p.cfg.BufferPartitions; i++ {
		partition := i
		ch, err := p.Buffer.Subscribe(partition, 0)
		if err != nil {
			log.Printf("platform: consumer: subscribe partition %d: %v", partition, err)
			continue
		}
		go p.consumePartition(ctx, partition, ch)
	}
}

func (p *Platform) consumePartition(ctx context.Context, partition int, ch <-chan ingestbuffer.Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			p.process(msg)
			if err := p.Buffer.Commit(partition, msg.Offset+1); err != nil {
				log.Printf("platform: consumer: commit partition %d: %v", partition, err)
			}
		}
	}
}

func (p *Platform) process(msg ingestbuffer.Message) {
	var env envelope
	if err := json.Unmarshal(msg.Payload, &env); err != nil {
		log.Printf("platform: consumer: decode envelope: %v", err)
		return
	}
	switch env.Kind {
	case "log":
		if env.Log != nil {
			p.processLog(*env.Log)
		}
	case "metric":
		if env.Metric != nil {
			p.processMetric(*env.Metric)
		}
	case "span":
		if env.Span != nil {
			p.processSpan(*env.Span)
		}
	}
}

func (p *Platform) processLog(entry domain.LogEntry) {
	doc := logDocFromEntry(entry)
	if err := p.LogsDB.Append(doc); err != nil {
		log.Printf("platform: consumer: logsdb append: %v", err)
		return
	}
	// Feed the semantic vector index so /v2/logs/semantic can find it by
	// meaning, not just keyword.
	if entry.Message != "" {
		p.Vector.Index(doc.ID, entry.Message)
	}
	// Feed the trace-log correlation / AIOps error-spike signal: an error
	// log becomes a LogSpike sample the root-cause correlator can match
	// against nearby anomalies and deployments.
	if strings.EqualFold(entry.Level, "error") {
		p.aiopsFeed.addSpike(aiops.LogSpike{
			Service:        entry.Service,
			Time:           entry.Time.Unix(),
			Count:          1,
			SampleMessages: []string{entry.Message},
		})
	}
}

func logDocFromEntry(entry domain.LogEntry) logsdb.LogDoc {
	return logsdb.LogDoc{
		ID:      entry.ID,
		Time:    entry.Time.UnixNano(),
		Host:    entry.Host,
		Service: entry.Service,
		Level:   entry.Level,
		Message: entry.Message,
		TraceID: entry.TraceID,
		SpanID:  entry.SpanID,
		Tags:    entry.Tags,
		Fields:  entry.Fields,
		Raw:     entry.Raw,
	}
}

func (p *Platform) processMetric(point domain.MetricPoint) {
	labels := map[string]string{"__name__": point.Name}
	if point.Service != "" {
		labels["service"] = point.Service
	}
	for k, v := range point.Tags {
		labels[k] = v
	}
	if err := p.TSDB.Append(labels, point.Time.UnixNano(), point.Value); err != nil {
		log.Printf("platform: consumer: tsdb append: %v", err)
		return
	}

	isAnomaly, score := p.Alerting.ObserveMetric(point.Name, point.Time.Unix(), point.Value)
	if isAnomaly {
		p.aiopsFeed.addAnomaly(aiops.Anomaly{
			Service: point.Service,
			Metric:  point.Name,
			Time:    point.Time.Unix(),
			Score:   score,
		})
	}
}

func (p *Platform) processSpan(sp domain.SpanEntry) {
	span := tracing.Span{
		TraceID:      sp.TraceID,
		SpanID:       sp.SpanID,
		ParentSpanID: sp.ParentSpanID,
		Service:      sp.Service,
		Name:         sp.Name,
		StartTime:    sp.StartTime.UnixNano(),
		EndTime:      sp.EndTime.UnixNano(),
		Status:       sp.Status,
		Tags:         sp.Tags,
		Attributes:   sp.Attributes,
	}
	p.Traces.Add(span)

	// Attach a P99-latency-style exemplar so a metric spike can jump
	// straight to the trace that caused it.
	if span.Duration() > 0 {
		p.Exemplars.Attach(sp.Service+"_latency_seconds", tracing.Exemplar{
			TraceID: sp.TraceID,
			Value:   span.Duration().Seconds(),
			Time:    span.EndTime,
			Labels:  sp.Tags,
		})
	}
}
