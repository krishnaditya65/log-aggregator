// Package pipeql implements a small pipelined log-query language in the
// style of Splunk SPL / Kusto: a "search" stage that fetches records from a
// LogSource, followed by zero or more "|"-chained stages (filter, stats,
// sort, limit) that transform the record stream.
package pipeql

import "fmt"

// TokenType identifies the lexical class of a Token.
type TokenType int

const (
	TokEOF TokenType = iota
	TokIdent
	TokNumber
	TokString
	TokStar // *
	TokPipe // |

	TokLParen // (
	TokRParen // )
	TokComma  // ,

	TokEQ  // =
	TokNEQ // !=
	TokGT  // >
	TokLT  // <
	TokGE  // >=
	TokLE  // <=
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
	case TokStar:
		return "STAR"
	case TokPipe:
		return "PIPE"
	case TokLParen:
		return "LPAREN"
	case TokRParen:
		return "RPAREN"
	case TokComma:
		return "COMMA"
	case TokEQ:
		return "EQ"
	case TokNEQ:
		return "NEQ"
	case TokGT:
		return "GT"
	case TokLT:
		return "LT"
	case TokGE:
		return "GE"
	case TokLE:
		return "LE"
	default:
		return "UNKNOWN"
	}
}
