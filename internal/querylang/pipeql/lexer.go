package pipeql

import (
	"strings"
	"unicode"
)

// Lex converts the input string into a slice of Tokens terminated with a
// TokEOF token. Bare (unquoted) words are lexed as TokIdent unless they
// look like a number, in which case they're lexed as TokNumber; the parser
// decides how to interpret each in context (field name, keyword, value).
func Lex(input string) []Token {
	var toks []Token
	runes := []rune(input)
	n := len(runes)
	i := 0

	for i < n {
		c := runes[i]
		switch {
		case unicode.IsSpace(c):
			i++

		case c == '|':
			toks = append(toks, Token{TokPipe, "|", i})
			i++
		case c == '*':
			toks = append(toks, Token{TokStar, "*", i})
			i++
		case c == '(':
			toks = append(toks, Token{TokLParen, "(", i})
			i++
		case c == ')':
			toks = append(toks, Token{TokRParen, ")", i})
			i++
		case c == ',':
			toks = append(toks, Token{TokComma, ",", i})
			i++
		case c == '=':
			toks = append(toks, Token{TokEQ, "=", i})
			i++
		case c == '!':
			if i+1 < n && runes[i+1] == '=' {
				toks = append(toks, Token{TokNEQ, "!=", i})
				i += 2
			} else {
				i++
			}
		case c == '>':
			if i+1 < n && runes[i+1] == '=' {
				toks = append(toks, Token{TokGE, ">=", i})
				i += 2
			} else {
				toks = append(toks, Token{TokGT, ">", i})
				i++
			}
		case c == '<':
			if i+1 < n && runes[i+1] == '=' {
				toks = append(toks, Token{TokLE, "<=", i})
				i += 2
			} else {
				toks = append(toks, Token{TokLT, "<", i})
				i++
			}
		case c == '"' || c == '\'':
			quote := c
			j := i + 1
			var sb strings.Builder
			for j < n && runes[j] != quote {
				if runes[j] == '\\' && j+1 < n {
					j++
				}
				sb.WriteRune(runes[j])
				j++
			}
			toks = append(toks, Token{TokString, sb.String(), i})
			i = j + 1

		case unicode.IsDigit(c) || (c == '-' && i+1 < n && unicode.IsDigit(runes[i+1])):
			start := i
			j := i + 1
			for j < n && (unicode.IsDigit(runes[j]) || runes[j] == '.') {
				j++
			}
			toks = append(toks, Token{TokNumber, string(runes[start:j]), start})
			i = j

		case isWordChar(c):
			start := i
			j := i
			for j < n && (isWordChar(runes[j]) || runes[j] == '.') {
				j++
			}
			toks = append(toks, Token{TokIdent, string(runes[start:j]), start})
			i = j

		default:
			i++
		}
	}

	toks = append(toks, Token{TokEOF, "", n})
	return toks
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '/' || r == '-'
}
