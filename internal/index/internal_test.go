package index

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBuildMatch pins the translation from a caller's expression to FTS5
// syntax. The end-to-end adversarial search test proves the results; this
// one proves the expression, because "no hits" and "the operator was treated
// as data" look identical from the outside for most inputs.
func TestBuildMatch(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "single term", in: "zeppelin", want: `"zeppelin"`},
		{name: "terms are ANDed", in: "zeppelin cache", want: `"zeppelin" AND "cache"`},
		{name: "surrounding space", in: "  zeppelin\t\ncache  ", want: `"zeppelin" AND "cache"`},
		{name: "phrase", in: `"zeppelin cache"`, want: `"zeppelin cache"`},
		{name: "phrase and term", in: `"zeppelin cache" warm`, want: `"zeppelin cache" AND "warm"`},
		{name: "escaped quote in a phrase", in: `"say ""hello"""`, want: `"say ""hello"""`},
		{name: "unterminated phrase", in: `"zeppelin cache`, want: `"zeppelin cache"`},
		{name: "prefix", in: "zepp*", want: `"zepp"*`},
		{name: "trailing stars collapse", in: "zepp***", want: `"zepp"*`},
		{name: "star inside a term stays data", in: "ze*pp", want: `"ze*pp"`},
		{name: "negation follows the positives", in: "zeppelin -cache", want: `"zeppelin" NOT "cache"`},
		{name: "several negations", in: "-cache zeppelin -warm", want: `"zeppelin" NOT "cache" NOT "warm"`},
		{name: "operators are quoted as data", in: "a OR b", want: `"a" AND "OR" AND "b"`},
		{name: "column filter is quoted as data", in: "kind:opaque", want: `"kind:opaque"`},
		{name: "parentheses are quoted as data", in: "(zeppelin)", want: `"(zeppelin)"`},
		{name: "quote splits a term", in: `zeppelin"cache"`, want: `"zeppelin" AND "cache"`},
		{name: "control characters separate words", in: "zeppelin\x00cache", want: `"zeppelin cache"`},
		{name: "invalid utf-8 is replaced", in: "\xffzeppelin", want: "\"\uFFFDzeppelin\""},
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
	for _, operator := range []string{" AND ", " NOT ", "*"} {
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
