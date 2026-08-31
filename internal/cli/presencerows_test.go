package cli

// The fleet presence rows `babel conductor status` prints (issue #118).
//
// The reader is a fake, and it has to be: the states this section exists to
// render honestly — a heartbeat four minutes old, one three hours old, a run
// that finished — are the states a healthy deployment cannot produce on demand.
// A test against a real store would either need PostgreSQL or a wall clock wound
// forward, and would still not reach the only rows that matter.
//
// What is checked here is the rendering contract and nothing else.
// internal/presence owns the classification, the age computation and the
// announce sequence, and its own tests own them.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/presence"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// fakePresenceReader is a wired presence.Reader over fixed rows.
type fakePresenceReader struct {
	rows []presence.Row
	fail error
}

func (f fakePresenceReader) Fleet(context.Context) ([]presence.Row, error) {
	if f.fail != nil {
		return nil, f.fail
	}
	return f.rows, nil
}

// presenceFleetFixture is one busy deployment as the catalog holds it.
//
// The pair of rows for cyc-3 is the case PresenceCore's design makes normal and
// a naive renderer would destroy: one conductor cycle announces twice, as the
// loop's own row and as the run inside it, both under the same run id. They are
// two facts — the loop can be alive while the run it started is not — so the
// section must show both and distinguish them by kind rather than collapse them.
func presenceFleetFixture(text string) []presence.Row {
	started := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	row := func(id, host string, local bool, kind presence.Kind, state presence.State,
		runID string, age time.Duration) presence.Row {
		// The authority is the kind's own, because #96's vocabulary is what a
		// conductor cycle and an operator's command actually record: a loop
		// draws under a policy, and a person types a command. A fixture that
		// gave both the same authority would render a column no deployment can
		// produce.
		authority := runstore.Authority{
			Kind: runstore.AuthorityOperator, Ref: "babel explore " + text,
		}
		if kind == presence.KindConductor {
			authority = runstore.Authority{
				Kind: runstore.AuthorityPolicy, Ref: "duty:self-improvement " + text,
			}
		}
		r := presence.Row{
			ID: presence.PresenceID(id), Host: host, Local: local, Kind: kind,
			RunID: runID, Recipe: "outcome-integrity " + text,
			PreparationID: "prep-" + runID,
			Authority:     authority,
			State:         state, StartedAt: started, HeartbeatAt: started.Add(time.Minute),
			HeartbeatAge: age,
		}
		if state != presence.StateRunning {
			r.FinishedAt = started.Add(2 * time.Minute)
			r.ReceiptRecordID = "rcp-" + runID
		}
		r.Freshness = presence.Classify(state, age)
		return r
	}
	return []presence.Row{
		// This machine, heartbeating normally.
		row("p1", testHostID, true, presence.KindExplore, presence.StateRunning,
			"run-local-1", 15*time.Second),
		// Another machine's conductor cycle and the run inside it: same run id,
		// two kinds, and the run has gone quiet while the loop has not.
		row("p2", "host-elsewhere", false, presence.KindConductor, presence.StateRunning,
			"run-cyc-3", 20*time.Second),
		row("p3", "host-elsewhere", false, presence.KindExplore, presence.StateRunning,
			"run-cyc-3", 4*time.Minute),
		// A third machine nothing has been heard from for hours.
		row("p4", "host-laptop", false, presence.KindExplore, presence.StateRunning,
			"run-laptop-9", 3*time.Hour),
		// And one that finished and committed its receipt.
		row("p5", "host-elsewhere", false, presence.KindConductor, presence.StateFinished,
			"run-cyc-2", 40*time.Minute),
	}
}

func presenceApp(reader presence.Reader) (*app, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	return &app{stdout: &stdout, stderr: &stderr, presenceRead: reader}, &stdout, &stderr
}

// TestPresenceRowsSayWhatAnAgeDoesNotEstablish is the assertion this whole
// section exists for.
//
// A stale or lost row must never render as a bare state. The row's own claim is
// "running", and the only truthful thing the terminal can add is how long ago
// that claim was made and that nothing since has confirmed or refuted it — so
// the cell carries the sentence, and a fresh row does not, because there is
// nothing to disclaim about a heartbeat fifteen seconds old.
//
// The pair with one run id is checked in the same pass: both rows are present,
// with different kinds, because a renderer that deduplicated on run id would
// hide exactly the case the two rows exist to make visible.
func TestPresenceRowsSayWhatAnAgeDoesNotEstablish(t *testing.T) {
	a, stdout, _ := presenceApp(fakePresenceReader{rows: presenceFleetFixture("plain")})
	rows, note := a.presenceRows(context.Background())
	if note != "" {
		t.Fatalf("a wired reader produced a note: %q", note)
	}
	if got, want := len(rows), 5; got != want {
		t.Fatalf("rows = %d, want %d", got, want)
	}
	a.writePresenceRows(rows, note)
	out := stdout.String()

	// Exactly the two doubtful rows disclaim, and they are the two whose
	// freshness internal/presence classified as stale and lost.
	if got, want := strings.Count(out, disclaimer), 2; got != want {
		t.Errorf("the disclaimer appears %d times, want %d:\n%s", got, want, out)
	}
	// The assertions read columns rather than search lines. A substring test
	// would match "explore" inside an authority reference and check the wrong
	// row, which is exactly the kind of quiet mismatch that lets a rendering
	// bug pass — and the two rows sharing one run id make a line search
	// ambiguous by construction.
	table := presenceTable(t, out)
	if got, want := len(table), 5; got != want {
		t.Fatalf("the table has %d rows, want %d (one line per run):\n%s", got, want, out)
	}
	for i, want := range []struct {
		host    string
		kind    string
		run     string
		state   string
		seen    string
		doubt   bool
		receipt string
	}{
		// This machine's own fresh run says whose it is, states its age, and
		// disclaims nothing: there is nothing to disclaim about a heartbeat
		// fifteen seconds old.
		{host: testHostID + thisHostSuffix, kind: "explore", run: "run-local-1",
			state: "running", seen: "<1m ago", receipt: missingValue},
		// The loop is heartbeating and the run it started has gone quiet. Two
		// rows, one run id, and only the second doubts — which is the whole
		// reason the pair is not collapsed.
		{host: "host-elsewhere", kind: "conductor", run: "run-cyc-3",
			state: "running", seen: "<1m ago", receipt: missingValue},
		{host: "host-elsewhere", kind: "explore", run: "run-cyc-3",
			state: "running", seen: "4m ago", doubt: true, receipt: missingValue},
		{host: "host-laptop", kind: "explore", run: "run-laptop-9",
			state: "running", seen: "3h ago", doubt: true, receipt: missingValue},
		// A finished row's age is a fact about when it ended, so it carries no
		// disclaimer — and it names the receipt, which is where presence stops
		// and durable analysis begins.
		{host: "host-elsewhere", kind: "conductor", run: "run-cyc-2",
			state: "finished", seen: "40m ago", receipt: "rcp-run-cyc-2"},
	} {
		got := table[i]
		if got.host != want.host || got.kind != want.kind || got.run != want.run {
			t.Errorf("row %d identity = %q/%q/%q, want %q/%q/%q",
				i, got.host, got.kind, got.run, want.host, want.kind, want.run)
		}
		if got.state != want.state {
			t.Errorf("row %d state = %q, want %q", i, got.state, want.state)
		}
		if got.receipt != want.receipt {
			t.Errorf("row %d receipt = %q, want %q", i, got.receipt, want.receipt)
		}
		wantSeen := want.seen
		if want.doubt {
			wantSeen += " — " + disclaimer
		}
		if got.seen != wantSeen {
			t.Errorf("row %d last seen = %q, want %q", i, got.seen, wantSeen)
		}
		// The authority is #96's, rendered as kind and reference, so "why is
		// that machine running this" is answerable from the row.
		if !strings.HasPrefix(got.authority, "operator ") && !strings.HasPrefix(got.authority, "policy ") {
			t.Errorf("row %d authority = %q, want a #96 kind and a reference", i, got.authority)
		}
	}

	// The prose above the table says what a row is before the operator reads
	// one. Without it the rows read as observations.
	if !strings.Contains(out, "A row is a claim") {
		t.Errorf("the section does not frame its rows:\n%s", out)
	}
	if !strings.Contains(out, "fleet presence\n") {
		t.Errorf("the section has no heading of its own:\n%s", out)
	}
}

// disclaimer is the sentence a doubtful row must carry, declared once so the
// tests assert the bytes the report prints rather than a drifting copy.
const disclaimer = "running or dead, this host cannot tell"

// presenceCells is one rendered table row, by column.
type presenceCells struct {
	host, kind, run, recipe, authority, state, seen, receipt string
}

// presenceTable parses the rendered section back into columns.
//
// It splits on runs of two or more spaces, which is exactly what writeTable
// inserts between cells and exactly what no cell contains: every value in this
// table is an identifier, a fixed word, or a sentence with single spaces. That
// makes a column assertion possible without the test knowing the column widths,
// which are a function of the fixture's own host ids.
func presenceTable(t *testing.T, out string) []presenceCells {
	t.Helper()
	header := strings.Index(out, "HOST")
	if header < 0 {
		t.Fatalf("the report has no table:\n%s", out)
	}
	var rows []presenceCells
	for _, line := range strings.Split(strings.TrimRight(out[header:], "\n"), "\n")[1:] {
		fields := splitCells(line)
		if len(fields) != 8 {
			t.Fatalf("row %q parsed into %d cells, want 8", line, len(fields))
		}
		rows = append(rows, presenceCells{
			host: fields[0], kind: fields[1], run: fields[2], recipe: fields[3],
			authority: fields[4], state: fields[5], seen: fields[6], receipt: fields[7],
		})
	}
	return rows
}

func splitCells(line string) []string {
	var out []string
	for _, field := range strings.Split(line, "  ") {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// TestPresenceRowsKeepTheReadersOrder checks that this file does not sort.
// internal/presence returns running rows before finished ones, newest heartbeat
// first; a second ordering here would be a second answer to the same question.
func TestPresenceRowsKeepTheReadersOrder(t *testing.T) {
	fixture := presenceFleetFixture("plain")
	a, _, _ := presenceApp(fakePresenceReader{rows: fixture})
	rows, _ := a.presenceRows(context.Background())
	for i, row := range rows {
		if row.RunID != fixture[i].RunID {
			t.Fatalf("row %d is %q, want %q; the reader's order was not kept",
				i, row.RunID, fixture[i].RunID)
		}
	}
}

// TestPresenceRowsExplainAnIdleDeployment covers the empty case, which must not
// read as a broken one.
//
// Two things have to be in the sentence. The window, because an empty list means
// "nothing inside it" rather than "nothing ever". And this machine's own runs,
// because the wrong reading an operator will reach for is "presence is not wired
// here" — it is, and the deployment is simply idle.
func TestPresenceRowsExplainAnIdleDeployment(t *testing.T) {
	a, stdout, _ := presenceApp(fakePresenceReader{})
	rows, note := a.presenceRows(context.Background())
	if note != "" || len(rows) != 0 {
		t.Fatalf("an idle fleet produced rows %d note %q", len(rows), note)
	}
	a.writePresenceRows(rows, note)
	out := stdout.String()
	for _, want := range []string{
		formatAge(presence.RetentionWindow),
		"This machine's own",
		"idle deployment",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the idle sentence does not mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "HOST") {
		t.Errorf("an idle fleet printed a table header:\n%s", out)
	}
}

// TestPresenceNotesReadDifferently is the degradation contract on the terminal.
//
// Three failures, three sentences, and none of them an error: `conductor status`
// was asked about this machine's loop, and a shared catalog it cannot reach must
// not stop it answering. The three read differently because they resolve
// differently — configuration, an outage that fixes itself, and a
// misconfiguration that does not — and an operator sent to the wrong one of
// those loses an afternoon.
func TestPresenceNotesReadDifferently(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fail   error
		phrase string
		remedy string
	}{
		{
			name:   "local mode",
			fail:   fmt.Errorf("presence: %w", presence.ErrNotConfigured),
			phrase: "local mode",
			remedy: "babel storage configure",
		},
		{
			name: "unreachable",
			fail: fmt.Errorf("presence: read fleet: %w: dial tcp: connection refused",
				sharedcatalog.ErrUnreachable),
			phrase: "could not be reached",
			remedy: "runs elsewhere are unaffected",
		},
		{
			name:   "refused",
			fail:   errors.New(`pq: relation "fleet_presence" does not exist`),
			phrase: "refused this read",
			remedy: "presence table may be missing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, stdout, _ := presenceApp(fakePresenceReader{fail: tc.fail})
			rows, note := a.presenceRows(context.Background())
			if len(rows) != 0 {
				t.Fatalf("a failed read produced %d rows", len(rows))
			}
			if !strings.Contains(note, tc.phrase) {
				t.Fatalf("note = %q, want it to mention %q", note, tc.phrase)
			}
			if !strings.Contains(note, tc.remedy) {
				t.Errorf("note = %q, want it to mention %q", note, tc.remedy)
			}
			a.writePresenceRows(rows, note)
			out := stdout.String()
			if !strings.Contains(out, note) {
				t.Errorf("the note did not reach the report:\n%s", out)
			}
			if strings.Contains(out, "HOST") {
				t.Errorf("a failed read printed a table header:\n%s", out)
			}
		})
	}
}

// TestPresenceNoteCarriesNoErrorText checks that the sentence is fixed text. A
// wrapped catalog error can quote a whole connection string, and the note is
// printed to stdout where `--json` callers and terminals both read it.
func TestPresenceNoteCarriesNoErrorText(t *testing.T) {
	const secret = "postgres://babel:hunter2@catalog.internal:5432/babel"
	a, stdout, _ := presenceApp(fakePresenceReader{
		fail: fmt.Errorf("presence: read fleet: %s: %w", secret, sharedcatalog.ErrUnreachable),
	})
	rows, note := a.presenceRows(context.Background())
	a.writePresenceRows(rows, note)
	for _, forbidden := range []string{secret, "hunter2", "catalog.internal"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Errorf("the report quotes %q:\n%s", forbidden, stdout.String())
		}
	}
}

// TestPresenceRowsEscapeHostileValues drives the fixture with a presentation
// attack in the fields a remote machine chooses.
//
// A recipe id and an authority reference are strings another host's operator
// typed, and they arrive here with no schema between them and this terminal —
// which makes this section a more direct path from a foreign machine to an
// operator's screen than any other CLI surface. Nothing may reach stdout raw,
// and nothing may be dropped either: a sanitizer that emptied the cells would
// pass every escaping assertion and leave the operator unable to see what the
// other machine is running.
func TestPresenceRowsEscapeHostileValues(t *testing.T) {
	a, stdout, stderr := presenceApp(fakePresenceReader{
		rows: presenceFleetFixture(hostileTitle),
	})
	rows, note := a.presenceRows(context.Background())
	a.writePresenceRows(rows, note)
	assertNoRawControls(t, "conductor status fleet rows", stdout.String(), stderr.String())
	if !strings.Contains(stdout.String(), "outcome-integrity") {
		t.Errorf("the recipe was dropped rather than escaped:\n%s", stdout.String())
	}
	for _, row := range rows {
		if row.Recipe == "" || row.AuthorityRef == "" {
			t.Errorf("a hostile row lost its fields: %+v", row)
		}
	}
}

// TestPresenceAgeIsNeverNegative covers the skew case. PostgreSQL subtracts a
// heartbeat another machine wrote from its own clock, so a writer running ahead
// puts a heartbeat in the catalog's future; "last seen -90s ago" is not a fact
// about anything, so it reads as the freshest thing the column can say.
func TestPresenceAgeIsNeverNegative(t *testing.T) {
	a, stdout, _ := presenceApp(fakePresenceReader{rows: []presence.Row{{
		ID: "p-skew", Host: "host-ahead", Kind: presence.KindExplore,
		RunID: "run-ahead", State: presence.StateRunning,
		HeartbeatAge: -90 * time.Second,
		Freshness:    presence.Classify(presence.StateRunning, -90*time.Second),
	}}})
	rows, note := a.presenceRows(context.Background())
	if got, want := rows[0].AgeSeconds, int64(0); got != want {
		t.Errorf("heartbeat_age_seconds = %d, want %d", got, want)
	}
	a.writePresenceRows(rows, note)
	if strings.Contains(stdout.String(), "-") && strings.Contains(stdout.String(), "-1m") {
		t.Errorf("the report rendered a negative age:\n%s", stdout.String())
	}
}

// TestConductorStatusPrintsTheFleetSectionBelowTheJournal drives the shipped
// command on an unconfigured machine.
//
// Two properties only the real command can show. That local mode does not fail
// the command — the loop's own state is what was asked for, and it still
// answers — and that the fleet section is printed after this machine's own
// journal and under its own heading, which is the whole separation: everything
// above is observed here and true, everything below is another machine's claim.
func TestConductorStatusPrintsTheFleetSectionBelowTheJournal(t *testing.T) {
	f := newFixture(t)
	stdout, stderr := f.ok("conductor", "status")
	if !strings.Contains(stdout, "fleet presence") {
		t.Fatalf("conductor status printed no fleet section:\n%s", stdout)
	}
	// In local mode the section is one honest sentence naming the ceremony.
	if !strings.Contains(stdout, "local mode") {
		t.Errorf("the fleet section does not explain local mode:\n%s", stdout)
	}
	// Order is the argument: the cycles table is this machine's own journal,
	// and the fleet comes after it.
	cycles := strings.Index(stdout, "no cycles recorded")
	fleet := strings.Index(stdout, "fleet presence")
	if cycles < 0 || fleet < cycles {
		t.Errorf("the fleet section is not below the local journal:\n%s", stdout)
	}
	assertNoRawControls(t, "conductor status", stdout, stderr)

	// --json carries the same answer as a field of its own, never merged into
	// the cycles: a caller that could not tell a local cycle from a remote
	// claim would treat an advisory heartbeat as a fact.
	stdout, stderr = f.ok("conductor", "status", "--json")
	assertNoRawControls(t, "conductor status --json", stdout, stderr)
	status := decodeJSON[conductorStatusResult](t, stdout)
	if len(status.Fleet) != 0 {
		t.Errorf("local mode reported %d fleet rows", len(status.Fleet))
	}
	if !strings.Contains(status.FleetNote, "local mode") {
		t.Errorf("fleet_note = %q", status.FleetNote)
	}
}
