package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeEscapesHostileControls(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii is untouched", "omp/host/project/session-1", "omp/host/project/session-1"},
		{"printable unicode is untouched", "héllo — wörld", "héllo — wörld"},
		{"space survives", "two words", "two words"},
		{"escape introducer", "\x1b[31mred\x1b[0m", "\\u{1B}[31mred\\u{1B}[0m"},
		{"osc introducer", "\x1b]0;title\x07", "\\u{1B}]0;title\\u{7}"},
		{"c0 controls", "a\tb\nc\rd\x00e", "a\\u{9}b\\u{A}c\\u{D}d\\u{0}e"},
		{"delete", "a\x7fb", "a\\u{7F}b"},
		{"c1 controls", "a\u0085b\u009bc", "a\\u{85}b\\u{9B}c"},
		{"bidi override", "start\u202eevil\u202cend", "start\\u{202E}evil\\u{202C}end"},
		{"bidi isolate", "\u2066a\u2069", "\\u{2066}a\\u{2069}"},
		{"zero width", "a\u200bb\u200fc", "a\\u{200B}b\\u{200F}c"},
		{"line separators", "a\u2028b\u2029c", "a\\u{2028}b\\u{2029}c"},
		{"byte order mark", "\ufeffkey", "\\u{FEFF}key"},
		{"invalid utf8", "a\xffb", "a\\x{FF}b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in)
			if got != tc.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("Sanitize(%q) produced invalid UTF-8", tc.in)
			}
			for _, r := range got {
				if unsafeRune(r) || r < 0x20 || r == 0x7f {
					t.Fatalf("Sanitize(%q) = %q still carries hostile rune %U", tc.in, got, r)
				}
			}
		})
	}
}

func TestTruncateCellStopsOnRuneBoundary(t *testing.T) {
	long := Sanitize(strings.Repeat("é", maxCellRunes*2))
	got := truncateCell(long)
	if n := utf8.RuneCountInString(got); n != maxCellRunes {
		t.Fatalf("truncated rune count = %d, want %d", n, maxCellRunes)
	}
	if !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("truncateCell = %q, want ellipsis suffix", got)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncateCell cut inside a rune")
	}

	exact := strings.Repeat("a", maxCellRunes)
	if got := truncateCell(exact); got != exact {
		t.Fatalf("truncateCell shortened a value already at the limit: %q", got)
	}
}

func TestTableBoundsHostileCellsWithoutLeakingControls(t *testing.T) {
	hostile := Sanitize(strings.Repeat("\x1b[0m", maxCellRunes))
	var out strings.Builder
	if err := writeTable(&out, []string{"A", "B"}, [][]string{{hostile, "tail"}}); err != nil {
		t.Fatal(err)
	}
	line := strings.SplitN(out.String(), "\n", 2)[1]
	if strings.ContainsRune(line, 0x1b) {
		t.Fatalf("table leaked ESC: %q", line)
	}
	if !strings.Contains(line, ellipsis) {
		t.Fatalf("table did not bound a hostile cell: %q", line)
	}
	if !strings.Contains(line, "tail") {
		t.Fatalf("hostile cell pushed the next column out: %q", line)
	}
}

func TestSanitizeHostIDMapsOntoValidName(t *testing.T) {
	cases := map[string]string{
		"Workstation.Local":      "workstation.local",
		"--weird--":              "weird--",
		"host name!":             "host-name-",
		strings.Repeat("h", 200): strings.Repeat("h", 64),
	}
	for in, want := range cases {
		if got := sanitizeHostID(in); got != want {
			t.Fatalf("sanitizeHostID(%q) = %q, want %q", in, got, want)
		}
	}
}
