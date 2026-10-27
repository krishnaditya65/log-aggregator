// Command agent is a lightweight ingestion agent: it tails log files,
// periodically scrapes a Prometheus-text-format /metrics endpoint, and
// forwards batched records as JSON to an aggregator's ingest endpoint,
// with a bounded in-memory queue, retry-with-backoff, and graceful
// shutdown.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	var (
		tailFlag        = flag.String("tail", "", "comma-separated list of file paths to tail")
		scrapeURL       = flag.String("scrape-url", "", "Prometheus /metrics URL to scrape periodically")
		scrapeInterval  = flag.Duration("scrape-interval", 15*time.Second, "interval between metric scrapes")
		forwardURL      = flag.String("forward-url", "http://localhost:8080/ingest", "URL to POST batched records to")
		forwardInterval = flag.Duration("forward-interval", 5*time.Second, "interval between forward flushes")
		batchSize       = flag.Int("batch-size", 100, "max records per forwarded batch / flush trigger threshold")
		queueSize       = flag.Int("queue-size", 10000, "bounded in-memory queue capacity (oldest dropped on overflow)")
		tailPoll        = flag.Duration("tail-poll-interval", 500*time.Millisecond, "how often to poll tailed files for new data")
	)
	flag.Parse()

	warn := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[agent] WARN "+format+"\n", args...)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fw := NewForwarder(*forwardURL, &http.Client{Timeout: 10 * time.Second}, *batchSize, *forwardInterval, *queueSize, warn)

	var wg sync.WaitGroup

	// Log tailing.
	paths := splitNonEmpty(*tailFlag, ",")
	logCh := make(chan Record, 256)
	for _, p := range paths {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := TailFile(ctx, p, *tailPoll, logCh, warn); err != nil && ctx.Err() == nil {
				warn("tail %s: %v", p, err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case rec, ok := <-logCh:
				if !ok {
					return
				}
				fw.Enqueue(rec)
			}
		}
	}()

	// Metrics scraping.
	if *scrapeURL != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 10 * time.Second}
			ticker := time.NewTicker(*scrapeInterval)
			defer ticker.Stop()
			scrapeOnce := func() {
				samples, err := ScrapeURL(ctx, client, *scrapeURL)
				if err != nil {
					warn("scrape %s: %v", *scrapeURL, err)
					return
				}
				for _, rec := range samplesToRecords(*scrapeURL, samples) {
					fw.Enqueue(rec)
				}
			}
			scrapeOnce()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					scrapeOnce()
				}
			}
		}()
	}

	// Forwarding loop runs until ctx is cancelled, then performs its own
	// final flush.
	fwDone := make(chan struct{})
	go func() {
		defer close(fwDone)
		fw.Run(ctx)
	}()

	<-ctx.Done()
	warn("shutting down, flushing pending records...")
	// Let producers (tailers, scraper, log consumer) fully stop before
	// the last flush so nothing is enqueued after we've drained.
	wg.Wait()
	<-fwDone
	// Catch anything enqueued between the forwarder's own shutdown
	// flush and the producers finishing.
	fw.FlushAll(context.Background())
}

func splitNonEmpty(s, sep string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, sep) {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
