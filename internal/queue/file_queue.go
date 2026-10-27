package queue

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"custom-log-aggregator/internal/domain"
)

type item struct {
	Type   domain.EntryType    `json:"type"`
	Log    *domain.LogEntry    `json:"log,omitempty"`
	Metric *domain.MetricPoint `json:"metric,omitempty"`
}

type FileQueue struct {
	mu          sync.Mutex
	file        *os.File
	writer      *bufio.Writer
	subscribers []chan Envelope
	segmentPath string
}

func NewFileQueue(dir string, _ int64) (*FileQueue, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "wal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &FileQueue{
		file:        f,
		writer:      bufio.NewWriter(f),
		segmentPath: path,
	}, nil
}

func (q *FileQueue) EnqueueLog(entry domain.LogEntry) error {
	return q.enqueue(item{Type: domain.EntryLog, Log: &entry})
}

func (q *FileQueue) EnqueueMetric(point domain.MetricPoint) error {
	return q.enqueue(item{Type: domain.EntryMetric, Metric: &point})
}

func (q *FileQueue) Subscribe() <-chan Envelope {
	ch := make(chan Envelope, 1024)
	q.mu.Lock()
	q.subscribers = append(q.subscribers, ch)
	q.mu.Unlock()
	return ch
}

func (q *FileQueue) enqueue(it item) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	raw, err := json.Marshal(it)
	if err != nil {
		return err
	}
	if _, err := q.writer.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := q.writer.Flush(); err != nil {
		return err
	}
	if err := q.file.Sync(); err != nil {
		return err
	}
	envelope := Envelope{Log: it.Log, Metric: it.Metric}
	for _, sub := range q.subscribers {
		select {
		case sub <- envelope:
		default:
		}
	}
	return nil
}

func (q *FileQueue) Replay(fn func(Envelope) error) error {
	f, err := os.Open(q.segmentPath)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var it item
		if err := json.Unmarshal(scanner.Bytes(), &it); err != nil {
			return fmt.Errorf("queue replay: %w", err)
		}
		if err := fn(Envelope{Log: it.Log, Metric: it.Metric}); err != nil {
			return err
		}
	}
	return scanner.Err()
}
