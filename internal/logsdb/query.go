package logsdb

import (
	"fmt"
	"regexp"
	"strings"
)

// Query expresses a search against a Store. All non-empty/non-nil fields
// are combined with AND semantics.
type Query struct {
	// Keyword, if set, must appear as an exact token in Message (after the
	// same lowercasing/tokenization used at index time). Answered via the
	// inverted index in O(matching docs).
	Keyword string

	// Tags requires doc.Tags[k] == v for every k/v pair, answered via the
	// inverted index ("tag:k=v" postings).
	Tags map[string]string

	// Fields requires fmt.Sprint(doc.Fields[k]) == v for every k/v pair,
	// answered via the inverted index ("field:k=v" postings).
	Fields map[string]string

	// Regex, if set, is matched against Message with regexp.MatchString.
	Regex string

	// Wildcard, if set, is a glob pattern ('*' = any run of chars, '?' =
	// single char) matched case-insensitively against Message. It is
	// translated to a regexp internally.
	Wildcard string

	// Limit caps the number of returned docs. 0 means unlimited.
	Limit int

	// compiled regex, built lazily by compile().
	regex *regexp.Regexp
}

// compile validates and pre-compiles the Regex/Wildcard fields. Called once
// by Search.
func (q *Query) compile() error {
	switch {
	case q.Regex != "":
		re, err := regexp.Compile(q.Regex)
		if err != nil {
			return err
		}
		q.regex = re
	case q.Wildcard != "":
		re, err := regexp.Compile("(?i)" + globToRegex(q.Wildcard))
		if err != nil {
			return err
		}
		q.regex = re
	}
	return nil
}

// hasIndexableTerm reports whether the query has at least one clause that
// can be answered directly from the inverted index (keyword/tag/field),
// letting Search narrow candidates before ever touching a raw doc.
func (q *Query) hasIndexableTerm() bool {
	return q.Keyword != "" || len(q.Tags) > 0 || len(q.Fields) > 0
}

// matchesPostFilter re-checks the parts of the query that the inverted
// index cannot fully answer (regex/wildcard), plus a cheap safety re-check
// of the indexable clauses in case of any index staleness.
func (q *Query) matchesPostFilter(d LogDoc) bool {
	if q.regex != nil && !q.regex.MatchString(d.Message) {
		return false
	}
	for k, v := range q.Tags {
		if d.Tags[k] != v {
			return false
		}
	}
	for k, v := range q.Fields {
		if fieldToString(d.Fields[k]) != v {
			return false
		}
	}
	if q.Keyword != "" {
		found := false
		for _, t := range uniqueTokens(d.Message) {
			if t == strings.ToLower(q.Keyword) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// globToRegex translates a glob pattern using '*' and '?' wildcards into an
// anchored regular expression, escaping every other regex metacharacter.
func globToRegex(glob string) string {
	var b strings.Builder
	b.WriteByte('^')
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteByte('$')
	return b.String()
}

func fieldToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
