package cli

import (
	"strings"
	"testing"
)

// pricedSessionStem names the fixture session whose transcript records what
// its one assistant turn cost.
const pricedSessionStem = "2026-01-05T12-13-14-151Z_00000000-0000-4000-8000-000000000005"

// writePricedSession plants a session whose log carries a usage block, next
// to the three unmeasured fixture sessions.
func (f *fixture) writePricedSession() {
	f.t.Helper()
	f.writeSession(sessionSpec{
		project:    "synthetic-priced",
		stem:       pricedSessionStem,
		id:         "00000000-0000-4000-8000-000000000005",
		title:      "Synthetic priced session",
		workspace:  "/synthetic/workspace/priced",
		pricedTurn: true,
	})
}

// What a session cost is recorded in its own transcript, and until now Babel
// threw it away on every describe. The listing is where an operator would ask
// the question, so this pins the numbers exactly: they are sums over the
// fixture's one usage block, not a shape the code is free to redefine.
func TestSessionsListCarriesRecordedUsage(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	f.writePricedSession()

	stdout, _ := f.ok("sessions", "list", "--json")
	rows := decode[sessionsResult](t, stdout).Sessions

	var priced *sessionRow
	unmeasured := 0
	for i, row := range rows {
		if strings.HasSuffix(row.SourceID, pricedSessionStem) {
			priced = &rows[i]
			continue
		}
		// Every other fixture session has a user message and nothing else,
		// which is exactly a transcript that recorded no usage. All four
		// fields must stay null rather than reporting a free session.
		if row.CostUSD != nil || row.TotalTokens != nil || row.Turns != nil || row.ToolErrors != nil {
			t.Errorf("%s records no usage but the listing reports some: %+v", row.SourceID, row)
		}
		unmeasured++
	}
	if priced == nil {
		t.Fatalf("the priced fixture session is not in the listing: %s", stdout)
	}
	if unmeasured == 0 {
		t.Fatal("the fixture listed no unmeasured session, so absence was never exercised")
	}

	if priced.CostUSD == nil || *priced.CostUSD != 1.25 {
		t.Errorf("cost_usd = %v, want the 1.25 the transcript records", priced.CostUSD)
	}
	if priced.TotalTokens == nil || *priced.TotalTokens != 300 {
		t.Errorf("total_tokens = %v, want 300", priced.TotalTokens)
	}
	if priced.Turns == nil || *priced.Turns != 1 {
		t.Errorf("turns = %v, want the one assistant turn", priced.Turns)
	}
	if priced.ToolErrors == nil || *priced.ToolErrors != 1 {
		t.Errorf("tool_errors = %v, want the one failing tool result", priced.ToolErrors)
	}

	// A second listing is served from the catalog rather than from a fresh
	// describe, and it must report the same numbers: the cached columns are
	// what a push sends to the shared catalog, so a listing that disagreed
	// with them would disagree with what other machines see.
	stdout, _ = f.ok("sessions", "list", "--json")
	for _, row := range decode[sessionsResult](t, stdout).Sessions {
		if !strings.HasSuffix(row.SourceID, pricedSessionStem) {
			continue
		}
		if row.CostUSD == nil || *row.CostUSD != 1.25 || row.Turns == nil || *row.Turns != 1 {
			t.Errorf("the cached listing lost the usage summary: %+v", row)
		}
	}
}

// The human listing is the surface an operator actually reads, and a column of
// nothing but "-" is worse than no column: it costs every other column its
// width on a corpus whose harnesses record no usage. So COST appears exactly
// when some row has one.
func TestSessionsListShowsCostOnlyWhenSomethingRecordedIt(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()

	stdout, _ := f.ok("sessions", "list")
	if strings.Contains(stdout, "COST") {
		t.Fatalf("the listing has a COST column although no session records one:\n%s", stdout)
	}

	f.writePricedSession()
	stdout, _ = f.ok("sessions", "list")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if !strings.Contains(lines[0], "COST") {
		t.Fatalf("the listing has no COST column although a session records one: %q", lines[0])
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		// COST is second from last, before GRADE, and neither cell carries
		// whitespace, so each row states its own cost.
		cost := fields[len(fields)-2]
		if strings.Contains(line, pricedSessionStem) {
			if cost != "$1.25" {
				t.Errorf("the priced session's cost rendered as %q, want $1.25\n%s", cost, stdout)
			}
			continue
		}
		if cost != missingValue {
			t.Errorf("an unmeasured session rendered a cost of %q, want %q: zero would claim it ran for free\n%s",
				cost, missingValue, stdout)
		}
	}
}
