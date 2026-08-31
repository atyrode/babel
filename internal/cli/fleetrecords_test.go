package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// The fleet read surface is tested here without a PostgreSQL server, and that
// is a decision rather than a shortcut.
//
// internal/fleet owns the reads and is tested against a real catalog in its own
// package. What lives in internal/cli is the rendering, and the rendering is
// where issue #109 item 3's promises are kept or broken: that a record is
// attributed to the machine that produced it and never to this one, that a
// staged record is visibly staged, that a column this machine cannot fill reads
// as absent rather than as zero, and that another host's model-authored text
// cannot move this terminal's cursor. Every one of those is a pure function of
// a []fleet.Record, so every one is exercised here — including the cases a
// healthy single-host deployment cannot produce, which is exactly the set that
// would otherwise never be tested at all.

// fleetFixtureCommit is the instant every committed fixture record shares, so
// the COMMITTED column is one known width and an assertion on the rendered
// bytes is about layout rather than about when the test ran.
var fleetFixtureCommit = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// fleetHypothesis builds one published candidate. The payload is the real
// wire shape, so the SUMMARY column is derived by the same code the local
// retrieval index uses rather than by a value the test handed it.
func fleetHypothesis(t *testing.T, id, statement string) *frontier.PublishedRecord {
	t.Helper()
	payload, err := json.Marshal(frontier.HypothesisPayload{Statement: statement})
	if err != nil {
		t.Fatal(err)
	}
	return &frontier.PublishedRecord{
		Schema: 1, Kind: frontier.PublishedHypothesis, ID: id, RootID: id,
		RunID: "run-x", CreatedAt: fleetFixtureCommit, Payload: payload,
	}
}

// fleetFinding builds one published finding, for the kind whose summary comes
// from a title rather than a statement.
func fleetFinding(t *testing.T, id, title string) *frontier.PublishedRecord {
	t.Helper()
	payload, err := json.Marshal(frontier.FindingPayload{Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return &frontier.PublishedRecord{
		Schema: 1, Kind: frontier.PublishedFinding, ID: id, RootID: id,
		RunID: "run-x", CreatedAt: fleetFixtureCommit, Payload: payload,
	}
}

// fleetProposal builds one published proposal: a real committed kind with no
// searchable output, which is the record that must still appear in a listing.
func fleetProposal(t *testing.T, id string) *frontier.PublishedRecord {
	t.Helper()
	return &frontier.PublishedRecord{
		Schema: 1, Kind: frontier.PublishedProposal, ID: id, RootID: id,
		RunID: "run-x", CreatedAt: fleetFixtureCommit, Payload: json.RawMessage(`{}`),
	}
}

// fleetRow assembles one fleet record's plaintext row and attribution.
func fleetRow(id, runID string, kind sharedcatalog.RecordKind,
	hostID, display, state string, committed *time.Time) sharedcatalog.FleetRecord {
	return sharedcatalog.FleetRecord{
		Record: sharedcatalog.AnalysisRecordRow{
			RecordID: id, RunID: runID, Kind: kind, Schema: 1,
			CreatedAt: fleetFixtureCommit,
		},
		HostID:           hostID,
		HostDisplayName:  display,
		OriginInstanceID: "instance-" + runID,
		SyncState:        state,
		CommittedAt:      committed,
	}
}

// renderFleetRecords renders one page exactly as `babel fleet records` does,
// through the same three functions the command calls, and returns both
// streams.
func renderFleetRecords(t *testing.T, records []fleet.Record,
	states map[string]string, localHost string) (stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut}
	rows := fleetRecordRows(records, states, localHost)
	res := fleetRecordsResult{
		Records:   rows,
		Counts:    countFleetRecords(rows),
		LocalHost: Sanitize(localHost),
		Limit:     effectiveRecordLimit(0),
	}
	if err := a.reportFleetRecords(res); err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String()
}

// TestFleetRecordsRenderEveryAttributionCase holds the fleet listing to issue
// #109 item 3's honesty rules, byte for byte.
//
// The five rows are the five cases that carry a promise. A record whose origin
// instance registered no host is labelled unattributed and never filed under
// this machine. A staged record shows the pending-sync vocabulary. A record the
// catalog holds no state for shows "local", which is a different claim. A
// record this instance cannot decrypt keeps its reason in place of a summary
// instead of vanishing. And this machine's own row is marked as its own, which
// is the one thing a cross-host listing cannot leave to context.
func TestFleetRecordsRenderEveryAttributionCase(t *testing.T) {
	committed := fleetFixtureCommit
	records := []fleet.Record{{
		FleetRecord: fleetRow("hyp_orphan", "run-1", sharedcatalog.KindHypothesis,
			"", "", sharedcatalog.SyncCommitted, &committed),
		Published: fleetHypothesis(t, "hyp_orphan", "an unattributed candidate"),
	}, {
		FleetRecord: fleetRow("hyp_staged", "run-2", sharedcatalog.KindHypothesis,
			"host-b", "", sharedcatalog.SyncPending, nil),
		Published: fleetHypothesis(t, "hyp_staged", "a staged candidate"),
	}, {
		FleetRecord: fleetRow("hyp_offline", "run-3", sharedcatalog.KindHypothesis,
			"host-c", "Workshop", sharedcatalog.SyncCommitted, &committed),
		Published: fleetHypothesis(t, "hyp_offline", "a candidate with no resolved state"),
	}, {
		FleetRecord: fleetRow("obs_sealed", "run-2", sharedcatalog.KindObservation,
			"host-b", "", sharedcatalog.SyncCommitted, &committed),
		Unopened: "record obs_sealed is sealed under key kid-9, which this instance does not hold",
	}, {
		FleetRecord: fleetRow("fnd_local", "run-4", sharedcatalog.KindFinding,
			"testhost", "", sharedcatalog.SyncCommitted, &committed),
		Published: fleetFinding(t, "fnd_local", "this machine's own committed finding"),
	}}
	// The third record is deliberately absent from the resolution, which is how
	// a record the catalog holds no state for resolves: SyncLocal, because
	// nothing claims it is going anywhere.
	states := map[string]string{
		"hyp_orphan":  sharedcatalog.SyncCommitted,
		"hyp_staged":  sharedcatalog.SyncPending,
		"hyp_offline": fleet.SyncLocal,
		"obs_sealed":  sharedcatalog.SyncCommitted,
		"fnd_local":   sharedcatalog.SyncCommitted,
	}

	stdout, stderr := renderFleetRecords(t, records, states, "testhost")
	const want = "fleet: 3 hosts, 5 records, 1 pending-sync, 1 unattributed, 1 unopened\n" +
		"\n" +
		"HOST                  SYNC          KIND         RECORD       RUN    COMMITTED             SUMMARY\n" +
		"unattributed          committed     hypothesis   hyp_orphan   run-1  2026-03-04T05:06:07Z  an unattributed candidate\n" +
		"host-b                pending-sync  hypothesis   hyp_staged   run-2  -                     a staged candidate\n" +
		"Workshop              local         hypothesis   hyp_offline  run-3  2026-03-04T05:06:07Z  a candidate with no resolved state\n" +
		"host-b                committed     observation  obs_sealed   run-2  2026-03-04T05:06:07Z  unopened: record obs_sealed is sealed under key kid-9, which this instance does not hold\n" +
		"testhost (this host)  committed     finding      fnd_local    run-4  2026-03-04T05:06:07Z  this machine's own committed finding\n"
	if stdout != want {
		t.Errorf("rendered listing\n%s\nwant\n%s", stdout, want)
	}
	// The notes name each condition once and state what it costs, which is what
	// the table cannot say in a cell.
	for _, phrase := range []string{
		"not yet globally reviewable",
		"registered no host",
		"could not be opened",
	} {
		if !strings.Contains(stderr, phrase) {
			t.Errorf("the diagnostics do not explain %q:\n%s", phrase, stderr)
		}
	}
}

// TestFleetRecordsNeverSubstituteTheLocalHost is the failure the attribution
// path exists to prevent, asserted directly: an unattributed record must not
// borrow the host of the machine reading it, and this machine's marker must not
// leak onto another machine's row.
func TestFleetRecordsNeverSubstituteTheLocalHost(t *testing.T) {
	committed := fleetFixtureCommit
	rows := fleetRecordRows([]fleet.Record{{
		FleetRecord: fleetRow("hyp_orphan", "run-1", sharedcatalog.KindHypothesis,
			"", "", sharedcatalog.SyncCommitted, &committed),
	}, {
		FleetRecord: fleetRow("hyp_other", "run-2", sharedcatalog.KindHypothesis,
			"host-b", "Neighbour", sharedcatalog.SyncCommitted, &committed),
	}}, nil, "testhost")

	if rows[0].Host != unattributedHost {
		t.Errorf("unattributed host rendered as %q, want %q", rows[0].Host, unattributedHost)
	}
	if rows[0].HostAttributed {
		t.Error("an unattributed record reports itself as attributed")
	}
	if rows[0].ThisHost || strings.Contains(rows[0].hostCell(), thisHostLabel) {
		t.Errorf("an unattributed record was claimed by this machine: %q", rows[0].hostCell())
	}
	if strings.Contains(rows[0].hostCell(), "testhost") {
		t.Errorf("the local host id leaked into an unattributed row: %q", rows[0].hostCell())
	}
	if rows[1].Host != "Neighbour" || !rows[1].HostAttributed {
		t.Errorf("display name rendered as %q (attributed %v), want the operator-assigned name",
			rows[1].Host, rows[1].HostAttributed)
	}
	if rows[1].ThisHost {
		t.Error("another machine's record was marked as this host's")
	}
	// A row with no resolved sync state falls back to the run's own state
	// rather than to a guess: nil states above is a build with nothing to ask.
	if rows[0].Sync != sharedcatalog.SyncCommitted {
		t.Errorf("unresolved sync state rendered as %q, want the run's own %q",
			rows[0].Sync, sharedcatalog.SyncCommitted)
	}
}

// TestFleetSyncVocabularyIsRenderedVerbatim pins the four strings both surfaces
// share. The web host chip and this table have to agree, so the values cross as
// wire values and are never restyled on the way to a cell.
//
// Each of the four is a different claim and none may stand in for another.
// "committed" asserts durability, "pending-sync" promises a sync in progress,
// "local" says nothing is carrying the record anywhere, and "unknown" says
// nothing answered — so the substitution that must never happen is any of them
// reading as any other, and "local" reading as "pending-sync" most of all.
func TestFleetSyncVocabularyIsRenderedVerbatim(t *testing.T) {
	committed := fleetFixtureCommit
	vocabulary := []string{
		sharedcatalog.SyncCommitted, sharedcatalog.SyncPending,
		fleet.SyncLocal, fleet.SyncUnknown,
	}
	for _, want := range vocabulary {
		t.Run(want, func(t *testing.T) {
			records := []fleet.Record{{
				FleetRecord: fleetRow("hyp_1", "run-1", sharedcatalog.KindHypothesis,
					"host-b", "", sharedcatalog.SyncCommitted, &committed),
			}}
			states := map[string]string{"hyp_1": want}
			rows := fleetRecordRows(records, states, "testhost")
			if rows[0].Sync != want {
				t.Fatalf("sync state rendered as %q, want %q", rows[0].Sync, want)
			}
			stdout, _ := renderFleetRecords(t, records, states, "testhost")
			// The assertion is on the row's SYNC cell rather than on the page:
			// the rollup line above the table is prose about the same states and
			// would satisfy a substring search for the wrong reason.
			lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
			row := lines[len(lines)-1]
			fields := strings.Fields(row)
			if len(fields) < 2 {
				t.Fatalf("the rendered row has no sync cell: %q", row)
			}
			// Exactly the vocabulary value: no upper-casing and no marker glued
			// to it, because it crosses to the web surface as the same string.
			if fields[1] != want {
				t.Errorf("the SYNC cell is %q, want %q, in row %q", fields[1], want, row)
			}
			for _, other := range vocabulary {
				if other == want {
					continue
				}
				if strings.Contains(row, other) {
					t.Errorf("%q was also rendered as %q in row %q", want, other, row)
				}
			}
			if syncCell(states, "hyp_1") != want {
				t.Errorf("syncCell = %q, want %q", syncCell(states, "hyp_1"), want)
			}
		})
	}

	// A state nothing answered for is "unknown", never a blank and never one of
	// the other three. Both paths that can meet the gap are checked: the row
	// builder's fallback, when the catalog row carries no state either, and the
	// local listings' column, when the resolution did not name the id.
	rows := fleetRecordRows([]fleet.Record{{
		FleetRecord: fleetRow("hyp_2", "run-2", sharedcatalog.KindHypothesis,
			"host-b", "", "", nil),
	}}, nil, "testhost")
	if rows[0].Sync != fleet.SyncUnknown {
		t.Errorf("an unanswered sync state rendered as %q, want %q", rows[0].Sync, fleet.SyncUnknown)
	}
	if got := syncCell(map[string]string{}, "hyp_2"); got != fleet.SyncUnknown {
		t.Errorf("syncCell for an unnamed id = %q, want %q", got, fleet.SyncUnknown)
	}
	if got := syncCell(map[string]string{"hyp_2": ""}, "hyp_2"); got != fleet.SyncUnknown {
		t.Errorf("syncCell for a blank state = %q, want %q", got, fleet.SyncUnknown)
	}
}

// TestFleetRecordsKeepUnsearchableKinds defends a row that has no summary to
// show. A proposal and a link commit to the shared catalog and are readable by
// id; what they have no row in is a search index. A listing that dropped them
// because Output reported ErrNotSearchable would hide committed analysis, which
// is a regression and not a filter.
func TestFleetRecordsKeepUnsearchableKinds(t *testing.T) {
	committed := fleetFixtureCommit
	records := []fleet.Record{{
		FleetRecord: fleetRow("prp_1", "run-1", sharedcatalog.KindProposal,
			"host-b", "", sharedcatalog.SyncCommitted, &committed),
		Published: fleetProposal(t, "prp_1"),
	}}
	rows := fleetRecordRows(records, map[string]string{"prp_1": sharedcatalog.SyncCommitted}, "testhost")
	if len(rows) != 1 {
		t.Fatalf("rendered %d rows, want the proposal", len(rows))
	}
	if rows[0].Summary != "" {
		t.Errorf("summary = %q, want absence for a kind with no searchable output", rows[0].Summary)
	}
	if rows[0].Unopened != "" {
		t.Errorf("unopened = %q; an unsearchable kind is not a failure to open", rows[0].Unopened)
	}
	if got := rows[0].summaryCell(); got != missingValue {
		t.Errorf("summary cell = %q, want %q", got, missingValue)
	}
	stdout, _ := renderFleetRecords(t, records, nil, "testhost")
	if !strings.Contains(stdout, "prp_1") || !strings.Contains(stdout, "proposal") {
		t.Errorf("the proposal is missing from the listing:\n%s", stdout)
	}
}

// hostileFleetText is another machine's model-authored text at its worst: an
// SGR sequence, an OSC introducer, a raw C1 CSI, a bidi override, a
// zero-width space, markup, and bytes that are not UTF-8 at all. Every one of
// them arrives through a record another host published, decrypted here, and
// none may reach this terminal raw (SPEC.md §8, §9).
const hostileFleetText = "\x1b[31mred\x1b[0m \x1b]0;retitled\x07 \x9b2J \u202egnitirw-thgir\u202c" +
	" \u200bzero <script>alert(1)</script> \xff\xfe"

// TestFleetRecordsEscapeHostileRemoteContent is the hard requirement: a remote
// record's content is untrusted exactly like any other model output, and it
// reaches this terminal through one renderer.
//
// Every channel a hostile value can take is planted: the host's display name,
// the record id, the run id, the kind, the summary derived from the payload,
// and the reason the record could not be opened. The assertion is on bytes and
// not on shape, so an escape that renders the value inert passes and a value
// that merely looks harmless does not.
//
// "<script>" is deliberately not among the escapes. Sanitize renders values for
// a terminal, where markup is inert text and angle brackets occur in
// legitimate statements; escaping them here would corrupt honest values to
// defend against an injection this surface cannot suffer. The browser is where
// markup matters, and internal/web sanitizes for that surface.
func TestFleetRecordsEscapeHostileRemoteContent(t *testing.T) {
	committed := fleetFixtureCommit
	record := fleetRow("hyp_"+hostileFleetText, "run-"+hostileFleetText,
		sharedcatalog.KindHypothesis, "host-"+hostileFleetText, hostileFleetText,
		sharedcatalog.SyncCommitted, &committed)
	records := []fleet.Record{{
		FleetRecord: record,
		Published:   fleetHypothesis(t, "hyp_1", "a candidate saying "+hostileFleetText),
	}, {
		FleetRecord: fleetRow("obs_2", "run-2", sharedcatalog.KindObservation,
			"host-b", "", sharedcatalog.SyncCommitted, &committed),
		Unopened: "cannot open: " + hostileFleetText,
	}, {
		// A short row for the invalid-UTF-8 channel on its own. The long row
		// above proves nothing escapes; this one proves the escape is reached,
		// because truncateCell bounds a cell at 120 runes and the escapes
		// expand a value well past that — a cut that lands inside an escape can
		// only ever land inside something already inert, which is exactly the
		// property render.go documents.
		FleetRecord: fleetRow("obs_3", "run-3", sharedcatalog.KindObservation,
			"host-d", "bad\xff\xfe", sharedcatalog.SyncCommitted, &committed),
	}}
	states := map[string]string{record.Record.RecordID: hostileFleetText}

	stdout, stderr := renderFleetRecords(t, records, states, "testhost")
	assertNoRawControls(t, "fleet records", stdout, stderr)
	for name, stream := range map[string]string{"stdout": stdout, "stderr": stderr} {
		for _, raw := range []string{"\x1b[31m", "\x1b]0;", "\x9b", "\u202e", "\u200b"} {
			if strings.Contains(stream, raw) {
				t.Errorf("%s carries %q raw:\n%q", name, raw, stream)
			}
		}
		if !utf8.ValidString(stream) {
			t.Errorf("%s is not valid UTF-8:\n%q", name, stream)
		}
	}
	// The escapes are visible rather than dropped: an operator has to be able
	// to see that the value carried something, which is why Sanitize renders
	// them as text instead of deleting them.
	if !strings.Contains(stdout, `\u{1B}`) || !strings.Contains(stdout, `\u{202E}`) {
		t.Errorf("the hostile bytes were dropped rather than escaped:\n%s", stdout)
	}
	if !strings.Contains(stdout, `bad\x{FF}\x{FE}`) {
		t.Errorf("invalid UTF-8 bytes were not rendered as bytes:\n%s", stdout)
	}

	// The machine-readable document carries the same rendering, because a
	// --json consumer writing a value to its own terminal is the same channel
	// one step later.
	var out bytes.Buffer
	a := &app{stdout: &out, stderr: &bytes.Buffer{}}
	rows := fleetRecordRows(records, states, "testhost")
	if err := a.emitJSON(fleetRecordsResult{Records: rows, Counts: countFleetRecords(rows)}); err != nil {
		t.Fatal(err)
	}
	assertNoRawControls(t, "fleet records --json", out.String(), "")
}

// TestFleetIngestReportsWhatItDidAndWhatItCost drives the ingest report's
// rendering, including the sentence that makes the command safe to run: it
// reconciles a rebuildable cache, so nothing it does can lose data (SPEC.md
// §14).
func TestFleetIngestReportsWhatItDidAndWhatItCost(t *testing.T) {
	var out, errOut bytes.Buffer
	a := &app{stdout: &out, stderr: &errOut}
	res := fleetIngestResult{
		Hosts: []fleetIngestHostRow{
			{Host: "host-b", Records: 7, Added: 3, Updated: 1, Removed: 2, Skipped: 3, Foreign: 1},
		},
		Forgotten:    []string{"host-retired"},
		Unattributed: 2,
		Unopened:     []string{"hyp_9: sealed under key kid-4"},
		Rebuilt:      true,
	}
	if err := a.reportFleetIngest(res); err != nil {
		t.Fatal(err)
	}
	stdout := out.String()
	for _, phrase := range []string{
		"HOST", "RECORDS", "ADDED", "UPDATED", "REMOVED", "SKIPPED", "FOREIGN",
		"host-b", "host-retired", "unattributed", "unopened",
		"losing this cache costs a re-index and never data",
		"no durable record was written",
	} {
		if !strings.Contains(stdout, phrase) {
			t.Errorf("the ingest report does not state %q:\n%s", phrase, stdout)
		}
	}
	// A record that could not be opened costs one record and never the ingest,
	// so its reason is a diagnostic beside a successful report.
	if !strings.Contains(errOut.String(), "sealed under key kid-4") {
		t.Errorf("the unopened record's reason was swallowed:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "could not be filed under any host") {
		t.Errorf("the unattributed records were not explained:\n%s", errOut.String())
	}
}

// TestFleetHostFlagParsing pins --host's two spellings. A host id admits no
// comma, so splitting one is unambiguous, and repeating the flag and listing
// values inside it must reach the same filter.
func TestFleetHostFlagParsing(t *testing.T) {
	c := newCmd("fleet records", fleetRecordsUsage)
	hosts, err := parseHostIDs(c, "--host", []string{"host-b, host-c", "host-b", "  ", "host-d"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"host-b", "host-c", "host-d"}
	if strings.Join(hosts, ",") != strings.Join(want, ",") {
		t.Errorf("--host parsed to %v, want %v with the duplicate collapsed", hosts, want)
	}
	if _, err := parseHostIDs(c, "--host", []string{"host-b", "NOT A HOST"}); err == nil {
		t.Error("a value that cannot be a host id was accepted as a filter")
	} else if !strings.Contains(err.Error(), "invalid --host host") {
		t.Errorf("the rejection does not name the flag: %s", err)
	}
	if hosts, err := parseHostIDs(c, "--host", nil); err != nil || hosts != nil {
		t.Errorf("no --host parsed to %v, %v; want the unfiltered read", hosts, err)
	}
}

// TestFleetKindFlagValidation holds --kind to the catalog's closed vocabulary,
// which migrations/0003's CHECK enforces in the database. A kind rejected here
// is a rejected invocation naming what was wanted, not a query that returns
// nothing and looks like an empty fleet.
func TestFleetKindFlagValidation(t *testing.T) {
	c := newCmd("fleet records", fleetRecordsUsage)
	kinds, err := parseRecordKinds(c, []string{"hypothesis", "receipt", "hypothesis"})
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kinds[0] != sharedcatalog.KindHypothesis || kinds[1] != sharedcatalog.KindReceipt {
		t.Errorf("--kind parsed to %v, want the two distinct kinds", kinds)
	}
	_, err = parseRecordKinds(c, []string{"hypotheses"})
	if err == nil {
		t.Fatal("an unknown kind was accepted")
	}
	if !strings.Contains(err.Error(), `unknown --kind "hypotheses"`) {
		t.Errorf("the rejection does not quote the value: %s", err)
	}
	for _, known := range recordKindVocabulary {
		if !strings.Contains(err.Error(), string(known)) {
			t.Errorf("the rejection does not offer %q as an option: %s", known, err)
		}
	}
}

// TestFleetRecordLimitMirrorsTheStore keeps the reported page size honest. A
// zero request means the store's default and an oversized one is clamped, so a
// document that echoed the request would not describe the page it carries.
func TestFleetRecordLimitMirrorsTheStore(t *testing.T) {
	for _, tc := range []struct {
		asked, want int
	}{
		{0, sharedcatalog.DefaultRecordLimit},
		{-5, sharedcatalog.DefaultRecordLimit},
		{25, 25},
		{sharedcatalog.MaxRecordLimit + 1, sharedcatalog.MaxRecordLimit},
	} {
		if got := effectiveRecordLimit(tc.asked); got != tc.want {
			t.Errorf("effectiveRecordLimit(%d) = %d, want %d", tc.asked, got, tc.want)
		}
	}
}

// TestFleetCommandsInLocalModeSayThereIsNoFleet is the local-mode contract for
// every surface that crosses machines: one sentence naming the remedy, exit
// non-zero, and no empty table that would read as "the fleet holds nothing".
//
// The fixture is unconfigured, which is what a Phase A machine is, so this also
// covers the ordinary case of an operator trying the flag before configuring
// shared mode.
func TestFleetCommandsInLocalModeSayThereIsNoFleet(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{
		{"fleet", "records"},
		{"fleet", "records", "--pending"},
		{"fleet", "records", "--host", "host-b,host-c", "--kind", "hypothesis"},
		{"fleet", "ingest"},
		{"fleet", "ingest", "--rebuild"},
		{"hypotheses", "--fleet"},
		{"findings", "--fleet"},
		{"review", "queue", "--fleet"},
		{"dispositions", "--fleet"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr := f.mustExit(exitFailure, args...)
			if !strings.Contains(stderr, "local mode") {
				t.Errorf("the failure does not say this machine has no fleet:\n%s", stderr)
			}
			if !strings.Contains(stderr, "babel storage configure") {
				t.Errorf("the failure does not name the remedy:\n%s", stderr)
			}
			// A stack of wrapped causes is what this message replaces.
			if strings.Count(stderr, ": ") > 2 {
				t.Errorf("the failure reads as wrapped causes rather than one sentence:\n%s", stderr)
			}
			if stdout != "" {
				t.Errorf("a failed fleet read wrote to stdout:\n%s", stdout)
			}
		})
	}
}

// TestFleetFlagRejectionsPrecedeTheCatalog keeps a rejected invocation a usage
// error even on a machine that has no fleet at all. Resolving the reader first
// would report "there is no fleet" for a mistyped flag, which sends the
// operator to configure shared mode over a typo.
func TestFleetFlagRejectionsPrecedeTheCatalog(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"fleet", "records", "--kind", "hypotheses"}, "unknown --kind"},
		{[]string{"fleet", "records", "--host", "NOT A HOST"}, "invalid --host host"},
		{[]string{"fleet", "ingest", "--host", "NOT A HOST"}, "invalid --host host"},
		{[]string{"fleet", "records", "stray-operand"}, "stray-operand"},
		{[]string{"fleet", "nonsense"}, "unknown fleet subcommand"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, stderr := f.mustExit(exitUsage, tc.args...)
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("the rejection does not name %q:\n%s", tc.want, stderr)
			}
			// Only the message line is examined: the usage text below it
			// describes local mode legitimately, and the failure this guards
			// against is a typo answered with "configure shared mode".
			message, _, _ := strings.Cut(stderr, "\n")
			if strings.Contains(message, "local mode") {
				t.Errorf("a rejected invocation was reported as a missing fleet:\n%s", message)
			}
		})
	}
}

// TestLocalListingsRenderSyncStateWithoutACatalog is SPEC.md §9's per-record
// sync state on a machine that has no shared backend: the column has to be
// there, it has to say "local", and it must not cost the listing its ability to
// answer. A machine in local mode is the common case, and a column that only
// worked in shared mode would be a column that broke `babel hypotheses`.
func TestLocalListingsRenderSyncStateWithoutACatalog(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	t.Run("hypotheses", func(t *testing.T) {
		stdout, _ := f.ok("hypotheses")
		if !strings.Contains(stdout, "SYNC") {
			t.Fatalf("the listing has no sync column:\n%s", stdout)
		}
		if strings.Contains(stdout, "HOST") {
			t.Errorf("a listing crossed machines without --fleet:\n%s", stdout)
		}
		if strings.Count(stdout, fleet.SyncLocal) < 1 {
			t.Errorf("no row reports its sync state:\n%s", stdout)
		}
		if strings.Contains(stdout, sharedcatalog.SyncPending) {
			t.Errorf("a record nothing is publishing was reported as staged:\n%s", stdout)
		}
		res := decodeJSON[hypothesesResult](t, mustStdout(t, f, "hypotheses", "--json"))
		if got := res.Sync[ids.hypothesis]; got != fleet.SyncLocal {
			t.Errorf("sync state of %s = %q, want %q", ids.hypothesis, got, fleet.SyncLocal)
		}
		if len(res.Sync) != len(res.Hypotheses) {
			t.Errorf("the sync column names %d of %d rows", len(res.Sync), len(res.Hypotheses))
		}
		if res.Fleet != nil {
			t.Errorf("a listing carried fleet rows without --fleet: %+v", res.Fleet)
		}
	})

	t.Run("findings", func(t *testing.T) {
		res := decodeJSON[findingsResult](t, mustStdout(t, f, "findings", "--json"))
		if got := res.Sync[ids.finding]; got != fleet.SyncLocal {
			t.Errorf("sync state of %s = %q, want %q", ids.finding, got, fleet.SyncLocal)
		}
		stdout, _ := f.ok("findings")
		if !strings.Contains(stdout, "SYNC") || !strings.Contains(stdout, fleet.SyncLocal) {
			t.Errorf("the findings listing does not render sync state:\n%s", stdout)
		}
	})

	t.Run("review queue", func(t *testing.T) {
		res := decodeJSON[queueResult](t, mustStdout(t, f, "review", "queue", "--json"))
		if len(res.Items) == 0 {
			t.Fatal("the seeded queue is empty")
		}
		for _, item := range res.Items {
			if got := res.Sync[item.ID]; got != fleet.SyncLocal {
				t.Errorf("sync state of %s = %q, want %q", item.ID, got, fleet.SyncLocal)
			}
		}
		stdout, _ := f.ok("review", "queue")
		if !strings.Contains(stdout, "SYNC") || !strings.Contains(stdout, fleet.SyncLocal) {
			t.Errorf("the review queue does not render sync state:\n%s", stdout)
		}
	})

	t.Run("dispositions", func(t *testing.T) {
		stdout, _ := f.ok("dispositions")
		if !strings.Contains(stdout, "SYNC") {
			t.Errorf("the dispositions listing has no sync column:\n%s", stdout)
		}
	})
}

// TestFleetSurfacesHelpOnStdout keeps the new commands' help where every other
// command's help is: stdout, with nothing on stderr, so `babel fleet -h` is
// pipeable like the rest.
func TestFleetSurfacesHelpOnStdout(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{
		{"fleet", "-h"},
		{"fleet", "records", "-h"},
		{"fleet", "ingest", "-h"},
	} {
		stdout, stderr := f.ok(args...)
		if !strings.Contains(stdout, "Usage:") {
			t.Errorf("babel %s printed no usage on stdout: %q", strings.Join(args, " "), stdout)
		}
		if stderr != "" {
			t.Errorf("babel %s wrote help to stderr: %q", strings.Join(args, " "), stderr)
		}
	}
}

// TestFleetListingHelpers covers the three decisions a listing makes when it
// crosses machines and that do not need a catalog to reach: how this machine's
// own rows are labelled, which kinds a review listing lets across, and that the
// operator is told the listing stopped being local.
func TestFleetListingHelpers(t *testing.T) {
	// With no reader the row is still this machine's, so it says so rather
	// than borrowing an id nothing here knows.
	if got := localHostCell(nil); got != thisHostLabel {
		t.Errorf("localHostCell(nil) = %q, want %q", got, thisHostLabel)
	}

	for _, tc := range []struct {
		entity frontier.EntityType
		want   []sharedcatalog.RecordKind
	}{
		{frontier.EntityHypothesis, []sharedcatalog.RecordKind{sharedcatalog.KindHypothesis}},
		{frontier.EntityObservation, []sharedcatalog.RecordKind{sharedcatalog.KindObservation}},
		{frontier.EntityFinding, []sharedcatalog.RecordKind{sharedcatalog.KindFinding}},
		{frontier.EntityProposal, []sharedcatalog.RecordKind{sharedcatalog.KindProposal}},
		// An unnarrowed queue crosses with the kinds §6.7 makes reviewable, and
		// deliberately not with observations: one is read through the record it
		// develops, never decided alone.
		{"", []sharedcatalog.RecordKind{
			sharedcatalog.KindHypothesis, sharedcatalog.KindFinding, sharedcatalog.KindProposal,
		}},
	} {
		got := reviewableRecordKinds(tc.entity)
		if len(got) != len(tc.want) {
			t.Fatalf("reviewableRecordKinds(%q) = %v, want %v", tc.entity, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("reviewableRecordKinds(%q) = %v, want %v", tc.entity, got, tc.want)
			}
		}
	}

	var errOut bytes.Buffer
	a := &app{stdout: &bytes.Buffer{}, stderr: &errOut}
	a.fleetListingNote(nil, 0, 0)
	if !strings.Contains(errOut.String(), "no committed record on any other host") {
		t.Errorf("an empty fleet half was not reported:\n%s", errOut.String())
	}

	errOut.Reset()
	rows := fleetRecordRows([]fleet.Record{{
		FleetRecord: fleetRow("hyp_1", "run-1", sharedcatalog.KindHypothesis,
			"host-b", "", sharedcatalog.SyncCommitted, &fleetFixtureCommit),
	}}, nil, "testhost")
	a.fleetListingNote(rows, sharedcatalog.DefaultRecordLimit, 0)
	note := errOut.String()
	if !strings.Contains(note, "came from other hosts") {
		t.Errorf("the operator was not told the listing crossed machines:\n%s", note)
	}
	if !strings.Contains(note, "full at") {
		t.Errorf("a full fleet page was not reported as full:\n%s", note)
	}
}
