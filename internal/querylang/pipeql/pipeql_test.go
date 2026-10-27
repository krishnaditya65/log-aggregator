package pipeql

import (
	"testing"
)

// fakeLogSource is an in-memory LogSource backed by a fixed set of
// synthetic records, for deterministic testing.
type fakeLogSource struct {
	records []LogRecord
}

func (f *fakeLogSource) Search(filters map[string]string, keywords []string, start, end int64) ([]LogRecord, error) {
	var out []LogRecord
	for _, r := range f.records {
		if r.Time < start || r.Time > end {
			continue
		}
		if !matchFilters(r, filters) {
			continue
		}
		if !matchKeywords(r, keywords) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func matchFilters(r LogRecord, filters map[string]string) bool {
	for k, v := range filters {
		fv, ok := getField(r, k)
		if !ok {
			return false
		}
		if fmtStr(fv) != v {
			return false
		}
	}
	return true
}

func fmtStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return "" // numeric equality filters aren't exercised in these tests
	}
}

func matchKeywords(r LogRecord, keywords []string) bool {
	for _, kw := range keywords {
		if !contains(r.Message, kw) {
			return false
		}
	}
	return true
}

func contains(haystack, needle string) bool {
	hl, nl := len(haystack), len(needle)
	if nl == 0 {
		return true
	}
	for i := 0; i+nl <= hl; i++ {
		if equalFold(haystack[i:i+nl], needle) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func sampleRecords() []LogRecord {
	return []LogRecord{
		{Time: 100, Host: "h1", Service: "checkout", Level: "info", Message: "request completed", Fields: map[string]any{"status": 200.0, "latency_ms": 50.0, "route": "/checkout"}},
		{Time: 101, Host: "h2", Service: "checkout", Level: "error", Message: "connection timeout to db", Fields: map[string]any{"status": 500.0, "latency_ms": 900.0, "route": "/checkout"}},
		{Time: 102, Host: "h1", Service: "cart", Level: "info", Message: "request completed", Fields: map[string]any{"status": 200.0, "latency_ms": 30.0, "route": "/cart"}},
		{Time: 103, Host: "h3", Service: "checkout", Level: "error", Message: "payment gateway timeout", Fields: map[string]any{"status": 504.0, "latency_ms": 1200.0, "route": "/checkout"}},
		{Time: 104, Host: "h2", Service: "cart", Level: "warn", Message: "slow response", Fields: map[string]any{"status": 200.0, "latency_ms": 800.0, "route": "/cart"}},
		{Time: 105, Host: "h1", Service: "checkout", Level: "info", Message: "request completed", Fields: map[string]any{"status": 200.0, "latency_ms": 40.0, "route": "/checkout"}},
	}
}

func mustParse(t *testing.T, q string) *Pipeline {
	t.Helper()
	toks := Lex(q)
	p, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse(%q) error: %v", q, err)
	}
	return p
}

// ---- Lexer / parser AST shape tests ----

func TestParseSearchFilterStats(t *testing.T) {
	p := mustParse(t, `search service="checkout" "timeout" | filter level="error" | stats count() by (host)`)
	if len(p.Stages) != 3 {
		t.Fatalf("got %d stages, want 3", len(p.Stages))
	}
	search, ok := p.Stages[0].(*SearchStage)
	if !ok {
		t.Fatalf("stage 0 = %T, want *SearchStage", p.Stages[0])
	}
	if search.Filters["service"] != "checkout" {
		t.Errorf("search filters = %+v", search.Filters)
	}
	if len(search.Keywords) != 1 || search.Keywords[0] != "timeout" {
		t.Errorf("search keywords = %+v", search.Keywords)
	}

	filter, ok := p.Stages[1].(*FilterStage)
	if !ok {
		t.Fatalf("stage 1 = %T, want *FilterStage", p.Stages[1])
	}
	cond, ok := filter.Expr.(*Condition)
	if !ok {
		t.Fatalf("filter.Expr = %T, want *Condition", filter.Expr)
	}
	if cond.Field != "level" || cond.Op != OpEQ || cond.Value != "error" {
		t.Errorf("condition = %+v", cond)
	}

	stats, ok := p.Stages[2].(*StatsStage)
	if !ok {
		t.Fatalf("stage 2 = %T, want *StatsStage", p.Stages[2])
	}
	if len(stats.Aggs) != 1 || stats.Aggs[0].Func != AggCount {
		t.Errorf("stats aggs = %+v", stats.Aggs)
	}
	if len(stats.By) != 1 || stats.By[0] != "host" {
		t.Errorf("stats by = %+v", stats.By)
	}
}

func TestParseStarSearchWithSortAndLimit(t *testing.T) {
	p := mustParse(t, `search * | filter status>=500 | stats p95(latency_ms) by (route) | sort desc | limit 20`)
	if len(p.Stages) != 5 {
		t.Fatalf("got %d stages, want 5", len(p.Stages))
	}
	search := p.Stages[0].(*SearchStage)
	if !search.Star {
		t.Errorf("expected star search")
	}
	filter := p.Stages[1].(*FilterStage)
	cond := filter.Expr.(*Condition)
	if cond.Field != "status" || cond.Op != OpGE || cond.Value != "500" {
		t.Errorf("condition = %+v", cond)
	}
	stats := p.Stages[2].(*StatsStage)
	if stats.Aggs[0].Func != AggP95 || stats.Aggs[0].Field != "latency_ms" {
		t.Errorf("stats aggs = %+v", stats.Aggs)
	}
	sortStage := p.Stages[3].(*SortStage)
	if !sortStage.Desc {
		t.Errorf("expected desc sort")
	}
	limit := p.Stages[4].(*LimitStage)
	if limit.N != 20 {
		t.Errorf("limit = %d, want 20", limit.N)
	}
}

func TestParseBooleanAndOr(t *testing.T) {
	p := mustParse(t, `search * | filter level="error" and status>=500`)
	filter := p.Stages[1].(*FilterStage)
	and, ok := filter.Expr.(*AndExpr)
	if !ok {
		t.Fatalf("filter.Expr = %T, want *AndExpr", filter.Expr)
	}
	lc := and.LHS.(*Condition)
	rc := and.RHS.(*Condition)
	if lc.Field != "level" || rc.Field != "status" {
		t.Errorf("and = %+v %+v", lc, rc)
	}
}

// ---- Executor tests ----

func TestRunSearchFilterStatsCount(t *testing.T) {
	src := &fakeLogSource{records: sampleRecords()}
	res, err := Run(`search service="checkout" | filter level="error" | stats count() by (host)`, src, 0, 1000)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !res.IsStats {
		t.Fatalf("expected stats result")
	}
	got := map[string]int{}
	for _, row := range res.StatsRows {
		host := row["host"].(string)
		got[host] = row["count()"].(int)
	}
	// h2 has 1 checkout error (timeout), h3 has 1 checkout error (payment gateway timeout)
	if got["h2"] != 1 {
		t.Errorf("h2 count = %v, want 1", got["h2"])
	}
	if got["h3"] != 1 {
		t.Errorf("h3 count = %v, want 1", got["h3"])
	}
	if _, ok := got["h1"]; ok {
		t.Errorf("h1 should have no error rows, got %+v", got)
	}
}

func TestRunStarSearchFilterNumericStatsSortLimit(t *testing.T) {
	src := &fakeLogSource{records: sampleRecords()}
	res, err := Run(`search * | filter status>=500 | stats p95(latency_ms) by (route) | sort desc | limit 20`, src, 0, 1000)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !res.IsStats {
		t.Fatalf("expected stats result")
	}
	if len(res.StatsRows) != 1 {
		t.Fatalf("got %d rows, want 1 (only /checkout has status>=500 rows)", len(res.StatsRows))
	}
	row := res.StatsRows[0]
	if row["route"] != "/checkout" {
		t.Fatalf("route = %v, want /checkout", row["route"])
	}
	// status>=500 rows for /checkout: latency 900 (h2) and 1200 (h3).
	// p95 of [900,1200] via linear interpolation: idx=0.95*1=0.95 -> 900+(1200-900)*0.95=1185
	want := 1185.0
	got := row["p95(latency_ms)"].(float64)
	if got != want {
		t.Errorf("p95 = %v, want %v", got, want)
	}
}

func TestRunKeywordSearch(t *testing.T) {
	src := &fakeLogSource{records: sampleRecords()}
	res, err := Run(`search service="checkout" "timeout"`, src, 0, 1000)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if res.IsStats {
		t.Fatalf("expected raw record result")
	}
	if len(res.Records) != 2 {
		t.Fatalf("got %d records, want 2 (both checkout timeouts)", len(res.Records))
	}
	for _, r := range res.Records {
		if r.Service != "checkout" {
			t.Errorf("unexpected service %q", r.Service)
		}
	}
}

func TestRunFilterAndOr(t *testing.T) {
	src := &fakeLogSource{records: sampleRecords()}
	res, err := Run(`search * | filter level="error" or level="warn"`, src, 0, 1000)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(res.Records) != 3 {
		t.Fatalf("got %d records, want 3 (2 error + 1 warn)", len(res.Records))
	}
}

func TestRunSortAscByField(t *testing.T) {
	src := &fakeLogSource{records: sampleRecords()}
	res, err := Run(`search * | filter service="checkout" | sort asc latency_ms | limit 2`, src, 0, 1000)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("got %d records, want 2", len(res.Records))
	}
	lat0 := res.Records[0].Fields["latency_ms"].(float64)
	lat1 := res.Records[1].Fields["latency_ms"].(float64)
	if lat0 != 40 || lat1 != 50 {
		t.Errorf("sorted latencies = %v, %v, want 40, 50", lat0, lat1)
	}
}

func TestRunStatsAvgSumMinMax(t *testing.T) {
	src := &fakeLogSource{records: sampleRecords()}
	res, err := Run(`search service="checkout"`, src, 0, 1000)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if len(res.Records) != 4 {
		t.Fatalf("got %d checkout records, want 4", len(res.Records))
	}

	res2, err := Run(`search service="checkout" | stats avg(latency_ms), sum(latency_ms), min(latency_ms), max(latency_ms)`, src, 0, 1000)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	row := res2.StatsRows[0]
	// checkout latencies: 50, 900, 1200, 40 -> sum=2190, avg=547.5, min=40, max=1200
	if row["sum(latency_ms)"].(float64) != 2190 {
		t.Errorf("sum = %v, want 2190", row["sum(latency_ms)"])
	}
	if row["avg(latency_ms)"].(float64) != 547.5 {
		t.Errorf("avg = %v, want 547.5", row["avg(latency_ms)"])
	}
	if row["min(latency_ms)"].(float64) != 40 {
		t.Errorf("min = %v, want 40", row["min(latency_ms)"])
	}
	if row["max(latency_ms)"].(float64) != 1200 {
		t.Errorf("max = %v, want 1200", row["max(latency_ms)"])
	}
}
