package pipeql

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Result is the outcome of running a pipeline. Exactly one of the two
// shapes is populated, indicated by IsStats: a raw record table (from a
// pipeline with no `stats` stage) or a stats table (grouped aggregation
// rows, column order given by StatsFields).
type Result struct {
	IsStats bool

	Records []LogRecord

	StatsFields []string
	StatsRows   []map[string]any
}

// Run parses and executes a pipeline query against src over [start, end]
// (unix seconds, inclusive), returning the final Result.
func Run(pipeline string, src LogSource, start, end int64) (Result, error) {
	toks := Lex(pipeline)
	ast, err := Parse(toks)
	if err != nil {
		return Result{}, fmt.Errorf("parse error: %w", err)
	}
	ex := &Executor{Source: src, Start: start, End: end}
	return ex.Execute(ast)
}

// Executor runs a parsed Pipeline against a LogSource, streaming records
// through each stage in order.
type Executor struct {
	Source LogSource
	Start  int64
	End    int64
}

// Execute runs every stage of the pipeline in sequence.
func (ex *Executor) Execute(p *Pipeline) (Result, error) {
	if len(p.Stages) == 0 {
		return Result{}, fmt.Errorf("empty pipeline")
	}
	search, ok := p.Stages[0].(*SearchStage)
	if !ok {
		return Result{}, fmt.Errorf("pipeline must start with a search stage")
	}

	filters := search.Filters
	if search.Star {
		filters = nil
	}
	records, err := ex.Source.Search(filters, search.Keywords, ex.Start, ex.End)
	if err != nil {
		return Result{}, fmt.Errorf("search: %w", err)
	}

	result := Result{Records: records}

	for _, stage := range p.Stages[1:] {
		switch st := stage.(type) {
		case *FilterStage:
			if result.IsStats {
				return Result{}, fmt.Errorf("filter stage cannot follow stats")
			}
			result.Records = filterRecords(result.Records, st.Expr)

		case *StatsStage:
			if result.IsStats {
				return Result{}, fmt.Errorf("only one stats stage is supported")
			}
			fields, rows, err := computeStats(result.Records, st)
			if err != nil {
				return Result{}, err
			}
			result = Result{IsStats: true, StatsFields: fields, StatsRows: rows}

		case *SortStage:
			if result.IsStats {
				sortStatsRows(result.StatsRows, result.StatsFields, st)
			} else {
				sortRecords(result.Records, st)
			}

		case *LimitStage:
			if result.IsStats {
				if st.N < len(result.StatsRows) {
					result.StatsRows = result.StatsRows[:st.N]
				}
			} else {
				if st.N < len(result.Records) {
					result.Records = result.Records[:st.N]
				}
			}

		default:
			return Result{}, fmt.Errorf("unsupported stage %T", stage)
		}
	}

	return result, nil
}

// ---- filter stage ----

func filterRecords(records []LogRecord, expr BoolExpr) []LogRecord {
	var out []LogRecord
	for _, r := range records {
		if evalBool(expr, r) {
			out = append(out, r)
		}
	}
	return out
}

func evalBool(expr BoolExpr, r LogRecord) bool {
	switch e := expr.(type) {
	case *AndExpr:
		return evalBool(e.LHS, r) && evalBool(e.RHS, r)
	case *OrExpr:
		return evalBool(e.LHS, r) || evalBool(e.RHS, r)
	case *KeywordExpr:
		return strings.Contains(strings.ToLower(r.Message), strings.ToLower(e.Word))
	case *Condition:
		return evalCondition(e, r)
	default:
		return false
	}
}

func evalCondition(c *Condition, r LogRecord) bool {
	fv, ok := getField(r, c.Field)
	if !ok {
		return false
	}
	fnum, fIsNum := toFloat(fv)
	cnum, cIsNum := parseFloat(c.Value)

	if fIsNum && cIsNum {
		switch c.Op {
		case OpEQ:
			return fnum == cnum
		case OpNE:
			return fnum != cnum
		case OpGT:
			return fnum > cnum
		case OpLT:
			return fnum < cnum
		case OpGE:
			return fnum >= cnum
		case OpLE:
			return fnum <= cnum
		}
	}

	fstr := fmt.Sprint(fv)
	switch c.Op {
	case OpEQ:
		return fstr == c.Value
	case OpNE:
		return fstr != c.Value
	case OpGT:
		return fstr > c.Value
	case OpLT:
		return fstr < c.Value
	case OpGE:
		return fstr >= c.Value
	case OpLE:
		return fstr <= c.Value
	}
	return false
}

// getField resolves a field name against a LogRecord: well-known fields
// first (time/_time, host, service, level, message/msg), then Fields.
func getField(r LogRecord, name string) (any, bool) {
	switch strings.ToLower(name) {
	case "time", "_time":
		return r.Time, true
	case "host":
		return r.Host, true
	case "service":
		return r.Service, true
	case "level":
		return r.Level, true
	case "message", "msg":
		return r.Message, true
	}
	if r.Fields != nil {
		if v, ok := r.Fields[name]; ok {
			return v, true
		}
	}
	return nil, false
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		return parseFloat(n)
	default:
		return 0, false
	}
}

func parseFloat(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// ---- stats stage ----

func computeStats(records []LogRecord, st *StatsStage) ([]string, []map[string]any, error) {
	type group struct {
		keyVals map[string]any
		values  map[int][]float64 // per agg index, collected numeric values
		count   int
	}

	groups := map[string]*group{}
	var order []string

	for _, r := range records {
		keyParts := make([]string, 0, len(st.By))
		keyVals := make(map[string]any, len(st.By))
		for _, f := range st.By {
			v, ok := getField(r, f)
			if !ok {
				v = ""
			}
			keyVals[f] = v
			keyParts = append(keyParts, fmt.Sprint(v))
		}
		key := strings.Join(keyParts, "\x1f")

		g, ok := groups[key]
		if !ok {
			g = &group{keyVals: keyVals, values: map[int][]float64{}}
			groups[key] = g
			order = append(order, key)
		}
		g.count++

		for i, agg := range st.Aggs {
			if agg.Func == AggCount {
				continue
			}
			fv, ok := getField(r, agg.Field)
			if !ok {
				continue
			}
			if n, isNum := toFloat(fv); isNum {
				g.values[i] = append(g.values[i], n)
			}
		}
	}

	fields := append([]string{}, st.By...)
	for _, agg := range st.Aggs {
		fields = append(fields, agg.As)
	}

	rows := make([]map[string]any, 0, len(order))
	for _, key := range order {
		g := groups[key]
		row := map[string]any{}
		for _, f := range st.By {
			row[f] = g.keyVals[f]
		}
		for i, agg := range st.Aggs {
			var val any
			switch agg.Func {
			case AggCount:
				val = g.count
			case AggSum:
				val = sumF(g.values[i])
			case AggAvg:
				vals := g.values[i]
				if len(vals) == 0 {
					val = 0.0
				} else {
					val = sumF(vals) / float64(len(vals))
				}
			case AggMin:
				val = minF(g.values[i])
			case AggMax:
				val = maxF(g.values[i])
			case AggP50:
				val = percentile(g.values[i], 0.50)
			case AggP95:
				val = percentile(g.values[i], 0.95)
			case AggP99:
				val = percentile(g.values[i], 0.99)
			default:
				return nil, nil, fmt.Errorf("unknown aggregation %q", agg.Func)
			}
			row[agg.As] = val
		}
		rows = append(rows, row)
	}

	return fields, rows, nil
}

func sumF(vals []float64) float64 {
	var s float64
	for _, v := range vals {
		s += v
	}
	return s
}

func minF(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func maxF(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// percentile computes the frac-th quantile (0..1) of vals using linear
// interpolation between the two nearest ranks.
func percentile(vals []float64, frac float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := frac * float64(len(sorted)-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	f := idx - float64(lo)
	return sorted[lo] + (sorted[hi]-sorted[lo])*f
}

// ---- sort stage ----

func sortRecords(records []LogRecord, st *SortStage) {
	field := st.Field
	if field == "" {
		field = "_time"
	}
	sort.SliceStable(records, func(i, j int) bool {
		vi, _ := getField(records[i], field)
		vj, _ := getField(records[j], field)
		if st.Desc {
			return compareAny(vi, vj) > 0
		}
		return compareAny(vi, vj) < 0
	})
}

func sortStatsRows(rows []map[string]any, fields []string, st *SortStage) {
	field := st.Field
	if field == "" && len(fields) > 0 {
		field = fields[len(fields)-1] // default: sort by the last (aggregation) column
	}
	sort.SliceStable(rows, func(i, j int) bool {
		vi := rows[i][field]
		vj := rows[j][field]
		if st.Desc {
			return compareAny(vi, vj) > 0
		}
		return compareAny(vi, vj) < 0
	})
}

// compareAny returns -1, 0, or 1 depending on whether a is less than, equal
// to, or greater than b, comparing numerically when both sides parse as
// numbers and lexicographically otherwise.
func compareAny(a, b any) int {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	as, bs := fmt.Sprint(a), fmt.Sprint(b)
	switch {
	case as < bs:
		return -1
	case as > bs:
		return 1
	default:
		return 0
	}
}
