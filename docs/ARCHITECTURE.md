# Architecture

## Layers

```
Ingestion Agents (cmd/agent, OTLP SDKs)
        │  HTTP  /v1/ingest/*, /v2/logs|metrics|traces
        │  OTLP  gRPC :4317, HTTP :4318
        ▼
Buffer  internal/ingestbuffer  - partitioned commit log, WAL + simulated-AZ
        │                        replication with quorum ack, per-tenant
        │                        token-bucket + shuffle-shard fair queuing,
        │                        backpressure (reject/block, sheds debug
        │                        priority first)
        ▼
Stream Processor  internal/platform/consumer.go  - drains every partition,
        │                        decodes envelopes, fans out to storage +
        │                        analysis engines
        ▼
Storage  internal/tsdb (metrics)     internal/logsdb (logs)
        │  chunked + columnar,       LogsDB-style host+time sort,
        │  delta-of-delta/XOR        gzip block compression, inverted
        │  encoding, doc-value       index (keyword/tag/regex/wildcard)
        │  block index, inverted
        │  label index, compaction,
        │  cardinality rollup
        ▼
Query   internal/querylang/metricsql   internal/querylang/pipeql
        (PromQL-like)                  (pipelined: search|filter|stats|sort|limit)
        internal/vectorsearch (HNSW semantic search over log messages)
        ▼
Alerting/AIOps  internal/alerting (rules, anomaly detection, grouping,
        │        Slack/PagerDuty/webhook routing, silencing, deploy
        │        correlation)   internal/aiops (rule+statistics root-cause
        │        correlation across anomalies/deploys/log-spikes)
        ▼
Lifecycle  tsdb/logsdb retention → internal/coldstore (Parquet-like columnar
           archive format with row-group min/max pushdown, local FS or
           S3-compatible w/ hand-rolled SigV4) → internal/lifecycle (second
           tier: how long archived objects live before deletion)
```

Cross-cutting: `internal/rbac` (users/teams/roles/tenants, HMAC bearer tokens, per-route
permission middleware), `internal/tracing` (spans, tail-based sampling, trace-metric exemplars),
`internal/profiling` (continuous CPU profiling + flamegraphs), `internal/mcptrace` (MCP JSON-RPC
tracing), `internal/llmobs` (GenAI/LLM call observability), `internal/dbwire` (Postgres/MySQL/
Redis/MongoDB wire-protocol parsing), `internal/ebpf` (Linux eBPF capture + portable fallback).

## Package map

| Package | Responsibility |
|---|---|
| `internal/domain` | Shared wire types: `LogEntry`, `MetricPoint`, `SpanEntry`, v1 `AlertRule`/`AlertEvent` |
| `internal/storage`, `internal/queue`, `internal/alerts`, `internal/processor` | The original v1 stack (untouched) |
| `internal/tsdb` | Embedded TSDB: chunked writes, columnar segments, doc-value skip index, label index, compaction, cardinality rollup |
| `internal/logsdb` | Embedded log search engine: host+time sort, block compression, inverted index, regex/wildcard |
| `internal/otlp` | OTLP gRPC + HTTP receivers, hand-rolled wire decode (no codegen dependency) |
| `internal/ingestbuffer` | Kafka-like partitioned buffer: WAL, replication+quorum, backpressure, tenant fair queuing |
| `internal/querylang/metricsql` | PromQL-like metrics query language: lexer, parser, evaluator |
| `internal/querylang/pipeql` | Pipelined log query language: lexer, parser, executor |
| `internal/alerting` | v2 alert engine: rules, anomaly detection, grouping, routing, silencing, deploy correlation |
| `internal/rbac` | Users/teams/roles/tenants, HMAC tokens, auth+authz middleware |
| `internal/tracing` | Spans, tail-based sampling, trace-metric exemplars |
| `internal/mcptrace` | MCP (Model Context Protocol) JSON-RPC call tracing |
| `internal/llmobs` | LLM/GenAI call observability (tokens, cost, latency) |
| `internal/dbwire` | Redis/Postgres/MySQL/MongoDB wire-protocol parsing |
| `internal/ebpf` | Linux eBPF RED-metrics/TLS-visibility capture + portable fallback |
| `internal/profiling` | Continuous CPU profiling + flamegraph construction |
| `internal/vectorsearch` | Local text embeddings + from-scratch HNSW index |
| `internal/aiops` | Rule/statistics-based root-cause correlation |
| `internal/coldstore` | Columnar archive format + query pushdown, FS/S3 blobstore |
| `internal/lifecycle` | Hot→warm→delete retention policy engine |
| `internal/platform` | Integration layer: wires every package above into one running system |
| `internal/api` | HTTP surface: v1 routes (`server.go`) + v2 routes (`v2.go`) |
| `cmd/aggregator` | Main binary |
| `cmd/agent` | Standalone lightweight ingestion agent (tail + scrape + forward) |

## API reference

### v1 (unchanged)

- `POST /v1/ingest/logs`, `POST /v1/ingest/metrics`
- `GET /v1/logs?q=&tag=&limit=`, `GET /v1/metrics?name=&tag=&start=&end=`
- `GET /v1/alerts`, `POST /v1/alerts`
- `GET /v1/stats`, `GET /healthz`, `GET /readyz`

### v2

- `POST /v2/logs`, `POST /v2/metrics`, `POST /v2/traces` - ingest via the buffer/stream-processor
  pipeline (same path OTLP uses).
- `GET /v2/logs/search?pipeline=<pipeql>&start=&end=` - pipelined log query.
- `GET /v2/logs/semantic?q=&k=` - semantic (embedding) search over indexed log messages.
- `GET /v2/metrics/query?query=<metricsql>&time=` - PromQL-like metric query.
- `GET /v2/traces/{id}` - fetch a tail-sampled-in trace by ID.
- `GET /v2/alerts/rules`, `POST /v2/alerts/rules` - list/create metricsql-backed alert rules.
- `POST /v2/alerts/silences` - create a maintenance silence.
- `POST /v2/deployments` - record a deployment event (feeds alert deploy-correlation + AIOps).
- `GET /v2/aiops/rootcause?window=` - root-cause hypotheses over recent anomalies/deploys/spikes.
- `POST /v2/profiling/capture?duration=`, `GET /v2/profiling/flamegraph?time=` - continuous
  profiling capture + flamegraph.
- `POST /v2/archive/ingest`, `GET /v2/archive/query?prefix=&predicates=col:op:val,...` - cold
  storage columnar archive + query-pushdown demo (`op` ∈ `=,!=,>,>=,<,<=`).
- `POST /v2/auth/token` - issue an RBAC bearer token (only when `CLA_RBAC_ENABLED=true`).
- `GET /v2/stats` - tsdb/logsdb/vector/tracing counters.

All `/v2/*` routes are open by default; set `CLA_RBAC_ENABLED=true` + `CLA_RBAC_SECRET` to require
a bearer token (`Authorization: Bearer <token>`) and enforce per-route permissions
(`logs:read`, `logs:write`, `metrics:read`, `metrics:write`, `alerts:read`, `alerts:write`,
`admin:*`), resolved against the tenant in `X-Tenant-ID` / `?tenant_id=` / the token's own claim.

### OTLP

- gRPC (`:4317`): `opentelemetry.proto.collector.{logs,metrics,trace}.v1.*Service/Export`
- HTTP (`:4318`): `POST /v1/logs`, `POST /v1/metrics`, `POST /v1/traces` (protobuf or JSON)

## Query language grammars

### metricsql (PromQL-like)

```
metric{label="val", ...}[5m]                          # range selector
sum|avg|min|max|count|quantile(op)(expr) by (labels)  # aggregation
rate(expr[5m]) | irate(expr[5m])                       # per-second increase, counter-reset-aware
avg_over_time|max_over_time|min_over_time|sum_over_time(expr[5m])
percentile(95, expr[5m])                               # interpolated, 0-100 scale, time-window
quantile(0.95, expr)                                   # cross-series aggregation, 0-1 scale
expr + expr | expr - expr | expr * expr | expr / expr
expr > N | expr < N | expr >= N | expr <= N | expr == N  # vector comparison filters (alerting)
```

### pipeql (pipelined log query)

```
search <field=value | "keyword" | *>
  | filter <cond> [and|or <cond>]...      # =, !=, >, <, >=, <=
  | stats <count()|sum|avg|min|max|p50|p95|p99>(<field?>) [, ...] [by (f1,f2)]
  | sort [asc|desc] [field]
  | limit N
```

## Honest capability matrix

Every item in the original brief, mapped to what actually exists in this repo. "Real" means
genuinely implemented, tested, and exercised end-to-end (see `internal/platform/platform_test.go`
for the live pipeline proof: publish → buffer → stream processor → tsdb/logsdb → query). Items
that are inherently tied to infrastructure this sandbox doesn't have (a Linux kernel with
`CAP_BPF`, a real S3 bucket, a labeled-incident dataset for training a model) are called out
explicitly rather than faked.

| Area | Status |
|---|---|
| Unified ingestion API, chunked TSDB, simple inverted index, static dashboards | **Real** - v1 stack, unchanged |
| Log parsing/structuring, tagging/filtering, regex/wildcard search, static webhook alerting | **Real** - `logsdb` query engine, v1 `alerts` |
| Distributed tracing (correlation IDs across services) | **Real** - `internal/tracing`, `SpanEntry`, `/v2/traces` |
| Stream aggregations (P95/P99, moving averages) pre-write | **Real** - `metricsql` percentile/quantile/`*_over_time`; alerting's anomaly detectors run on the live stream |
| Dynamic retention (compress, cold-storage, delete) | **Real** - tsdb/logsdb `OnExpireSegment` → `coldstore` archival → `internal/lifecycle` deletion tier |
| RBAC (team/service visibility) | **Real** - `internal/rbac`, tenant isolation, per-route permissions |
| Backpressure management, priority shedding | **Real** - `ingestbuffer` reject/block policies, debug-priority shedding |
| Custom query language (PromQL-like) | **Real** - `metricsql` |
| Advanced alert routing (Slack/PagerDuty, grouping, silencing) | **Real** - `internal/alerting`; requires a real webhook URL/routing key to actually deliver, but the HTTP client, payload shapes, and retry logic are real and tested against `httptest` fixtures |
| Anomaly detection (statistical, not static thresholds) | **Real** - rolling z-score + EWMA detectors |
| Native OTLP ingestion (gRPC + HTTP) | **Real** - hand-rolled wire decode (no `go.opentelemetry.io/proto/otlp` codegen dependency), tested with live gRPC/HTTP round trips |
| Kafka-scale buffering, HA WAL, quorum replication | **Real algorithm**, simulated topology - `ingestbuffer`'s WAL/replication/quorum logic is genuine, but "AZs" are local directories, not networked nodes; swapping in real network replication means implementing the `Replicator` interface against gRPC, which the package doc says explicitly |
| Tail-based trace sampling | **Real** - error/slow traces always kept, healthy traces deterministically sampled by trace-ID hash |
| Dynamic cardinality management | **Real** - `tsdb.CardinalityLimiter` rolls up over-threshold label values to `_other_` |
| Columnar storage + doc-value skippers | **Real** - `tsdb` segment block index (proven via a decoded-block counter in tests), `coldstore` row-group min/max skipping (proven via a decode-count assertion) |
| LogsDB-style host+time sort for compression | **Real** - explicit sort-before-flush in `logsdb`, compression-ratio asserted in tests |
| Trace-to-metric exemplars | **Real** - `tracing.ExemplarStore`, attached from span latency in the stream processor |
| Continuous profiling / flamegraphs | **Real** - `runtime/pprof` CPU capture + a hand-rolled pprof-protobuf decoder building a flamegraph tree |
| Pipelined query language | **Real** - `pipeql` |
| Semantic vector search | **Real algorithm, local-only embeddings** - a from-scratch HNSW index over a deterministic hashing/TF-IDF embedding (no external embedding API called); swap in `vectorsearch.Embedder` for a real neural model when one is available |
| Automated root-cause analysis (AIOps) | **Real, rule/statistics-based** - deploy + log-spike correlation with time-decayed confidence; the package doc is explicit this is *not* a trained ML model (no labeled incident corpus exists to train one against) |
| Tenant-aware fair queuing (shuffle sharding) | **Real** - deterministic per-tenant shard assignment + token-bucket limiting in `ingestbuffer` |
| Query pushdown to cold storage | **Real** - `coldstore.Archiver.QueryPushdown`, proven via row-group skip-count assertions |
| HA WAL across AZs | **Real algorithm, simulated topology** (see Kafka row above) |
| GenAI/LLM observability | **Real parsing** for OpenAI/Anthropic-shaped chat-completion JSON (tokens, cost, latency) via an `http.RoundTripper` wrapper; other providers fall back to latency/status-only capture unless their JSON shape matches |
| eBPF capture (RED metrics, TLS visibility) | **Real loader code, untestable here** - `internal/ebpf`'s Linux build (`//go:build linux`) uses `github.com/cilium/ebpf` correctly and documents the intended kprobe/uprobe programs, but this sandbox has no Linux kernel/`CAP_BPF` to actually load and run eBPF bytecode against; a portable, fully-tested userspace fallback (`ebpf_fallback.go`) provides the same `REDEvent` shape on non-Linux |
| Trace-log correlation via eBPF write-syscall interception | **Documented + userspace equivalent implemented** (`tracing.InjectContext`); the kernel-level interception itself needs the same Linux/`CAP_BPF` environment as above |
| Database wire protocol parsing (Postgres/MySQL/Redis/Mongo) | **Real** - byte-level parsers for all four, tested against hand-built protocol fixtures |
| MCP (Model Context Protocol) tracing | **Real** - JSON-RPC 2.0 request/response correlation over `io.ReadWriter` or `http.RoundTripper` |
| Multi-tenant isolation | **Real** - `rbac.TenantScoper`, deny-by-default cross-tenant access |

**Not implemented, and why**: a trained anomaly-detection/RCA model (no labeled incident dataset to
train on - the statistical/rule-based AIOps above is the legitimate real-world alternative used by
most observability vendors before they have enough labeled data); a hosted neural embedding model
for vector search (no external API dependency by design, so it works offline - point
`vectorsearch.Embedder` at a real model when you have one); genuinely networked WAL replication
across real availability zones (needs real multi-host infrastructure this repo doesn't provision -
the `Replicator` interface is the extension point).
