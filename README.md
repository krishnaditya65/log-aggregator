# Custom Log Aggregator

A from-scratch log and metrics aggregation stack, built in Go, covering the full brief: basic
ingestion and storage, structured parsing, a PromQL-like and a pipelined query language, RBAC and
multi-tenancy, distributed tracing, semantic (vector) search, AIOps root-cause correlation,
columnar cold storage with query pushdown, dynamic retention, and Slack/PagerDuty alert routing.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full design, the complete API reference,
the metricsql/pipeql grammars, and an honest capability matrix mapping every item in the original
brief (including the "2026 cutting-edge" section - eBPF, OTLP, vector search, LLM observability,
etc.) to what is actually implemented here versus what is inherently infrastructure-dependent.

## Two stacks, one binary

- **v1 (`/v1/*`)**: the original simple stack - in-memory/JSONL storage, a file-backed queue, static
  threshold alerts. Untouched, still works exactly as before.
- **v2 (`/v2/*`)**: the advanced stack - OTLP ingestion, a Kafka-like buffer, a real embedded TSDB
  and LogsDB, metricsql/pipeql query languages, alerting v2 (anomaly detection, grouping, Slack/
  PagerDuty routing, silencing), RBAC, distributed tracing, semantic search, AIOps, continuous
  profiling, and cold storage archival with query pushdown.

Both run side by side in `cmd/aggregator`, sharing the HTTP port for the REST API plus dedicated
ports for OTLP gRPC (`:4317`) and OTLP/HTTP (`:4318`).

## Services

- `aggregator` (`cmd/aggregator`): the Go API, processor, and every v2 subsystem.
- `agent` (`cmd/agent`): a lightweight standalone ingestion agent - tails log files, scrapes
  Prometheus-format `/metrics` endpoints, and forwards batches to the aggregator.
- `dashboard` (`web/dashboard`): a Next.js UI with a Basic tab (v1) and an Advanced tab (v2: pipeql
  search, metricsql query + sparkline, semantic search, alert rules, AIOps root-cause).

## Run locally

```bash
docker compose up --build
```

- API: `http://localhost:8080` (v1 + v2)
- OTLP gRPC: `localhost:4317`
- OTLP/HTTP: `http://localhost:4318` (`/v1/logs`, `/v1/metrics`, `/v1/traces`)
- Dashboard: `http://localhost:3000`

Or run the aggregator directly:

```bash
go build -o aggregator ./cmd/aggregator
CLA_DATA_DIR=./data ./aggregator
```

And the ingestion agent:

```bash
go run ./cmd/agent -tail /var/log/app.log -scrape-url http://localhost:9100/metrics \
  -forward-url http://localhost:8080/v2/logs
```

## Key flows

- **Ingest**: HTTP (`/v1/ingest/*`, `/v2/logs`, `/v2/metrics`, `/v2/traces`) or OTLP (gRPC/HTTP) →
  `ingestbuffer` (partitioned, replicated, backpressure-aware, tenant-fair-queued commit log) →
  stream processor → `tsdb` + `logsdb` (+ tracing, vector index, alerting anomaly detectors, AIOps
  feed).
- **Query**: `metricsql` (PromQL-like) against `tsdb`; `pipeql` (pipelined, Splunk/Kusto-style)
  against `logsdb`; semantic search against the HNSW vector index.
- **Alert**: rule evaluation (static or metricsql-expression-based) with `for`-duration gating,
  statistical anomaly detection (z-score, EWMA), Alertmanager-style grouping, Slack/PagerDuty/
  webhook routing, maintenance silencing, deploy-correlated root-cause hints.
- **Retire**: `tsdb`/`logsdb` segments age out, get archived to cold storage (local filesystem or
  S3-compatible, hand-implemented SigV4), then deleted; `internal/lifecycle` separately governs how
  long archived cold-storage objects are kept.

## Configuration

All via environment variables on `aggregator`:

| Variable | Default | Purpose |
|---|---|---|
| `CLA_DATA_DIR` | `./data` | Root directory for all local storage |
| `CLA_HTTP_ADDR` | `:8080` | REST API (v1 + v2) |
| `CLA_OTLP_GRPC_ADDR` | `:4317` | OTLP gRPC receiver |
| `CLA_OTLP_HTTP_ADDR` | `:4318` | OTLP/HTTP receiver |
| `CLA_DASHBOARD_ORIGIN` | `*` | CORS origin for the dashboard |
| `CLA_HOT_RETENTION` | `168h` | How long tsdb/logsdb keep hot segments before archiving |
| `CLA_RBAC_ENABLED` | `false` | Require bearer tokens + permission checks on `/v2/*` |
| `CLA_RBAC_SECRET` | *(required if RBAC enabled)* | HMAC signing secret for tokens |
| `CLA_SLACK_WEBHOOK_URL` | *(unset)* | Slack incoming webhook for alert routing |
| `CLA_PAGERDUTY_ROUTING_KEY` | *(unset)* | PagerDuty Events API v2 routing key |
| `CLA_S3_ENDPOINT`/`CLA_S3_BUCKET`/`CLA_S3_ACCESS_KEY`/`CLA_S3_SECRET_KEY`/`CLA_S3_REGION` | *(unset)* | S3-compatible cold storage; falls back to local filesystem if unset |

## Tests

```bash
go build ./...
go vet ./...
go test ./... -race
```

Every package (30+) has real unit tests with concrete assertions - see
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for what each one actually proves.
