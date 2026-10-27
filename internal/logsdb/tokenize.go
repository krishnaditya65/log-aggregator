package logsdb

import "strings"

// tokenize splits a message into lowercase, punctuation-stripped terms.
// It is intentionally simple: split on anything that is not a letter or a
// digit, lowercase, drop empties. No stemming.
func tokenize(msg string) []string {
	if msg == "" {
		return nil
	}
	var terms []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			terms = append(terms, cur.String())
			cur.Reset()
		}
	}
	for _, r := range msg {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			cur.WriteRune(r - 'A' + 'a')
		default:
			flush()
		}
	}
	flush()
	return terms
}

// uniqueTokens tokenizes and de-duplicates, since inverted-index postings
// only need one entry per (term, doc) pair.
func uniqueTokens(msg string) []string {
	toks := tokenize(msg)
	if len(toks) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(toks))
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}
