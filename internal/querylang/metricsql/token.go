// Package metricsql implements a small PromQL-like query language for
// querying time series metrics: vector/matrix selectors, aggregations,
// functions (rate, irate, avg_over_time, max_over_time, quantile/percentile)
// and binary arithmetic/comparison operators.
package metricsql

import "fmt"

// TokenType identifies the lexical class of a Token.
type TokenType int

const (
	TokEOF TokenType = iota
	TokIdent
	TokNumber
	TokString
	TokDuration // e.g. 5m, 30s, 1h

	TokLBrace   // {
	TokRBrace   // }
	TokLParen   // (
	TokRParen   // )
	TokLBracket // [
	TokRBracket // ]
	TokComma    // ,

	TokEQ   // =
	TokNEQ  // !=
	TokEQEQ // ==
	TokGT   // >
	TokLT   // <
	TokGE   // >=
	TokLE   // <=

	TokPlus  // +
	TokMinus // -
	TokMul   // *
	TokDiv   // /

	TokBy // by (keyword, contextual)
)

// Token is a single lexical token produced by Lex.
type Token struct {
	Type TokenType
	Text string
	Pos  int
}

func (t Token) String() string {
	return fmt.Sprintf("%s(%q)@%d", tokenTypeName(t.Type), t.Text, t.Pos)
}

func tokenTypeName(tt TokenType) string {
	switch tt {
	case TokEOF:
		return "EOF"
	case TokIdent:
		return "IDENT"
	case TokNumber:
		return "NUMBER"
	case TokString:
		return "STRING"
	case TokDuration:
		return "DURATION"
	case TokLBrace:
		return "LBRACE"
	case TokRBrace:
		return "RBRACE"
	case TokLParen:
		return "LPAREN"
	case TokRParen:
		return "RPAREN"
	case TokLBracket:
		return "LBRACKET"
	case TokRBracket:
		return "RBRACKET"
	case TokComma:
		return "COMMA"
	case TokEQ:
		return "EQ"
	case TokNEQ:
		return "NEQ"
	case TokEQEQ:
		return "EQEQ"
	case TokGT:
		return "GT"
	case TokLT:
		return "LT"
	case TokGE:
		return "GE"
	case TokLE:
		return "LE"
	case TokPlus:
		return "PLUS"
	case TokMinus:
		return "MINUS"
	case TokMul:
		return "MUL"
	case TokDiv:
		return "DIV"
	case TokBy:
		return "BY"
	default:
		return "UNKNOWN"
	}
}
