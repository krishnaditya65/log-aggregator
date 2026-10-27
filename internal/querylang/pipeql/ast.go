package pipeql

// Stage is implemented by every pipeline stage node.
type Stage interface {
	stageNode()
}

// SearchStage is always the first stage of a pipeline. It fetches the
// initial record set from a LogSource. Filters are field=value equality
// matchers (typically pushed down to the source); Keywords are free-text
// terms that must appear in the record's message. Star indicates a bare
// `search *` (fetch everything in the time range).
type SearchStage struct {
	Star     bool
	Filters  map[string]string
	Keywords []string
}

// FilterStage narrows the record stream using a boolean expression.
type FilterStage struct {
	Expr BoolExpr
}

// AggFunc names a stats aggregation function.
type AggFunc string

const (
	AggCount AggFunc = "count"
	AggSum   AggFunc = "sum"
	AggAvg   AggFunc = "avg"
	AggMin   AggFunc = "min"
	AggMax   AggFunc = "max"
	AggP50   AggFunc = "p50"
	AggP95   AggFunc = "p95"
	AggP99   AggFunc = "p99"
)

// AggCall is a single aggregation in a stats stage, e.g. count() or
// p95(latency_ms). As is the output column name (defaults to a rendering
// of the call itself, e.g. "p95(latency_ms)").
type AggCall struct {
	Func  AggFunc
	Field string // empty for count()
	As    string
}

// StatsStage computes one or more aggregations, optionally grouped by a set
// of fields.
type StatsStage struct {
	Aggs []AggCall
	By   []string
}

// SortStage sorts the current record/row stream by a field (defaulting to
// "_time" for raw records or the first stats column for stats tables) in
// ascending or descending order.
type SortStage struct {
	Desc  bool
	Field string // may be empty to mean "default field"
}

// LimitStage truncates the stream to at most N rows.
type LimitStage struct {
	N int
}

func (*SearchStage) stageNode() {}
func (*FilterStage) stageNode() {}
func (*StatsStage) stageNode()  {}
func (*SortStage) stageNode()   {}
func (*LimitStage) stageNode()  {}

// BoolExpr is implemented by boolean expression nodes used in filter
// stages: AndExpr, OrExpr, NotExpr, Condition, and KeywordExpr.
type BoolExpr interface {
	boolNode()
}

// AndExpr is the logical conjunction of two boolean expressions.
type AndExpr struct{ LHS, RHS BoolExpr }

// OrExpr is the logical disjunction of two boolean expressions.
type OrExpr struct{ LHS, RHS BoolExpr }

// CompareOp identifies a comparison operator used in a Condition.
type CompareOp string

const (
	OpEQ CompareOp = "="
	OpNE CompareOp = "!="
	OpGT CompareOp = ">"
	OpLT CompareOp = "<"
	OpGE CompareOp = ">="
	OpLE CompareOp = "<="
)

// Condition compares a record field against a literal value.
type Condition struct {
	Field string
	Op    CompareOp
	Value string
}

// KeywordExpr matches records whose message contains the given substring
// (case-insensitive).
type KeywordExpr struct {
	Word string
}

func (*AndExpr) boolNode()     {}
func (*OrExpr) boolNode()      {}
func (*Condition) boolNode()   {}
func (*KeywordExpr) boolNode() {}

// Pipeline is the top-level AST: an ordered list of stages, always starting
// with a SearchStage.
type Pipeline struct {
	Stages []Stage
}
