package cli

import (
	"bytes"
	"context"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/reference/resolve"
)

// The citation section on the record-describing commands (issue #113).
//
// These cases drive the shipped command surface — Run, the real durable file,
// a real reference.Store — because what they are about is the wiring: an edge
// store that lists edges and a command that never reaches it is the failure a
// package test cannot see.

// absentHypothesisID is a well-formed candidate identifier this fixture never
// writes.
//
// An edge naming it stands for the case internal/explore already names: a
// citation asserted when its endpoint existed, read on a host whose database
// has since been restored from an older backup. The graph's shape is knowable
// even when the record is not, and the section has to show the edge rather
// than drop it — a citation silently omitted is a citation nobody can question.
const absentHypothesisID = "hyp_00000000000000000000000000000000"

// citedSessionKey stands in for a durable session key: the digest shape
// sharedcatalog.SessionUID mints, planted directly because this fixture is in
// local mode and therefore derives no keys of its own.
const citedSessionKey = "0000000000000000000000000000000000000000000000000000000000000001"

// seedCitations plants the citations these cases read back: three the seeded
// candidate makes, and one made about it.
//
// The registry vouches for every endpoint, which is fixture scaffolding rather
// than a shortcut around #113's anchoring gate. That gate refuses an edge whose
// endpoint this machine cannot demonstrate, and proving it does is
// internal/reference's own test; what these cases are about is what a command
// does with citations already recorded — the one naming an unreachable record
// included, which no gated write could plant.
//
// The clock steps one second per edge, so newest-first ordering is a property
// of the rows rather than of how fast the machine ran.
func seedCitations(t *testing.T, dir string, ids seeded) {
	t.Helper()
	registry := reference.NewRegistry()
	vouch := reference.ResolverFunc(func(context.Context, string) (bool, error) { return true, nil })
	for _, namespace := range []string{
		resolve.NamespaceHypothesis, resolve.NamespaceFinding, resolve.NamespaceSession,
	} {
		if err := registry.Register(namespace, vouch); err != nil {
			t.Fatal(err)
		}
	}
	tick := 0
	store, err := reference.Open(dir,
		reference.WithResolvers(registry),
		reference.WithClock(func() time.Time {
			tick++
			return time.Date(2026, time.March, 1, 12, 0, tick, 0, time.UTC)
		}))
	if err != nil {
		t.Fatal(err)
	}

	candidate := reference.RecordRef{Kind: resolve.NamespaceHypothesis, ID: ids.hypothesis}
	for _, edge := range []reference.Edge{{
		// A run absorbing a session as evidence, with the note carrying the
		// presentation attack: an edge's note is the one field of it a model
		// writes.
		Kind:      reference.KindEvidence,
		From:      candidate,
		To:        reference.RecordRef{Kind: resolve.NamespaceSession, ID: citedSessionKey},
		ActorKind: reference.ActorRun,
		ActorRef:  "run-seed",
		Note:      "cited because " + hostileStatement,
	}, {
		// Babel's own absorption, which names no actor reference at all.
		Kind:      reference.KindEvidence,
		From:      candidate,
		To:        reference.RecordRef{Kind: resolve.NamespaceFinding, ID: ids.finding},
		ActorKind: reference.ActorSystem,
	}, {
		Kind:      reference.KindInspiredBy,
		From:      candidate,
		To:        reference.RecordRef{Kind: resolve.NamespaceHypothesis, ID: ids.deferred},
		ActorKind: reference.ActorOperator,
		ActorRef:  "synthetic-operator",
	}, {
		Kind:      reference.KindRefines,
		From:      reference.RecordRef{Kind: resolve.NamespaceHypothesis, ID: absentHypothesisID},
		To:        candidate,
		ActorKind: reference.ActorOperator,
		ActorRef:  "synthetic-operator",
	}} {
		if _, err := store.Append(context.Background(), edge); err != nil {
			store.Close()
			t.Fatalf("seed %s edge to %s: %v", edge.Kind, edge.To, err)
		}
	}
	// Closed before any command runs, so the reads under test are not sharing
	// a handle with the writer that planted their rows.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestHypothesisShowCarriesItsCitationsInJSON is the machine-readable half:
// both directions, every endpoint, and the per-direction counts by kind a
// caller reads before any row.
func TestHypothesisShowCarriesItsCitationsInJSON(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()
	seedCitations(t, f.dataDir, ids)

	stdout, stderr := f.ok("hypothesis", "show", ids.hypothesis, "--json")
	res := decodeJSON[hypothesisResult](t, stdout)
	if res.References == nil {
		t.Fatal("a build that records citations emitted no references field")
	}
	if want := (map[string]int{"evidence": 2, "inspired_by": 1}); !maps.Equal(res.References.Counts.Cites, want) {
		t.Errorf("outgoing counts = %v, want %v", res.References.Counts.Cites, want)
	}
	if want := (map[string]int{"refines": 1}); !maps.Equal(res.References.Counts.CitedBy, want) {
		t.Errorf("backlink counts = %v, want %v", res.References.Counts.CitedBy, want)
	}
	if len(res.References.Cites) != 3 || len(res.References.CitedBy) != 1 {
		t.Fatalf("citations = %d out, %d in; want 3 and 1", len(res.References.Cites), len(res.References.CitedBy))
	}

	// The rows themselves, newest first. The actor cell is asserted for each
	// of the three actor kinds an edge can carry, because "system" carries no
	// reference and a renderer keyed on the reference would drop it.
	for _, tc := range []struct {
		name      string
		row       citationRow
		kind      string
		otherKind string
		otherID   string
		actor     string
	}{
		{"newest first is the operator's inspiration", res.References.Cites[0],
			"inspired_by", "hypothesis", ids.deferred, "operator synthetic-operator"},
		{"babel's own absorption names no reference", res.References.Cites[1],
			"evidence", "finding", ids.finding, "system"},
		{"the run's session evidence", res.References.Cites[2],
			"evidence", "session", citedSessionKey, "run run-seed"},
		{"the backlink names a record this host does not hold", res.References.CitedBy[0],
			"refines", "hypothesis", absentHypothesisID, "operator synthetic-operator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.row.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", tc.row.Kind, tc.kind)
			}
			if tc.row.OtherKind != tc.otherKind || tc.row.OtherID != tc.otherID {
				t.Errorf("endpoint = %s:%s, want %s:%s",
					tc.row.OtherKind, tc.row.OtherID, tc.otherKind, tc.otherID)
			}
			if tc.row.Actor != tc.actor {
				t.Errorf("actor = %q, want %q", tc.row.Actor, tc.actor)
			}
			if tc.row.ID == "" || tc.row.CreatedAt == "" {
				t.Errorf("the edge lost its identity or its timestamp: %+v", tc.row)
			}
		})
	}

	// The document stayed backward compatible: the fields that were there
	// before this section are still there.
	if res.Hypothesis.ID != ids.hypothesis || len(res.LinksFrom) != 1 || len(res.Observations) != 1 {
		t.Errorf("adding the citations changed the rest of the document: %+v", res)
	}
	assertNoRawControls(t, "hypothesis show --json", stdout, stderr)
}

// TestHypothesisShowPrintsTheCitationSection is the terminal half: the counts
// line first, then one row per edge in both directions.
func TestHypothesisShowPrintsTheCitationSection(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()
	seedCitations(t, f.dataDir, ids)

	stdout, stderr := f.ok("hypothesis", "show", ids.hypothesis)
	section, ok := citationSection(t, stdout)
	if !ok {
		t.Fatalf("no references section in:\n%s", stdout)
	}
	if want := "cites: evidence 2, inspired_by 1  cited by: refines 1"; section[0] != want {
		t.Errorf("counts line = %q, want %q", section[0], want)
	}
	if want := "DIR"; !strings.HasPrefix(section[1], want) {
		t.Errorf("second line = %q, want the table header", section[1])
	}
	rows := section[2:]
	if len(rows) != 4 {
		t.Fatalf("the table holds %d rows, want one per edge:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"outgoing inspiration", []string{"cites", "inspired_by", "hypothesis:" + ids.deferred, "operator synthetic-operator"}},
		{"outgoing absorption", []string{"cites", "evidence", "finding:" + ids.finding, "system"}},
		{"outgoing session evidence", []string{"cites", "evidence", "session:" + citedSessionKey, "run run-seed"}},
		{"backlink from a record this host does not hold", []string{"cited by", "refines", "hypothesis:" + absentHypothesisID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, cell := range tc.want {
				if !strings.Contains(rows[0], cell) {
					t.Fatalf("row %q is missing %q", rows[0], cell)
				}
			}
			rows = rows[1:]
		})
	}
	// The section is an addition: the links table internal/frontier owns is
	// still above it, under its own heading.
	if !strings.Contains(stdout, "\nlinks\n") {
		t.Errorf("the frontier's own links section disappeared:\n%s", stdout)
	}
	assertNoRawControls(t, "hypothesis show", stdout, stderr)
}

// TestCitationNotesAreRenderedThroughTheSanitizer plants a presentation attack
// in the one field of an edge a model writes and reads it back off both
// surfaces. An edge's note is content (SPEC.md §9), so it arrives here the same
// way a session title does: from a producer outside Babel's trust boundary.
func TestCitationNotesAreRenderedThroughTheSanitizer(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()
	seedCitations(t, f.dataDir, ids)

	for _, args := range [][]string{
		{"hypothesis", "show", ids.hypothesis},
		{"hypothesis", "show", ids.hypothesis, "--json"},
		{"finding", "show", ids.finding},
		{"finding", "show", ids.finding, "--json"},
	} {
		label := strings.Join(args, " ")
		t.Run(label, func(t *testing.T) {
			stdout, stderr := f.ok(args...)
			assertNoRawControls(t, label, stdout, stderr)
			for _, raw := range []string{"\x1b[31m", "\x1b]0;", "\x9b", "\u202e"} {
				if strings.Contains(stdout, raw) {
					t.Errorf("stdout carries %q raw:\n%q", raw, stdout)
				}
			}
		})
	}

	// Non-vacuity: the hostile note really did reach both surfaces, escaped
	// rather than dropped. An assertion that only proves the absence of
	// control bytes would also pass on a section that printed nothing.
	stdout, _ := f.ok("hypothesis", "show", ids.hypothesis)
	if !strings.Contains(stdout, `cited because \u{1B}[31mred`) {
		t.Errorf("the note was dropped rather than escaped:\n%s", stdout)
	}
	stdout, _ = f.ok("hypothesis", "show", ids.hypothesis, "--json")
	res := decodeJSON[hypothesisResult](t, stdout)
	if res.References == nil || !strings.Contains(res.References.Cites[2].Note, `\u{202E}`) {
		t.Errorf("the document carries an unescaped or missing note: %+v", res.References)
	}
}

// TestFindingShowCarriesItsBacklinks proves the second record command reads the
// other end of the same edge: the absorption that cites this finding is a
// backlink here and an outgoing citation on the candidate.
func TestFindingShowCarriesItsBacklinks(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()
	seedCitations(t, f.dataDir, ids)

	stdout, stderr := f.ok("finding", "show", ids.finding, "--json")
	res := decodeJSON[findingResult](t, stdout)
	if res.References == nil {
		t.Fatal("a build that records citations emitted no references field")
	}
	if want := (map[string]int{"evidence": 1}); !maps.Equal(res.References.Counts.CitedBy, want) {
		t.Errorf("backlink counts = %v, want %v", res.References.Counts.CitedBy, want)
	}
	if len(res.References.CitedBy) != 1 || res.References.CitedBy[0].OtherID != ids.hypothesis {
		t.Fatalf("backlinks = %+v, want the candidate's absorption", res.References.CitedBy)
	}
	// Nothing cites out of this finding, and that reads as an empty direction
	// rather than as a missing one: the finding was looked at.
	if len(res.References.Cites) != 0 || res.References.Counts.Cites != nil {
		t.Errorf("outgoing citations = %+v, want none", res.References.Cites)
	}
	if res.Finding.ID != ids.finding || len(res.Proposals) != 1 {
		t.Errorf("adding the citations changed the rest of the document: %+v", res)
	}
	assertNoRawControls(t, "finding show --json", stdout, stderr)
}

// TestCitationSectionStatesABuildWithNoReferenceGraph covers the degradation a
// render surface owes: a build with no edge store prints the section and says
// what it does not have, rather than printing an empty table that would read as
// "nothing cites this record".
func TestCitationSectionStatesABuildWithNoReferenceGraph(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refs    *citations
		readErr error
		want    string
		absent  string
	}{
		{
			name:   "no edge store at all",
			want:   noCitationGraph,
			absent: "cites:",
		},
		{
			name:    "a store that would not answer",
			readErr: errNoSessionKey,
			want:    "no deployment identity",
			absent:  "cites:",
		},
		{
			name: "a store with nothing recorded",
			refs: &citations{},
			want: "cites: none  cited by: none",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			a := &app{stdout: &stdout, stderr: &stderr}
			if err := a.writeCitations(tc.refs, tc.readErr); err != nil {
				t.Fatalf("writeCitations: %v", err)
			}
			out := stdout.String()
			if !strings.HasPrefix(out, "\nreferences\n") {
				t.Fatalf("the section lost its heading:\n%q", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("section = %q, want it to state %q", out, tc.want)
			}
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Errorf("an absent graph was reported as a counted one:\n%q", out)
			}
			if stderr.Len() != 0 {
				t.Errorf("the section wrote to stderr: %q", stderr.String())
			}
		})
	}
}

// TestSessionsInspectStatesWhyItNamesNoCitations is the same degradation
// reached through a real command. A session is named in the graph by the
// durable key SessionUID derives, so a local-mode install has no key to look
// up — and the command says so and succeeds, rather than failing or showing an
// empty table.
func TestSessionsInspectStatesWhyItNamesNoCitations(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()

	listing := decodeJSON[sessionsResult](t, mustStdout(t, f, "sessions", "list", "--json"))
	if len(listing.Sessions) == 0 {
		t.Fatal("the fixture listed no sessions")
	}
	selector := listing.Sessions[0].Selector

	stdout, stderr := f.ok("sessions", "inspect", selector)
	section, ok := citationSection(t, stdout)
	if !ok {
		t.Fatalf("no references section in:\n%s", stdout)
	}
	if !strings.Contains(section[0], "no deployment identity") {
		t.Errorf("the section does not name the reason: %q", section[0])
	}
	if strings.Contains(stdout, "cites:") {
		t.Errorf("a session with no derivable key was given a counts line:\n%s", stdout)
	}
	assertNoRawControls(t, "sessions inspect", stdout, stderr)

	// The document omits the field rather than carrying an empty object, and
	// the invocation stays silent on stderr: a machine with no deployment
	// identity is a permanent state, not a fault to warn about on every call.
	stdout, stderr = f.ok("sessions", "inspect", selector, "--json")
	if res := decodeJSON[inspectResult](t, stdout); res.References != nil {
		t.Errorf("references = %+v, want the field absent", res.References)
	}
	if stderr != "" {
		t.Errorf("sessions inspect --json wrote diagnostics: %q", stderr)
	}
}

// citationSection returns the lines of the references section below its
// heading, which is the last thing every record view writes.
func citationSection(t *testing.T, stdout string) ([]string, bool) {
	t.Helper()
	const heading = "\nreferences\n"
	at := strings.Index(stdout, heading)
	if at < 0 {
		return nil, false
	}
	body := strings.TrimRight(stdout[at+len(heading):], "\n")
	if body == "" {
		return nil, false
	}
	return strings.Split(body, "\n"), true
}
