package metricsql

import "time"

// Expr is implemented by every AST node that can be evaluated to a value
// (instant vector, range vector, or scalar).
type Expr interface {
	exprNode()
}

// NumberLiteral is a bare scalar constant, e.g. 0.95 or 500.
type NumberLiteral struct {
	Val float64
}

// VectorSelector selects an instant vector: a metric name plus a set of
// label matchers, e.g. http_requests_total{service="checkout",env="prod"}.
// Matchers use equality semantics; Metric may be empty to mean "any metric"
// (only valid when at least one label matcher is present) though in
// practice queries always name a metric.
type VectorSelector struct {
	Metric        string
	LabelMatchers map[string]string
}

// MatrixSelector selects a range vector: a VectorSelector plus a lookback
// window, e.g. http_requests_total{service="checkout"}[5m].
type MatrixSelector struct {
	Vector *VectorSelector
	Range  time.Duration
}

// Call is a function invocation such as rate(...), irate(...),
// avg_over_time(...), max_over_time(...) or percentile(95, ...).
type Call struct {
	Func string
	Args []Expr
}

// AggregateExpr groups an instant vector by a set of labels and reduces
// each group to a single value: sum, avg, min, max, count, quantile.
// For "quantile" the Param field carries the phi (0..1) argument, e.g.
// quantile(0.95, expr).
type AggregateExpr struct {
	Op    string
	Param Expr // used only by quantile; nil otherwise
	Expr  Expr
	By    []string
}

// BinaryExpr applies an arithmetic (+ - * /) or comparison (> < >= <= == !=)
// operator between two sub-expressions. Comparison operators filter the
// left-hand vector, keeping only series whose value satisfies the
// comparison against the right-hand side (which may be scalar or vector).
type BinaryExpr struct {
	Op  string
	LHS Expr
	RHS Expr
}

func (*NumberLiteral) exprNode()  {}
func (*VectorSelector) exprNode() {}
func (*MatrixSelector) exprNode() {}
func (*Call) exprNode()           {}
func (*AggregateExpr) exprNode()  {}
func (*BinaryExpr) exprNode()     {}
