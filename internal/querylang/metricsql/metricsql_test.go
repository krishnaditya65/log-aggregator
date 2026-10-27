package metricsql

import (
	"math"
	"sort"
	"testing"
	"time"
)

// fakeDataSource is an in-memory DataSource backed by a fixed set of
// synthetic series, for deterministic testing.
type fakeDataSource struct {
	series []Series
}

func (f *fakeDataSource) QueryRange(matchers map[string]string, start, end int64) ([]Series, error) {
	var out []Series
	for _, s := range f.series {
		if !matchAll(s.Labels, matchers) {
			continue
		}
		var pts []Point
		for _, p := range s.Points {
			if p.T >= start && p.T <= end {
				pts = append(pts, p)
			}
		}
		if len(pts) == 0 {
			continue
		}
		out = append(out, Series{Labels: s.Labels, Points: pts})
	}
	return out, nil
}

func matchAll(labels, matchers map[string]string) bool {
	for k, v := range matchers {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func mustParse(t *testing.T, q string) Expr {
	t.Helper()
	toks := Lex(q)
	expr, err := Parse(toks)
	if err != nil {
		t.Fatalf("parse(%q) error: %v", q, err)
	}
	return expr
}

// ---- Lexer / parser AST shape tests ----

func TestLexVectorSelector(t *testing.T) {
	toks := Lex(`http_requests_total{service="checkout", env="prod"}`)
	wantTypes := []TokenType{TokIdent, TokLBrace, TokIdent, TokEQ, TokString, TokComma, TokIdent, TokEQ, TokString, TokRBrace, TokEOF}
	if len(toks) != len(wantTypes) {
		t.Fatalf("got %d tokens, want %d: %v", len(toks), len(wantTypes), toks)
	}
	for i, wt := range wantTypes {
		if toks[i].Type != wt {
			t.Errorf("token %d: got %s, want %s", i, tokenTypeName(toks[i].Type), tokenTypeName(wt))
		}
	}
}

func TestParseVectorSelector(t *testing.T) {
	expr := mustParse(t, `http_requests_total{service="checkout", env="prod"}`)
	vs, ok := expr.(*VectorSelector)
	if !ok {
		t.Fatalf("got %T, want *VectorSelector", expr)
	}
	if vs.Metric != "http_requests_total" {
		t.Errorf("metric = %q", vs.Metric)
	}
	if vs.LabelMatchers["service"] != "checkout" || vs.LabelMatchers["env"] != "prod" {
		t.Errorf("matchers = %+v", vs.LabelMatchers)
	}
}

func TestParseMatrixSelector(t *testing.T) {
	expr := mustParse(t, `http_requests_total{service="checkout"}[5m]`)
	ms, ok := expr.(*MatrixSelector)
	if !ok {
		t.Fatalf("got %T, want *MatrixSelector", expr)
	}
	if ms.Vector.Metric != "http_requests_total" {
		t.Errorf("metric = %q", ms.Vector.Metric)
	}
	if ms.Range != 5*time.Minute {
		t.Errorf("range = %v, want 5m", ms.Range)
	}
}

func TestParseAggregateWithByAndRateCall(t *testing.T) {
	expr := mustParse(t, `sum(rate(http_requests_total[5m])) by (service)`)
	agg, ok := expr.(*AggregateExpr)
	if !ok {
		t.Fatalf("got %T, want *AggregateExpr", expr)
	}
	if agg.Op != "sum" {
		t.Errorf("op = %q", agg.Op)
	}
	if len(agg.By) != 1 || agg.By[0] != "service" {
		t.Errorf("by = %+v", agg.By)
	}
	call, ok := agg.Expr.(*Call)
	if !ok {
		t.Fatalf("agg.Expr = %T, want *Call", agg.Expr)
	}
	if call.Func != "rate" {
		t.Errorf("call.Func = %q", call.Func)
	}
	if _, ok := call.Args[0].(*MatrixSelector); !ok {
		t.Fatalf("call.Args[0] = %T, want *MatrixSelector", call.Args[0])
	}
}

func TestParseQuantile(t *testing.T) {
	expr := mustParse(t, `quantile(0.95, http_requests_total)`)
	agg, ok := expr.(*AggregateExpr)
	if !ok {
		t.Fatalf("got %T, want *AggregateExpr", expr)
	}
	if agg.Op != "quantile" {
		t.Errorf("op = %q", agg.Op)
	}
	nl, ok := agg.Param.(*NumberLiteral)
	if !ok || nl.Val != 0.95 {
		t.Fatalf("param = %+v", agg.Param)
	}
}

func TestParsePercentileCall(t *testing.T) {
	expr := mustParse(t, `percentile(95, http_request_duration_ms{route="/checkout"}[5m])`)
	call, ok := expr.(*Call)
	if !ok {
		t.Fatalf("got %T, want *Call", expr)
	}
	if call.Func != "percentile" {
		t.Errorf("func = %q", call.Func)
	}
	if len(call.Args) != 2 {
		t.Fatalf("args = %d, want 2", len(call.Args))
	}
	if nl, ok := call.Args[0].(*NumberLiteral); !ok || nl.Val != 95 {
		t.Fatalf("args[0] = %+v", call.Args[0])
	}
	if _, ok := call.Args[1].(*MatrixSelector); !ok {
		t.Fatalf("args[1] = %T, want *MatrixSelector", call.Args[1])
	}
}

func TestParseBinaryArithAndComparison(t *testing.T) {
	expr := mustParse(t, `sum(rate(errors[5m])) by (service) / sum(rate(requests[5m])) by (service) > 0.05`)
	cmp, ok := expr.(*BinaryExpr)
	if !ok {
		t.Fatalf("got %T, want *BinaryExpr", expr)
	}
	if cmp.Op != ">" {
		t.Fatalf("op = %q", cmp.Op)
	}
	div, ok := cmp.LHS.(*BinaryExpr)
	if !ok || div.Op != "/" {
		t.Fatalf("cmp.LHS = %+v", cmp.LHS)
	}
}

// ---- Evaluator numeric tests ----

func TestEvalRateWithCounterReset(t *testing.T) {
	// Counter increases steadily then resets (process restart) at t=300.
	ds := &fakeDataSource{series: []Series{
		{
			Labels: map[string]string{"__name__": "http_requests_total", "service": "checkout"},
			Points: []Point{
				{T: 0, V: 100},
				{T: 60, V: 160},
				{T: 120, V: 220},
				{T: 180, V: 280},
				{T: 240, V: 0}, // reset
				{T: 300, V: 40},
			},
		},
	}}
	expr := mustParse(t, `rate(http_requests_total{service="checkout"}[5m])`)
	out, err := Eval(expr, ds, 300)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d series, want 1", len(out))
	}
	// increases: 60,60,60,60(reset->0 counted as 0? we count cur=0 as +0),40 => actually let's verify via formula
	// total = (160-100)+(220-160)+(280-220)+(0<280 -> +0)+(40-0) = 60+60+60+0+40 = 220
	// duration = 300-0 = 300
	want := 220.0 / 300.0
	got := out[0].Points[0].V
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("rate = %v, want %v", got, want)
	}
	if out[0].Labels["service"] != "checkout" {
		t.Errorf("labels = %+v", out[0].Labels)
	}
}

func TestEvalIRate(t *testing.T) {
	ds := &fakeDataSource{series: []Series{
		{
			Labels: map[string]string{"__name__": "http_requests_total", "service": "checkout"},
			Points: []Point{
				{T: 0, V: 100},
				{T: 60, V: 160},
				{T: 120, V: 250}, // last two points: 160 -> 250 over 60s
			},
		},
	}}
	expr := mustParse(t, `irate(http_requests_total{service="checkout"}[5m])`)
	out, err := Eval(expr, ds, 120)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	want := (250.0 - 160.0) / 60.0
	if math.Abs(out[0].Points[0].V-want) > 1e-9 {
		t.Errorf("irate = %v, want %v", out[0].Points[0].V, want)
	}
}

func TestEvalPercentileOverTime(t *testing.T) {
	// Latency samples: 10,20,30,...,100 ms (10 samples)
	var pts []Point
	for i := 1; i <= 10; i++ {
		pts = append(pts, Point{T: int64(i * 10), V: float64(i * 10)})
	}
	ds := &fakeDataSource{series: []Series{
		{Labels: map[string]string{"__name__": "http_request_duration_ms", "route": "/checkout"}, Points: pts},
	}}
	expr := mustParse(t, `percentile(50, http_request_duration_ms{route="/checkout"}[200s])`)
	out, err := Eval(expr, ds, 100)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	// values sorted: 10..100 step 10, n=10, idx = 0.5*9 = 4.5 -> interpolate between vals[4]=50 and vals[5]=60 -> 55
	want := 55.0
	if math.Abs(out[0].Points[0].V-want) > 1e-9 {
		t.Errorf("p50 = %v, want %v", out[0].Points[0].V, want)
	}
}

func TestEvalSumByAggregation(t *testing.T) {
	ds := &fakeDataSource{series: []Series{
		{Labels: map[string]string{"__name__": "http_requests_total", "service": "checkout", "instance": "a"}, Points: []Point{{T: 100, V: 5}}},
		{Labels: map[string]string{"__name__": "http_requests_total", "service": "checkout", "instance": "b"}, Points: []Point{{T: 100, V: 7}}},
		{Labels: map[string]string{"__name__": "http_requests_total", "service": "cart", "instance": "a"}, Points: []Point{{T: 100, V: 3}}},
	}}
	expr := mustParse(t, `sum(http_requests_total) by (service)`)
	out, err := Eval(expr, ds, 100)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	got := map[string]float64{}
	for _, s := range out {
		got[s.Labels["service"]] = s.Points[0].V
	}
	if got["checkout"] != 12 {
		t.Errorf("checkout sum = %v, want 12", got["checkout"])
	}
	if got["cart"] != 3 {
		t.Errorf("cart sum = %v, want 3", got["cart"])
	}
}

func TestEvalQuantileAggregation(t *testing.T) {
	ds := &fakeDataSource{series: []Series{
		{Labels: map[string]string{"__name__": "latency", "instance": "a"}, Points: []Point{{T: 100, V: 10}}},
		{Labels: map[string]string{"__name__": "latency", "instance": "b"}, Points: []Point{{T: 100, V: 20}}},
		{Labels: map[string]string{"__name__": "latency", "instance": "c"}, Points: []Point{{T: 100, V: 30}}},
	}}
	expr := mustParse(t, `quantile(0.5, latency)`)
	out, err := Eval(expr, ds, 100)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d series, want 1", len(out))
	}
	if out[0].Points[0].V != 20 {
		t.Errorf("median = %v, want 20", out[0].Points[0].V)
	}
}

func TestEvalBinaryArithmeticBetweenVectors(t *testing.T) {
	ds := &fakeDataSource{series: []Series{
		{Labels: map[string]string{"__name__": "errors", "service": "checkout"}, Points: []Point{{T: 100, V: 5}}},
		{Labels: map[string]string{"__name__": "requests", "service": "checkout"}, Points: []Point{{T: 100, V: 100}}},
	}}
	expr := mustParse(t, `errors{service="checkout"} / requests{service="checkout"}`)
	out, err := Eval(expr, ds, 100)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d series, want 1: %+v", len(out), out)
	}
	if out[0].Points[0].V != 0.05 {
		t.Errorf("ratio = %v, want 0.05", out[0].Points[0].V)
	}
}

func TestEvalAlertingComparisonFiltersVector(t *testing.T) {
	ds := &fakeDataSource{series: []Series{
		{Labels: map[string]string{"__name__": "error_ratio", "service": "checkout"}, Points: []Point{{T: 100, V: 0.1}}},
		{Labels: map[string]string{"__name__": "error_ratio", "service": "cart"}, Points: []Point{{T: 100, V: 0.01}}},
	}}
	expr := mustParse(t, `error_ratio > 0.05`)
	out, err := Eval(expr, ds, 100)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d series, want 1: %+v", len(out), out)
	}
	if out[0].Labels["service"] != "checkout" {
		t.Errorf("filtered series = %+v, want checkout", out[0].Labels)
	}
}

func TestEvalAvgMaxOverTime(t *testing.T) {
	ds := &fakeDataSource{series: []Series{
		{Labels: map[string]string{"__name__": "cpu", "host": "h1"}, Points: []Point{
			{T: 0, V: 10}, {T: 60, V: 20}, {T: 120, V: 30},
		}},
	}}
	avgExpr := mustParse(t, `avg_over_time(cpu{host="h1"}[3m])`)
	out, err := Eval(avgExpr, ds, 120)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	if out[0].Points[0].V != 20 {
		t.Errorf("avg = %v, want 20", out[0].Points[0].V)
	}

	maxExpr := mustParse(t, `max_over_time(cpu{host="h1"}[3m])`)
	out2, err := Eval(maxExpr, ds, 120)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	if out2[0].Points[0].V != 30 {
		t.Errorf("max = %v, want 30", out2[0].Points[0].V)
	}
}

func TestEvalDeterministicOrdering(t *testing.T) {
	ds := &fakeDataSource{series: []Series{
		{Labels: map[string]string{"__name__": "m", "service": "b"}, Points: []Point{{T: 0, V: 1}}},
		{Labels: map[string]string{"__name__": "m", "service": "a"}, Points: []Point{{T: 0, V: 2}}},
	}}
	expr := mustParse(t, `m`)
	out, err := Eval(expr, ds, 0)
	if err != nil {
		t.Fatalf("eval error: %v", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Labels["service"] < out[j].Labels["service"] })
	if len(out) != 2 || out[0].Labels["service"] != "a" || out[1].Labels["service"] != "b" {
		t.Fatalf("out = %+v", out)
	}
}
