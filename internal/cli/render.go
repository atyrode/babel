package cli

import (
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

// maxCellRunes bounds one rendered table or detail cell. A hostile title or
// workspace is otherwise free to push every other column off screen, which
// is a presentation attack even once its control characters are escaped
// (SPEC.md §8).
const maxCellRunes = 120

// ellipsis marks a cell truncated at a rune boundary.
const ellipsis = "…"

// missingValue renders an absent nullable field. Babel never synthesizes a
// value to satisfy a shape (SPEC.md §3), so absence is displayed as
// absence.
const missingValue = "-"

// Sanitize is Babel's single terminal-safe renderer (SPEC.md §8, §9). Every
// untrusted dynamic value — session titles, workspaces, paths, adapter
// metadata, deferred reasons, wrapped error text — passes through it before
// reaching a terminal, on stdout as well as stderr, so no source-controlled
// byte can move the cursor, reorder text, or hide characters.
//
// Escaped as visible "\u{HEX}" (invalid UTF-8 bytes as "\x{HH}"):
//
//   - C0 controls U+0000-U+001F, which includes ESC and therefore every
//     CSI/OSC/DCS introducer; space is the only C0-adjacent byte kept;
//   - DEL U+007F and the C1 controls U+0080-U+009F;
//   - bidi overrides and isolates U+202A-U+202E and U+2066-U+2069;
//   - zero-width and invisible controls U+200B-U+200F, the line and
//     paragraph separators U+2028/U+2029, and U+FEFF;
//   - bytes that are not valid UTF-8.
//
// Tabs and newlines are C0 controls, so Sanitize escapes them: it renders
// values, never layout. Callers compose lines from sanitized values rather
// than sanitizing composed lines.
func Sanitize(s string) string {
	if !mayNeedEscape(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c < 0x20 || c == 0x7f {
				writeEscape(&b, "\\u{", uint32(c), 1)
			} else {
				b.WriteByte(c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			writeEscape(&b, "\\x{", uint32(s[i]), 2)
			i++
			continue
		}
		if unsafeRune(r) {
			writeEscape(&b, "\\u{", uint32(r), 1)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// mayNeedEscape reports whether s contains any byte that could require
// escaping. Plain ASCII text — the overwhelming majority of rendered
// values — takes this fast path and is returned unchanged and unallocated.
func mayNeedEscape(s string) bool {
	for i := range len(s) {
		if c := s[i]; c < 0x20 || c >= 0x7f {
			return true
		}
	}
	return false
}

// unsafeRune reports whether a decoded rune is a control, bidi, or
// invisible character that must not reach a terminal raw.
func unsafeRune(r rune) bool {
	switch {
	case r >= 0x80 && r <= 0x9f: // C1 controls
		return true
	case r >= 0x200b && r <= 0x200f: // zero-width and bidi marks
		return true
	case r == 0x2028 || r == 0x2029: // line and paragraph separators
		return true
	case r >= 0x202a && r <= 0x202e: // bidi embeddings and overrides
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	case r == 0xfeff: // zero-width no-break space
		return true
	}
	return false
}

const hexDigits = "0123456789ABCDEF"

// writeEscape appends one visible escape: "\u{HEX}" for a rune, "\x{HH}" for
// a raw byte, which is padded to minDigits so it reads as a byte.
func writeEscape(b *strings.Builder, prefix string, v uint32, minDigits int) {
	b.WriteString(prefix)
	var buf [8]byte
	n := 0
	for shift := 28; shift >= 0; shift -= 4 {
		d := (v >> uint(shift)) & 0xf
		if d == 0 && n == 0 && shift > 0 {
			continue
		}
		buf[n] = hexDigits[d]
		n++
	}
	for pad := n; pad < minDigits; pad++ {
		b.WriteByte('0')
	}
	b.Write(buf[:n])
	b.WriteByte('}')
}

// truncateCell bounds one already-sanitized value to maxCellRunes on a rune
// boundary. Truncation is a table-layout rule, not a safety rule, so it
// applies only where columns must line up: identifiers, digests, and paths
// in machine-readable output are never shortened, because a caller has to
// be able to feed them straight back to another command.
//
// Because it runs after escaping, a cut can only ever land inside an
// already-inert escape sequence.
func truncateCell(s string) string {
	if utf8.RuneCountInString(s) <= maxCellRunes {
		return s
	}
	n := 0
	for i := range s {
		if n == maxCellRunes-1 {
			return s[:i] + ellipsis
		}
		n++
	}
	return s
}

// sanitizeAll renders a slice of untrusted values, returning nil for an
// empty input so an omitempty JSON field stays absent.
func sanitizeAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = Sanitize(s)
	}
	return out
}

// writeTable writes an aligned table. Every cell must already have passed
// through Sanitize; writeTable adds the layout the renderer deliberately
// refuses to carry inside a value, and bounds each cell so one hostile
// value cannot push the other columns off screen.
func writeTable(w io.Writer, header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if len(header) > 0 {
		if _, err := io.WriteString(tw, strings.Join(header, "\t")+"\n"); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if _, err := io.WriteString(tw, strings.Join(boundRow(row), "\t")+"\n"); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// boundRow bounds every cell of one row.
func boundRow(row []string) []string {
	out := make([]string, len(row))
	for i, c := range row {
		out[i] = truncateCell(c)
	}
	return out
}

// writeDetail writes an aligned "field  value" block. Values must already
// be sanitized; they are not truncated, because the value column is last
// and therefore has no alignment to protect, while the detail view is
// exactly where an operator copies a revision key to feed the next command.
func writeDetail(w io.Writer, rows [][2]string) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, row := range rows {
		if _, err := io.WriteString(tw, row[0]+"\t"+row[1]+"\n"); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// formatTime renders a timestamp in UTC RFC3339, and the empty string for a
// zero value so an absent time stays absent.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// formatTimePtr renders a nullable timestamp.
func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}

// yesNo picks one of two fixed labels.
func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// orMissing displays an empty value as absent.
func orMissing(s string) string {
	if s == "" {
		return missingValue
	}
	return s
}

// derefOrMissing renders a nullable already-rendered value.
func derefOrMissing(p *string) string {
	if p == nil || *p == "" {
		return missingValue
	}
	return *p
}
