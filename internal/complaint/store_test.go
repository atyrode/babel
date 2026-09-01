package complaint

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/sharedcatalog"
	"github.com/atyrode/babel/internal/sync"
)

// harness opens the complaint component in one temporary directory. There is no
// sibling store to compose with, which is the arrangement production uses and
// the point of the design: a complaint is free-standing, so nothing has to exist
// before the operator can say something.
type harness struct {
	t     *testing.T
	ctx   context.Context
	store *Store
	clock *clock
}

// clock advances a fixed step per read, so ordering assertions do not depend on
// two writes landing in different nanoseconds.
type clock struct{ at time.Time }

func (c *clock) now() time.Time {
	c.at = c.at.Add(time.Second)
	return c.at
}

func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	store, err := Open(t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("open complaints: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close complaints: %v", err)
		}
	})
	c := &clock{at: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)}
	store.now = c.now
	return &harness{t: t, ctx: context.Background(), store: store, clock: c}
}

func (h *harness) tell(text string) Complaint {
	h.t.Helper()
	told, err := h.store.Tell(h.ctx, TellInput{Text: text, By: "alex", Host: "workstation-linux"})
	if err != nil {
		h.t.Fatalf("tell %q: %v", text, err)
	}
	return told
}

// TestACapturedComplaintIsWhatTheOperatorSaid covers the one guarantee this
// package cannot compromise on.
//
// A complaint is the only record in the corpus whose author is a person and
// whose content nothing derived. Summarizing it, classifying it, or normalizing
// its wording would each replace what the operator said with Babel's reading of
// it, and the whole value of unprompted input is that it was not filtered
// through what Babel expected to hear.
func TestACapturedComplaintIsWhatTheOperatorSaid(t *testing.T) {
	h := newHarness(t)
	const said = "I am having a hard time enforcing my repository rules.\n" +
		"Every agent reads them and then does something else."

	told := h.tell("  " + said + "\n")

	if told.Text != said {
		t.Errorf("stored text = %q, want the operator's own words with only the surrounding whitespace gone", told.Text)
	}
	if told.By != "alex" || told.Host != "workstation-linux" {
		t.Errorf("attribution = (%q, %q), want the operator and the capture host", told.By, told.Host)
	}
	if told.RootID != told.ID || told.AncestorID != "" || told.Sequence != 1 {
		t.Errorf("chain = %+v, want a first wording that amends nothing", told)
	}
	if told.Redacted {
		t.Error("a complaint with no secret in it was reported as redacted")
	}

	read, err := h.store.Complaint(h.ctx, told.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !reflect.DeepEqual(read, told) {
		t.Errorf("read back %+v, want %+v", read, told)
	}
}

// TestAComplaintHasNoLifecycleToReport is #115's charter guard as an assertion.
//
// The moment a complaint acquires a resolved state, Babel has become a ticket
// queue and GitHub already is one. The guard cannot live in a comment, because a
// later change that adds a status column would leave the comment true-sounding
// and the design gone; so this reads the durable schema and refuses any column
// that could hold one.
func TestAComplaintHasNoLifecycleToReport(t *testing.T) {
	h := newHarness(t)
	h.tell("the release notes keep claiming tests ran")

	rows, err := h.store.db.QueryContext(h.ctx, `SELECT name FROM pragma_table_info('complaint')`)
	if err != nil {
		t.Fatalf("read the complaint schema: %v", err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the complaint schema: %v", err)
	}

	forbidden := []string{"status", "state", "resolved", "resolution", "closed",
		"closed_at", "assignee", "assigned_to", "priority", "severity", "due", "due_at"}
	for _, column := range columns {
		for _, banned := range forbidden {
			if column == banned {
				t.Errorf("the complaint table carries a %q column: a complaint is steering pressure, "+
					"not a work item, and a lifecycle column is how that stops being true (#115)", column)
			}
		}
	}
	if len(columns) == 0 {
		t.Fatal("no columns read, so this guard proved nothing")
	}
}

// TestAmendingAppendsAndNeverOverwrites covers the one mutation a complaint
// admits.
//
// The operator may say it better later, and the earlier wording is history
// rather than error: a record that replaced its predecessor would discard the
// sentence somebody already cited, which is the same argument #87 makes for
// every other revision chain. Only the head may be amended, because two current
// wordings of one complaint would be two answers to what the operator says.
func TestAmendingAppendsAndNeverOverwrites(t *testing.T) {
	h := newHarness(t)
	first := h.tell("the rules get ignored")

	second, err := h.store.Amend(h.ctx, AmendInput{
		ComplaintID: first.ID,
		Text:        "the repository rules get ignored by every agent, not just one",
		By:          "alex", Host: "workstation-linux",
	})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if second.RootID != first.RootID || second.AncestorID != first.ID || second.Sequence != 2 {
		t.Errorf("amendment = %+v, want the second wording of %s", second, first.RootID)
	}

	// The earlier wording is still readable, and still says what it said.
	kept, err := h.store.Complaint(h.ctx, first.ID)
	if err != nil {
		t.Fatalf("read the amended wording: %v", err)
	}
	if kept.Text != first.Text {
		t.Errorf("the first wording now reads %q, want %q", kept.Text, first.Text)
	}

	chain, err := h.store.Revisions(h.ctx, second.ID)
	if err != nil {
		t.Fatalf("read the chain: %v", err)
	}
	if len(chain) != 2 || chain[0].ID != first.ID || chain[1].ID != second.ID {
		t.Fatalf("chain = %+v, want both wordings oldest first", chain)
	}
	// Asking from either end reads the same chain: an operator holding an id
	// out of a citation must not have to find the root first.
	fromRoot, err := h.store.Revisions(h.ctx, first.ID)
	if err != nil {
		t.Fatalf("read the chain from its root: %v", err)
	}
	if !reflect.DeepEqual(fromRoot, chain) {
		t.Errorf("reading from the root gave %+v, want %+v", fromRoot, chain)
	}

	if _, err := h.store.Amend(h.ctx, AmendInput{
		ComplaintID: first.ID, Text: "a competing second wording",
		By: "alex", Host: "workstation-linux",
	}); !errors.Is(err, ErrSuperseded) {
		t.Errorf("amending a superseded wording = %v, want ErrSuperseded", err)
	}
}

// TestHeadsAreWhatTheOperatorCurrentlySays covers the retrieval surface's
// membership rule.
//
// An amended sentence is not what the operator says now, and indexing both
// wordings would make one complaint match twice and read as two independent
// grievances - the same reason internal/frontier offers only head revisions.
func TestHeadsAreWhatTheOperatorCurrentlySays(t *testing.T) {
	h := newHarness(t)
	stale := h.tell("the deploy is flaky")
	other := h.tell("the review queue is unreadable on a phone")
	fresh, err := h.store.Amend(h.ctx, AmendInput{
		ComplaintID: stale.ID, Text: "the deploy is flaky on the second attempt only",
		By: "alex", Host: "workstation-linux",
	})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}

	heads, err := h.store.Heads(h.ctx)
	if err != nil {
		t.Fatalf("read heads: %v", err)
	}
	got := make([]string, 0, len(heads))
	for _, head := range heads {
		got = append(got, head.ID)
	}
	// Newest first: the last thing the operator said is the first thing a
	// listing shows.
	want := []string{fresh.ID, other.ID}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("heads = %v, want %v", got, want)
	}

	outputs, err := h.store.Outputs(h.ctx)
	if err != nil {
		t.Fatalf("flatten for retrieval: %v", err)
	}
	if len(outputs) != 2 {
		t.Fatalf("flattened %d outputs, want the two heads", len(outputs))
	}
	first := outputs[0]
	switch {
	case first.Kind != frontier.OutputComplaint:
		t.Errorf("kind = %q, want %q", first.Kind, frontier.OutputComplaint)
	case first.ID != fresh.ID || first.RootID != stale.ID:
		t.Errorf("identity = (%q, %q), want the head under the chain's root", first.ID, first.RootID)
	case first.Text != fresh.Text:
		t.Errorf("indexed text = %q, want the wording itself", first.Text)
	case first.Summary == "":
		t.Error("the head flattened to no summary, so a listing has no line to show")
	}
	// The three fields a complaint has nothing to put in stay empty, which is
	// the charter guard again: no subject, no producing run, no status.
	if first.Subject != (frontier.Ref{}) || first.RunID != "" || first.Status != "" {
		t.Errorf("flattened output carries %+v; a complaint answers about no record, "+
			"was produced by no run, and has no lifecycle state", first)
	}
}

// TestAppendIsSafeOnADeploymentWithNoComplaintStore covers the seam both index
// reconcilers call.
//
// IndexFrontier deletes the local rows the set it is given does not name, so the
// set has to be assembled the same way at every call site. A nil store
// contributing nothing rather than erroring is what lets a caller that opened no
// complaint component reconcile exactly as it did before this package existed.
func TestAppendIsSafeOnADeploymentWithNoComplaintStore(t *testing.T) {
	ctx := context.Background()
	existing := []frontier.Output{{Kind: frontier.OutputFinding, ID: "fnd_1"}}

	got, err := Append(ctx, nil, existing)
	if err != nil {
		t.Fatalf("Append with no store: %v", err)
	}
	if !reflect.DeepEqual(got, existing) {
		t.Errorf("Append with no store = %+v, want the input unchanged", got)
	}

	h := newHarness(t)
	told := h.tell("the archive push is slower than it was")
	got, err = Append(ctx, h.store, existing)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(got) != 2 || got[0].ID != "fnd_1" || got[1].ID != told.ID {
		t.Errorf("Append = %+v, want the frontier's output and then the complaint", got)
	}
}

// TestCaptureRedactsSecretsAndSaysSo covers the preflight #115 requires on
// capture.
//
// Operators paste secrets into text boxes. This text becomes an immutable sealed
// object and a row in a retrieval index, and neither can be un-said, so
// redaction runs before anything is stored rather than on the way out. The flag
// travels with the record because the stored text then visibly differs from what
// was typed, and a reader who could not tell would attribute Babel's placeholder
// to the operator.
func TestCaptureRedactsSecretsAndSaysSo(t *testing.T) {
	h := newHarness(t)
	// Assembled rather than written whole: a repository with push protection
	// must not contain a contiguous credential-shaped literal.
	secret := "ghp_" + "synthetic000000000000000000000000"

	told, err := h.store.Tell(h.ctx, TellInput{
		Text: "the agent keeps pasting my token " + secret + " into its plans",
		By:   "alex", Host: "workstation-linux",
	})
	if err != nil {
		t.Fatalf("tell: %v", err)
	}
	if strings.Contains(told.Text, secret) {
		t.Error("the stored complaint carries the secret verbatim")
	}
	if !told.Redacted {
		t.Error("redaction happened and the record does not say so")
	}
	if !strings.Contains(told.Text, "keeps pasting my token") {
		t.Errorf("stored text = %q, want everything but the secret kept", told.Text)
	}
}

// TestCaptureRefusesWhatItCannotAttributeOrStore covers the three refusals, and
// nothing else is refused on purpose.
//
// A complaint that had to be well-formed to be heard would be a form, and #115
// is explicit that this is not one: there is no validation of what it says, no
// required subject and no required record. What is refused is a complaint with no
// words, one nobody wrote, and one too large to seal - each of which is a fact
// about the capture rather than a judgement of the content.
func TestCaptureRefusesWhatItCannotAttributeOrStore(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		in   TellInput
	}{
		{"no words", TellInput{Text: "   \n\t ", By: "alex", Host: "h1"}},
		{"no operator", TellInput{Text: "something is wrong", Host: "h1"}},
		{"no capture host", TellInput{Text: "something is wrong", By: "alex"}},
		{"past the bound", TellInput{
			Text: strings.Repeat("x", MaxTextBytes+1), By: "alex", Host: "h1",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.store.Tell(h.ctx, tc.in); !errors.Is(err, ErrInvalidValue) {
				t.Errorf("Tell = %v, want ErrInvalidValue", err)
			}
		})
	}

	// And the case that must NOT be refused: a complaint naming nothing, about
	// nothing, on a machine that has analysed nothing.
	if _, err := h.store.Tell(h.ctx, TellInput{
		Text: "this is all a bit much", By: "alex", Host: "h1",
	}); err != nil {
		t.Errorf("a free-standing complaint was refused: %v", err)
	}
}

// TestTheDurableRowRefusesToBeEditedOrDeleted covers the append-only discipline
// at the level that survives a future writer.
//
// A record whose immutability depends on nobody writing the wrong statement is
// not immutable. An operator's own sentence is the last thing that should be
// silently editable, and #115 has no delete at all: an unaddressed complaint is
// information, so removing it destroys the only evidence that nothing happened.
func TestTheDurableRowRefusesToBeEditedOrDeleted(t *testing.T) {
	h := newHarness(t)
	told := h.tell("the same mistake keeps coming back")

	if _, err := h.store.db.ExecContext(h.ctx,
		`UPDATE complaint SET operator_id = 'someone else' WHERE id = ?`, told.ID); err == nil {
		t.Error("a complaint was re-attributed by an UPDATE")
	}
	if _, err := h.store.db.ExecContext(h.ctx,
		`DELETE FROM complaint WHERE id = ?`, told.ID); err == nil {
		t.Error("a complaint was deleted")
	}
	if _, err := h.store.Complaint(h.ctx, told.ID); err != nil {
		t.Errorf("the complaint did not survive the attempts: %v", err)
	}
}

// TestAnUnknownComplaintIsNamedAsAWrongReference covers the sentinel the
// resolver registry reads.
//
// Nothing here is ever deleted, so a reference this store cannot find was always
// wrong. That distinction is what keeps #113's anchoring gate honest: absence
// means the citation was hallucinated, and any other failure means this machine
// could not check - which must never be reported as the same thing.
func TestAnUnknownComplaintIsNamedAsAWrongReference(t *testing.T) {
	h := newHarness(t)
	_, err := h.store.Complaint(h.ctx, "cmp_0000000000000000")
	if !errors.Is(err, ErrUnknownComplaint) {
		t.Fatalf("unknown id = %v, want ErrUnknownComplaint", err)
	}
	if !strings.Contains(err.Error(), "cmp_0000000000000000") {
		t.Errorf("the refusal does not name the reference: %s", err)
	}
}

// recordingAppender collects the edges a write path minted.
type recordingAppender struct {
	edges []reference.Edge
	err   error
}

func (r *recordingAppender) Append(ctx context.Context, e reference.Edge) (reference.Edge, error) {
	if r.err != nil {
		return reference.Edge{}, r.err
	}
	r.edges = append(r.edges, e)
	return e, nil
}

// TestEdgesShadowTheChainAndTheOperatorsAim covers both link forms this package
// emits, and the direction each carries.
//
// A supersedes edge is the amendment chain's graph-visible shadow. An addresses
// edge minted here is the operator's own aim - "this is what I am complaining
// about" - and it points away from the complaint, because the edge that answers
// "was this addressed?" is the opposite one: a hypothesis or proposal citing the
// complaint. Nothing the operator wrote can be evidence that Babel acted.
func TestEdgesShadowTheChainAndTheOperatorsAim(t *testing.T) {
	appender := &recordingAppender{}
	h := newHarness(t, WithReferences(appender, func(error) {}))

	told, err := h.store.Tell(h.ctx, TellInput{
		Text: "the handoff drops the constraints every time", By: "alex", Host: "h1",
		Addresses: []frontier.Ref{
			{Type: frontier.EntityHypothesis, ID: "hyp_1"},
			// Named twice: one aim stated twice is one edge.
			{Type: frontier.EntityHypothesis, ID: "hyp_1"},
			// Named with nothing in it: skipped, never fatal.
			{Type: frontier.EntityFinding, ID: ""},
		},
	})
	if err != nil {
		t.Fatalf("tell: %v", err)
	}
	if len(appender.edges) != 1 {
		t.Fatalf("minted %d edges, want one addresses edge: %+v", len(appender.edges), appender.edges)
	}
	aim := appender.edges[0]
	switch {
	case aim.Kind != reference.KindAddresses:
		t.Errorf("kind = %q, want %q", aim.Kind, reference.KindAddresses)
	case aim.From != (reference.RecordRef{Kind: Namespace, ID: told.ID}):
		t.Errorf("from = %+v, want the complaint", aim.From)
	case aim.To != (reference.RecordRef{Kind: "hypothesis", ID: "hyp_1"}):
		t.Errorf("to = %+v, want the record the operator named", aim.To)
	case aim.ActorKind != reference.ActorOperator || aim.ActorRef != "alex":
		t.Errorf("actor = (%q, %q), want the operator", aim.ActorKind, aim.ActorRef)
	}

	appender.edges = nil
	amended, err := h.store.Amend(h.ctx, AmendInput{
		ComplaintID: told.ID, Text: "the handoff drops the constraints on every retry",
		By: "alex", Host: "h1",
	})
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	if len(appender.edges) != 1 || appender.edges[0].Kind != reference.KindSupersedes {
		t.Fatalf("minted %+v, want one supersedes edge", appender.edges)
	}
	chain := appender.edges[0]
	if chain.From.ID != amended.ID || chain.To.ID != told.ID {
		t.Errorf("supersedes %s -> %s, want %s -> %s",
			chain.From.ID, chain.To.ID, amended.ID, told.ID)
	}
	// An amendment does NOT inherit its predecessor's aim: an edge is an
	// assertion the operator made about a particular record, and copying it
	// forward would be this package asserting it on their behalf.
	if len(appender.edges) != 1 {
		t.Errorf("the amendment minted %+v; the aim is not inherited", appender.edges)
	}
}

// TestACapturedComplaintSurvivesARefusedEdge covers the emission contract's
// failure side.
//
// The complaint is the thing that mattered. An operator told that their sentence
// was refused because a citation would not bind is an operator who stops telling
// Babel things, so a refused edge is a warning and the record stays durable -
// which the next append of the same triple repairs, because Append is idempotent
// on (kind, from, to) by contract.
func TestACapturedComplaintSurvivesARefusedEdge(t *testing.T) {
	refused := errors.New("no resolver vouches for that record")
	appender := &recordingAppender{err: refused}
	var warned []error
	h := newHarness(t, WithReferences(appender, func(err error) { warned = append(warned, err) }))

	told, err := h.store.Tell(h.ctx, TellInput{
		Text: "the queue keeps re-proposing what I declined", By: "alex", Host: "h1",
		Addresses: []frontier.Ref{{Type: frontier.EntityHypothesis, ID: "hyp_gone"}},
	})
	if err != nil {
		t.Fatalf("a refused edge failed the capture: %v", err)
	}
	if _, err := h.store.Complaint(h.ctx, told.ID); err != nil {
		t.Errorf("the complaint is not durable: %v", err)
	}
	if len(warned) != 1 || !errors.Is(warned[0], refused) {
		t.Errorf("warnings = %v, want the refusal reported once", warned)
	}
}

// fakeHook is a sync.Hook that records what the write path staged. It stands in
// for a Publisher because a real one holds a PostgreSQL handle, an encrypted
// object store and a payload keyring, and none of the three is needed to prove
// what this package stages, under which closure, and when it asks for a commit.
type fakeHook struct {
	t *testing.T

	staged     []sync.Record
	producedBy []string
	committed  []sync.Closure

	publish   bool
	appendErr error
}

func (h *fakeHook) Append(ctx context.Context, tx *sql.Tx, producedBy string, rec sync.Record) (sync.Closure, bool, error) {
	h.t.Helper()
	if tx == nil {
		h.t.Errorf("record %s was staged outside the writer's transaction", rec.EntityID)
	}
	if h.appendErr != nil {
		return sync.Closure{}, false, h.appendErr
	}
	h.staged = append(h.staged, rec)
	h.producedBy = append(h.producedBy, producedBy)
	return sync.Closure{RunID: rec.EntityID}, h.publish, nil
}

func (h *fakeHook) StageTx(ctx context.Context, tx *sql.Tx, rec sync.Record) error {
	h.t.Errorf("StageTx was called for %s; this package stages through Append", rec.EntityID)
	return nil
}

func (h *fakeHook) DeclareTx(ctx context.Context, tx *sql.Tx, c sync.Closure) error {
	h.t.Errorf("DeclareTx was called for %s; Append declares a closure of one itself", c.RunID)
	return nil
}

func (h *fakeHook) CommitInline(ctx context.Context, c sync.Closure) error {
	h.committed = append(h.committed, c)
	return nil
}

// TestAComplaintPublishesAsItsOwnClosure covers the publication shape and the
// closure rule.
//
// No run produced a complaint, and the runs that later answer it did not exist
// when the operator wrote it, so it is its own closure of one and publishes as
// soon as its transaction commits. Attaching it to a run would try to join a
// closure that run declared when it ended, and migration 0003 fixes a closure's
// record count at declaration and never lets it move.
func TestAComplaintPublishesAsItsOwnClosure(t *testing.T) {
	hook := &fakeHook{t: t, publish: true}
	h := newHarness(t, WithSync(hook))

	told := h.tell("the same three files get rewritten every session")

	if len(hook.staged) != 1 {
		t.Fatalf("staged %d records, want one", len(hook.staged))
	}
	rec := hook.staged[0]
	switch {
	case rec.Kind != sharedcatalog.KindComplaint:
		t.Errorf("kind = %q, want %q", rec.Kind, sharedcatalog.KindComplaint)
	case rec.EntityID != told.ID:
		t.Errorf("entity id = %q, want %q", rec.EntityID, told.ID)
	case rec.Schema != RecordSchema:
		t.Errorf("schema = %d, want %d", rec.Schema, RecordSchema)
	case hook.producedBy[0] != "":
		t.Errorf("produced by %q, want no producing run", hook.producedBy[0])
	}
	if len(hook.committed) != 1 || hook.committed[0].RunID != told.ID {
		t.Errorf("committed %+v, want the complaint's own closure", hook.committed)
	}

	var published publishedComplaint
	if err := json.Unmarshal(rec.Payload, &published); err != nil {
		t.Fatalf("decode the published bytes: %v", err)
	}
	switch {
	case published.Text != told.Text:
		t.Errorf("published text = %q, want the operator's words", published.Text)
	case published.OperatorID != told.By || published.HostID != told.Host:
		t.Errorf("published attribution = (%q, %q)", published.OperatorID, published.HostID)
	case published.RootID != told.RootID || published.Sequence != 1:
		t.Errorf("published chain = (%q, %d)", published.RootID, published.Sequence)
	}
	// The plaintext projection stays empty: a complaint has no edge shape of
	// its own, and its aims publish as edges through internal/reference.
	if rec.Edge != nil {
		t.Errorf("staged an edge projection %+v; a complaint is not an edge", rec.Edge)
	}

	// Nothing the shared catalog could read as a lifecycle travels either.
	var raw map[string]any
	if err := json.Unmarshal(rec.Payload, &raw); err != nil {
		t.Fatalf("decode the published bytes as an object: %v", err)
	}
	for _, banned := range []string{"status", "state", "resolved", "closed", "assignee", "priority"} {
		if _, present := raw[banned]; present {
			t.Errorf("the published complaint carries %q; steering pressure has no lifecycle (#115)", banned)
		}
	}
}

// TestAStagingFailureRollsTheCaptureBack covers the atomicity the shared
// transaction buys.
//
// A complaint that committed locally while its journal row did not would be
// durable, invisible to the publisher, and reported by nothing: steering this
// machine believes it captured and the fleet will never hear about. A refused
// write is visible and that silence is not, so the refusal is the better
// outcome.
func TestAStagingFailureRollsTheCaptureBack(t *testing.T) {
	refused := errors.New("staging refused")
	hook := &fakeHook{t: t, appendErr: refused}
	h := newHarness(t, WithSync(hook))

	_, err := h.store.Tell(h.ctx, TellInput{
		Text: "this should not survive", By: "alex", Host: "h1",
	})
	if !errors.Is(err, refused) {
		t.Fatalf("Tell = %v, want the staging refusal", err)
	}
	heads, err := h.store.Heads(h.ctx)
	if err != nil {
		t.Fatalf("read heads: %v", err)
	}
	if len(heads) != 0 {
		t.Errorf("the durable row survived a refused staging: %+v", heads)
	}
}

// TestAMalformedPublicationIsRefusedBeforeItIsSealed covers why validation lives
// in the marshaller.
//
// These bytes become an immutable, sealed object in the shared catalog, and a
// malformed one cannot be corrected there - analysis_records is insert-only - so
// the only place a refusal costs nothing is before the transaction that stages
// it.
func TestAMalformedPublicationIsRefusedBeforeItIsSealed(t *testing.T) {
	valid := publishedComplaint{
		ID: "cmp_1", RootID: "cmp_1", Sequence: 1,
		OperatorID: "alex", HostID: "h1", Text: "something", CreatedAt: "2026-08-31T12:00:00.000000000Z",
	}
	if _, err := json.Marshal(valid); err != nil {
		t.Fatalf("a well-formed complaint was refused: %v", err)
	}

	cases := map[string]func(*publishedComplaint){
		"no id":                  func(p *publishedComplaint) { p.ID = "" },
		"no chain":               func(p *publishedComplaint) { p.RootID = "" },
		"unattributed":           func(p *publishedComplaint) { p.OperatorID = "" },
		"no capture host":        func(p *publishedComplaint) { p.HostID = "" },
		"no words":               func(p *publishedComplaint) { p.Text = "" },
		"no capture time":        func(p *publishedComplaint) { p.CreatedAt = "" },
		"first wording amending": func(p *publishedComplaint) { p.AncestorID = "cmp_0" },
		"amendment of nothing":   func(p *publishedComplaint) { p.Sequence = 2 },
		"off the chain":          func(p *publishedComplaint) { p.Sequence = 0 },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			broken := valid
			break_(&broken)
			if _, err := json.Marshal(broken); err == nil {
				t.Error("a complaint no reader could place was encoded anyway")
			}
		})
	}
}
