# custom-log-aggregator — Infra, Dependency & Complexity Analysis

Audience: LLM agents writing/running tests. Nothing here has been executed.

## 1. What it is
Self-contained observability platform (logs + metrics + traces + profiling + alerting + AIOps) in Go: 139 Go files / 24.2k LOC, 2 eBPF C files, Next.js dashboard (23 TS/TSX files). Notable: almost everything is hand-built on the standard library — own TSDB, logs DB, query languages, vector search, cold store, queue, DB wire protocol. Only Go deps: `cilium/ebpf`, `grpc`, `protobuf`.

## 2. Deployables (`cmd/`, `docker-compose.yml`)
| Service | Port | Role |
|---|---|---|
| `aggregator` | 8080 | HTTP API v1 + v2 + `/internal/*`; storage engines; query |
| `ingest-gateway` | 4317 (OTLP gRPC), 4318 (OTLP/HTTP) | receives OTLP, buffers, forwards to aggregator `/internal/{logs,metrics,traces}` |
| `alerting-service` | 8083 | rule evaluation (queries aggregator `/internal/metrics/query_range`), silences, notifications (Slack `CLA_SLACK_WEBHOOK_URL`, PagerDuty `CLA_PAGERDUTY_ROUTING_KEY`) |
| `agent` | – | host agent: tails/collects and ships (has 2 tests) |
| `dashboard` (web/dashboard) | 3000 | Next.js UI: logs, search, metrics, traces, alerts, archive, aiops, profiling |

## 3. Internal packages
Ingest path: `otlp`, `ingestgateway`, `ingestbuffer`, `ingest`, `queue`, `processor`.
Storage: `logsdb`, `tsdb`, `storage`, `coldstore` (archive tier), `lifecycle` (retention/tiering), `vectorsearch` (semantic log search), `dbwire` (DB wire protocol, 5 tests).
Query: `querylang/pipeql` (log pipelines), `querylang/metricsql`, `api`.
Features: `alerting` (6 tests), `alertingsvc`, `alerts`, `tracing`, `mcptrace`, `llmobs` (LLM observability), `aiops` (root cause), `profiling` (flamegraphs), `ebpf` (+`bpf/*.c`), `rbac`, `platform`, `domain`, `util`.

## 4. HTTP surface (extracted)
v1: `/v1/ingest/logs`, `/v1/ingest/metrics`, `/v1/logs`, `/v1/metrics`, `/v1/traces`, `/v1/alerts`, `/v1/stats`, `/healthz`, `/readyz`.
v2: `POST /v2/auth/token`, `POST /v2/logs`, `GET /v2/logs/search`, `GET /v2/logs/semantic`, `POST /v2/metrics`, `GET /v2/metrics/query`, `POST /v2/traces`, `GET /v2/traces/{id}`, `GET|POST /v2/alerts/rules`, `POST /v2/alerts/silences`, `POST /v2/archive/ingest`, `GET /v2/archive/query`, `GET /v2/aiops/rootcause`, `POST /v2/profiling/capture`, `GET /v2/profiling/flamegraph`, `POST /v2/deployments`, `GET /v2/stats`.
Internal: `POST /internal/{logs,metrics,traces}`, `POST /internal/metrics/query_range`.
Query params seen: `q query pipeline predicates start end time window duration limit k name tag prefix tenant_id`. Headers: `Authorization`, `X-Tenant-ID`, `Content-Type`.

## 5. Existing tests
34 test files: `internal/alerting(6)`, `dbwire(5)`, `coldstore(4)`, `cmd/agent(2)`, `tests/(1)`, one each in vectorsearch, tsdb, tracing, rbac, pipeql, metricsql, profiling, platform, otlp, mcptrace, logsdb, llmobs, lifecycle, ingestbuffer, ebpf, aiops. Docs: `docs/{ARCHITECTURE,CODEBASE_REFERENCE}.md`.

## 6. Complexity & risk hot-spots
| # | Area | Risk |
|---|---|---|
| 1 | Custom TSDB (`tsdb`) | out-of-order/duplicate samples, series cardinality explosion, chunk compaction, WAL/replay, staleness, counter resets in rate() |
| 2 | Custom logs DB (`logsdb`, `storage`) | index/segment rotation, concurrent ingest+query, crash consistency, retention deleting segments being read |
| 3 | Ingest gateway buffer (`ingestbuffer`, `queue`) | back-pressure, bounded memory, data loss on crash (buffer in RAM?), duplicate forward on retry (at-least-once ⇒ dedupe?), ordering |
| 4 | OTLP decode (`otlp`) | protobuf + JSON encodings, gzip, partial success semantics, huge batches |
| 5 | Query languages | parser robustness (fuzz), expensive queries (regex, unbounded time range, high-cardinality group by) ⇒ query DoS; limit/timeout enforcement |
| 6 | Multi-tenancy (`rbac`, `X-Tenant-ID`, `tenant_id` param) | tenant taken from header/param vs token ⇒ cross-tenant read/write |
| 7 | Alerting | `for` duration state machine (pending→firing→resolved), flapping, silences, dedupe/grouping, missed evaluations when aggregator slow, notification retry & exactly-once-ish, eval during aggregator outage (no-data semantics) |
| 8 | Cold store / lifecycle | hot→cold move atomicity; archive query merging hot+cold without gaps/dupes |
| 9 | Vector search | embedding dimension, k bounds, recall, index rebuild under ingest |
| 10 | `/internal/*` on same port 8080 as public API | must be network-isolated or authenticated |
| 11 | eBPF / profiling | privileged, kernel-version dependent; capture must be bounded and non-blocking; graceful no-op when unavailable (containers, macOS) |
| 12 | `dbwire` | custom wire protocol parser — untrusted bytes |
| 13 | AIOps / LLM obs | nondeterminism; cost/timeouts; prompt-injection via log content |

## 7. Invariants
1. **No silent loss**: every record acknowledged with 2xx by gateway/aggregator is queryable after flush interval (and after restart, per durability contract).
2. **No duplication beyond contract**: retries by client/gateway do not create duplicate log lines / double-count metrics (or duplicates are bounded and documented).
3. **Tenant isolation**: a query under tenant A never returns B's data on any endpoint (search, semantic, metrics, traces, archive, stats, alerts, aiops).
4. **Query correctness**: results of pipeql/metricsql equal a brute-force reference over the same raw input.
5. **Time semantics**: `[start,end)` bounds respected; results ordered by timestamp; late/out-of-order data placed correctly.
6. **Alert state machine**: fires iff condition true continuously ≥ `for`; resolves once; silenced alerts send nothing; one notification per transition per group.
7. **Retention**: data older than retention is not returned; data newer is never deleted; hot+cold union is gap-free and duplicate-free.

## 8. Run
`docker compose up --build` (aggregator 8080, gateway 4317/4318, alerting 8083, dashboard 3000). Go tests: `go test ./...`. eBPF tests need Linux + privileges.

## 9. Caveat
Request body schemas, the `/v2/auth/token` contract, storage paths/volumes and env var names (only the two `CLA_*` notification vars were found) were not read; confirm in `internal/api`, `internal/platform`, `docker-compose.yml`.
