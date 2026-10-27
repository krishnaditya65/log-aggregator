package pipeql

import (
	"fmt"
	"strconv"
	"strings"
)

var aggFuncNames = map[string]AggFunc{
	"count": AggCount, "sum": AggSum, "avg": AggAvg, "min": AggMin, "max": AggMax,
	"p50": AggP50, "p95": AggP95, "p99": AggP99,
}

// Parser is a recursive-descent parser over a token stream produced by Lex.
type Parser struct {
	toks []Token
	pos  int
}

// Parse parses a token stream into a Pipeline AST. The pipeline must begin
// with a `search` stage; subsequent stages are separated by `|`.
func Parse(tokens []Token) (*Pipeline, error) {
	p := &Parser{toks: tokens}

	first, err := p.parseStage(true)
	if err != nil {
		return nil, err
	}
	if _, ok := first.(*SearchStage); !ok {
		return nil, fmt.Errorf("pipeline must start with a 'search' stage")
	}
	stages := []Stage{first}

	for p.cur().Type == TokPipe {
		p.advance()
		st, err := p.parseStage(false)
		if err != nil {
			return nil, err
		}
		stages = append(stages, st)
	}

	if p.cur().Type != TokEOF {
		return nil, fmt.Errorf("unexpected token %s", p.cur())
	}

	return &Pipeline{Stages: stages}, nil
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

func (p *Parser) expectKeyword(kw string) error {
	if p.cur().Type != TokIdent || !strings.EqualFold(p.cur().Text, kw) {
		return fmt.Errorf("expected %q but got %s", kw, p.cur())
	}
	p.advance()
	return nil
}

func (p *Parser) atKeyword(kw string) bool {
	return p.cur().Type == TokIdent && strings.EqualFold(p.cur().Text, kw)
}

func (p *Parser) parseStage(isFirst bool) (Stage, error) {
	if p.cur().Type != TokIdent {
		return nil, fmt.Errorf("expected stage keyword but got %s", p.cur())
	}
	kw := strings.ToLower(p.cur().Text)
	switch kw {
	case "search":
		p.advance()
		return p.parseSearchStage()
	case "filter":
		p.advance()
		return p.parseFilterStage()
	case "stats":
		p.advance()
		return p.parseStatsStage()
	case "sort":
		p.advance()
		return p.parseSortStage()
	case "limit":
		p.advance()
		return p.parseLimitStage()
	default:
		return nil, fmt.Errorf("unknown stage %q", kw)
	}
}

// parseSearchStage parses `search *` or `search field=value "keyword" ...`.
func (p *Parser) parseSearchStage() (Stage, error) {
	if p.cur().Type == TokStar {
		p.advance()
		return &SearchStage{Star: true}, nil
	}
	st := &SearchStage{Filters: map[string]string{}}
	for {
		switch p.cur().Type {
		case TokPipe, TokEOF:
			return st, nil
		case TokString:
			st.Keywords = append(st.Keywords, p.advance().Text)
		case TokIdent:
			if p.peek(1).Type == TokEQ {
				field := p.advance().Text
				p.advance() // =
				val, err := p.parseLiteralText()
				if err != nil {
					return nil, err
				}
				st.Filters[field] = val
			} else {
				// bare word treated as a keyword term
				st.Keywords = append(st.Keywords, p.advance().Text)
			}
		default:
			return nil, fmt.Errorf("unexpected token %s in search stage", p.cur())
		}
	}
}

func (p *Parser) parseLiteralText() (string, error) {
	switch p.cur().Type {
	case TokString, TokIdent, TokNumber:
		return p.advance().Text, nil
	default:
		return "", fmt.Errorf("expected a value but got %s", p.cur())
	}
}

func (p *Parser) parseFilterStage() (Stage, error) {
	expr, err := p.parseOrExpr()
	if err != nil {
		return nil, err
	}
	return &FilterStage{Expr: expr}, nil
}

func (p *Parser) parseOrExpr() (BoolExpr, error) {
	lhs, err := p.parseAndExpr()
	if err != nil {
		return nil, err
	}
	for p.atKeyword("or") {
		p.advance()
		rhs, err := p.parseAndExpr()
		if err != nil {
			return nil, err
		}
		lhs = &OrExpr{LHS: lhs, RHS: rhs}
	}
	return lhs, nil
}

func (p *Parser) parseAndExpr() (BoolExpr, error) {
	lhs, err := p.parseBoolPrimary()
	if err != nil {
		return nil, err
	}
	for p.atKeyword("and") {
		p.advance()
		rhs, err := p.parseBoolPrimary()
		if err != nil {
			return nil, err
		}
		lhs = &AndExpr{LHS: lhs, RHS: rhs}
	}
	return lhs, nil
}

func (p *Parser) parseBoolPrimary() (BoolExpr, error) {
	switch p.cur().Type {
	case TokLParen:
		p.advance()
		inner, err := p.parseOrExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(TokRParen); err != nil {
			return nil, err
		}
		return inner, nil

	case TokString:
		return &KeywordExpr{Word: p.advance().Text}, nil

	case TokIdent:
		field := p.advance().Text
		op, err := p.parseCompareOp()
		if err != nil {
			return nil, err
		}
		val, err := p.parseLiteralText()
		if err != nil {
			return nil, err
		}
		return &Condition{Field: field, Op: op, Value: val}, nil

	default:
		return nil, fmt.Errorf("unexpected token %s in filter expression", p.cur())
	}
}

func (p *Parser) parseCompareOp() (CompareOp, error) {
	switch p.cur().Type {
	case TokEQ:
		p.advance()
		return OpEQ, nil
	case TokNEQ:
		p.advance()
		return OpNE, nil
	case TokGT:
		p.advance()
		return OpGT, nil
	case TokLT:
		p.advance()
		return OpLT, nil
	case TokGE:
		p.advance()
		return OpGE, nil
	case TokLE:
		p.advance()
		return OpLE, nil
	default:
		return "", fmt.Errorf("expected comparison operator but got %s", p.cur())
	}
}

// parseStatsStage parses `<agg>(<field?>) [, <agg>(<field?>)]* [by (f1, f2)]`.
func (p *Parser) parseStatsStage() (Stage, error) {
	st := &StatsStage{}
	for {
		call, err := p.parseAggCall()
		if err != nil {
			return nil, err
		}
		st.Aggs = append(st.Aggs, call)
		if p.cur().Type == TokComma {
			p.advance()
			continue
		}
		break
	}
	if p.atKeyword("by") {
		p.advance()
		fields, err := p.parseFieldList()
		if err != nil {
			return nil, err
		}
		st.By = fields
	}
	return st, nil
}

func (p *Parser) parseAggCall() (AggCall, error) {
	if p.cur().Type != TokIdent {
		return AggCall{}, fmt.Errorf("expected aggregation function but got %s", p.cur())
	}
	name := strings.ToLower(p.cur().Text)
	fn, ok := aggFuncNames[name]
	if !ok {
		return AggCall{}, fmt.Errorf("unknown aggregation function %q", name)
	}
	p.advance()
	if _, err := p.expect(TokLParen); err != nil {
		return AggCall{}, err
	}
	var field string
	if p.cur().Type == TokIdent {
		field = p.advance().Text
	}
	if _, err := p.expect(TokRParen); err != nil {
		return AggCall{}, err
	}
	as := string(fn) + "(" + field + ")"
	return AggCall{Func: fn, Field: field, As: as}, nil
}

func (p *Parser) parseFieldList() ([]string, error) {
	if _, err := p.expect(TokLParen); err != nil {
		return nil, err
	}
	var fields []string
	for {
		if p.cur().Type == TokRParen {
			break
		}
		id, err := p.expect(TokIdent)
		if err != nil {
			return nil, err
		}
		fields = append(fields, id.Text)
		if p.cur().Type == TokComma {
			p.advance()
			continue
		}
		break
	}
	if _, err := p.expect(TokRParen); err != nil {
		return nil, err
	}
	return fields, nil
}

func (p *Parser) parseSortStage() (Stage, error) {
	st := &SortStage{}
	if p.atKeyword("asc") {
		p.advance()
		st.Desc = false
	} else if p.atKeyword("desc") {
		p.advance()
		st.Desc = true
	} else {
		st.Desc = false
	}
	if p.cur().Type == TokIdent {
		st.Field = p.advance().Text
	}
	return st, nil
}

func (p *Parser) parseLimitStage() (Stage, error) {
	tok, err := p.expect(TokNumber)
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(tok.Text)
	if err != nil {
		return nil, fmt.Errorf("invalid limit %q: %w", tok.Text, err)
	}
	return &LimitStage{N: n}, nil
}
