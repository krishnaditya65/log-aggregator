# custom-log-aggregator — Performance, Profiling & Benchmarking Reference (04)

Audience: LLM agents / engineers. Companion to 01/02/03. **[src]** = read from source. Targets are hypotheses to calibrate. Nothing executed.

## 0. Limits
Covers ingest throughput, end-to-end visibility latency, query latency, storage efficiency, and resource behaviour for the listed workloads. Passing does not prove the absence of performance defects for other log shapes, cardinalities, retention sizes or hardware.

## 1. Performance model [src]

| Process | Port | Role |
|---|---|---|
| `aggregator` (`cmd/aggregator`) | 8080 (`CLA_HTTP_ADDR`) | HTTP v1 + v2 + `/internal/*`; storage engines; query |
| `ingest-gateway` | 4317 gRPC / 4318 HTTP (`CLA_OTLP_GRPC_ADDR`, `CLA_OTLP_HTTP_ADDR`) | OTLP receivers |
| `alerting-service` | 8083 | evaluates rules by querying aggregator `/internal/metrics/query_range` |
| `agent` (`cmd/agent`) | — | log shipper |
| dashboard | 3000 | Next.js UI |

Ingest path: `POST /v2/logs` (JSON array of LogEntry) / `POST /internal/logs` / OTLP → `ingestbuffer` (partitions, `shuffleshard.go`, `tokenbucket.go` per-tenant rate, `replicator.go`) → **WAL that fsyncs on every `Append` [src ingestbuffer/wal.go:19,130]** → processors → `logsdb` (segments + tokenizer index, compression stats), `tsdb` (metrics), `tracing`, `vectorsearch` (semantic), `coldstore`/archive. Also `internal/queue/file_queue.go`: fsync per write [src :74], in-memory channel 1024.

Backpressure [src ingestbuffer/types.go]: per-partition backlog vs `MaxPartitionDepth`; `PolicyReject` (non-debug → `ErrBackpressure`; debug-priority → `ErrDropped`) or `PolicyBlock`. Debug priority is always shed first.

Query path: `GET /v2/logs/search`, `/v1/logs`, `/v2/logs/semantic` (vector), `/v2/metrics/query` (MetricsQL-like `internal/querylang/metricsql`), `/v2/traces/{id}`, `/v2/archive/query`, `/v2/aiops/rootcause`, `/v2/profiling/flamegraph`.

Self-observability [src]:
- `GET /v2/stats` (permission `metrics:read`) returns at least `tsdb_decoded_blocks`, `logsdb_segment_stats` (compression), `vector_index_size`, `tracing_pending_traces`, accepts `start` (VERIFY full field list in `internal/api/v2.go` ≈ line 540).
- `POST /v2/profiling/capture` (permission `admin:*`) → built-in `internal/profiling` (runtime/pprof CPU sampling, stores raw pprof bytes, hand-decodes pprof proto) → `GET /v2/profiling/flamegraph`. **Use it, but validate against `go tool pprof` (§7) because the decoder is custom.**
- **No Prometheus `/metrics` endpoint** for the process itself (`/v1/metrics` and `/v2/metrics` are the product's own TSDB API). Process metrics must come from OS tools or an added `promhttp` handler in a perf build.

Env [src]: `CLA_DATA_DIR`, `CLA_HOT_RETENTION`, `CLA_RBAC_ENABLED`, `CLA_RBAC_SECRET`, `CLA_DASHBOARD_ORIGIN`, Slack/PagerDuty keys.

## 2. Environment rules
Linux host; WAL/data on a dedicated NVMe (record model, filesystem, mount options — fsync-per-append makes the device the bottleneck). Record Go version, GOMAXPROCS, GOGC/GOMEMLIMIT, `CLA_*`. Generator on separate host/cores. Fresh `CLA_DATA_DIR` per run or a recorded pre-filled snapshot (for query tests). Warm-up 60 s. 3 reps, CV < 5%. Use `CLA_RBAC_ENABLED` both off and on (token verification cost).

## 3. Data-retrieval contract
`perf-results/cla/<YYYYMMDD-HHMM>-<sha>/<scenario>/`: `meta.json`, `k6-summary.json`, `ghz.json` (OTLP gRPC), `visibility.csv` (ingest→searchable), `stats/stats-<epoch>.json` (poll `/v2/stats` every 10 s), `pprof/*` (both built-in capture and `go tool pprof`), `disk/{du.csv,iostat.txt,fsync_lat.txt}`, `host/pidstat.txt`, `summary.json`.

```json
{"project":"cla","scenario":"PERF-ING-02","git":"…","entries_per_s":85000,"bytes_per_s":42000000,"batch":500,
 "ingest_lat_ms":{"p50":12,"p99":60},"visibility_ms":{"p50":800,"p99":2400},"rejected_rate":0.0,"dropped_debug":0,
 "fsync_per_s":170,"entries_per_fsync":500,"disk_bytes_per_entry":38,"compression_ratio":11.2,
 "cpu_cores":5.1,"rss_mb":1400,"pass":true}
```
Retrieval: `jq -s 'map(select(.scenario|test("ING")))|.[]|[.ts,.batch,.entries_per_s,.visibility_ms.p99]|@tsv' history.jsonl`.

Stats poller:
```bash
while true; do curl -s -H "Authorization: Bearer $TOK" localhost:8080/v2/stats > $OUT/stats/stats-$(date +%s).json; sleep 10; done &
jq -s 'map({t:input_filename, vec:.vector_index_size, pend:.tracing_pending_traces, seg:.logsdb_segment_stats})' $OUT/stats/*.json > $OUT/stats/series.json
```
Process sampling (no /metrics): `pidstat -h -u -r -d -p $(pgrep -f cmd/aggregator|head -1) 1 > $OUT/host/pidstat.txt`; disk: `du -sb $CLA_DATA_DIR/* ` every 30 s → `disk/du.csv`.
Optional perf build: register `promhttp.Handler()` on `/debug/metrics` and `net/http/pprof` on 127.0.0.1:6060 (build tag `pprof`) to get `go_*`/`process_*` series.

**Visibility latency (most important e2e number):** each generator entry carries a unique `trace_marker` and `sent_ns`; a prober searches `GET /v2/logs/search?q=<marker>` every 100 ms until found → `visibility_ms = found_ns − sent_ns`, written to `visibility.csv`. Sample 1% of entries as markers.

## 4. Tool matrix
| Need | Tool | Command |
|---|---|---|
| HTTP ingest/query load | k6 | §8.1 |
| OTLP gRPC load | `ghz`, `telemetrygen` (otel-collector-contrib) | `telemetrygen logs --otlp-insecure --otlp-endpoint localhost:4317 --rate 20000 --duration 120s --workers 8` |
| OTLP HTTP | `telemetrygen logs --otlp-http --otlp-endpoint localhost:4318` | |
| Agent path | run N `cmd/agent` tailing files written by `flog` | `flog -f json -l -d 100us -o /var/log/app.log -t log` |
| Go micro benches | `go test -bench` + `benchstat` | §5 |
| Profiles | built-in `/v2/profiling/capture` + `go tool pprof` + `go tool trace` | §7 |
| Disk/fsync | `iostat -x`, `biolatency-bpfcc`, `bpftrace` fsync histogram, `fio --fsync=1` baseline | §7.3 |
| Storage efficiency | `du`, `/v2/stats.logsdb_segment_stats` | bytes/entry, compression |
| Kernel/off-CPU | `perf`, `offcputime-bpfcc` | |

## 5. Micro-benchmarks (add `*_bench_test.go`)
| ID | Package | Benchmark | Record |
|---|---|---|---|
| MB-01 | `ingestbuffer` | `BenchmarkWAL_Append` record 256 B/4 KB on tmpfs vs NVMe | ops/s (≈ 1/fsync latency on disk), ns/op |
| MB-02 | `ingestbuffer` | `BenchmarkPublish_Parallel` partitions 1/8/32, replicators 0/2 | msgs/s, allocs |
| MB-03 | `ingestbuffer` | `BenchmarkTokenBucket_Allow` 1/10k tenants | ns/op |
| MB-04 | `ingestbuffer` | `BenchmarkShuffleShard_Pick` | ns/op |
| MB-05 | `queue` | `BenchmarkFileQueue_Enqueue` | ops/s (fsync-bound) |
| MB-06 | `logsdb` | `BenchmarkTokenize` messages 100 B/2 KB, unicode | MB/s |
| MB-07 | `logsdb` | `BenchmarkSegmentWrite`, `BenchmarkSegmentQuery_Term/Phrase/Regex` on 1M entries | ns/op, compression ratio |
| MB-08 | `querylang/metricsql` | parse+eval `rate(x[5m])`, `sum by (svc)(…)` over 10k series | ns/op |
| MB-09 | `tsdb` | append & decode blocks (Gorilla-style?) 1M samples | samples/s, bytes/sample |
| MB-10 | `vectorsearch` | insert/search dims 384, N 100k | QPS, recall@10 |
| MB-11 | `otlp` | decode ExportLogsServiceRequest 100/1000 records | ns/op, allocs |
| MB-12 | `profiling` | `pprofdecode` on 1 MB profile | ns/op |
| MB-13 | `rbac` | token verify + permission check | ns/op |
```bash
go test -run='^$' -bench=. -benchmem -count=10 ./internal/... | tee $OUT/micro.txt; benchstat base/micro.txt $OUT/micro.txt
```

## 6. Macro scenarios
### 6.1 Ingest
| ID | Workload | Load | Measure | Target (calibrate) |
|---|---|---|---|---|
| PERF-ING-01 | `POST /v2/logs` 1 entry/request | open 100→5000 rps | entries/s ceiling; fsync/s | ceiling ≈ fsync rate (finding if per-entry fsync) |
| PERF-ING-02 | batch 10/100/500/1000 entries/request | open | entries/s, entries/fsync, p99 | near-linear gain with batch |
| PERF-ING-03 | entry size 200 B / 2 KB / 32 KB | | MB/s, compression ratio, bytes/entry on disk | |
| PERF-ING-04 | OTLP gRPC via ingest-gateway (telemetrygen) | 5k→100k logs/s | logs/s, gateway CPU, gRPC p99 | |
| PERF-ING-05 | 1, 10, 1000 tenants (token buckets, shuffle shard) | | fairness: small tenant p99 when big tenant floods | small tenant unaffected |
| PERF-ING-06 | Backpressure Reject vs Block | overload 2× ceiling | % `ErrBackpressure`, debug drops, producer latency (Block) | no unbounded memory |
| PERF-ING-07 | Mixed priorities 50% debug at 2× ceiling | | only debug shed first | |
| PERF-ING-08 | High-cardinality labels/fields (1M unique values) | | index growth, RSS | |
| PERF-ING-09 | Replication on (replicators 2) | | throughput cost vs off | |
| PERF-VIS-01 | Visibility latency at 25/50/75% ceiling | | ingest→searchable p50/p99 | p99 < 5 s |

### 6.2 Query
Pre-fill datasets: S (10M entries / 1 day), M (100M / 7 days), L (1B / 30 days — if disk allows). Record dataset in meta.json.
| ID | Query | Measure |
|---|---|---|
| PERF-Q-01 | term search, recent 15 min | p50/p99 at 1/10/50 concurrent users |
| PERF-Q-02 | term search, 7 days | p99; segments scanned |
| PERF-Q-03 | phrase / wildcard / regex | worst-case CPU; timeouts |
| PERF-Q-04 | rare term vs common term (selectivity) | |
| PERF-Q-05 | `/v2/logs/semantic` vector search | QPS, p99 vs index size |
| PERF-Q-06 | `/v2/metrics/query` range over 10k series, step 15 s, 24 h | p99, decoded blocks (`tsdb_decoded_blocks` Δ) |
| PERF-Q-07 | `/v2/traces/{id}` | p99 |
| PERF-Q-08 | `/v2/archive/query` cold | latency vs hot |
| PERF-Q-09 | Alerting load: 1k rules evaluated by alerting-service every 30 s | aggregator `/internal/metrics/query_range` load, rule eval lag |
| PERF-Q-10 | Query during max ingest | ingest throughput drop and query p99 inflation (interference matrix) |

### 6.3 Stress / soak / retention
| ID | Procedure | Pass / capture |
|---|---|---|
| PERF-STRESS-01 | Ramp ingest until reject/err > 1% | breaking point, failure mode |
| PERF-SOAK-01 | 60% ceiling 24 h with continuous queries | RSS flat; disk growth = rate × bytes/entry; retention (`CLA_HOT_RETENTION`) deletes on schedule; query p99 hour 1 vs 24 |
| PERF-RET-01 | Retention/compaction boundary | CPU/IO spike when segments expire; ingest p99 during |
| PERF-REC-01 | kill -9 at 80% ceiling; restart | WAL replay time, entries lost (should be 0 for acked) |
| PERF-DISK-01 | Disk 95% full | graceful reject vs crash |
| PERF-SLOWDISK-01 | `dm-delay` 10 ms on data device | ingest collapses to ≈100 entries/fsync-per-s ⇒ quantifies fsync dependence |

## 7. Profiling playbooks
### 7.1 Built-in capture (validate vs standard tooling)
```bash
curl -s -XPOST -H "Authorization: Bearer $ADMIN_TOK" "localhost:8080/v2/profiling/capture" -d '{"seconds":30}'   # VERIFY body/params in v2CaptureProfile
curl -s -H "Authorization: Bearer $TOK" "localhost:8080/v2/profiling/flamegraph?…" > $OUT/pprof/builtin-flame.json
```
Cross-check: capture `go tool pprof` CPU profile for the same window (perf build) and compare top-10 functions; disagreement ⇒ decoder bug (file it).
### 7.2 Standard Go profiling (perf build with pprof on 127.0.0.1:6060)
```bash
B=http://127.0.0.1:6060/debug/pprof
curl -s "$B/profile?seconds=30" > $OUT/pprof/cpu.pb.gz; curl -s $B/heap > $OUT/pprof/heap.pb.gz; curl -s $B/allocs > $OUT/pprof/allocs.pb.gz
curl -s $B/mutex > $OUT/pprof/mutex.pb.gz; curl -s $B/block > $OUT/pprof/block.pb.gz; curl -s "$B/trace?seconds=5" > $OUT/pprof/trace.out
go tool pprof -top -nodecount=40 $OUT/pprof/cpu.pb.gz > $OUT/pprof/cpu.top.txt
go tool pprof -base $OUT/pprof/heap-t0.pb.gz $OUT/pprof/heap.pb.gz   # growth during soak
```
### 7.3 fsync / disk
```bash
sudo bpftrace -e 'tracepoint:syscalls:sys_enter_fsync,tracepoint:syscalls:sys_enter_fdatasync /comm=="aggregator"/ {@s[tid]=nsecs;@n=count()}
 tracepoint:syscalls:sys_exit_fsync,tracepoint:syscalls:sys_exit_fdatasync /@s[tid]/ {@us=hist((nsecs-@s[tid])/1000);delete(@s[tid])}
 interval:s:1 {print(@n); clear(@n)}' > $OUT/disk/fsync_lat.txt
fio --name=fsyncbase --rw=write --bs=4k --size=256m --fsync=1 --directory=$CLA_DATA_DIR > $OUT/disk/fio_fsync.txt   # device fsync ceiling
iostat -x 1 > $OUT/disk/iostat.txt
```
`entries_per_fsync = entries/s ÷ fsync/s`. If ≈ 1 → per-entry fsync is the throughput limiter; recommend group commit.
### 7.4 Expected signatures
| Symptom | Evidence | Cause |
|---|---|---|
| Ingest plateaus at a few hundred–few thousand entries/s regardless of CPU | fsync/s ≈ entries/s; off-CPU in `fsync` | fsync per Append [src] |
| CPU dominated by `encoding/json` | cpu profile | v2 JSON array decode; consider streaming decoder |
| `logsdb` tokenizer hot | cpu | per-entry tokenization; SIMD/ASCII fast path |
| Heap growth with high-cardinality fields | heap diff | in-memory postings |
| Query p99 spikes during ingest | trace / mutex | shared locks between writer & reader in logsdb/tsdb |
| `tracing_pending_traces` grows unbounded | /v2/stats | incomplete traces never flushed |

## 8. Scripts
### 8.1 k6 ingest + visibility markers
```javascript
import http from 'k6/http'; import { check } from 'k6';
const B=__ENV.BASE||'http://localhost:8080'; const BATCH=+__ENV.BATCH||100;
const H={'Content-Type':'application/json', Authorization:`Bearer ${__ENV.TOK}`};
export const options={ scenarios:{ ingest:{executor:'constant-arrival-rate',rate:+__ENV.RPS||200,timeUnit:'1s',duration:__ENV.DUR||'5m',preAllocatedVUs:200,maxVUs:2000}},
  thresholds:{'http_req_failed':['rate<0.001'],'http_req_duration':['p(99)<200']}, summaryTrendStats:['med','p(95)','p(99)','p(99.9)','max'] };
export default function(){
  const now=Date.now(); const arr=[];
  for(let i=0;i<BATCH;i++) arr.push({timestamp:new Date(now).toISOString(), level: i%10===0?'debug':'info', service:`svc-${i%20}`,
     tenant_id: __ENV.TENANT||'t1', message:`req ${__VU}-${__ITER}-${i} user=${Math.floor(Math.random()*1e6)} path=/api/v1/items latency_ms=${Math.random()*100|0}`,
     fields:{ marker: (i===0 ? `mk-${__VU}-${__ITER}-${now}` : undefined) }});   // VERIFY LogEntry field names in 02 facts table
  const r=http.post(`${B}/v2/logs`, JSON.stringify(arr), {headers:H}); check(r,{'2xx':x=>x.status<300});
}
```
### 8.2 Visibility prober (Python)
```python
import requests, time, csv, sys, threading, queue
B,TOK,OUT=sys.argv[1],sys.argv[2],sys.argv[3]; H={'Authorization':f'Bearer {TOK}'}; q=queue.Queue()
def probe():
    w=csv.writer(open(OUT,'a'))
    while True:
        mk,sent=q.get(); 
        while True:
            r=requests.get(f'{B}/v2/logs/search',params={'q':mk},headers=H,timeout=5)   # VERIFY query param name
            if r.ok and mk in r.text: w.writerow([mk,sent,time.time_ns(),(time.time_ns()-sent)/1e6]); break
            time.sleep(0.1)
for _ in range(8): threading.Thread(target=probe,daemon=True).start()
while True:   # inject a marker every 200 ms through the same ingest endpoint
    mk=f'vis-{time.time_ns()}'; sent=time.time_ns()
    requests.post(f'{B}/v2/logs',json=[{'timestamp':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime()),'level':'info','service':'probe','tenant_id':'t1','message':mk}],headers=H)
    q.put((mk,sent)); time.sleep(0.2)
```
Percentiles: `python -c "import pandas as p;d=p.read_csv('visibility.csv',header=None);print(d[3].quantile([.5,.9,.99,.999]))"`.
### 8.3 Prefill for query tests
`flog -f json -n 10000000 | split -l 1000 - chunk_` then POST each chunk as an array (convert to LogEntry with `jq -s`), or write segments via an internal loader if available (faster; VERIFY). Record dataset hash.
### 8.4 OTLP
```bash
telemetrygen logs --otlp-insecure --otlp-endpoint localhost:4317 --rate 50000 --duration 300s --workers 16 --body "$(head -c 300 /dev/urandom | base64)" 2>&1 | tee $OUT/telemetrygen.txt
```

## 9. Scalability
GOMAXPROCS 1..16 and partitions 1..32 → USL fit; data device is shared ⇒ σ from fsync serialization. Query scaling vs dataset size (S/M/L): plot p99 vs entries — should be ~ log or bounded by time-range pruning, not linear in total data.

## 10. Regression gates
benchstat on MB-*; PERF-ING-02 (batch 100, 5 min) entries/s ≥ 0.9× baseline; PERF-VIS-01 p99 ≤ 1.2×; PERF-Q-01/06 p99 ≤ 1.2× on dataset S; built-in vs pprof top-10 agreement.

## 11. Report template
```
Run <date> sha <sha> | host, disk model/fs | CLA_* | dataset
| Scenario | load | entries/s | MB/s | ingest p99 | visibility p50/p99 | reject% | fsync/s | entries/fsync | bytes/entry | CPU | RSS | verdict |
Query table: | query | dataset | conc | p50 | p99 | decoded blocks |
Top CPU frames / heap growth / fsync histogram / findings
```

## 12. Risks from source
| # | Risk | Scenario |
|---|---|---|
| R1 | fsync on every WAL Append and file-queue write ⇒ throughput bounded by device fsync rate | PERF-ING-01/02, SLOWDISK-01 |
| R2 | No process metrics endpoint ⇒ blind production ops | §3 |
| R3 | Custom pprof decoder may misreport | §7.1 |
| R4 | Tenant fairness depends on token buckets keyed by body `tenant_id` (spoofable, 02) | PERF-ING-05 |
| R5 | Unauthenticated v1/internal ingest allows unthrottled load | PERF-STRESS-01 |
| R6 | Pending traces / vector index unbounded growth | SOAK-01 via /v2/stats |
