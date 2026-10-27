// Command ingest-gateway hosts the OTLP receiver (gRPC :4317, OTLP/HTTP
// :4318) standalone, decoupled from cmd/aggregator's storage engine: it
// decodes OTLP logs/metrics/traces exactly as it always did, but instead of
// writing straight onto a local ingestbuffer.Buffer it forwards the
// translated domain objects over HTTP to cmd/aggregator's internal
// /internal/{logs,metrics,traces} endpoints (see internal/ingestgateway).
//
// This mirrors cmd/alerting-service's split: a standalone binary that talks
// to cmd/aggregator over the network via AGGREGATOR_URL rather than being
// wired into the same process.
package main

import (
	"log"
	"os"

	"custom-log-aggregator/internal/ingestgateway"
	"custom-log-aggregator/internal/otlp"
)

func main() {
	grpcAddr := env("CLA_OTLP_GRPC_ADDR", ":4317")
	httpAddr := env("CLA_OTLP_HTTP_ADDR", ":4318")
	aggregatorURL := env("AGGREGATOR_URL", "http://localhost:8080")

	sink := ingestgateway.NewForwardSink(aggregatorURL)
	receiver := otlp.NewReceiver()

	errCh := make(chan error, 2)
	if grpcAddr != "" {
		go func() {
			log.Printf("otlp gRPC receiver listening on %s (forwarding to %s)", grpcAddr, aggregatorURL)
			errCh <- receiver.Start(grpcAddr, sink)
		}()
	}
	if httpAddr != "" {
		go func() {
			log.Printf("otlp/http receiver listening on %s (forwarding to %s)", httpAddr, aggregatorURL)
			errCh <- receiver.StartHTTP(httpAddr, sink)
		}()
	}

	log.Fatal(<-errCh)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
