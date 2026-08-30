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

// MaxMatchTerms bounds how many of a caller's terms reach FTS5. Terms are
// optional and unioned, so cost grows with the number of them in a way an
// intersection's did not: every extra term widens the candidate set instead of
// narrowing it. No keyword query a person or an analysis worker writes runs
// past a couple of dozen words, and a query that does is answered on its first
// MaxMatchTerms rather than refused, because a truncated answer is still an
// answer and this function's contract is that a query never fails on its
// content.
const MaxMatchTerms = 32

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
//   - whitespace separates terms, and terms are optional: a record matching
//     any of them is a hit, and relevance order decides which hits are worth
//     a caller's attention;
//   - "a quoted phrase" matches its words adjacently, and "" inside a phrase
//     is a literal quote; an unterminated phrase closes at end of input,
//     because a partly typed query should search rather than fail;
//   - a leading - excludes a term, and an exclusion is not optional: it
//     removes a record from the result whichever other terms it matched;
//   - a trailing * on an unquoted term matches by prefix; and
//   - everything else is data. AND, OR, NOT, NEAR, parentheses, colons, and
//     carets have no special meaning: they are matched as the words they are.
//
// Optional terms are the correction of a measured failure and not a
// preference. Terms were ANDed, and an analysis worker's ordinary keyword
// bag — five words, each of them common in the corpus separately — asks for
// one record holding all five, which on a corpus of individual transcript
// records is almost never anything. Four such queries against the operator's
// index returned 0, 0, 1 and 0 hits while every individual term matched
// hundreds or thousands of records. A union answers the question the worker
// was asking.
//
// A union alone would answer too much, so relevance carries the weight the
// conjunction used to: Search orders by FTS5's bm25, which already scores a
// record by how many of the expression's phrases it matched and weighs each
// by its inverse document frequency, so a record holding four rare terms
// outranks one holding a single ubiquitous one, and a term matching most of
// the corpus contributes almost nothing. Nothing here reimplements that.
// Membership is broad, order is discriminating, and Query.Limit bounds what a
// caller is handed — which is the only reason a union is safe to serve.
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
	if len(terms) > MaxMatchTerms {
		terms = terms[:MaxMatchTerms]
	}

	var positives, negatives []matchTerm
	for _, t := range terms {
		if t.negated {
			negatives = append(negatives, t)
			continue
		}
		positives = append(positives, t)
	}
	if len(positives) == 0 {
		return "", ErrNoSearchableTerm
	}

	var b strings.Builder
	if phrase, ok := wholePhrase(positives); ok {
		writeTerm(&b, phrase)
		b.WriteString(" OR ")
	}
	for i, t := range positives {
		if i > 0 {
			b.WriteString(" OR ")
		}
		writeTerm(&b, t)
	}
	if len(negatives) == 0 {
		return b.String(), nil
	}
	union := b.String()
	if len(positives) > 1 {
		// FTS5 binds NOT tighter than OR, so an unbracketed "a OR b NOT c"
		// would exclude c from b alone and leave it reachable through a.
		// A single positive is already one operand and needs no brackets.
		union = "(" + union + ")"
	}
	b.Reset()
	b.WriteString(union)
	for _, t := range negatives {
		b.WriteString(" NOT ")
		writeTerm(&b, t)
	}
	return b.String(), nil
}

// wholePhrase is the caller's positive terms read as one adjacent phrase, and
// reports whether there is one to try.
//
// It is an extra disjunct rather than a separate query: FTS5's bm25 sums a
// row's score over every phrase in the expression that matched it, so a record
// carrying the caller's words adjacently matches the phrase *and* each word
// and therefore outranks a record that merely holds the same words scattered.
// A phrase of n words is also rarer than any of its words, and bm25 weighs a
// phrase by its own inverse document frequency, so the preference costs
// nothing to state and needs no tuning.
//
// A prefix term is excluded because FTS5 accepts a star only after a phrase's
// last token, so a prefix term in any other position has no phrase to form.
func wholePhrase(positives []matchTerm) (matchTerm, bool) {
	if len(positives) < 2 {
		return matchTerm{}, false
	}
	words := make([]string, len(positives))
	for i, t := range positives {
		if t.prefix {
			return matchTerm{}, false
		}
		words[i] = t.text
	}
	return matchTerm{text: strings.Join(words, " ")}, true
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
