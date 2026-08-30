package index

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBuildMatch pins the translation from a caller's expression to FTS5
// syntax. The end-to-end adversarial search test proves the results; this
// one proves the expression, because "no hits" and "the operator was treated
// as data" look identical from the outside for most inputs.
//
// The union cases are the correction of a measured failure: terms used to be
// ANDed, and an ordinary five-word keyword bag matched nothing at all.
func TestBuildMatch(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "single term", in: "zeppelin", want: `"zeppelin"`},
		{name: "terms are optional and tried as a phrase first", in: "zeppelin cache",
			want: `"zeppelin cache" OR "zeppelin" OR "cache"`},
		{name: "surrounding space", in: "  zeppelin\t\ncache  ",
			want: `"zeppelin cache" OR "zeppelin" OR "cache"`},
		{name: "phrase", in: `"zeppelin cache"`, want: `"zeppelin cache"`},
		{name: "phrase and term", in: `"zeppelin cache" warm`,
			want: `"zeppelin cache warm" OR "zeppelin cache" OR "warm"`},
		{name: "escaped quote in a phrase", in: `"say ""hello"""`, want: `"say ""hello"""`},
		{name: "unterminated phrase", in: `"zeppelin cache`, want: `"zeppelin cache"`},
		{name: "prefix", in: "zepp*", want: `"zepp"*`},
		{name: "trailing stars collapse", in: "zepp***", want: `"zepp"*`},
		{name: "star inside a term stays data", in: "ze*pp", want: `"ze*pp"`},
		// A star binds only to a phrase's last token, so a prefix term
		// anywhere in the bag leaves no whole phrase to try.
		{name: "a prefix term suppresses the whole phrase", in: "zepp* cache",
			want: `"zepp"* OR "cache"`},
		{name: "one positive needs no brackets", in: "zeppelin -cache",
			want: `"zeppelin" NOT "cache"`},
		{name: "an exclusion brackets the whole union", in: "zeppelin warm -cache",
			want: `("zeppelin warm" OR "zeppelin" OR "warm") NOT "cache"`},
		{name: "several negations", in: "-cache zeppelin -warm",
			want: `"zeppelin" NOT "cache" NOT "warm"`},
		{name: "operators are quoted as data", in: "a OR b",
			want: `"a OR b" OR "a" OR "OR" OR "b"`},
		{name: "column filter is quoted as data", in: "kind:opaque", want: `"kind:opaque"`},
		{name: "parentheses are quoted as data", in: "(zeppelin)", want: `"(zeppelin)"`},
		{name: "quote splits a term", in: `zeppelin"cache"`,
			want: `"zeppelin cache" OR "zeppelin" OR "cache"`},
		{name: "control characters separate words", in: "zeppelin\x00cache", want: `"zeppelin cache"`},
		{name: "invalid utf-8 is replaced", in: "\xffzeppelin", want: "\"\uFFFDzeppelin\""},
		// A hyphen is FTS5 syntax and a word character to everybody else.
		// Inside a term it is data, and the term is a phrase whose two
		// tokens must be adjacent.
		{name: "an interior hyphen is data", in: "human-agent coordination",
			want: `"human-agent coordination" OR "human-agent" OR "coordination"`},
		{name: "no searchable term", in: "-zeppelin", wantErr: ErrNoSearchableTerm},
		{name: "punctuation only", in: "*** ---", wantErr: ErrNoSearchableTerm},
		{name: "empty", in: "", wantErr: ErrNoSearchableTerm},
		{name: "too long", in: strings.Repeat("z", MaxMatchBytes+1), wantErr: ErrMatchTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildMatch(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("buildMatch(%q) error = %v, want %v", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildMatch(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("buildMatch(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestBuildMatchNeverEmitsUnquotedData is the property that matters more
// than any single case: whatever a caller sends, every byte of it ends up
// inside an FTS5 string literal, so it cannot become syntax. The check is
// that the expression is a sequence of quoted literals joined by this
// package's own operators.
func TestBuildMatchNeverEmitsUnquotedData(t *testing.T) {
	hostile := []string{
		`zeppelin OR basilisk`,
		`NEAR(a b, 3)`,
		`text : "a" AND (b OR c)`,
		`"" "" ""x`,
		`^a -b* "c"" d`,
		"a\x00b\x1bc",
		`\ / [] {} $ % ^ & * ( ) ; --`,
		strings.Repeat(`"`, 64) + "zeppelin",
	}
	for _, in := range hostile {
		expression, err := buildMatch(in)
		if errors.Is(err, ErrNoSearchableTerm) {
			continue
		}
		if err != nil {
			t.Errorf("buildMatch(%q): unexpected error %v", in, err)
			continue
		}
		if !utf8.ValidString(expression) {
			t.Errorf("buildMatch(%q) = %q, which is not valid UTF-8", in, expression)
		}
		if rest := skeleton(expression); rest != "" {
			t.Errorf("buildMatch(%q) = %s, which has unquoted content %q", in, expression, rest)
		}
	}
}

// TestBuildMatchCapsTerms covers the bound a union needs and an intersection
// did not. Every extra optional term widens the candidate set, so a query
// with hundreds of words is answered on its first MaxMatchTerms rather than
// compiled whole — and answered, not refused, because a query never fails on
// its content.
func TestBuildMatchCapsTerms(t *testing.T) {
	words := make([]string, 0, MaxMatchTerms*3)
	for i := range cap(words) {
		words = append(words, fmt.Sprintf("w%d", i))
	}
	expression, err := buildMatch(strings.Join(words, " "))
	if err != nil {
		t.Fatalf("buildMatch: %v", err)
	}
	// One disjunct per surviving term, plus the whole-phrase disjunct.
	if got, want := strings.Count(expression, " OR ")+1, MaxMatchTerms+1; got != want {
		t.Errorf("expression has %d disjuncts, want %d: %s", got, want, expression)
	}
	if strings.Contains(expression, fmt.Sprintf(`"w%d"`, MaxMatchTerms)) {
		t.Errorf("term past the cap survived: %s", expression)
	}
}

// skeleton removes every quoted literal and this package's own operators
// from an expression and returns whatever is left, which must be nothing.
func skeleton(expression string) string {
	var out strings.Builder
	for i := 0; i < len(expression); {
		if expression[i] != '"' {
			out.WriteByte(expression[i])
			i++
			continue
		}
		i++
		for i < len(expression) {
			if expression[i] == '"' {
				if i+1 < len(expression) && expression[i+1] == '"' {
					i += 2
					continue
				}
				i++
				break
			}
			i++
		}
	}
	rest := out.String()
	// " OR " joins the optional terms and the brackets scope an exclusion
	// across them; both are written by this package, never by a caller.
	for _, operator := range []string{" AND ", " OR ", " NOT ", "*", "(", ")"} {
		rest = strings.ReplaceAll(rest, operator, "")
	}
	return rest
}

func TestTruncateText(t *testing.T) {
	long := strings.Repeat("a", MaxIndexedTextBytes+10)
	// A multi-byte rune straddling the limit must not be split: the cut
	// falls back to the rune boundary before it.
	straddling := strings.Repeat("a", MaxIndexedTextBytes-1) + "€€"

	cases := []struct {
		name      string
		in        string
		wantLen   int
		wantTrunc bool
	}{
		{name: "short", in: "zeppelin", wantLen: len("zeppelin")},
		{name: "exactly at the limit", in: strings.Repeat("a", MaxIndexedTextBytes), wantLen: MaxIndexedTextBytes},
		{name: "over the limit", in: long, wantLen: MaxIndexedTextBytes, wantTrunc: true},
		{name: "rune straddling the limit", in: straddling, wantLen: MaxIndexedTextBytes - 1, wantTrunc: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated := truncateText(tc.in)
			if len(got) != tc.wantLen || truncated != tc.wantTrunc {
				t.Errorf("truncateText(%d bytes) = %d bytes, truncated %v; want %d bytes, truncated %v",
					len(tc.in), len(got), truncated, tc.wantLen, tc.wantTrunc)
			}
			if !utf8.ValidString(got) {
				t.Error("truncated text is not valid UTF-8")
			}
			if !strings.HasPrefix(tc.in, got) {
				t.Error("truncated text is not a prefix of its input")
			}
		})
	}
}
