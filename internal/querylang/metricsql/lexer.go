package metricsql

import (
	"strings"
	"unicode"
)

// Lex converts the input string into a slice of Tokens terminated with a
// TokEOF token. It never returns an error; malformed input surfaces as
// unexpected tokens which the parser will reject.
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
			continue

		case c == '{':
			toks = append(toks, Token{TokLBrace, "{", i})
			i++
		case c == '}':
			toks = append(toks, Token{TokRBrace, "}", i})
			i++
		case c == '(':
			toks = append(toks, Token{TokLParen, "(", i})
			i++
		case c == ')':
			toks = append(toks, Token{TokRParen, ")", i})
			i++
		case c == '[':
			// could be range duration [5m]
			j := i + 1
			for j < n && runes[j] != ']' {
				j++
			}
			toks = append(toks, Token{TokLBracket, "[", i})
			if j < n {
				inner := string(runes[i+1 : j])
				toks = append(toks, Token{TokDuration, inner, i + 1})
				toks = append(toks, Token{TokRBracket, "]", j})
				i = j + 1
			} else {
				i++
			}
		case c == ']':
			toks = append(toks, Token{TokRBracket, "]", i})
			i++
		case c == ',':
			toks = append(toks, Token{TokComma, ",", i})
			i++
		case c == '+':
			toks = append(toks, Token{TokPlus, "+", i})
			i++
		case c == '-':
			toks = append(toks, Token{TokMinus, "-", i})
			i++
		case c == '*':
			toks = append(toks, Token{TokMul, "*", i})
			i++
		case c == '/':
			toks = append(toks, Token{TokDiv, "/", i})
			i++
		case c == '=':
			if i+1 < n && runes[i+1] == '=' {
				toks = append(toks, Token{TokEQEQ, "==", i})
				i += 2
			} else {
				toks = append(toks, Token{TokEQ, "=", i})
				i++
			}
		case c == '!':
			if i+1 < n && runes[i+1] == '=' {
				toks = append(toks, Token{TokNEQ, "!=", i})
				i += 2
			} else {
				// unknown, skip
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

		case unicode.IsDigit(c) || (c == '.' && i+1 < n && unicode.IsDigit(runes[i+1])):
			start := i
			j := i
			for j < n && (unicode.IsDigit(runes[j]) || runes[j] == '.') {
				j++
			}
			// duration suffix like 5m, 30s, 1h, 1d, 1w, 500ms
			numEnd := j
			if j < n && isDurationUnitStart(runes[j]) {
				k := j
				for k < n && unicode.IsLetter(runes[k]) {
					k++
				}
				unit := string(runes[j:k])
				if isValidDurationUnit(unit) {
					toks = append(toks, Token{TokDuration, string(runes[start:k]), start})
					i = k
					continue
				}
			}
			toks = append(toks, Token{TokNumber, string(runes[start:numEnd]), start})
			i = numEnd

		case unicode.IsLetter(c) || c == '_':
			start := i
			j := i
			for j < n && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_' || runes[j] == ':') {
				j++
			}
			text := string(runes[start:j])
			if strings.EqualFold(text, "by") {
				toks = append(toks, Token{TokBy, text, start})
			} else {
				toks = append(toks, Token{TokIdent, text, start})
			}
			i = j

		default:
			// unknown character, skip it
			i++
		}
	}

	toks = append(toks, Token{TokEOF, "", n})
	return toks
}

func isDurationUnitStart(r rune) bool {
	switch r {
	case 'm', 's', 'h', 'd', 'w', 'y':
		return true
	default:
		return false
	}
}

func isValidDurationUnit(u string) bool {
	switch u {
	case "ms", "s", "m", "h", "d", "w", "y":
		return true
	default:
		return false
	}
}
