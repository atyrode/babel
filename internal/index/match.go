package index

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxMatchBytes bounds a caller's match expression. A search expression is a
// query, not a document: a kilobyte holds any phrase an investigator or an
// analysis worker will type, while an unbounded one is a way to make FTS5
// compile a pathological expression on untrusted input.
const MaxMatchBytes = 1 << 10

// Errors a match expression can produce. They are sentinels so a caller can
// distinguish "you typed something unsearchable" from "the index failed",
// and they deliberately do not quote the expression back: a query can carry
// pasted transcript or credential material, and §9 keeps that out of errors
// and logs.
var (
	ErrMatchTooLong     = errors.New("index: match expression longer than MaxMatchBytes")
	ErrNoSearchableTerm = errors.New("index: match expression contains no searchable term")
)

// matchTerm is one parsed term of a caller's expression.
type matchTerm struct {
	text    string
	negated bool
	prefix  bool
}

// buildMatch translates a caller's expression into an FTS5 MATCH expression.
//
// A caller's string is never concatenated into SQL, and it never becomes
// FTS5 syntax either: the expression handed to MATCH is built here from
// quoted string literals, and MATCH itself receives it as a bound parameter.
// That is the whole reason this function exists rather than passing the
// caller's text through. FTS5's expression language has operators (AND, OR,
// NOT, NEAR), column filters, parentheses, carets, and quoting rules; a
// transcript search box that forwarded them would let an ordinary query —
// a stray quote, a hyphen, a colon, a bracket — either error or compile into
// something the caller did not ask for.
//
// The grammar this package accepts is small, controlled, and documented:
//
//   - whitespace separates terms, and terms are ANDed;
//   - "a quoted phrase" matches its words adjacently, and "" inside a phrase
//     is a literal quote; an unterminated phrase closes at end of input,
//     because a partly typed query should search rather than fail;
//   - a leading - excludes a term;
//   - a trailing * on an unquoted term matches by prefix; and
//   - everything else is data. AND, OR, NOT, NEAR, parentheses, colons, and
//     carets have no special meaning: they are matched as the words they are.
//
// Control characters, including NUL, cannot be FTS5 tokens and are replaced
// by separators, so an expression carrying them searches its remaining words
// instead of reaching the driver with an embedded NUL. A term with no letter
// and no digit has no token to match and is dropped; an expression that is
// entirely such terms is ErrNoSearchableTerm, because silently matching
// everything would answer a question the caller did not ask.
func buildMatch(expression string) (string, error) {
	if len(expression) > MaxMatchBytes {
		return "", fmt.Errorf("%w: %d bytes", ErrMatchTooLong, len(expression))
	}
	terms := parseMatch(expression)

	var b strings.Builder
	positives := 0
	for _, t := range terms {
		if t.negated {
			continue
		}
		if positives > 0 {
			b.WriteString(" AND ")
		}
		writeTerm(&b, t)
		positives++
	}
	if positives == 0 {
		return "", ErrNoSearchableTerm
	}
	// FTS5 reads NOT as a binary operator binding tighter than AND, so the
	// exclusions follow the ANDed positives and apply to the whole
	// conjunction.
	for _, t := range terms {
		if !t.negated {
			continue
		}
		b.WriteString(" NOT ")
		writeTerm(&b, t)
	}
	return b.String(), nil
}

// writeTerm emits one term as an FTS5 string literal. Doubling the quote is
// FTS5's own escape, and it is the only escaping needed because the literal
// is the only place caller data appears.
func writeTerm(b *strings.Builder, t matchTerm) {
	b.WriteByte('"')
	b.WriteString(strings.ReplaceAll(t.text, `"`, `""`))
	b.WriteByte('"')
	if t.prefix {
		b.WriteByte('*')
	}
}

func parseMatch(expression string) []matchTerm {
	var terms []matchTerm
	var b strings.Builder
	i := 0
	for i < len(expression) {
		r, size := utf8.DecodeRuneInString(expression[i:])
		if unicode.IsSpace(r) {
			i += size
			continue
		}
		var t matchTerm
		if r == '-' {
			t.negated = true
			i += size
		}
		b.Reset()
		switch {
		case i < len(expression) && expression[i] == '"':
			i = readPhrase(expression, i+1, &b)
		default:
			i = readWord(expression, i, &b)
			// A trailing star is the caller asking for a prefix match;
			// stars anywhere else are data, and unicode61 treats them
			// as separators.
			if word := b.String(); strings.HasSuffix(word, "*") {
				t.prefix = true
				b.Reset()
				b.WriteString(strings.TrimRight(word, "*"))
			}
		}
		if t.text = searchable(b.String()); t.text != "" {
			terms = append(terms, t)
		}
	}
	return terms
}

// readPhrase copies a quoted phrase's content, translating "" into one
// quote, and returns the offset past the closing quote or end of input.
func readPhrase(expression string, i int, b *strings.Builder) int {
	for i < len(expression) {
		if expression[i] == '"' {
			if i+1 < len(expression) && expression[i+1] == '"' {
				b.WriteByte('"')
				i += 2
				continue
			}
			return i + 1
		}
		b.WriteByte(expression[i])
		i++
	}
	return i
}

// readWord copies an unquoted term, which ends at whitespace or at the quote
// that starts the next term.
func readWord(expression string, i int, b *strings.Builder) int {
	for i < len(expression) {
		r, size := utf8.DecodeRuneInString(expression[i:])
		if unicode.IsSpace(r) || r == '"' {
			return i
		}
		b.WriteString(expression[i : i+size])
		i += size
	}
	return i
}

// searchable normalizes one term's content and reports it empty when it
// holds nothing the tokenizer could index. Ranging over a string yields
// U+FFFD for an invalid byte, so the result is always valid UTF-8 — which
// SQLite's TEXT storage and FTS5's tokenizer both require.
func searchable(term string) string {
	var b strings.Builder
	b.Grow(len(term))
	token := false
	for _, r := range term {
		if unicode.IsControl(r) {
			b.WriteByte(' ')
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			token = true
		}
		b.WriteRune(r)
	}
	if !token {
		return ""
	}
	return b.String()
}
