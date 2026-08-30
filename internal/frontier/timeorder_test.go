package frontier

import (
	"testing"
	"time"
)

// TestStoredTimestampsSortAsText is the regression for a defect that would only
// have appeared under load. Timestamps are stored as text and two queries order
// by a timestamp column, so text order must equal chronological order.
// time.RFC3339Nano trims trailing zeros, which breaks that: comparing
// "12:00:00.1Z" against "12:00:00.12Z" measures 'Z' against '2' and places the
// earlier instant second. Sub-tenth-of-a-second gaps are precisely when two
// records land in one operation, so the bug would corrupt exactly the ordering
// that matters.
func TestStoredTimestampsSortAsText(t *testing.T) {
	base := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	// Fractions chosen so a trimming layout inverts them: 100ms trims to ".1",
	// 120ms to ".12", and ".1Z" > ".12Z" as text.
	instants := []time.Time{
		base,
		base.Add(1 * time.Nanosecond),
		base.Add(100 * time.Millisecond),
		base.Add(120 * time.Millisecond),
		base.Add(1 * time.Second),
		base.Add(90 * time.Second),
	}
	var prev string
	for i, instant := range instants {
		got := formatTime(instant)
		if i > 0 && !(prev < got) {
			t.Errorf("text order disagrees with time order:\n  %q (earlier)\n  %q (later)", prev, got)
		}
		// The layout must remain parseable by the reader, which uses
		// RFC3339Nano.
		back, err := parseTime(got)
		if err != nil {
			t.Fatalf("parseTime(%q): %v", got, err)
		}
		if !back.Equal(instant) {
			t.Errorf("round trip lost precision: %v -> %q -> %v", instant, got, back)
		}
		prev = got
	}

	// Every rendering must be the same width, since equal width is what makes
	// lexicographic comparison a total order over instants.
	width := len(formatTime(base))
	for _, instant := range instants {
		if got := len(formatTime(instant)); got != width {
			t.Errorf("width %d != %d for %v; a variable-width fraction is the defect itself",
				got, width, instant)
		}
	}
}
