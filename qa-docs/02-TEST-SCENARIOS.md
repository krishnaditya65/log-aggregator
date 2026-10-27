# custom-log-aggregator — Detailed Test Reference

Audience: LLM/QA agents. Nothing executed. **[src]** = read from source. Scripts: `03-TEST-SCRIPTS.md` (note: body shapes there must follow §1 below — v2 ingest bodies are **JSON arrays** of entries).
Format: ID · Setup · Action · Expected · Priority · Tags.

---------------------------------------------------------------------------------------------------
## 1. System facts [src]
| Area | Fact |
|---|---|
| Services | aggregator :8080 (v1, v2, `/internal/*` on the **same** port, published in compose), ingest-gateway :4317 gRPC / :4318 HTTP, alerting-service :8083, agent (flags), dashboard :3000 |
| Env | `CLA_DATA_DIR=/data`, `CLA_HTTP_ADDR=:8080`, `CLA_QUEUE_SEGMENT_SIZE=1048576`, `CLA_DASHBOARD_ORIGIN=http://localhost:3000` (CORS), `CLA_HOT_RETENTION=168h` (then segments archive to cold), **`CLA_RBAC_ENABLED=false`** (default in compose), `CLA_RBAC_SECRET`, `CLA_OTLP_GRPC_ADDR`, `CLA_OTLP_HTTP_ADDR`, `AGGREGATOR_URL`, `ALERTING_SERVICE_ADDR`, `CLA_SLACK_WEBHOOK_URL`, `CLA_PAGERDUTY_ROUTING_KEY`, `CLA_S*` (storage settings) |
| Agent flags | `--tail <file>`, `--tail-poll-interval 500ms`, `--scrape-url`, `--scrape-interval 15s`, `--forward-url http://localhost:8080/ingest`, `--forward-interval 5s`, `--batch-size 100`, `--queue-size 10000` |
| v1 routes (no auth wrapper) | `/healthz`, `/readyz` (always 200, shallow), `/v1/ingest/logs`, `/v1/ingest/metrics`, `/v1/logs?q=&tag=&limit=` (default limit 50), `/v1/metrics`, `/v1/alerts`, `/v1/stats` |
| v2 routes + permission (enforced only when RBAC enabled) | `POST /v2/logs` logs:write · `POST /v2/metrics` metrics:write · `POST /v2/traces` logs:write · `GET /v2/logs/search` logs:read · `GET /v2/logs/semantic` logs:read · `GET /v2/metrics/query` metrics:read · `GET /v2/traces/{id}` logs:read · `GET /v2/alerts/rules` alerts:read · `POST /v2/alerts/rules` alerts:write · `POST /v2/alerts/silences` alerts:write · `POST /v2/deployments` alerts:write · `GET /v2/aiops/rootcause` metrics:read · `POST /v2/profiling/capture` admin:* · `GET /v2/profiling/flamegraph` admin:* · `POST /v2/archive/ingest` admin:* · `GET /v2/archive/query` admin:* · `GET /v2/stats` metrics:read · `POST /v2/auth/token` **unauthenticated** |
| Internal (no auth) | `POST /internal/logs`, `/internal/metrics`, `/internal/traces`, `/internal/metrics/query_range` |
| Token issue | `POST /v2/auth/token {user_id, tenant_id, ttl_seconds}` → `{token, expires_in}`; ttl ≤ 0 → 3600; **no credential check**: issues a valid signed token for any user/tenant; 404 `RBAC disabled on this deployment (CLA_RBAC_ENABLED=false)` when RBAC off |
| Token format | hand-rolled HS256-like: `b64url(header).b64url(payload{sub,tenant_id,iat,exp}).b64url(HMAC-SHA256)`; empty secret → `rbac: secret must not be empty` |
| Tenant resolution | `X-Tenant-ID` header → `tenant_id` query → token tenant; `Store.Authorize(user, action, tenant)` must find a binding; errors `unauthorized: no authenticated principal` (401), `forbidden: …` / `forbidden: insufficient permissions` (403), `Authorization header must use Bearer scheme` |
| RBAC model | users{id,email,teams}, teams, roles{id,name,permissions[{action,resource}]}, tenants, bindings{user_id\|team_id, role_id, tenant_id}; wildcard `admin:*` (tests assert `admin:*` must not grant `billing:read`) |
| Ingest bodies | v2 logs = `[LogEntry]`; LogEntry `{id,time,service,host,level,message,trace_id,span_id,tenant_id,tags,fields,raw}` (missing time → now; missing id → generated); Metric `{id,name,time,service,value,unit,tenant_id,tags,exemplar}`; Span `{trace_id,span_id,parent_span_id,service,name,start_time,end_time,status,tenant_id,tags,attributes}` — **tenant_id comes from the body** |
| Other bodies | v2 alert rule (aggregator) domain `{id,name,metric,threshold,window,operator,webhook_url,tags,enabled,…}`; alerting-service rule `{id,name,expr,for_seconds,severity,labels,annotations}`; silence `{matchers,start,end}`; deployment `{service,version,time}`; archive `{rows,…}`; forward sink `{entry, priority}` |
| Query errors | `missing ?query=` (metrics), `missing ?pipeline=` (pipeql), `invalid or missing ?time=`, `trace not found or not yet complete` (404), `profile not found` (404), `method not allowed` (405) |
| Constants | aiops window 15 min, minSamples 2/10, maxEvents 1000, maxProfiles 50, vector DefaultDims 128, LLM MaxPromptChars/MaxCompletionChars 2000, fingerprint 64, WAL trailer 4 B, tsdb blockSize 128 |
| Alert engine | per rule `ruleBreachState{firstBreach, firing}`; fires when continuously breached ≥ `For`; reset when not breached |
| Notifications | Slack blocks payload; PagerDuty Events v2 `{routing_key, event_action, dedup_key, payload{summary,severity,source}}` |

---------------------------------------------------------------------------------------------------
## 2. Environments & data
* **E-OPEN**: compose as shipped (RBAC off). **E-RBAC**: `CLA_RBAC_ENABLED=true`, `CLA_RBAC_SECRET=<32B>`, RBAC store seeded: tenants A, B; users `alice` (A admin), `bob` (A viewer: logs:read, metrics:read, alerts:read), `ing` (A ingest: logs:write, metrics:write), `carol` (B admin), `svc` (bindings A+B).
* **Datasets**: DS-LOG-10k (deterministic: service∈{api,db,web}, level∈{info,warn,error}, message templates with numbers, tags host/env, unique `run` tag), DS-MET (counter with resets, gauge, histogram-like), DS-TRACE (100 traces × 5–50 spans incl. orphans), DS-OTLP (protobuf & JSON fixtures for logs/metrics/traces incl. gzip).
* **Oracle**: every ingested record carries unique id; reference computations done in Python over the same dataset.

---------------------------------------------------------------------------------------------------
## 3. Smoke
CLA-SMK-001 `/healthz` `/readyz` 200 (note shallow) · 002 E-RBAC `POST /v2/auth/token` → token · 003 `POST /v2/logs` `[entry]` → 2xx; `GET /v2/logs/search` finds it · 004 metric ingest + `GET /v2/metrics/query?query=` · 005 trace ingest + `GET /v2/traces/{id}` · 006 OTLP/HTTP to :4318 reaches aggregator · 007 `/v2/stats` keys `tsdb_decoded_blocks, logsdb_segment_stats, vector_index_size, tracing_pending_traces` · 008 dashboard pages 200 · 009 alerting-service health and rule list.

---------------------------------------------------------------------------------------------------
## 4. Authentication & authorization (E-RBAC) — P0 block
| ID | Action | Expected | Pri |
|---|---|---|---|
| CLA-AUTH-001 | `POST /v2/auth/token {"user_id":"alice","tenant_id":"A"}` with **no credentials** | **Current: 200 valid token → anyone can mint tokens for any user/tenant (critical bypass)**; expected: require credential (password/API key/service secret) | P0 |
| CLA-AUTH-002 | mint token for `carol`/B, read B data | proves 001 impact | P0 |
| CLA-AUTH-003 | `ttl_seconds` 0 / -1 / 1 / 315360000 (10 y) / 9e18 | ≤0 → 3600; huge → overflow of `time.Duration(ttl)*time.Second`? (negative exp → immediately expired or never-expiring) → cap required | P1 |
| CLA-AUTH-004 | `user_id`/`tenant_id` empty, 1 MB, unicode, `"../"` | rejected | P2 |
| CLA-AUTH-005 | no Authorization on each v2 route | 401 `unauthorized…` | P0 |
| CLA-AUTH-006 | `Authorization: Basic …`, `bearer x`, `Bearer` | 401 `Authorization header must use Bearer scheme` / invalid | P1 |
| CLA-AUTH-007 | token tampering: payload tenant changed, signature truncated, alg header changed to `none`, extra segment, base64 std vs url | 401 (signature over header+payload; verify `alg` ignored) | P0 |
| CLA-AUTH-008 | expired token (`exp` past), `exp` boundary | 401 | P0 |
| CLA-AUTH-009 | signature compare constant-time (`hmac.Equal`?) | code review + timing | P2 |
| CLA-AUTH-010 | secret rotation: change `CLA_RBAC_SECRET`, restart | old tokens rejected | P1 |
| CLA-AUTH-011 | permission matrix: alice/bob/ing/carol/svc × 17 v2 routes × tenant {A,B} via header, query, token | allowed iff binding grants action in that tenant (table auto-generated; 170 cells) | P0 |
| CLA-AUTH-012 | bob (A viewer) with `X-Tenant-ID: B` | 403 (no binding) | P0 |
| CLA-AUTH-013 | svc with `X-Tenant-ID: B` vs `?tenant_id=A` together | header wins [src]; authorization against B; data returned must be B's | P0 |
| CLA-AUTH-014 | **authorization tenant vs data tenant**: bob authorized for A sends `X-Tenant-ID: A` but search handler filters by ? (query param / body / nothing) | the handler must filter by the *authorized* tenant; if handler ignores tenant → cross-tenant leak | P0 |
| CLA-AUTH-015 | ingest: `ing` (A) posts entries with `"tenant_id":"B"` | **body tenant_id must be overridden/validated** against authorized tenant; currently body-controlled → cross-tenant data poisoning | P0 |
| CLA-AUTH-016 | `admin:*` holder calls non-admin routes (`logs:read`) | allowed or not — wildcard semantics documented; `admin:*` ≠ `billing:read` (existing test) | P1 |
| CLA-AUTH-017 | v1 routes with RBAC enabled | **no auth** → full read/write bypass of RBAC (P0) | P0 |
| CLA-AUTH-018 | `/internal/*` from outside | unauthenticated + published port → ingest/query bypass (P0); must be network-isolated | P0 |
| CLA-AUTH-019 | E-OPEN: `/v2/auth/token` | 404 with message | P2 |
| CLA-AUTH-020 | CORS: `Origin` ≠ `CLA_DASHBOARD_ORIGIN` | no ACAO for foreign origins | P1 |

---------------------------------------------------------------------------------------------------
## 5. Log ingest & search
| ID | Action | Expected | Pri |
|---|---|---|---|
| CLA-LOG-001 | `POST /v2/logs` array of 1, 100, 10k entries | 2xx; all searchable after flush; count exact | P0 |
| CLA-LOG-002 | entry without `time` / without `id` | time = now UTC; id generated unique | P1 |
| CLA-LOG-003 | entry with client `id` duplicated across two posts | dedupe? or two records — document; affects retry semantics | P1 |
| CLA-LOG-004 | body object instead of array, `[]`, `null`, `[{}]`, invalid JSON, trailing data | 400 / 2xx no-op / 400 / defined / 400 / 400 (`decodeBody` strictness) | P1 |
| CLA-LOG-005 | `time` formats: RFC3339, RFC3339Nano, no zone, epoch number, year 0001, 9999, far future | parse errors → 400; zero → now | P1 |
| CLA-LOG-006 | `level` values outside set, uppercase | stored; search by level case semantics | P2 |
| CLA-LOG-007 | message 0 B, 1 MB, 64 MB; 10k `fields`; `raw` binary/base64 | limits enforced (body size cap?) — absence of `MaxBytesReader` → memory DoS check | P1 |
| CLA-LOG-008 | unicode, invalid UTF-8, NUL, ANSI, CRLF, `<script>` | stored verbatim; JSON-escaped; UI escapes (XSS) | P1 |
| CLA-LOG-009 | v1 `/v1/ingest/logs` same payloads | parity with v2 | P2 |
| CLA-LOG-010 | `GET /v1/logs?q=error` / `&tag=host:h1` / `&limit=0,-1,1,1e9,abc` | limit ≤0/abc → 50; huge → bounded? | P1 |
| CLA-LOG-011 | `GET /v2/logs/search` with query language (pipeql via `?pipeline=`): filter, parse, stats count by, sort, limit | equals oracle on DS-LOG-10k | P0 |
| CLA-LOG-012 | search time range `start`/`end` RFC3339; inverted; invalid (silently ignored by `timeRangeParams`!) | invalid → currently ignored (full scan) → should 400 | P2 |
| CLA-LOG-013 | ordering of results (time desc?) deterministic for ties | documented | P2 |
| CLA-LOG-014 | `GET /v2/logs/semantic?q=&k=` | k results; planted near-duplicate ranks first; dims 128 | P1 |
| CLA-LOG-015 | semantic k=0, k=1e6, empty q | 400/clamped | P2 |
| CLA-LOG-016 | read-after-write latency | p99 ≤ flush interval | P1 |
| CLA-LOG-017 | 1M entries search with regex predicate `(a+)+$` | timeout / RE2 linear (Go regexp is RE2 → safe) — verify custom engine | P1 |

## 6. Metrics
| ID | Action | Expected | Pri |
|---|---|---|---|
| CLA-MET-001 | ingest gauge series; `GET /v2/metrics/query?query=name{tag="x"}` | values | P0 |
| CLA-MET-002 | missing `query` | 400 `missing ?query=` | P1 |
| CLA-MET-003 | metricsql functions: rate over counter with reset, sum by, avg/min/max, arithmetic, offset | oracle equality (rate never negative) | P0 |
| CLA-MET-004 | out-of-order, duplicate timestamp, NaN, ±Inf, 1e308 values | policy consistent; no panic | P1 |
| CLA-MET-005 | 1M series cardinality | limit/back-pressure; memory | P1 |
| CLA-MET-006 | `/internal/metrics/query_range` (used by alerting) contract `{matchers,start,end}` | correct points | P0 |
| CLA-MET-007 | tsdb block boundaries (128 samples) — query spanning block edge | no gap/dup | P1 |
| CLA-MET-008 | exemplars with trace_id link | returned | P3 |

## 7. Traces
CLA-TRC-001 spans of one trace posted out of order and across requests → `GET /v2/traces/{id}` assembled tree · 002 trace still pending → 404 `trace not found or not yet complete` → after completion window 200 · 003 orphan span (missing parent) included/flagged · 004 cyclic parent refs → no infinite loop · 005 end < start → rejected/flagged · 006 100k spans trace → bounded · 007 `tracing_pending_traces` stat reflects · 008 malformed id → 404/400.

## 8. OTLP ingest-gateway
| ID | Action | Expected | Pri |
|---|---|---|---|
| CLA-OTLP-001 | OTLP/HTTP protobuf & JSON for logs/metrics/traces (`/v1/logs`, `/v1/metrics`, `/v1/traces` on :4318) | 200; data at aggregator with resource/service mapping | P0 |
| CLA-OTLP-002 | OTLP gRPC :4317 via `telemetrygen` | same | P0 |
| CLA-OTLP-003 | gzip encoded; corrupt gzip; gzip bomb | 200 / 400 / bounded | P1 |
| CLA-OTLP-004 | partial success (mixed valid/invalid records) | `partial_success.rejected_*` per spec | P2 |
| CLA-OTLP-005 | aggregator down → gateway buffer (`ingestbuffer`, priority) | buffered up to cap, then 503/RESOURCE_EXHAUSTED; drained in order on recovery | P0 |
| CLA-OTLP-006 | gateway forward retries → duplicates? | dedupe or documented at-least-once | P1 |
| CLA-OTLP-007 | tenant mapping for OTLP (header? resource attr?) | defined; spoof via resource attribute prevented | P0 |
| CLA-OTLP-008 | gateway SIGTERM with full buffer | flush or persist; loss quantified | P1 |

## 9. Alerting
### 9.1 aggregator rules (`/v2/alerts/rules`)
CLA-ALR-001 create rule `{name, metric, threshold, window, operator, webhook_url, enabled}` → listed · 002 invalid operator/negative window/threshold NaN/empty name/duplicate id → 400 · 003 webhook_url internal (SSRF) → rejected · 004 disabled rule never fires · 005 v1 `/v1/alerts` parity.
### 9.2 alerting-service engine
| ID | Timeline (eval interval I, `for_seconds`=F) | Expected | Pri |
|---|---|---|---|
| CLA-ALE-001 | breach continuous for F−I | not firing, no notification | P0 |
| CLA-ALE-002 | breach continuous ≥ F | firing once; exactly one notification | P0 |
| CLA-ALE-003 | breach F/2, gap one eval, breach F/2 | not firing (`firstBreach` reset) | P0 |
| CLA-ALE-004 | firing then condition false | resolved notification once; state reset | P0 |
| CLA-ALE-005 | `for_seconds` 0 | fires on first breach | P1 |
| CLA-ALE-006 | flapping every eval | no notification storm (dedupe/grouping) | P1 |
| CLA-ALE-007 | silence matching labels during firing | no notification; after expiry and still firing → notify (document) | P0 |
| CLA-ALE-008 | silence `start > end`, past window, empty matchers (match-all!) | validated; match-all requires explicit flag | P1 |
| CLA-ALE-009 | data source `/internal/metrics/query_range` returns 5xx/timeout | no-data handling: keep state, no false resolve/fire | P0 |
| CLA-ALE-010 | alerting-service restart while firing | state in memory → re-fire on restart? measure duplicate notifications | P1 |
| CLA-ALE-011 | two alerting replicas | duplicate notifications (no leader) — document | P2 |
| CLA-ALE-012 | Slack payload `{text, blocks}` schema; PagerDuty `{routing_key, event_action: trigger/resolve, dedup_key stable per rule}` | schema-valid; resolve uses same dedup_key | P0 |
| CLA-ALE-013 | Slack 429 / PD 5xx / timeouts | retry with backoff; eval loop not blocked | P1 |
| CLA-ALE-014 | secrets in logs (`routing_key`, webhook URL) | redacted | P1 |
| CLA-ALE-015 | 10k rules | evaluation lateness ≤ interval | P2 |
| CLA-ALE-016 | rule expr parse errors / unknown metric | rule marked errored, not crash | P1 |

## 10. AIOps, deployments, profiling, LLM obs, archive
| ID | Action | Expected | Pri |
|---|---|---|---|
| CLA-AIO-001 | `POST /v2/deployments {service,version,time}` then error spike → `GET /v2/aiops/rootcause` | deployment correlated within 15-min window | P1 |
| CLA-AIO-002 | fewer than minSamples data points | graceful "insufficient data" | P2 |
| CLA-AIO-003 | > maxEvents (1000) deployments | bounded memory (oldest evicted) | P2 |
| CLA-PRF-001 | `POST /v2/profiling/capture` (admin) | profile captured; ≤ maxProfiles (50) retained | P2 |
| CLA-PRF-002 | `GET /v2/profiling/flamegraph?…` unknown id | 404 `profile not found` | P2 |
| CLA-PRF-003 | eBPF unavailable (macOS/container w/o CAP_BPF) | graceful 501/empty; service healthy | P1 |
| CLA-LLM-001 | llmobs ingestion with prompt > 2000 chars | truncated to MaxPromptChars | P2 |
| CLA-LLM-002 | prompt injection text in logs fed to AIOps/LLM | no tool/actions triggered; output treated as data | P1 |
| CLA-ARC-001 | `POST /v2/archive/ingest {rows}` then `GET /v2/archive/query` | rows returned | P1 |
| CLA-ARC-002 | hot retention 168h boundary: data at 167h59m (hot) vs 168h01m (cold) | each queryable exactly once across hot+cold | P0 |
| CLA-ARC-003 | lifecycle move during queries | no gap/dup | P0 |
| CLA-ARC-004 | cold store object corrupted | detected (checksum), quarantined | P1 |

## 11. Agent
CLA-AGT-001 tail file appends → forwarded batches of ≤100 every 5 s · 002 rotation rename+create · 003 copytruncate · 004 file deleted while tailing · 005 multiline (stack traces) · 006 restart resumes from checkpoint (no dup/loss) · 007 forward target down 1 h → queue 10000 cap; drop policy & counters · 008 scrape-url Prometheus text parse incl. malformed lines · 009 `--forward-url` default `/ingest` vs actual routes (`/v1/ingest/logs`) — **likely mismatch** → verify default works.

## 12. Storage engines & dbwire
CLA-STO-001 logsdb: ingest → segment rotation (`CLA_QUEUE_SEGMENT_SIZE` 1 MB) → compaction → query equal · 002 crash at each stage (fail-points) → reopen no partial segment · 003 WAL trailer 4 B: truncate tail at every offset → recover · 004 tsdb chunk encode/decode property test (random series incl. NaN, equal timestamps) · 005 `dbwire` protocol fuzz (5 existing tests) · 006 disk full → ingest 5xx, no corruption · 007 data dir permission errors at start → clear error.

## 13. E2E
CLA-E2E-001 OTel SDK app → gateway → aggregator → dashboard: log/metric/trace correlation by trace_id · 002 incident: error rate ↑ → alert pending → firing → Slack mock → silence → deployment marker → rootcause → recovery → resolved · 003 retention cycle (hot→cold→query) · 004 multi-tenant (E-RBAC with AUTH-001 fixed or tokens minted by admin): two tenants identical data; every read endpoint isolated · 005 agent tailing nginx logs end-to-end.

## 14. Concurrency, consistency
CLA-RACE-001 `go test -race ./...` + race binaries · 002 64 writers + 16 readers 5 min → exact counts, readers see monotonic prefix · 003 ingest during rotation/compaction/flush · 004 query during retention move · 005 concurrent creation of same series · 006 rule update during eval · 007 gateway buffer producer > forwarder + shutdown · 008 vector index insert+search · CLA-CON-001 exact-count oracle under each chaos case · 002 counter sums · 003 v1 vs v2 agree · 004 hot∪cold exact · 005 `/v2/stats` vs actual.

## 15. Load / stress / spike / soak / scalability
| ID | Profile | Criteria |
|---|---|---|
| CLA-LOAD-001 | 50k logs/s in batches of 500, 15 min | ack p99 < 200 ms; 0 loss; lag < 5 s |
| CLA-LOAD-002 | 200k samples/s, 100k series | sustained |
| CLA-LOAD-003 | 5k spans/s | trace fetch p99 < 300 ms |
| CLA-LOAD-004 | 50 qps mixed queries during 001 | p99 < 2 s; ingest impact < 10 % |
| CLA-LOAD-005 | telemetrygen OTLP 20k logs/s gRPC | parity with direct |
| CLA-STR-001 | ramp ingest to failure | 429/503 at gateway; no OOM |
| CLA-STR-002 | cardinality bomb | limit trips |
| CLA-STR-003 | expensive query flood | concurrency limit; ingest healthy |
| CLA-STR-004 | 100 MB single request body ×10 concurrent | no OOM (body cap needed) |
| CLA-SPK-001 | 10× burst 60 s | lag recovers < 5 min |
| CLA-SOAK-001 | 72 h 30 % with retention cycling | plateau |
| CLA-SCAL-001..004 | data size, tenants, gateways, rules | sub-linear / linear |

## 16. Chaos / durability
| ID | Fault | Expected |
|---|---|---|
| CLA-CHA-001 | kill -9 aggregator under ingest | acked data present after restart (document fsync policy/loss window) |
| CLA-CHA-002 | kill -9 gateway | buffer loss quantified; SDK retries |
| CLA-CHA-003 | aggregator down 5 min | gateway buffers → 503; drains in order |
| CLA-CHA-004 | aggregator slow 2 s | gateway backoff, no storm |
| CLA-CHA-005 | alerting restart while firing | see ALE-010 |
| CLA-CHA-006 | aggregator unreachable from alerting | ALE-009 |
| CLA-CHA-007 | Slack/PD down | bounded retries |
| CLA-CHA-008 | kill during flush/compaction loop | no partial segments |
| CLA-CHA-009 | corrupt segment | quarantine |
| CLA-CHA-010 | disk full / RO / slow disk | clean errors |
| CLA-CHA-011 | clock skew agents ±10 min | source timestamps preserved; alert timers monotonic |
| CLA-CHA-012 | SIGTERM each service | graceful drain |
| CLA-CHA-013 | OOM during big query | restart, data intact |
| CLA-DUR-001..006 | restart preserves all signals/rules/silences/alert states; WAL replay idempotent ×10; old on-disk format opens; cold checksums | as stated |

## 17. Known findings from source
1. **`/v2/auth/token` mints tokens for any user/tenant without credentials (P0).** 2. v1 API and `/internal/*` unauthenticated on public port (P0). 3. Ingest trusts body `tenant_id` (P0). 4. RBAC disabled by default. 5. `/healthz`/`/readyz` always 200. 6. Invalid `start`/`end` silently ignored. 7. TTL overflow in token issuance. 8. Agent default `--forward-url …/ingest` may not match any route. 9. Alert state in memory only.

## 18. Exit criteria
All P0 pass (requires fixes for §17.1–3); exact-count oracle zero loss/dup under LOAD-001 + CHA-001/003/008/012; isolation matrix 100 %; alert golden timelines pass; fuzzers clean; soak plateau.
