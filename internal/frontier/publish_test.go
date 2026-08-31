package frontier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// What this file defends is the property publish.go exists for: a record that
// is durable on this machine is, in the same instant, known to be owed to the
// fleet - and a record that is not durable was never staged.
//
// The hook is a fake rather than a real Publisher on purpose. A real one needs
// PostgreSQL, an object store and a keyring, and none of those would make these
// assertions stronger: what is being measured here is which records this store
// stages, under which run, with which kind and which wire shape. The protocol
// itself is measured in internal/sync's own suite, against a real database.

// recordingHook records what a store stages and publishes.
type recordingHook struct {
	staged   []babelsync.Record
	runs     []string
	closures []babelsync.Closure
	declared []babelsync.Closure

	// appendErr, when set, fails the staging call. It is how the atomicity
	// property is measured: a staging failure must take the durable write down
	// with it, because the alternative is a record nothing will ever publish.
	appendErr error
	// commitErr, when set, fails the post-commit publication.
	commitErr error
	// openRuns names the runs whose closures the fake reports as still open,
	// which is what internal/sync's Append decides from the journal.
	openRuns map[string]bool
}

func newRecordingHook() *recordingHook {
	return &recordingHook{openRuns: map[string]bool{}}
}

func (h *recordingHook) Append(ctx context.Context, tx *sql.Tx, producedBy string, rec babelsync.Record) (babelsync.Closure, bool, error) {
	if h.appendErr != nil {
		return babelsync.Closure{}, false, h.appendErr
	}
	h.runs = append(h.runs, producedBy)
	if producedBy != "" && h.openRuns[producedBy] {
		rec.RunID = producedBy
		h.staged = append(h.staged, rec)
		return babelsync.Closure{}, false, nil
	}
	rec.RunID = rec.EntityID
	h.staged = append(h.staged, rec)
	c := babelsync.Closure{RunID: rec.EntityID, ContinuesRunID: producedBy}
	h.declared = append(h.declared, c)
	return c, true, nil
}

func (h *recordingHook) StageTx(ctx context.Context, tx *sql.Tx, rec babelsync.Record) error {
	if h.appendErr != nil {
		return h.appendErr
	}
	h.staged = append(h.staged, rec)
	return nil
}

func (h *recordingHook) DeclareTx(ctx context.Context, tx *sql.Tx, c babelsync.Closure) error {
	if h.appendErr != nil {
		return h.appendErr
	}
	h.declared = append(h.declared, c)
	return nil
}

func (h *recordingHook) CommitInline(ctx context.Context, c babelsync.Closure) error {
	h.closures = append(h.closures, c)
	return h.commitErr
}

// only returns the single staged record, failing the test when there is not
// exactly one: every assertion below is about one write.
func (h *recordingHook) only(t *testing.T) babelsync.Record {
	t.Helper()
	if len(h.staged) != 1 {
		t.Fatalf("staged %d records, want 1: %+v", len(h.staged), h.staged)
	}
	return h.staged[0]
}

// wire decodes a staged record's payload back through the wire form, which is
// also a round-trip assertion: a record this store stages must be one an
// ingesting host can decode.
func wire(t *testing.T, rec babelsync.Record) PublishedRecord {
	t.Helper()
	decoded, err := DecodePublishedRecord(rec.Payload)
	if err != nil {
		t.Fatalf("decode staged payload for %s: %v", rec.EntityID, err)
	}
	return decoded
}

func openStoreWithHook(t *testing.T, h *recordingHook) *Store {
	t.Helper()
	store, err := Open(t.TempDir(), WithSync(h))
	if err != nil {
		t.Fatalf("open frontier with a publication hook: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close frontier: %v", err)
		}
	})
	return store
}

func TestHypothesisStagesIntoItsRunsClosure(t *testing.T) {
	h := newRecordingHook()
	h.openRuns["run-1"] = true
	s := openStoreWithHook(t, h)
	ctx := context.Background()

	record, err := s.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Actor:   Actor{Kind: ActorRun, ID: "run-1"},
		Payload: HypothesisPayload{Statement: "the release pipeline retries on a stale lock"},
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}

	staged := h.only(t)
	if staged.EntityID != record.ID {
		t.Errorf("staged entity %q, want %q", staged.EntityID, record.ID)
	}
	if staged.Kind != sharedcatalog.KindHypothesis {
		t.Errorf("staged kind %q, want %q", staged.Kind, sharedcatalog.KindHypothesis)
	}
	if staged.Schema != RecordSchema {
		t.Errorf("staged schema %d, want %d", staged.Schema, RecordSchema)
	}
	if len(h.runs) != 1 || h.runs[0] != "run-1" {
		t.Errorf("staged under runs %v, want [run-1]", h.runs)
	}

	// A record produced by a run whose closure is still open publishes nothing
	// yet: the run declares its closure when it ends, because 0003 fixes
	// record_count at declaration and a closure may not be declared while it
	// can still grow.
	if len(h.closures) != 0 {
		t.Errorf("an in-run record published immediately: %+v", h.closures)
	}

	got := wire(t, staged)
	if got.Kind != PublishedHypothesis || got.ID != record.ID {
		t.Errorf("wire form = %+v", got)
	}
	if got.RootID != record.ID {
		t.Errorf("wire root id = %q, want the record's own id for a first revision", got.RootID)
	}
	if got.Status != StatusUntriaged {
		t.Errorf("wire status = %q, want %q", got.Status, StatusUntriaged)
	}
	if got.Ancestor != "" {
		t.Errorf("a first revision carries ancestor %q", got.Ancestor)
	}

	// The wire form carries what no plaintext column holds and nothing that a
	// reader derives. Both halves matter: root id and status are unavailable
	// from 0003's columns, and a shipped summary would be a second answer to a
	// question describePayload already answers.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(staged.Payload, &raw); err != nil {
		t.Fatalf("decode staged payload as an object: %v", err)
	}
	for _, required := range []string{"root_id", "status", "payload", "created_at"} {
		if _, ok := raw[required]; !ok {
			t.Errorf("wire form omits %q", required)
		}
	}
	for _, derived := range []string{"summary", "text"} {
		if _, ok := raw[derived]; ok {
			t.Errorf("wire form carries the derived field %q", derived)
		}
	}
}

// A revision's wire form carries the chain, and it carries it from the revision
// row this store just wrote rather than from a second walk of the ancestor
// column.
func TestRevisionStagesItsChain(t *testing.T) {
	h := newRecordingHook()
	h.openRuns["run-1"] = true
	s := openStoreWithHook(t, h)
	ctx := context.Background()

	first, err := s.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Actor:   Actor{Kind: ActorRun, ID: "run-1"},
		Payload: HypothesisPayload{Statement: "the first wording"},
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	h.staged = nil

	second, err := s.CreateHypothesis(ctx, HypothesisInput{
		RunID:      "run-1",
		AncestorID: first.ID,
		Reason:     "sharper",
		Actor:      Actor{Kind: ActorRun, ID: "run-1"},
		Payload:    HypothesisPayload{Statement: "the second wording"},
	})
	if err != nil {
		t.Fatalf("revise hypothesis: %v", err)
	}

	got := wire(t, h.only(t))
	if got.ID != second.ID {
		t.Fatalf("staged %q, want %q", got.ID, second.ID)
	}
	if got.RootID != first.ID {
		t.Errorf("wire root id = %q, want the chain root %q", got.RootID, first.ID)
	}
	if got.Ancestor != first.ID {
		t.Errorf("wire ancestor = %q, want %q", got.Ancestor, first.ID)
	}
}

// A link's whole meaning is outside its payload, so its endpoints and its type
// travel on the wire. Without them a reader would know that something was
// asserted about the corpus and not what.
func TestLinkStagesItsEndpoints(t *testing.T) {
	h := newRecordingHook()
	h.openRuns["run-1"] = true
	s := openStoreWithHook(t, h)
	ctx := context.Background()

	from := mustHypothesis(t, s, "run-1", "the cache is cold on deploy")
	to := mustHypothesis(t, s, "run-1", "the deploy warms the cache")
	h.staged = nil
	h.closures = nil

	link, err := s.Link(ctx, LinkInput{
		FromID: from.ID, ToID: to.ID, Type: LinkContradicts, Note: "one of these is wrong",
	})
	if err != nil {
		t.Fatalf("link: %v", err)
	}

	staged := h.only(t)
	if staged.Kind != sharedcatalog.KindLink {
		t.Errorf("staged kind %q, want %q", staged.Kind, sharedcatalog.KindLink)
	}
	got := wire(t, staged)
	if got.Edge == nil {
		t.Fatal("a link's wire form carries no endpoints")
	}
	if got.Edge.FromID != from.ID || got.Edge.ToID != to.ID || got.Edge.Type != LinkContradicts {
		t.Errorf("wire edge = %+v, want %s -> %s as %s", got.Edge, from.ID, to.ID, LinkContradicts)
	}

	// A link names no run, so it is its own commit of one and publishes at once.
	if len(h.closures) != 1 || h.closures[0].RunID != link.ID {
		t.Errorf("published closures = %+v, want one for %s", h.closures, link.ID)
	}

	// It has no searchable view, and that refusal belongs to the read side
	// rather than to publication.
	if _, err := got.Output(); !errors.Is(err, ErrNotSearchable) {
		t.Errorf("link Output() = %v, want ErrNotSearchable", err)
	}
}

// A proposal publishes and is deliberately unsearchable, which is the case that
// proved the publication vocabulary had to be wider than the search vocabulary.
func TestProposalPublishesButIsNotSearchable(t *testing.T) {
	h := newRecordingHook()
	h.openRuns["run-1"] = true
	s := openStoreWithHook(t, h)
	ctx := context.Background()
	hypothesis := mustHypothesis(t, s, "run-1", "the queue drops messages under load")
	evidence, err := NewEvidence(syntheticLocator(12), "the log line")
	if err != nil {
		t.Fatalf("build evidence: %v", err)
	}
	observation, err := s.CreateObservation(ctx, ObservationInput{
		HypothesisID: hypothesis.ID,
		RunID:        "run-1",
		RecipeID:     "recipe-a",
		Actor:        Actor{Kind: ActorRun, ID: "run-1"},
		Payload: ObservationPayload{
			Claim:                 "the queue dropped two messages",
			Confidence:            ConfidenceModerate,
			Impact:                ImpactModerate,
			Evidence:              []Evidence{evidence},
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		t.Fatalf("create observation: %v", err)
	}
	finding, err := s.CreateFinding(ctx, FindingInput{
		RunID:          "run-1",
		ObservationIDs: []string{observation.ID},
		Actor:          Actor{Kind: ActorRun, ID: "run-1"},
		Payload: FindingPayload{
			Title:                 "the queue drops under load",
			Pattern:               "messages vanish above a threshold",
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		t.Fatalf("create finding: %v", err)
	}
	h.staged = nil

	proposal, err := s.CreateProposal(ctx, ProposalInput{
		RunID:      "run-1",
		FindingIDs: []string{finding.ID},
		Actor:      Actor{Kind: ActorRun, ID: "run-1"},
		Payload: ProposalPayload{
			Title:          "bound the queue",
			Problem:        "unbounded growth drops messages",
			Outcome:        "no message is dropped",
			Impact:         ImpactModerate,
			Classification: ClassificationPrivate,
		},
	})
	if err != nil {
		t.Fatalf("create proposal: %v", err)
	}

	staged := h.only(t)
	if staged.Kind != sharedcatalog.KindProposal {
		t.Errorf("staged kind %q, want %q", staged.Kind, sharedcatalog.KindProposal)
	}
	got := wire(t, staged)
	if got.ID != proposal.ID || got.Kind != PublishedProposal {
		t.Errorf("wire form = %+v", got)
	}
	if _, err := got.Output(); !errors.Is(err, ErrNotSearchable) {
		t.Errorf("proposal Output() = %v, want ErrNotSearchable", err)
	}
}

// An operator's decision names no producing run: it is its own commit of one,
// not part of the closure of whichever run produced the record it is about -
// a closure that run has already declared and closed.
func TestDecisionIsItsOwnClosure(t *testing.T) {
	h := newRecordingHook()
	h.openRuns["run-1"] = true
	s := openStoreWithHook(t, h)
	ctx := context.Background()

	hypothesis := mustHypothesis(t, s, "run-1", "the retry storm is self-inflicted")
	h.staged = nil
	h.runs = nil
	h.closures = nil

	event, err := s.Decide(ctx, DispositionInput{
		Subject:     Ref{Type: EntityHypothesis, ID: hypothesis.ID},
		Disposition: DispositionAccept,
		ReviewerID:  "operator-a",
		Note:        "worth pursuing",
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}

	staged := h.only(t)
	if staged.Kind != sharedcatalog.KindDisposition {
		t.Errorf("staged kind %q, want %q", staged.Kind, sharedcatalog.KindDisposition)
	}
	if len(h.runs) != 1 || h.runs[0] != "" {
		t.Errorf("a decision was staged under run %v, want no producing run", h.runs)
	}
	if len(h.closures) != 1 || h.closures[0].RunID != event.ID {
		t.Errorf("published closures = %+v, want one for %s", h.closures, event.ID)
	}
	got := wire(t, staged)
	if got.Answer == nil || got.Answer.Decision != DispositionAccept || got.Answer.Reviewer != "operator-a" {
		t.Errorf("wire answer = %+v", got.Answer)
	}
	if got.Subject.ID != hypothesis.ID || got.Subject.Type != EntityHypothesis {
		t.Errorf("wire subject = %+v", got.Subject)
	}
}

// §4.7's reject-and-refine is one atomic operation, and it has to stay atomic
// across the boundary this store cannot reach on its own: two independent
// commits could leave the fleet holding a rejection that authorized nothing, or
// a refinement no recorded rejection authorizes. One closure of two is what
// makes the pair atomic remotely, because a run row is the visibility boundary.
func TestRejectAndRefinePublishesAsOneClosure(t *testing.T) {
	h := newRecordingHook()
	h.openRuns["run-1"] = true
	s := openStoreWithHook(t, h)
	ctx := context.Background()

	hypothesis := mustHypothesis(t, s, "run-1", "the deploy order is wrong")
	h.staged = nil
	h.declared = nil
	h.closures = nil

	rejection, request, err := s.RejectAndRefine(ctx, DispositionInput{
		Subject:    Ref{Type: EntityHypothesis, ID: hypothesis.ID},
		ReviewerID: "operator-a",
		Note:       "not as stated",
	}, RefinementPayload{Guidance: "look at the lock, not the order"})
	if err != nil {
		t.Fatalf("reject and refine: %v", err)
	}

	if len(h.staged) != 2 {
		t.Fatalf("staged %d records, want 2", len(h.staged))
	}
	for _, rec := range h.staged {
		if rec.RunID != rejection.ID {
			t.Errorf("record %s staged under run %q, want the rejection's id %q",
				rec.EntityID, rec.RunID, rejection.ID)
		}
		if rec.Kind != sharedcatalog.KindDisposition {
			t.Errorf("record %s kind %q, want %q", rec.EntityID, rec.Kind, sharedcatalog.KindDisposition)
		}
	}
	if len(h.declared) != 1 || h.declared[0].RunID != rejection.ID {
		t.Errorf("declared closures = %+v, want exactly one for %s", h.declared, rejection.ID)
	}
	if len(h.closures) != 1 || h.closures[0].RunID != rejection.ID {
		t.Errorf("published closures = %+v, want exactly one for %s", h.closures, rejection.ID)
	}

	// The two halves are distinguishable on the wire by the absence of a
	// decision, which is what §4.7's "there is no standalone refine" means.
	var sawRejection, sawRefinement bool
	for _, rec := range h.staged {
		got := wire(t, rec)
		if got.Answer == nil {
			t.Fatalf("review answer %s carries no answer", rec.EntityID)
		}
		switch {
		case rec.EntityID == rejection.ID:
			sawRejection = got.Answer.Decision == DispositionReject
		case rec.EntityID == request.ID:
			sawRefinement = got.Answer.Decision == "" && got.Answer.Reviewer == "operator-a"
		}
	}
	if !sawRejection || !sawRefinement {
		t.Errorf("wire forms did not distinguish the rejection from the refinement (%v, %v)",
			sawRejection, sawRefinement)
	}
}

// A staging failure takes the durable write down with it. This is the property
// the whole in-transaction design exists for: the alternative is a record that
// is durable, invisible to the publisher, and reported by nothing.
func TestStagingFailureLeavesNothingDurable(t *testing.T) {
	h := newRecordingHook()
	h.appendErr = errors.New("the journal refused this record")
	s := openStoreWithHook(t, h)
	ctx := context.Background()

	_, err := s.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Actor:   Actor{Kind: ActorRun, ID: "run-1"},
		Payload: HypothesisPayload{Statement: "this write must not survive"},
	})
	if err == nil {
		t.Fatal("a refused staging call did not fail the write")
	}

	hypotheses, total, err := s.Hypotheses(ctx, ListFilter{})
	if err != nil {
		t.Fatalf("list hypotheses: %v", err)
	}
	if total != 0 || len(hypotheses) != 0 {
		t.Errorf("the durable store holds %d hypotheses after a refused staging call", total)
	}
}

// A publication failure after the commit is a different matter: the record is
// durable and staged, and internal/sync reports it. What this asserts is that
// the store surfaces the error rather than swallowing it, and that the record
// is still there.
func TestPublicationFailureLeavesTheRecordDurable(t *testing.T) {
	h := newRecordingHook()
	// The candidate joins its run's open closure and so publishes nothing; the
	// decision that follows is its own closure of one, and that is the write
	// whose publication fails.
	h.openRuns["run-1"] = true
	h.commitErr = errors.New("a caller bug in the closure")
	s := openStoreWithHook(t, h)
	ctx := context.Background()

	hypothesis, err := mustHypothesisErr(s, "run-1", "the record survives a failed publication")
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	if _, err := s.Decide(ctx, DispositionInput{
		Subject:     Ref{Type: EntityHypothesis, ID: hypothesis.ID},
		Disposition: DispositionDefer,
		ReviewerID:  "operator-a",
	}); err == nil {
		t.Fatal("a caller-bug publication error was swallowed")
	}
	if history, err := s.DispositionHistory(ctx, Ref{Type: EntityHypothesis, ID: hypothesis.ID}); err != nil {
		t.Fatalf("read disposition history: %v", err)
	} else if len(history) != 1 {
		t.Errorf("the decision is not durable: history holds %d events", len(history))
	}
}

// Without a hook the frontier is a purely local durable store, which is the
// default and a supported deployment. Nothing is staged and no write path
// behaves differently.
func TestLocalOnlyModeStagesNothing(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	if _, err := s.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Actor:   Actor{Kind: ActorRun, ID: "run-1"},
		Payload: HypothesisPayload{Statement: "a local-only candidate"},
	}); err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}

	// The journal's tables are not even created without a hook, so their
	// absence is the assertion. A store that had staged would have had to
	// create them.
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'sync_record'`).Scan(&n)
	if err != nil {
		t.Fatalf("inspect the durable schema: %v", err)
	}
	if n != 0 {
		t.Error("a local-only frontier created the publication journal")
	}
}

// mustHypothesis creates a candidate or fails the test.
func mustHypothesis(t *testing.T, s *Store, runID, statement string) Hypothesis {
	t.Helper()
	record, err := mustHypothesisErr(s, runID, statement)
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	return record
}

func mustHypothesisErr(s *Store, runID, statement string) (Hypothesis, error) {
	return s.CreateHypothesis(context.Background(), HypothesisInput{
		RunID:   runID,
		Actor:   Actor{Kind: ActorRun, ID: runID},
		Payload: HypothesisPayload{Statement: statement},
	})
}

var _ = event.Locator{}
