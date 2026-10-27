package metricsql

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

var aggregateOps = map[string]bool{
	"sum": true, "avg": true, "min": true, "max": true, "count": true, "quantile": true,
}

var callFuncs = map[string]bool{
	"rate": true, "irate": true, "avg_over_time": true, "max_over_time": true,
	"min_over_time": true, "sum_over_time": true, "percentile": true,
}

// Parser is a recursive-descent parser over a token stream produced by Lex.
type Parser struct {
	toks []Token
	pos  int
}

// Parse parses a token stream into an Expr AST.
func Parse(tokens []Token) (Expr, error) {
	p := &Parser{toks: tokens}
	expr, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.cur().Type != TokEOF {
		return nil, fmt.Errorf("unexpected token %s after expression", p.cur())
	}
	return expr, nil
}

func (p *Parser) cur() Token {
	if p.pos >= len(p.toks) {
		return Token{Type: TokEOF}
	}
	return p.toks[p.pos]
}

func (p *Parser) peek(n int) Token {
	idx := p.pos + n
	if idx >= len(p.toks) {
		return Token{Type: TokEOF}
	}
	return p.toks[idx]
}

func (p *Parser) advance() Token {
	t := p.cur()
	p.pos++
	return t
}

func (p *Parser) expect(tt TokenType) (Token, error) {
	if p.cur().Type != tt {
		return Token{}, fmt.Errorf("expected %s but got %s", tokenTypeName(tt), p.cur())
	}
	return p.advance(), nil
}

// parseExpr handles comparison operators (lowest precedence): > < >= <= == !=
func (p *Parser) parseExpr() (Expr, error) {
	lhs, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		op := ""
		switch p.cur().Type {
		case TokGT:
			op = ">"
		case TokLT:
			op = "<"
		case TokGE:
			op = ">="
		case TokLE:
			op = "<="
		case TokEQEQ:
			op = "=="
		case TokNEQ:
			op = "!="
		default:
			return lhs, nil
		}
		p.advance()
		rhs, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		lhs = &BinaryExpr{Op: op, LHS: lhs, RHS: rhs}
	}
}

func (p *Parser) parseAdditive() (Expr, error) {
	lhs, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		op := ""
		switch p.cur().Type {
		case TokPlus:
			op = "+"
		case TokMinus:
			op = "-"
		default:
			return lhs, nil
		}
		p.advance()
		rhs, err := p.parseMultiplicative()
		if err != nil {
			return nil, err
		}
		lhs = &BinaryExpr{Op: op, LHS: lhs, RHS: rhs}
	}
}

func (p *Parser) parseMultiplicative() (Expr, error) {
	lhs, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		op := ""
		switch p.cur().Type {
		case TokMul:
			op = "*"
		case TokDiv:
			op = "/"
		default:
			return lhs, nil
		}
		p.advance()
		rhs, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		lhs = &BinaryExpr{Op: op, LHS: lhs, RHS: rhs}
	}
}

func (p *Parser) parseUnary() (Expr, error) {
	if p.cur().Type == TokMinus {
		p.advance()
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if nl, ok := operand.(*NumberLiteral); ok {
			return &NumberLiteral{Val: -nl.Val}, nil
		}
		return &BinaryExpr{Op: "-", LHS: &NumberLiteral{Val: 0}, RHS: operand}, nil
	}
	return p.parsePrimary()
}

func (p *Parser) parsePrimary() (Expr, error) {
	tok := p.cur()
	switch tok.Type {
	case TokNumber:
		p.advance()
		v, err := strconv.ParseFloat(tok.Text, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", tok.Text, err)
		}
		return &NumberLiteral{Val: v}, nil

	case TokLParen:
		p.advance()
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TokRParen); err != nil {
			return nil, err
		}
		return inner, nil

	case TokIdent:
		name := tok.Text
		lower := strings.ToLower(name)

		if aggregateOps[lower] && p.peek(1).Type == TokLParen {
			return p.parseAggregate(lower)
		}
		if callFuncs[lower] && p.peek(1).Type == TokLParen {
			return p.parseCall(name)
		}
		return p.parseSelector()

	default:
		return nil, fmt.Errorf("unexpected token %s", tok)
	}
}

// parseAggregate parses `<op>(<expr> [, <param>]) [by (label, ...)]`.
// For quantile, the phi parameter comes first: quantile(0.95, expr).
func (p *Parser) parseAggregate(op string) (Expr, error) {
	p.advance() // op ident
	if _, err := p.expect(TokLParen); err != nil {
		return nil, err
	}

	agg := &AggregateExpr{Op: op}

	first, err := p.parseExpr()
	if err != nil {
		return nil, err
	}

	if op == "quantile" {
		if _, err := p.expect(TokComma); err != nil {
			return nil, err
		}
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		agg.Param = first
		agg.Expr = inner
	} else {
		agg.Expr = first
	}

	if _, err := p.expect(TokRParen); err != nil {
		return nil, err
	}

	if p.cur().Type == TokBy {
		p.advance()
		labels, err := p.parseLabelList()
		if err != nil {
			return nil, err
		}
		agg.By = labels
	}

	return agg, nil
}

func (p *Parser) parseLabelList() ([]string, error) {
	if _, err := p.expect(TokLParen); err != nil {
		return nil, err
	}
	var labels []string
	for {
		if p.cur().Type == TokRParen {
			break
		}
		id, err := p.expect(TokIdent)
		if err != nil {
			return nil, err
		}
		labels = append(labels, id.Text)
		if p.cur().Type == TokComma {
			p.advance()
			continue
		}
		break
	}
	if _, err := p.expect(TokRParen); err != nil {
		return nil, err
	}
	return labels, nil
}

func (p *Parser) parseCall(name string) (Expr, error) {
	p.advance() // func ident
	if _, err := p.expect(TokLParen); err != nil {
		return nil, err
	}
	var args []Expr
	for {
		if p.cur().Type == TokRParen {
			break
		}
		arg, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if p.cur().Type == TokComma {
			p.advance()
			continue
		}
		break
	}
	if _, err := p.expect(TokRParen); err != nil {
		return nil, err
	}
	return &Call{Func: name, Args: args}, nil
}

// parseSelector parses a metric name, optional label matchers, and an
// optional range (matrix) selector: metric{labels}[range].
func (p *Parser) parseSelector() (Expr, error) {
	nameTok, err := p.expect(TokIdent)
	if err != nil {
		return nil, err
	}
	vs := &VectorSelector{Metric: nameTok.Text, LabelMatchers: map[string]string{}}

	if p.cur().Type == TokLBrace {
		p.advance()
		for {
			if p.cur().Type == TokRBrace {
				break
			}
			label, err := p.expect(TokIdent)
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(TokEQ); err != nil {
				return nil, err
			}
			val, err := p.expect(TokString)
			if err != nil {
				return nil, err
			}
			vs.LabelMatchers[label.Text] = val.Text
			if p.cur().Type == TokComma {
				p.advance()
				continue
			}
			break
		}
		if _, err := p.expect(TokRBrace); err != nil {
			return nil, err
		}
	}

	if p.cur().Type == TokLBracket {
		p.advance()
		durTok, err := p.expect(TokDuration)
		if err != nil {
			return nil, err
		}
		dur, err := ParseDuration(durTok.Text)
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TokRBracket); err != nil {
			return nil, err
		}
		return &MatrixSelector{Vector: vs, Range: dur}, nil
	}

	return vs, nil
}

// ParseDuration parses Prometheus-style durations like "5m", "30s", "1h",
// "1d", "1w", "500ms".
func ParseDuration(s string) (time.Duration, error) {
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	numPart := s[:i]
	unit := s[i:]
	val, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	var mul time.Duration
	switch unit {
	case "ms":
		mul = time.Millisecond
	case "s":
		mul = time.Second
	case "m":
		mul = time.Minute
	case "h":
		mul = time.Hour
	case "d":
		mul = 24 * time.Hour
	case "w":
		mul = 7 * 24 * time.Hour
	case "y":
		mul = 365 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("unknown duration unit %q", unit)
	}
	return time.Duration(val * float64(mul)), nil
}
