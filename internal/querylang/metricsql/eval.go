package metricsql

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// instantLookback mirrors PromQL's default 5m staleness window: when an
// instant vector selector is evaluated at evalTime, the most recent sample
// at or before evalTime within this window is used.
const instantLookback = 5 * 60 // seconds

// Evaluator evaluates a metricsql AST against a DataSource.
type Evaluator struct {
	DS       DataSource
	EvalTime int64
}

// Eval is the main entry point: it evaluates expr against ds at evalTime
// (a unix timestamp in seconds) and returns the resulting set of series.
//
// For every expression type except a bare *MatrixSelector, the result is an
// instant vector: each Series has exactly one Point at evalTime. A bare
// *MatrixSelector (e.g. `metric{...}[5m]` used at the top level) instead
// returns the raw range vector with all points in the window, since it has
// no well-defined single value.
func Eval(expr Expr, ds DataSource, evalTime int64) ([]Series, error) {
	e := &Evaluator{DS: ds, EvalTime: evalTime}
	if ms, ok := expr.(*MatrixSelector); ok {
		return e.evalRangeVector(ms)
	}
	return e.evalInstant(expr)
}

func (e *Evaluator) evalRangeVector(ms *MatrixSelector) ([]Series, error) {
	matchers := buildMatchers(ms.Vector)
	start := e.EvalTime - int64(ms.Range.Seconds())
	series, err := e.DS.QueryRange(matchers, start, e.EvalTime)
	if err != nil {
		return nil, fmt.Errorf("query range for %s: %w", ms.Vector.Metric, err)
	}
	for i := range series {
		sort.Slice(series[i].Points, func(a, b int) bool { return series[i].Points[a].T < series[i].Points[b].T })
	}
	return series, nil
}

func buildMatchers(vs *VectorSelector) map[string]string {
	m := make(map[string]string, len(vs.LabelMatchers)+1)
	for k, v := range vs.LabelMatchers {
		m[k] = v
	}
	if vs.Metric != "" {
		m["__name__"] = vs.Metric
	}
	return m
}

func (e *Evaluator) evalInstant(expr Expr) ([]Series, error) {
	switch v := expr.(type) {
	case *NumberLiteral:
		return []Series{{Labels: nil, Points: []Point{{T: e.EvalTime, V: v.Val}}}}, nil

	case *VectorSelector:
		return e.evalVectorSelector(v)

	case *Call:
		return e.evalCall(v)

	case *AggregateExpr:
		return e.evalAggregate(v)

	case *BinaryExpr:
		return e.evalBinary(v)

	case *MatrixSelector:
		// A matrix selector nested inside another expression that expects
		// an instant vector is invalid; only functions may consume it
		// directly (handled in evalCall).
		return nil, fmt.Errorf("range vector used where instant vector expected: %s[%s]", v.Vector.Metric, v.Range)

	default:
		return nil, fmt.Errorf("unsupported expression type %T", expr)
	}
}

func (e *Evaluator) evalVectorSelector(vs *VectorSelector) ([]Series, error) {
	matchers := buildMatchers(vs)
	start := e.EvalTime - instantLookback
	series, err := e.DS.QueryRange(matchers, start, e.EvalTime)
	if err != nil {
		return nil, fmt.Errorf("query range for %s: %w", vs.Metric, err)
	}
	var out []Series
	for _, s := range series {
		var latest *Point
		for i := range s.Points {
			p := s.Points[i]
			if p.T <= e.EvalTime && (latest == nil || p.T > latest.T) {
				pp := p
				latest = &pp
			}
		}
		if latest == nil {
			continue
		}
		out = append(out, Series{Labels: s.Labels, Points: []Point{{T: e.EvalTime, V: latest.V}}})
	}
	return out, nil
}

// evalCall handles rate(), irate(), *_over_time() and percentile().
func (e *Evaluator) evalCall(c *Call) ([]Series, error) {
	switch c.Func {
	case "rate", "irate", "avg_over_time", "max_over_time", "min_over_time", "sum_over_time":
		if len(c.Args) != 1 {
			return nil, fmt.Errorf("%s() expects exactly 1 argument, got %d", c.Func, len(c.Args))
		}
		ms, ok := c.Args[0].(*MatrixSelector)
		if !ok {
			return nil, fmt.Errorf("%s() expects a range vector argument (e.g. metric[5m])", c.Func)
		}
		raw, err := e.evalRangeVector(ms)
		if err != nil {
			return nil, err
		}
		var out []Series
		for _, s := range raw {
			if len(s.Points) == 0 {
				continue
			}
			var val float64
			switch c.Func {
			case "rate":
				val = computeRate(s.Points)
			case "irate":
				val = computeIRate(s.Points)
			case "avg_over_time":
				val = avgPoints(s.Points)
			case "max_over_time":
				val = maxPoints(s.Points)
			case "min_over_time":
				val = minPoints(s.Points)
			case "sum_over_time":
				val = sumPoints(s.Points)
			}
			out = append(out, Series{Labels: s.Labels, Points: []Point{{T: e.EvalTime, V: val}}})
		}
		return out, nil

	case "percentile":
		if len(c.Args) != 2 {
			return nil, fmt.Errorf("percentile() expects 2 arguments: percentile(N, metric[range])")
		}
		nl, ok := c.Args[0].(*NumberLiteral)
		if !ok {
			return nil, fmt.Errorf("percentile() first argument must be a number 0-100")
		}
		ms, ok := c.Args[1].(*MatrixSelector)
		if !ok {
			return nil, fmt.Errorf("percentile() second argument must be a range vector, e.g. metric[5m]")
		}
		raw, err := e.evalRangeVector(ms)
		if err != nil {
			return nil, err
		}
		frac := nl.Val / 100.0
		var out []Series
		for _, s := range raw {
			if len(s.Points) == 0 {
				continue
			}
			vals := pointValues(s.Points)
			val := interpolatedPercentile(vals, frac)
			out = append(out, Series{Labels: s.Labels, Points: []Point{{T: e.EvalTime, V: val}}})
		}
		return out, nil

	default:
		return nil, fmt.Errorf("unknown function %q", c.Func)
	}
}

// computeRate implements PromQL-style per-second rate over a counter,
// accounting for counter resets: whenever a sample is lower than the
// previous one, we assume the counter reset to zero and treat the new
// sample's value as pure increase rather than a negative delta.
func computeRate(points []Point) float64 {
	if len(points) < 2 {
		return 0
	}
	total := 0.0
	prev := points[0].V
	for i := 1; i < len(points); i++ {
		cur := points[i].V
		if cur < prev {
			total += cur // counter reset: count the post-reset value as increase
		} else {
			total += cur - prev
		}
		prev = cur
	}
	duration := float64(points[len(points)-1].T - points[0].T)
	if duration <= 0 {
		return 0
	}
	return total / duration
}

// computeIRate uses only the last two samples in the window (instantaneous
// rate), also handling counter resets.
func computeIRate(points []Point) float64 {
	if len(points) < 2 {
		return 0
	}
	last := points[len(points)-1]
	prev := points[len(points)-2]
	dt := float64(last.T - prev.T)
	if dt <= 0 {
		return 0
	}
	var dv float64
	if last.V < prev.V {
		dv = last.V
	} else {
		dv = last.V - prev.V
	}
	return dv / dt
}

func avgPoints(points []Point) float64 { return sumPoints(points) / float64(len(points)) }

func sumPoints(points []Point) float64 {
	var s float64
	for _, p := range points {
		s += p.V
	}
	return s
}

func maxPoints(points []Point) float64 {
	m := points[0].V
	for _, p := range points[1:] {
		if p.V > m {
			m = p.V
		}
	}
	return m
}

func minPoints(points []Point) float64 {
	m := points[0].V
	for _, p := range points[1:] {
		if p.V < m {
			m = p.V
		}
	}
	return m
}

func pointValues(points []Point) []float64 {
	vals := make([]float64, len(points))
	for i, p := range points {
		vals[i] = p.V
	}
	return vals
}

// interpolatedPercentile computes the frac-th quantile (0..1) of vals using
// linear interpolation between the two nearest ranks, matching common
// percentile implementations (e.g. numpy's default / Excel PERCENTILE.INC).
func interpolatedPercentile(vals []float64, frac float64) float64 {
	if len(vals) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	if frac <= 0 {
		return sorted[0]
	}
	if frac >= 1 {
		return sorted[len(sorted)-1]
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := frac * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	frac2 := idx - float64(lo)
	return sorted[lo] + (sorted[hi]-sorted[lo])*frac2
}

// evalAggregate implements sum/avg/min/max/count/quantile aggregation over
// an instant vector, grouped by the labels named in By (or a single group
// spanning all series if By is empty).
func (e *Evaluator) evalAggregate(a *AggregateExpr) ([]Series, error) {
	inner, err := e.evalInstant(a.Expr)
	if err != nil {
		return nil, err
	}

	var phi float64
	if a.Op == "quantile" {
		if a.Param == nil {
			return nil, fmt.Errorf("quantile() requires a phi parameter, e.g. quantile(0.95, expr)")
		}
		pv, err := e.evalInstant(a.Param)
		if err != nil {
			return nil, err
		}
		if len(pv) == 0 || len(pv[0].Points) == 0 {
			return nil, fmt.Errorf("quantile() phi parameter did not evaluate to a scalar")
		}
		phi = pv[0].Points[0].V
	}

	type group struct {
		labels map[string]string
		vals   []float64
	}
	groups := map[string]*group{}
	var order []string

	for _, s := range inner {
		if len(s.Points) == 0 {
			continue
		}
		key, labels := groupKey(s.Labels, a.By)
		g, ok := groups[key]
		if !ok {
			g = &group{labels: labels}
			groups[key] = g
			order = append(order, key)
		}
		g.vals = append(g.vals, s.Points[0].V)
	}

	out := make([]Series, 0, len(order))
	for _, key := range order {
		g := groups[key]
		var val float64
		switch a.Op {
		case "sum":
			val = sumFloats(g.vals)
		case "avg":
			val = sumFloats(g.vals) / float64(len(g.vals))
		case "min":
			val = minFloats(g.vals)
		case "max":
			val = maxFloats(g.vals)
		case "count":
			val = float64(len(g.vals))
		case "quantile":
			val = interpolatedPercentile(g.vals, phi)
		default:
			return nil, fmt.Errorf("unknown aggregation operator %q", a.Op)
		}
		out = append(out, Series{Labels: g.labels, Points: []Point{{T: e.EvalTime, V: val}}})
	}
	return out, nil
}

func groupKey(labels map[string]string, by []string) (string, map[string]string) {
	if len(by) == 0 {
		return "", map[string]string{}
	}
	sub := make(map[string]string, len(by))
	parts := make([]string, 0, len(by))
	for _, k := range by {
		v := labels[k]
		sub[k] = v
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ","), sub
}

func sumFloats(vals []float64) float64 {
	var s float64
	for _, v := range vals {
		s += v
	}
	return s
}

func minFloats(vals []float64) float64 {
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func maxFloats(vals []float64) float64 {
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// evalBinary evaluates arithmetic (+ - * /) and comparison (> < >= <= == !=)
// operators between two instant-vector or scalar sub-expressions.
//
// Vector-vector operations are matched by their full label set (excluding
// __name__): only series present on both sides with identical labels are
// combined. Vector-scalar operations apply the scalar to every series on
// the vector side. Comparison operators filter the vector side, keeping
// only series whose value satisfies the comparison (Prometheus's default,
// non-"bool" filtering behavior).
func (e *Evaluator) evalBinary(b *BinaryExpr) ([]Series, error) {
	lhs, err := e.evalInstant(b.LHS)
	if err != nil {
		return nil, err
	}
	rhs, err := e.evalInstant(b.RHS)
	if err != nil {
		return nil, err
	}

	lhsScalar := isScalar(lhs)
	rhsScalar := isScalar(rhs)

	switch b.Op {
	case "+", "-", "*", "/":
		return applyArith(b.Op, lhs, rhs, lhsScalar, rhsScalar, e.EvalTime)
	case ">", "<", ">=", "<=", "==", "!=":
		return applyCompare(b.Op, lhs, rhs, lhsScalar, rhsScalar)
	default:
		return nil, fmt.Errorf("unknown binary operator %q", b.Op)
	}
}

func isScalar(series []Series) bool {
	return len(series) == 1 && series[0].Labels == nil
}

func applyArith(op string, lhs, rhs []Series, lhsScalar, rhsScalar bool, evalTime int64) ([]Series, error) {
	apply := func(a, b float64) float64 {
		switch op {
		case "+":
			return a + b
		case "-":
			return a - b
		case "*":
			return a * b
		case "/":
			return a / b
		}
		return math.NaN()
	}

	switch {
	case lhsScalar && rhsScalar:
		return []Series{{Labels: nil, Points: []Point{{T: evalTime, V: apply(lhs[0].Points[0].V, rhs[0].Points[0].V)}}}}, nil

	case rhsScalar:
		scalarVal := rhs[0].Points[0].V
		out := make([]Series, 0, len(lhs))
		for _, s := range lhs {
			if len(s.Points) == 0 {
				continue
			}
			out = append(out, Series{Labels: s.Labels, Points: []Point{{T: evalTime, V: apply(s.Points[0].V, scalarVal)}}})
		}
		return out, nil

	case lhsScalar:
		scalarVal := lhs[0].Points[0].V
		out := make([]Series, 0, len(rhs))
		for _, s := range rhs {
			if len(s.Points) == 0 {
				continue
			}
			out = append(out, Series{Labels: s.Labels, Points: []Point{{T: evalTime, V: apply(scalarVal, s.Points[0].V)}}})
		}
		return out, nil

	default:
		rhsByKey := map[string]Series{}
		for _, s := range rhs {
			rhsByKey[labelKey(s.Labels)] = s
		}
		var out []Series
		for _, ls := range lhs {
			if len(ls.Points) == 0 {
				continue
			}
			rs, ok := rhsByKey[labelKey(ls.Labels)]
			if !ok || len(rs.Points) == 0 {
				continue
			}
			out = append(out, Series{Labels: ls.Labels, Points: []Point{{T: evalTime, V: apply(ls.Points[0].V, rs.Points[0].V)}}})
		}
		return out, nil
	}
}

func applyCompare(op string, lhs, rhs []Series, lhsScalar, rhsScalar bool) ([]Series, error) {
	cmp := func(a, b float64) bool {
		switch op {
		case ">":
			return a > b
		case "<":
			return a < b
		case ">=":
			return a >= b
		case "<=":
			return a <= b
		case "==":
			return a == b
		case "!=":
			return a != b
		}
		return false
	}

	if lhsScalar && rhsScalar {
		if cmp(lhs[0].Points[0].V, rhs[0].Points[0].V) {
			return lhs, nil
		}
		return nil, nil
	}

	if rhsScalar {
		scalarVal := rhs[0].Points[0].V
		var out []Series
		for _, s := range lhs {
			if len(s.Points) == 0 {
				continue
			}
			if cmp(s.Points[0].V, scalarVal) {
				out = append(out, s)
			}
		}
		return out, nil
	}

	if lhsScalar {
		scalarVal := lhs[0].Points[0].V
		var out []Series
		for _, s := range rhs {
			if len(s.Points) == 0 {
				continue
			}
			if cmp(scalarVal, s.Points[0].V) {
				out = append(out, s)
			}
		}
		return out, nil
	}

	rhsByKey := map[string]Series{}
	for _, s := range rhs {
		rhsByKey[labelKey(s.Labels)] = s
	}
	var out []Series
	for _, ls := range lhs {
		if len(ls.Points) == 0 {
			continue
		}
		rs, ok := rhsByKey[labelKey(ls.Labels)]
		if !ok || len(rs.Points) == 0 {
			continue
		}
		if cmp(ls.Points[0].V, rs.Points[0].V) {
			out = append(out, ls)
		}
	}
	return out, nil
}

func labelKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k == "__name__" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}
