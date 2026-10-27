package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// boundedQueue is an in-memory FIFO queue of Records with a fixed
// capacity. When full, Push drops the oldest queued record to make room
// for the new one and reports the drop via warn.
type boundedQueue struct {
	mu       sync.Mutex
	items    []Record
	capacity int
	warn     func(format string, args ...any)
	dropped  int
}

func newBoundedQueue(capacity int, warn func(format string, args ...any)) *boundedQueue {
	return &boundedQueue{capacity: capacity, warn: warn}
}

func (q *boundedQueue) Push(r Record) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.capacity {
		q.items = q.items[1:] // drop oldest
		q.dropped++
		if q.warn != nil {
			q.warn("forward queue full (capacity %d): dropping oldest record", q.capacity)
		}
	}
	q.items = append(q.items, r)
}

// PopBatch removes and returns up to n records from the front of the
// queue.
func (q *boundedQueue) PopBatch(n int) []Record {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	if n > len(q.items) {
		n = len(q.items)
	}
	batch := make([]Record, n)
	copy(batch, q.items[:n])
	q.items = q.items[n:]
	return batch
}

// PutBack re-queues a batch at the front (used when a forward attempt
// exhausts its retries so the data isn't silently lost immediately —
// subject to the same drop-oldest overflow policy as Push).
func (q *boundedQueue) PutBack(batch []Record) {
	q.mu.Lock()
	defer q.mu.Unlock()
	combined := append(append([]Record{}, batch...), q.items...)
	if len(combined) > q.capacity {
		dropped := len(combined) - q.capacity
		combined = combined[dropped:]
		q.dropped += dropped
		if q.warn != nil {
			q.warn("forward queue full (capacity %d): dropping %d oldest record(s) after failed send", q.capacity, dropped)
		}
	}
	q.items = combined
}

func (q *boundedQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Forwarder batches queued Records and POSTs them as JSON to a
// configured URL on a timer and/or once a batch-size threshold is hit,
// retrying failed sends with exponential backoff.
type Forwarder struct {
	URL        string
	Client     *http.Client
	BatchSize  int
	Interval   time.Duration
	MaxRetries int
	Warn       func(format string, args ...any)

	queue *boundedQueue
}

func NewForwarder(url string, client *http.Client, batchSize int, interval time.Duration, queueCap int, warn func(format string, args ...any)) *Forwarder {
	if warn == nil {
		warn = func(string, ...any) {}
	}
	return &Forwarder{
		URL:        url,
		Client:     client,
		BatchSize:  batchSize,
		Interval:   interval,
		MaxRetries: 5,
		Warn:       warn,
		queue:      newBoundedQueue(queueCap, warn),
	}
}

// Enqueue adds a record to the bounded queue, dropping the oldest queued
// record if full.
func (fw *Forwarder) Enqueue(r Record) {
	fw.queue.Push(r)
	if fw.queue.Len() >= fw.BatchSize {
		// Best-effort immediate flush trigger; Run's ticker also drains
		// periodically, this just avoids unnecessary latency once a
		// batch's worth has accumulated. Errors are handled/retried
		// inside flushOnce, so we ignore the return here.
		_ = fw.flushOnce(context.Background())
	}
}

// QueueLen reports the current number of queued (not yet sent) records.
func (fw *Forwarder) QueueLen() int { return fw.queue.Len() }

// Run drains the queue on Interval until ctx is cancelled, then performs
// one final flush of anything still queued before returning.
func (fw *Forwarder) Run(ctx context.Context) {
	ticker := time.NewTicker(fw.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fw.FlushAll(context.Background())
			return
		case <-ticker.C:
			_ = fw.flushOnce(ctx)
		}
	}
}

// FlushAll drains and sends the entire queue, batch by batch.
func (fw *Forwarder) FlushAll(ctx context.Context) {
	for fw.queue.Len() > 0 {
		if err := fw.flushOnce(ctx); err != nil {
			fw.Warn("final flush: %v", err)
			return
		}
	}
}

// flushOnce pops one batch and sends it with retry+backoff. On
// exhausted retries the batch is dropped (subject to warning) rather
// than requeued indefinitely, to avoid unbounded retry loops during
// shutdown.
func (fw *Forwarder) flushOnce(ctx context.Context) error {
	batch := fw.queue.PopBatch(fw.BatchSize)
	if len(batch) == 0 {
		return nil
	}

	body, err := json.Marshal(batch)
	if err != nil {
		fw.Warn("marshal batch: %v", err)
		return err
	}

	backoff := 200 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt <= fw.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				fw.queue.PutBack(batch)
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
		}

		if err := fw.send(ctx, body); err != nil {
			lastErr = err
			continue
		}
		return nil
	}

	fw.Warn("forward: giving up after %d attempts, dropping batch of %d record(s): %v", fw.MaxRetries+1, len(batch), lastErr)
	return lastErr
}

func (fw *Forwarder) send(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fw.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := fw.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("forward: unexpected status %s", resp.Status)
	}
	return nil
}
