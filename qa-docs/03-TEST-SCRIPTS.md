# custom-log-aggregator — Test Scripts (reference, NOT executed)

Body shapes are assumptions (`{"logs":[{"timestamp","level","service","message","attributes"}]}`, `{"metrics":[{"name","labels","value","timestamp"}]}`, token `{"tenant_id","role"}`→`{"token"}`, search response `{"results":[...]}`) — confirm in `internal/api/*.go` and fix `lib.sh` helpers only. Needs bash, curl, jq, docker, k6, python3, go; optional `telemetrygen`.

## lib.sh
```bash
#!/usr/bin/env bash
set -euo pipefail
AGG=${AGG:-http://localhost:8080}; GW=${GW:-http://localhost:4318}; ALR=${ALR:-http://localhost:8083}; FLUSH=${FLUSH:-3}
PASS=0; FAIL=0; ok(){ PASS=$((PASS+1)); echo "PASS $1"; }; ko(){ FAIL=$((FAIL+1)); echo "FAIL $1 :: ${2:-}"; }
# [src] POST /v2/auth/token {user_id, tenant_id, ttl_seconds} -> {token, expires_in}; requires CLA_RBAC_ENABLED=true (else 404)
# NOTE: it requires NO credential — CLA-AUTH-001 (P0). tok USER TENANT
tok(){ curl -s -X POST $AGG/v2/auth/token -H 'Content-Type: application/json' -d "{\"user_id\":\"${2:-alice}\",\"tenant_id\":\"$1\",\"ttl_seconds\":3600}" | jq -r '.token // empty'; }
api(){ local m=$1 p=$2 t=$3 d=${4:-}; curl -s -o /tmp/b -w '%{http_code}' -X $m "$AGG$p" ${t:+-H "Authorization: Bearer $t"} -H "X-Tenant-ID: ${TENANT:-A}" -H 'Content-Type: application/json' ${d:+-d "$d"}; }
now_ns(){ python3 -c 'import time;print(time.time_ns())'; }; iso(){ date -u +%FT%TZ; }
# [src] v2 ingest body is a JSON ARRAY of LogEntry {id,time,service,host,level,message,trace_id,span_id,tenant_id,tags,fields,raw}
log(){ # log TOKEN RUN IDX  -> one uniquely identifiable record
  api POST /v2/logs "$1" "[{\"id\":\"$2-$3\",\"time\":\"$(iso)\",\"level\":\"info\",\"service\":\"qa\",\"message\":\"qa-$2-$3\",\"tenant_id\":\"${TENANT:-A}\",\"tags\":{\"run\":\"$2\"}}]"; }
count(){ # count TOKEN RUN -> number of records for run id
  curl -s -G "$AGG/v2/logs/search" -H "Authorization: Bearer $1" --data-urlencode "q=run:$2" --data-urlencode "limit=10000000" | jq '(.results // .logs // .) | length'; }
summary(){ echo "pass=$PASS fail=$FAIL"; [[ $FAIL == 0 ]]; }
```

## smoke.sh
```bash
. ./lib.sh; T=$(tok tenant-a); R=smk$RANDOM
for u in $AGG/healthz $AGG/readyz; do [[ $(curl -s -o /dev/null -w '%{http_code}' $u) == 200 ]] && ok "SMK-001 $u" || ko "SMK-001 $u"; done
[[ -n $T && $T != null ]] && ok SMK-002 || ko SMK-002
[[ $(log $T $R 1) =~ ^20 ]] && sleep $FLUSH && [[ $(count $T $R) == 1 ]] && ok SMK-003 || ko SMK-003 "$(cat /tmp/b)"
api POST /v2/metrics $T "{\"metrics\":[{\"name\":\"qa_gauge\",\"labels\":{\"run\":\"$R\"},\"value\":42,\"timestamp\":\"$(iso)\"}]}" >/dev/null; sleep $FLUSH
curl -s -G $AGG/v2/metrics/query -H "Authorization: Bearer $T" --data-urlencode "query=qa_gauge{run=\"$R\"}" | grep -q 42 && ok SMK-004 || ko SMK-004
TID=$(openssl rand -hex 16); api POST /v2/traces $T "{\"spans\":[{\"trace_id\":\"$TID\",\"span_id\":\"$(openssl rand -hex 8)\",\"name\":\"root\",\"start\":\"$(iso)\",\"duration_ms\":5}]}" >/dev/null; sleep $FLUSH
[[ $(api GET /v2/traces/$TID $T) == 200 ]] && ok SMK-005 || ko SMK-005
curl -s -o /dev/null -w '%{http_code}' -X POST $GW/v1/logs -H 'Content-Type: application/json' -d "{\"resourceLogs\":[{\"resource\":{\"attributes\":[{\"key\":\"service.name\",\"value\":{\"stringValue\":\"qa\"}}]},\"scopeLogs\":[{\"logRecords\":[{\"timeUnixNano\":\"$(now_ns)\",\"body\":{\"stringValue\":\"otlp-$R\"},\"attributes\":[{\"key\":\"run\",\"value\":{\"stringValue\":\"otlp$R\"}}]}]}]}]}" | grep -q '^20' && ok SMK-006-accept || ko SMK-006-accept
[[ $(api GET /v2/stats $T) == 200 ]] && ok SMK-007 || ko SMK-007; [[ $(api GET /v2/alerts/rules $T) == 200 ]] && ok SMK-009 || ko SMK-009
summary
```

## tenant_isolation.sh (NEG-001/013, E2E-004) — P0
```bash
. ./lib.sh; A=$(tok tenant-a); B=$(tok tenant-b); V=$(tok tenant-a viewer); R=iso$RANDOM
for i in 1 2 3; do log $A $R $i >/dev/null; done; sleep $FLUSH
[[ $(count $A $R) == 3 ]] && ok ISO-own || ko ISO-own
[[ $(count $B $R) == 0 ]] && ok ISO-search || ko ISO-search "tenant B sees A's logs"
leak(){ grep -q "qa-$R" /tmp/b && ko "$1" "LEAK" || ok "$1"; }
curl -s -o /tmp/b -G $AGG/v2/logs/search -H "Authorization: Bearer $B" -H 'X-Tenant-ID: tenant-a' --data-urlencode "q=run:$R"; leak ISO-header-override
curl -s -o /tmp/b -G $AGG/v2/logs/search -H "Authorization: Bearer $B" --data-urlencode "q=run:$R" --data-urlencode tenant_id=tenant-a; leak ISO-param-override
curl -s -o /tmp/b -G $AGG/v2/logs/semantic -H "Authorization: Bearer $B" --data-urlencode "q=qa-$R-1" --data-urlencode k=10; leak ISO-semantic
curl -s -o /tmp/b -G $AGG/v2/archive/query -H "Authorization: Bearer $B" --data-urlencode "q=run:$R"; leak ISO-archive
curl -s -o /tmp/b -G $AGG/v1/logs --data-urlencode "q=run:$R"; leak ISO-v1-unauth
curl -s -o /tmp/b -G $AGG/v2/logs/search -H "Authorization: Bearer $B" --data-urlencode "q=run:$R\" OR tenant_id:\"tenant-a"; leak ISO-query-injection
for p in /v2/logs/search /v2/metrics/query /v2/stats /v2/alerts/rules /v2/aiops/rootcause /v2/profiling/flamegraph /v2/archive/query; do
  [[ $(curl -s -o /dev/null -w '%{http_code}' $AGG$p) == 401 ]] && ok "AUTH $p" || ko "AUTH $p" "no auth required"; done
[[ $(api POST /v2/alerts/rules $V '{"name":"x","expr":"up==0","for":"1m"}') == 403 ]] && ok RBAC-viewer || ko RBAC-viewer
for p in logs metrics traces metrics/query_range; do c=$(curl -s -o /dev/null -w '%{http_code}' -X POST $AGG/internal/$p -H 'Content-Type: application/json' -d '{}'); [[ $c == 401 || $c == 403 || $c == 404 ]] && ok "NEG-013 /internal/$p" || ko "NEG-013 /internal/$p" "unauthenticated internal endpoint ($c)"; done
summary
```

## negative_query_fuzz.sh (NEG-002/009)
```bash
. ./lib.sh; T=$(tok tenant-a)
while IFS= read -r q; do s=$(date +%s); c=$(curl -s -o /tmp/b -m 15 -w '%{http_code}' -G $AGG/v2/logs/search -H "Authorization: Bearer $T" --data-urlencode "q=$q" || echo 000); e=$(( $(date +%s)-s ))
  [[ $c != 000 && $c != 5* && $e -lt 12 ]] && ok "Q[$c ${e}s] ${q:0:30}" || ko "Q ${q:0:30}" "code=$c t=${e}s"; done <<'EOF'
(
)
"unterminated
a AND
| | |
level:info | stats count by
message =~ "(a+)+$"
message =~ "((((((((((x*)*)*)*)*)*)*)*)*)*)*"
* | sort | sort | sort | sort | sort
$(printf 'a%.0s' {1..100000})
EOF
for qs in 'start=2030-01-01T00:00:00Z&end=2020-01-01T00:00:00Z' 'limit=-1' 'limit=999999999999' 'start=garbage' 'start=1900-01-01T00:00:00Z&end=2100-01-01T00:00:00Z'; do c=$(curl -s -o /dev/null -m 20 -w '%{http_code}' "$AGG/v2/logs/search?q=*&$qs" -H "Authorization: Bearer $T"); [[ $c == 400 || $c == 200 ]] && ok "P $qs" || ko "P $qs" $c; done
for mq in 'rate(' 'sum by () ()' 'x{a=~"(a+)+$"}' 'qa_gauge[100y]' '1/0' 'histogram_quantile(2, x)'; do c=$(curl -s -o /dev/null -m 15 -w '%{http_code}' -G $AGG/v2/metrics/query -H "Authorization: Bearer $T" --data-urlencode "query=$mq"); [[ $c != 5* && $c != 000 ]] && ok "M $mq" || ko "M $mq" $c; done
printf '\x1f\x8b\x08\x00garbage' | curl -s -o /dev/null -w '%{http_code}\n' -X POST $AGG/v2/logs -H "Authorization: Bearer $T" -H 'Content-Encoding: gzip' -H 'Content-Type: application/json' --data-binary @- | grep -q 400 && ok NEG-002-badgzip || ko NEG-002-badgzip
dd if=/dev/zero bs=1M count=2048 2>/dev/null | gzip -9 > /tmp/bomb.gz; c=$(curl -s -o /dev/null -m 30 -w '%{http_code}' -X POST $AGG/v2/logs -H "Authorization: Bearer $T" -H 'Content-Encoding: gzip' --data-binary @/tmp/bomb.gz); [[ $c == 4* ]] && ok NEG-002-bomb || ko NEG-002-bomb $c
[[ $(curl -s -o /dev/null -w '%{http_code}' $AGG/healthz) == 200 ]] && ok alive-after-fuzz || ko alive-after-fuzz
summary
```

## exact_count.py — loss/dup oracle (CON-001, RACE-002/003; reused by chaos)
```python
import sys, json, time, threading, urllib.request, urllib.parse, uuid, datetime
AGG, TOKEN, N, W, VIA = sys.argv[1], sys.argv[2], int(sys.argv[3]), int(sys.argv[4]), (sys.argv[5] if len(sys.argv) > 5 else 'agg')
run = uuid.uuid4().hex[:8]; acked = set(); lock = threading.Lock()
def post(ids):
    body = json.dumps([{'id': i, 'time': datetime.datetime.utcnow().isoformat() + 'Z', 'level': 'info', 'service': 'qa', 'message': f'id-{i}', 'tags': {'run': run}} for i in ids]).encode()   # [src] array of LogEntry
    for attempt in range(8):                                  # client retries => exposes dup-on-retry bugs
        try:
            r = urllib.request.urlopen(urllib.request.Request(f'{AGG}/v2/logs', body, {'Authorization': f'Bearer {TOKEN}', 'Content-Type': 'application/json'}), timeout=5)
            if r.status < 300:
                with lock: acked.update(ids)
                return
        except Exception: time.sleep(min(2 ** attempt * 0.1, 5))
def worker(w): [post([f'{w}-{b}-{j}' for j in range(100)]) for b in range(N // W // 100)]
ts = [threading.Thread(target=worker, args=(w,)) for w in range(W)]; [t.start() for t in ts]; [t.join() for t in ts]
time.sleep(float(__import__('os').environ.get('SETTLE', 15)))
got = json.load(urllib.request.urlopen(urllib.request.Request(f'{AGG}/v2/logs/search?' + urllib.parse.urlencode({'q': f'run:{run}', 'limit': N * 2}), headers={'Authorization': f'Bearer {TOKEN}'})))
ids = [r['id'] for r in (got if isinstance(got, list) else (got.get('results') or got.get('logs') or []))]
lost = acked - set(ids); dup = len(ids) - len(set(ids))
print(f'run={run} acked={len(acked)} stored={len(ids)} lost={len(lost)} dup={dup}'); sys.exit(1 if lost or dup else 0)
```

## chaos.sh (CHA-001/003/008/013, DUR-004)
```bash
. ./lib.sh; T=$(tok tenant-a)
run(){ SETTLE=${3:-30} python3 exact_count.py $AGG $T 200000 16 & W=$!; sleep 8; eval "$2"; wait $W && ok "$1" || ko "$1" "loss or duplicates (compare with documented durability window)"; }
run CHA-001 'docker compose kill -s KILL aggregator; sleep 5; docker compose up -d aggregator'
run CHA-003 'docker compose stop aggregator; sleep 60; docker compose start aggregator' 90
run CHA-013 'docker compose kill -s TERM aggregator; sleep 10; docker compose up -d aggregator'
run CHA-004 'docker compose pause aggregator; sleep 20; docker compose unpause aggregator'
run CHA-008 'for i in 1 2 3 4 5 6; do docker compose kill -s KILL aggregator; docker compose up -d aggregator; sleep 4; done' 60
# through the gateway path: set AGG to a thin shim or point exact_count at OTLP (extend script) and repeat with `kill gateway`
R=dur$RANDOM; for i in $(seq 50); do log $T $R $i >/dev/null; done; sleep $FLUSH
for i in $(seq 10); do docker compose restart aggregator >/dev/null; sleep 5; [[ $(count $T $R) == 50 ]] || { ko DUR-004 "restart $i count=$(count $T $R)"; break; }; done; ok DUR-004
summary
```

## alerting_e2e.sh (FUN-007/008, CHA-005/007, E2E-002)
```bash
. ./lib.sh; T=$(tok tenant-a); R=al$RANDOM
python3 - <<'EOF' &   # mock Slack receiver on :9999 counting notifications; set CLA_SLACK_WEBHOOK_URL=http://host.docker.internal:9999/hook
import http.server, json
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(s): b=s.rfile.read(int(s.headers['Content-Length'])); open('/tmp/notif.log','ab').write(b+b'\n'); s.send_response(200); s.end_headers()
    def log_message(*a): pass
http.server.HTTPServer(('0.0.0.0',9999),H).serve_forever()
EOF
MOCK=$!; : > /tmp/notif.log
api POST /v2/alerts/rules $T "{\"name\":\"qa-$R\",\"expr\":\"qa_err{run=\\\"$R\\\"} > 5\",\"for\":\"1m\",\"severity\":\"critical\"}" >/dev/null
push(){ api POST /v2/metrics $T "{\"metrics\":[{\"name\":\"qa_err\",\"labels\":{\"run\":\"$R\"},\"value\":$1,\"timestamp\":\"$(iso)\"}]}" >/dev/null; }
for i in $(seq 4); do push 10; sleep 10; done; [[ $(grep -c "qa-$R" /tmp/notif.log) == 0 ]] && ok ALERT-pending-no-notify || ko ALERT-pending-no-notify "fired before for: elapsed"
for i in $(seq 6); do push 10; sleep 10; done; [[ $(grep -c "qa-$R" /tmp/notif.log) == 1 ]] && ok ALERT-fired-once || ko ALERT-fired-once "count=$(grep -c "qa-$R" /tmp/notif.log)"
docker compose restart alerting-service; for i in $(seq 6); do push 10; sleep 10; done; [[ $(grep -c "qa-$R" /tmp/notif.log) == 1 ]] && ok CHA-005-no-refire || ko CHA-005-no-refire "duplicate after restart"
for i in $(seq 9); do push 0; sleep 10; done; [[ $(grep -c "qa-$R" /tmp/notif.log) == 2 ]] && grep "qa-$R" /tmp/notif.log | tail -1 | grep -qi resolv && ok ALERT-resolved-once || ko ALERT-resolved-once
api POST /v2/alerts/silences $T "{\"matchers\":{\"alertname\":\"qa-$R\"},\"duration\":\"10m\"}" >/dev/null; for i in $(seq 9); do push 10; sleep 10; done
[[ $(grep -c "qa-$R" /tmp/notif.log) == 2 ]] && ok FUN-008-silenced || ko FUN-008-silenced
kill $MOCK; summary
```

## load.k6.js
```js
import http from 'k6/http'; import { check } from 'k6';
const AGG = __ENV.AGG || 'http://localhost:8080', T = __ENV.TOKEN, H = { headers: { Authorization: `Bearer ${T}`, 'Content-Type': 'application/json' } };
const ingest = (rate, dur) => ({ executor: 'constant-arrival-rate', rate, timeUnit: '1s', duration: dur, preAllocatedVUs: 200, maxVUs: 2000, exec: 'ingest' });
const P = { load: { i: ingest(100, '15m'), q: { executor: 'constant-arrival-rate', rate: 50, timeUnit: '1s', duration: '15m', preAllocatedVUs: 50, maxVUs: 500, exec: 'query' } },   // 100 batches x 500 = 50k logs/s
  stress: { i: { executor: 'ramping-arrival-rate', startRate: 50, timeUnit: '1s', preAllocatedVUs: 500, maxVUs: 5000, exec: 'ingest', stages: [{ target: 200, duration: '3m' }, { target: 800, duration: '5m' }, { target: 2000, duration: '5m' }, { target: 0, duration: '2m' }] } },
  spike: { i: { executor: 'ramping-arrival-rate', startRate: 20, timeUnit: '1s', preAllocatedVUs: 1000, maxVUs: 5000, exec: 'ingest', stages: [{ target: 20, duration: '1m' }, { target: 1000, duration: '5s' }, { target: 1000, duration: '1m' }, { target: 20, duration: '5s' }, { target: 20, duration: '5m' }] } },
  soak: { i: ingest(30, '24h'), q: { executor: 'constant-arrival-rate', rate: 5, timeUnit: '1s', duration: '24h', preAllocatedVUs: 10, maxVUs: 100, exec: 'query' } } };
export const options = { scenarios: P[__ENV.PROFILE || 'load'], thresholds: { 'http_req_duration{k:ingest}': ['p(99)<200'], 'http_req_duration{k:query}': ['p(99)<2000'], 'http_req_failed{k:ingest}': ['rate<0.001'] } };
export function ingest() { const now = new Date().toISOString(), logs = [];
  for (let i = 0; i < 500; i++) logs.push({ timestamp: now, level: ['info', 'warn', 'error'][i % 3], service: `svc${i % 20}`, message: `req ${__VU}-${__ITER}-${i} took ${i}ms`, attributes: { run: 'load', host: `h${i % 50}` } });
  const r = http.post(`${AGG}/v2/logs`, JSON.stringify({ logs }), { ...H, tags: { k: 'ingest' } }); check(r, { acked: x => x.status < 300, 'backpressure not crash': x => x.status < 300 || x.status === 429 || x.status === 503 }); }
export function query() { const qs = ['level:error', 'service:svc3 AND level:warn', 'level:error | stats count by service', 'message:"took 499ms"'];
  check(http.get(`${AGG}/v2/logs/search?q=${encodeURIComponent(qs[__ITER % qs.length])}&limit=100`, { ...H, tags: { k: 'query' } }), { ok: x => x.status === 200 }); }
```
OTLP path: `telemetrygen logs --otlp-endpoint localhost:4317 --otlp-insecure --rate 20000 --duration 10m --workers 8` (same for `metrics`, `traces`).

## Go tests to add
```go
// internal/querylang/pipeql/fuzz_test.go, metricsql/fuzz_test.go, internal/otlp/fuzz_test.go, internal/dbwire/fuzz_test.go
func FuzzParse(f *testing.F){ for _,s:=range []string{"level:error","a | stats count by b","("}{f.Add(s)}; f.Fuzz(func(t *testing.T,q string){ _,_ = Parse(q) }) } // must not panic or hang (>1s)
// internal/tsdb/reference_test.go (invariant 4): random series+samples (seeded, incl. out-of-order, dup, counter resets) -> compare Query() to naive O(n) reference
// internal/logsdb/crash_test.go: write N, close without flush via failpoint at each stage (pre-index, post-segment, mid-compaction), reopen, assert count ∈ {acked subset}, no dup, no panic
// internal/alerting/timeline_test.go: table of (t, value) series + rule -> expected transitions [(t,pending),(t,firing),(t,resolved)] with fake clock
```

## Run order
`go test -race ./...` → smoke → tenant_isolation → negative_query_fuzz → exact_count (steady) → k6 load → chaos → alerting_e2e → k6 stress/spike → soak (sample RSS/goroutines/disk each minute).
