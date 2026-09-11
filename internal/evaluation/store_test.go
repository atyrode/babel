package evaluation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// What this file defends is the authority boundary and the lifecycle
// vocabulary: who may say what, about which exact revision, with what
// evidence, and what a retry or a stale worker is allowed to change.
//
// The resolver and the publication hook are fakes rather than a real frontier
// and a real Publisher. Neither would make these assertions stronger - what is
// measured here is which records this store accepts and refuses, and what it
// stages - and a real Publisher needs PostgreSQL, an object store and a
// keyring, none of which decide any rule in this package.

// fixedClock advances by a millisecond per call, so ordering is deterministic
// without sleeping and two records never collide on one instant.
type fixedClock struct {
	at time.Time
}

func (c *fixedClock) now() time.Time {
	c.at = c.at.Add(time.Millisecond)
	return c.at
}

// fakeResolver answers for a fixed corpus. A subject it does not hold is
// ErrNotFound, which is what a remote revision this instance cannot read looks
// like.
type fakeResolver struct {
	artifacts map[Subject]Artifact
	// err, when set, is returned for every lookup: the "could not look"
	// case, which must never read as "checked, fine".
	err error
}

func (r *fakeResolver) Artifact(ctx context.Context, subject Subject) (Artifact, error) {
	if r.err != nil {
		return Artifact{}, r.err
	}
	artifact, ok := r.artifacts[subject]
	if !ok {
		return Artifact{}, fmt.Errorf("%w: %s", ErrNotFound, subject)
	}
	return artifact, nil
}

// fakeRecords is the published-record resolver: what another instance wrote.
type fakeRecords struct {
	records map[string]Record
	err     error
}

func (r *fakeRecords) EvaluationRecord(ctx context.Context, id string) (Record, error) {
	if r.err != nil {
		return Record{}, r.err
	}
	record, ok := r.records[id]
	if !ok {
		return Record{}, fmt.Errorf("%w: evaluation record %s", ErrNotFound, id)
	}
	return record, nil
}

// recordingHook records what the store stages and publishes.
type recordingHook struct {
	staged   []babelsync.Record
	runs     []string
	closures []babelsync.Closure
}

func (h *recordingHook) Append(ctx context.Context, tx *sql.Tx, producedBy string,
	rec babelsync.Record) (babelsync.Closure, bool, error) {
	h.runs = append(h.runs, producedBy)
	rec.RunID = rec.EntityID
	h.staged = append(h.staged, rec)
	return babelsync.Closure{RunID: rec.EntityID, ContinuesRunID: producedBy}, true, nil
}

func (h *recordingHook) StageTx(ctx context.Context, tx *sql.Tx, rec babelsync.Record) error {
	h.staged = append(h.staged, rec)
	return nil
}

func (h *recordingHook) DeclareTx(ctx context.Context, tx *sql.Tx, c babelsync.Closure) error {
	return nil
}

func (h *recordingHook) CommitInline(ctx context.Context, c babelsync.Closure) error {
	h.closures = append(h.closures, c)
	return nil
}

// count reports how many records have been staged, which is what the
// idempotence assertions are actually about.
func (h *recordingHook) count() int { return len(h.staged) }

const (
	testProposal = "prop_0001"
	testRun      = "run_0001"
	otherRun     = "run_0002"
)

func proposalSubject() Subject { return Subject{Kind: "proposal", ID: testProposal} }

func testPolicy() Policy {
	return Policy{
		Version:          "test-1",
		Enabled:          true,
		CadenceSeconds:   3600,
		OverdueSeconds:   86400,
		InitialReviews:   2,
		CooldownSeconds:  3600,
		CoverageShare:    0.5,
		ExplorationShare: 0.2,
		DiscoveryShare:   0.1,
		MaxItemReviews:   5,
		// PerCycleCost bounds one run's whole UTC day rather than one
		// assignment, so the fixture leaves room for the several grants a
		// single test run takes out.
		PerCycleCost: 10,
		DailyCost:    20,
		LeaseSeconds: 600,
		BatchSize:    2,
	}
}

func testEvidence(t *testing.T) frontier.Evidence {
	t.Helper()
	evidence, err := frontier.NewEvidence(
		event.Locator{Path: "sessions/a.jsonl", Line: 4, Digest: "abc123"}, "the log says so")
	if err != nil {
		t.Fatalf("build evidence: %v", err)
	}
	return evidence
}

type harness struct {
	store    *Store
	hook     *recordingHook
	resolver *fakeResolver
	records  *fakeRecords
	clock    *fixedClock
}

// newHarness opens a store over a throwaway durable file with one resolvable
// proposal, authored by a run that is not the reviewing run.
func newHarness(t *testing.T, opts ...Option) *harness {
	t.Helper()
	clock := &fixedClock{at: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	resolver := &fakeResolver{artifacts: map[Subject]Artifact{
		proposalSubject(): {
			Subject:   proposalSubject(),
			RootID:    testProposal,
			HeadID:    testProposal,
			RunID:     "run_author",
			CreatedAt: clock.at,
			Title:     "cache the index",
			// A decided proposal: reopening is the act a reconsideration
			// decision can order, and it needs a real review state to move.
			ReviewStatus:   "deferred",
			ContextVersion: "ctx-1",
		},
	}}
	hook := &recordingHook{}
	records := &fakeRecords{records: map[string]Record{}}
	opts = append([]Option{WithSync(hook), WithRecordResolver(records)}, opts...)
	store, err := Open(t.TempDir(), resolver, opts...)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	store.now = clock.now
	if local, ok := store.coord.(*localCoordinator); ok {
		local.now = clock.now
	}
	t.Cleanup(func() { store.Close() })
	return &harness{store: store, hook: hook, resolver: resolver, records: records, clock: clock}
}

// claim grants one assignment in the given role.
func (h *harness) claim(t *testing.T, id, role, runID string) Assignment {
	t.Helper()
	granted, err := h.store.Claim(context.Background(), Assignment{
		ID:            id,
		Subject:       proposalSubject(),
		RunID:         runID,
		Role:          role,
		PolicyVersion: testPolicy().Version,
		Seed:          1 << 63,
		InputDigest:   "digest-1",
		Lane:          "coverage",
		ReservedCost:  1,
	}, testPolicy())
	if err != nil {
		t.Fatalf("claim %s: %v", id, err)
	}
	return granted
}

// A bare vote is a complete review. All three reception values are accepted
// with no prose, the record binds to the exact revision that was read, and the
// attribution survives the wire form a second instance decodes.
func TestSubmitBareVotePublishesAttributedRevision(t *testing.T) {
	for _, vote := range Votes() {
		t.Run(vote, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			granted := h.claim(t, "asg_"+vote, RoleReception, testRun)
			record, err := h.store.Submit(ctx, Submission{
				AssignmentID: granted.ID,
				RunID:        testRun,
				Fence:        granted.Fence,
				Assessment:   &Assessment{Vote: vote},
				Cost:         0.5,
				Provenance:   Provenance{Model: "m", Profile: "p", Recipe: "r", RecipeVersion: 1, Blinded: true},
			})
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if record.Assessment.Vote != vote {
				t.Fatalf("vote = %q, want %q", record.Assessment.Vote, vote)
			}
			if len(record.Assessment.Contributions) != 0 {
				t.Fatalf("a bare vote invented %d contributions", len(record.Assessment.Contributions))
			}
			if record.Subject != proposalSubject() {
				t.Fatalf("subject = %v, want the exact revision %v", record.Subject, proposalSubject())
			}
			if record.ActorKind != ActorRun || record.ActorID != testRun {
				t.Fatalf("attribution = %s/%s, want run/%s", record.ActorKind, record.ActorID, testRun)
			}
			if record.Assessment.ContextVersion != "ctx-1" {
				t.Fatalf("read context = %q, want the context the grant named", record.Assessment.ContextVersion)
			}
			// The staged bytes are what a non-producing instance sees, so the
			// round trip is the actual acceptance criterion.
			var assessment *babelsync.Record
			for i := range h.hook.staged {
				if h.hook.staged[i].EntityID == record.ID {
					assessment = &h.hook.staged[i]
				}
			}
			if assessment == nil {
				t.Fatalf("the assessment was not staged for publication")
			}
			if assessment.Schema != RecordSchema {
				t.Fatalf("staged schema = %d, want %d", assessment.Schema, RecordSchema)
			}
			if len(assessment.Subjects) != 0 || assessment.Edge != nil {
				t.Fatalf("an evaluation record projected content-derived judgement into plaintext columns")
			}
			decoded, err := Decode(assessment.Payload)
			if err != nil {
				t.Fatalf("decode staged record: %v", err)
			}
			if decoded.Assessment.Vote != vote || decoded.ActorID != testRun || decoded.Subject != proposalSubject() {
				t.Fatalf("decoded record lost attribution or revision: %+v", decoded)
			}
		})
	}
}

// A contribution with no vote is a lawful pre-review, and an assessment that
// states nothing at all is not: that is a skip, and recording it as a
// completion would be a review that said nothing.
func TestSubmitContributionWithoutVoteAndRefusalOfEmptyAssessment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	granted := h.claim(t, "asg_contrib", RoleEvidence, testRun)
	record, err := h.store.Submit(ctx, Submission{
		AssignmentID: granted.ID,
		RunID:        testRun,
		Fence:        granted.Fence,
		Assessment: &Assessment{Contributions: []Contribution{{
			Kind:     ContributionEvidence,
			Text:     "the benchmark contradicts the claim",
			Evidence: []frontier.Evidence{testEvidence(t)},
		}}},
	})
	if err != nil {
		t.Fatalf("submit contribution-only assessment: %v", err)
	}
	if record.Assessment.Vote != "" {
		t.Fatalf("a contribution-only assessment minted the vote %q", record.Assessment.Vote)
	}

	empty := h.claim(t, "asg_empty", RoleReception, testRun)
	_, err = h.store.Submit(ctx, Submission{
		AssignmentID: empty.ID,
		RunID:        testRun,
		Fence:        empty.Fence,
		Assessment:   &Assessment{},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty assessment error = %v, want ErrInvalid", err)
	}
}

// A retry of the identical result is the same event: one record, one attempt,
// one published statement. A different result under the same grant is not a
// retry and must not quietly become a second vote.
func TestSubmitRetryIsIdempotentAndDifferentResultConflicts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	granted := h.claim(t, "asg_retry", RoleReception, testRun)
	in := Submission{
		AssignmentID: granted.ID,
		RunID:        testRun,
		Fence:        granted.Fence,
		Assessment:   &Assessment{Vote: VoteSupport},
		Cost:         0.25,
	}
	first, err := h.store.Submit(ctx, in)
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	staged := h.hook.count()
	second, err := h.store.Submit(ctx, in)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("retry minted a second record %s beside %s", second.ID, first.ID)
	}
	if h.hook.count() != staged {
		t.Fatalf("retry staged %d more records", h.hook.count()-staged)
	}
	attempts, err := h.store.Attempts(ctx)
	if err != nil {
		t.Fatalf("attempts: %v", err)
	}
	completed := 0
	for _, attempt := range attempts {
		if attempt.State == AttemptCompleted {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("completed attempts = %d, want 1: a retry inflated the history", completed)
	}

	changed := in
	changed.Assessment = &Assessment{Vote: VoteOppose}
	if _, err := h.store.Submit(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed result error = %v, want ErrConflict", err)
	}
}

// A skip and a failure complete the claim and reconcile its reservation
// without producing a statement. Neither is a vote, and both stay visible as
// attempt history so a repeated skip reads as a gap rather than as negative
// reception.
func TestSubmitSkipAndFailureJournalWithoutVoting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		id    string
		in    func(a Assignment) Submission
		state string
	}{
		{
			name: "skip",
			id:   "asg_skip",
			in: func(a Assignment) Submission {
				return Submission{AssignmentID: a.ID, RunID: testRun, Fence: a.Fence,
					SkipReason: "no evaluator for this source", Cost: 0.1}
			},
			state: AttemptSkipped,
		},
		{
			name: "failure",
			id:   "asg_fail",
			in: func(a Assignment) Submission {
				return Submission{AssignmentID: a.ID, RunID: testRun, Fence: a.Fence,
					FailedReason: "the worker crashed", Cost: 0.2}
			},
			state: AttemptFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			granted := h.claim(t, tc.id, RoleReception, testRun)
			record, err := h.store.Submit(ctx, tc.in(granted))
			if err != nil {
				t.Fatalf("submit %s: %v", tc.name, err)
			}
			if record.ID != "" {
				t.Fatalf("%s produced the record %s", tc.name, record.ID)
			}
			events, err := h.store.Events(ctx)
			if err != nil {
				t.Fatalf("events: %v", err)
			}
			for _, ev := range events {
				if ev.Kind == KindAssessment && ev.AssignmentID == granted.ID {
					t.Fatalf("%s became an assessment", tc.name)
				}
			}
			found := false
			attempts, err := h.store.Attempts(ctx)
			if err != nil {
				t.Fatalf("attempts: %v", err)
			}
			for _, attempt := range attempts {
				if attempt.AssignmentID == granted.ID && attempt.State == tc.state {
					found = true
				}
			}
			if !found {
				t.Fatalf("no %s attempt was journalled", tc.state)
			}
			// The reservation is reconciled at what was actually spent, so a
			// skip does not keep holding the day's allowance.
			var actual float64
			var settled int
			if err := h.store.db.QueryRow(`SELECT actual, settled FROM evaluation_spend
				WHERE assignment_id = ?`, granted.ID).Scan(&actual, &settled); err != nil {
				t.Fatalf("read spend: %v", err)
			}
			if settled != 1 {
				t.Fatalf("the reservation for a %s was never reconciled", tc.name)
			}
		})
	}
}

// A correction is a linked descendant: the earlier statement stays readable,
// exactly one statement per grant is active, and a second correction of the
// same statement is refused rather than creating two live votes.
func TestCorrectAppendsLinkedCorrectionAndKeepsOneActiveVote(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	granted := h.claim(t, "asg_correct", RoleReception, testRun)
	in := Submission{
		AssignmentID: granted.ID,
		RunID:        testRun,
		Fence:        granted.Fence,
		Assessment:   &Assessment{Vote: VoteSupport},
	}
	first, err := h.store.Submit(ctx, in)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	corrected := in
	corrected.Assessment = &Assessment{Vote: VoteUnsure, Uncertainty: "I misread the diff"}
	correction, err := h.store.Correct(ctx, first.ID, corrected)
	if err != nil {
		t.Fatalf("correct: %v", err)
	}
	if correction.SupersedesID != first.ID {
		t.Fatalf("correction supersedes %q, want %s", correction.SupersedesID, first.ID)
	}
	kept, err := h.store.Record(ctx, first.ID)
	if err != nil {
		t.Fatalf("the corrected statement was lost: %v", err)
	}
	if kept.Assessment.Vote != VoteSupport {
		t.Fatalf("the earlier statement was rewritten to %q", kept.Assessment.Vote)
	}
	if _, err := h.store.Correct(ctx, first.ID, corrected); !errors.Is(err, ErrConflict) {
		t.Fatalf("second correction of one statement error = %v, want ErrConflict", err)
	}
}

// The operator surface is an authority boundary, not a convenience. A person
// cannot author what a run observes, an unattributed act is refused, and each
// operator kind must carry what makes it auditable.
func TestOperatorAuthorityAndRequiredContent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		in   OperatorInput
	}{
		{
			name: "a person cannot author a run's assessment",
			in:   OperatorInput{Kind: KindAssessment, Operator: "alex", Subject: proposalSubject()},
		},
		{
			name: "a person cannot raise a run-authored reconsider item",
			in:   OperatorInput{Kind: KindReconsider, Operator: "alex", Subject: proposalSubject()},
		},
		{
			name: "a person cannot claim a coverage sweep",
			in:   OperatorInput{Kind: KindCheckpoint, Operator: "alex"},
		},
		{
			name: "an unattributed act is refused",
			in:   OperatorInput{Kind: KindFeedback, Operator: "  ", Subject: proposalSubject(), Reason: "not now"},
		},
		{
			name: "feedback exists to carry a reason",
			in:   OperatorInput{Kind: KindFeedback, Operator: "alex", Subject: proposalSubject()},
		},
		{
			name: "criteria state at least one criterion",
			in:   OperatorInput{Kind: KindCriteria, Operator: "alex", Subject: proposalSubject()},
		},
		{
			name: "criteria state one condition once",
			in: OperatorInput{Kind: KindCriteria, Operator: "alex", Subject: proposalSubject(),
				Criteria: []Criterion{
					{ID: "crit_1", Description: "p99 drops below 100ms"},
					{ID: "crit_1", Description: "and also something else"},
				}},
		},
		{
			name: "a reconsideration decision must link the item it answers",
			in: OperatorInput{Kind: KindReconsiderDecision, Operator: "alex", Subject: proposalSubject(),
				Decision: ReconsiderReopen, Reason: "the new benchmark matters"},
		},
		{
			name: "a reconsideration decision must say which way it went",
			in: OperatorInput{Kind: KindReconsiderDecision, Operator: "alex", Subject: proposalSubject(),
				Reason: "reopening; the new benchmark matters", RelatedID: "evr_item"},
		},
		{
			name: "only a reconsideration decision carries a decision",
			in: OperatorInput{Kind: KindFeedback, Operator: "alex", Subject: proposalSubject(),
				Reason: "not now", Decision: ReconsiderRetain},
		},
		{
			name: "an unknown kind is refused by name",
			in:   OperatorInput{Kind: "disposition", Operator: "alex", Subject: proposalSubject()},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.store.Operator(ctx, tc.in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}

	feedback, err := h.store.Operator(ctx, OperatorInput{
		Kind:     KindFeedback,
		Operator: "alex",
		Subject:  proposalSubject(),
		Reason:   "wrong-remedy: this fixes the symptom",
		// A citation of a decision this instance cannot resolve is retained:
		// it authorizes nothing, and an operator's actual reason must not be
		// refused because the thing it refers to lives elsewhere.
		RelatedID: "unknown_elsewhere",
	})
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if feedback.ActorKind != ActorOperator || feedback.ActorID != "alex" {
		t.Fatalf("feedback attribution = %s/%s", feedback.ActorKind, feedback.ActorID)
	}
	if feedback.Assessment != nil {
		t.Fatalf("an operator act carried an assessment")
	}
}

// A reconsideration decision has to name a reconsider item that exists and is
// about the same subject - shape alone is not authority - and it has to say
// which way it went. The polarity is a field and not a reading of the reason:
// a retain and a reopen with equally reasonable prose are opposite acts, and a
// reader that had to classify the sentence would be inventing the decision.
func TestOperatorReconsiderDecisionRequiresResolvedItemAndExplicitPolarity(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.store.Operator(ctx, OperatorInput{
		Kind:      KindReconsiderDecision,
		Operator:  "alex",
		Subject:   proposalSubject(),
		Decision:  ReconsiderRetain,
		Reason:    "the prior ruling still holds",
		RelatedID: "evr_nothing",
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unresolvable reconsider item error = %v, want ErrInvalid", err)
	}

	item, err := h.store.Reconsider(ctx, ReconsiderInput{
		Subject:    proposalSubject(),
		RunID:      testRun,
		RelatedID:  "disp_0001",
		Reason:     "the benchmark it relied on changed",
		Provenance: Provenance{ContextVersion: "ctx-2"},
	})
	if err != nil {
		t.Fatalf("reconsider: %v", err)
	}
	decision, err := h.store.Operator(ctx, OperatorInput{
		Kind:      KindReconsiderDecision,
		Operator:  "alex",
		Subject:   proposalSubject(),
		Decision:  ReconsiderRetain,
		Reason:    "the new benchmark does not change the ruling",
		RelatedID: item.ID,
	})
	if err != nil {
		t.Fatalf("retain decision: %v", err)
	}
	if decision.RelatedID != item.ID || decision.Decision != ReconsiderRetain {
		t.Fatalf("decision links %q and reads %q, want %s and %s",
			decision.RelatedID, decision.Decision, item.ID, ReconsiderRetain)
	}

	// The polarity has to survive the wire, because the instance that renders
	// "reopened" or "retained" is usually not the one that recorded it.
	staged := h.hook.staged[len(h.hook.staged)-1]
	published, err := DecodePublished(staged.Payload, staged.EntityID, staged.Schema)
	if err != nil {
		t.Fatalf("decode published decision: %v", err)
	}
	if published.Decision != ReconsiderRetain {
		t.Fatalf("the published decision reads %q, so a second instance cannot tell a retain from a reopen",
			published.Decision)
	}

	// A reopen is a different act: it moves the review disposition, so a
	// deployment with no review writer refuses it rather than recording a
	// reopening that never happened.
	if _, err := h.store.Operator(ctx, OperatorInput{
		Kind:      KindReconsiderDecision,
		Operator:  "alex",
		Subject:   proposalSubject(),
		Decision:  ReconsiderReopen,
		Reason:    "the new benchmark matters",
		RelatedID: item.ID,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("reopen with no review writer error = %v, want ErrUnavailable", err)
	}
}

// An explicit reopen is the one operator act here that moves a §4.7
// disposition, and it moves it in the transaction that records the decision:
// a decision reading "reopened" beside a record still reading deferred is the
// disagreement docs/evaluation-lifecycle.md's "reopening is explicitly
// operator-driven" exists to prevent.
func TestReconsiderReopenMovesTheReviewDisposition(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	front, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() { front.Close() })

	clock := &fixedClock{at: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	resolver := &fakeResolver{artifacts: map[Subject]Artifact{
		proposalSubject(): {Subject: proposalSubject(), HeadID: testProposal, RootID: testProposal,
			RunID: "run_author", ReviewStatus: "rejected", ContextVersion: "ctx-1"},
	}}
	store, err := Open(dir, resolver, WithDispositionStore(front))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	store.now = clock.now

	item, err := store.Reconsider(ctx, ReconsiderInput{
		Subject:    proposalSubject(),
		RunID:      testRun,
		RelatedID:  "disp_0001",
		Reason:     "the benchmark it relied on changed",
		Provenance: Provenance{ContextVersion: "ctx-2"},
	})
	if err != nil {
		t.Fatalf("reconsider: %v", err)
	}
	decision, err := store.Operator(ctx, OperatorInput{
		Kind:      KindReconsiderDecision,
		Operator:  "alex",
		Subject:   proposalSubject(),
		Decision:  ReconsiderReopen,
		Reason:    "the new benchmark matters",
		RelatedID: item.ID,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if decision.Decision != ReconsiderReopen {
		t.Fatalf("decision reads %q, want %s", decision.Decision, ReconsiderReopen)
	}

	subject := frontier.Ref{Type: frontier.EntityProposal, ID: testProposal}
	history, err := front.DispositionHistory(ctx, subject)
	if err != nil {
		t.Fatalf("disposition history: %v", err)
	}
	if len(history) != 1 || history[0].Disposition != frontier.DispositionReopen {
		t.Fatalf("the reopen did not move the review disposition: %+v", history)
	}
	if history[0].ReviewerID != "alex" || history[0].Payload.Note != "the new benchmark matters" {
		t.Fatalf("the reopened disposition lost its attribution or its reason: %+v", history[0])
	}
	status, err := front.ReviewStatus(ctx, subject)
	if err != nil {
		t.Fatalf("review status: %v", err)
	}
	if status != frontier.ReviewNew {
		t.Fatalf("review status = %q, want %s: the reopened record is not back in the reading order",
			status, frontier.ReviewNew)
	}

	// The same decision against a record that is already open is refused: a
	// reopen of open work reopens nothing.
	resolver.artifacts[proposalSubject()] = Artifact{Subject: proposalSubject(), HeadID: testProposal,
		RunID: "run_author", ReviewStatus: "new", ContextVersion: "ctx-1"}
	if _, err := store.Operator(ctx, OperatorInput{
		Kind:      KindReconsiderDecision,
		Operator:  "alex",
		Subject:   proposalSubject(),
		Decision:  ReconsiderReopen,
		Reason:    "again",
		RelatedID: item.ID,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("reopening open work error = %v, want ErrInvalid", err)
	}
}

// One material change raises one reconsider item however often the sweep that
// finds it runs, and a genuinely later change raises its own.
func TestReconsiderIsIdempotentPerMaterialChange(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := ReconsiderInput{
		Subject:    proposalSubject(),
		RunID:      testRun,
		RelatedID:  "disp_0001",
		Reason:     "the benchmark it relied on changed",
		Provenance: Provenance{ContextVersion: "ctx-2"},
	}
	first, err := h.store.Reconsider(ctx, in)
	if err != nil {
		t.Fatalf("reconsider: %v", err)
	}
	again, err := h.store.Reconsider(ctx, in)
	if err != nil {
		t.Fatalf("re-detected change: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("the same material change raised a second item %s", again.ID)
	}
	later := in
	later.Provenance = Provenance{ContextVersion: "ctx-3"}
	third, err := h.store.Reconsider(ctx, later)
	if err != nil {
		t.Fatalf("later change: %v", err)
	}
	if third.ID == first.ID {
		t.Fatalf("a later material change was folded into the earlier item")
	}
}

// A coverage sweep says only that it finished. It carries no author and no
// opinion, and a repeated sweep at the same instant over the same inventory is
// one completion rather than two.
func TestCheckpointRecordsCompletionWithoutOpinion(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 11, 13, 0, 0, 0, time.UTC)
	if err := h.store.Checkpoint(ctx, at, "inventory-1", 42); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := h.store.Checkpoint(ctx, at, "inventory-1", 42); err != nil {
		t.Fatalf("repeated checkpoint: %v", err)
	}
	events, err := h.store.Events(ctx)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	checkpoints := 0
	for _, ev := range events {
		if ev.Kind != KindCheckpoint {
			continue
		}
		checkpoints++
		if ev.ActorKind != "" || ev.ActorID != "" {
			t.Fatalf("a sweep claimed the author %s/%s", ev.ActorKind, ev.ActorID)
		}
		if ev.Assessment != nil || !ev.Subject.Zero() {
			t.Fatalf("a sweep carried an opinion or a subject: %+v", ev)
		}
		if ev.Checkpoint.Covered != 42 || ev.Checkpoint.InputDigest != "inventory-1" {
			t.Fatalf("checkpoint payload = %+v", ev.Checkpoint)
		}
	}
	if checkpoints != 1 {
		t.Fatalf("checkpoints = %d, want 1", checkpoints)
	}
}

// Migrations are idempotent across opens, and a file written by a newer build
// is refused by name rather than read as though the newer records were not
// there.
func TestOpenMigratesIdempotentlyAndRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	resolver := &fakeResolver{artifacts: map[Subject]Artifact{}}
	first, err := Open(dir, resolver)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	second, err := Open(dir, resolver)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := second.db.Exec(`UPDATE schema_migration SET version = ? WHERE component = ?`,
		len(migrations)+1, component); err != nil {
		t.Fatalf("simulate a newer build: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := Open(dir, resolver); err == nil {
		t.Fatalf("a database migrated by a newer build was opened silently")
	}
}

// A record that was written in local mode is owed to the fleet the moment
// shared publication is configured, and staging it twice must not happen.
func TestRestageStagesLocalRecordsOnce(t *testing.T) {
	dir := t.TempDir()
	clock := &fixedClock{at: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
	resolver := &fakeResolver{artifacts: map[Subject]Artifact{
		proposalSubject(): {Subject: proposalSubject(), HeadID: testProposal, RunID: "run_author",
			ContextVersion: "ctx-1"},
	}}
	local, err := Open(dir, resolver)
	if err != nil {
		t.Fatalf("open local: %v", err)
	}
	local.now = clock.now
	if localCoord, ok := local.coord.(*localCoordinator); ok {
		localCoord.now = clock.now
	}
	ctx := context.Background()
	if _, err := local.Operator(ctx, OperatorInput{
		Kind:     KindCriteria,
		Operator: "alex",
		Subject:  proposalSubject(),
		Criteria: []Criterion{{ID: "crit_1", Description: "p99 drops below 100ms"}},
	}); err != nil {
		t.Fatalf("record criteria locally: %v", err)
	}
	if err := local.Close(); err != nil {
		t.Fatalf("close local: %v", err)
	}

	hook := &recordingHook{}
	shared, err := Open(dir, resolver, WithSync(hook))
	if err != nil {
		t.Fatalf("open shared: %v", err)
	}
	defer shared.Close()
	staged, err := shared.Restage(ctx)
	if err != nil {
		t.Fatalf("restage: %v", err)
	}
	if staged != 1 {
		t.Fatalf("restaged %d records, want 1", staged)
	}
	if len(hook.staged) != 1 {
		t.Fatalf("staged %d records", len(hook.staged))
	}
	if _, err := Decode(hook.staged[0].Payload); err != nil {
		t.Fatalf("restaged bytes are not canonical: %v", err)
	}
}

// An outcome is measured against the operator's criteria version, and
// resolving that version is only half the check: the answers have to be
// answers to the conditions it actually states. A run that could answer a
// condition nobody wrote, or call three of five conditions a verification,
// would be choosing its own target - which is the retrospective substitution
// §4.12 refuses.
func TestOutcomeResultsBindToTheOperatorsCriteria(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	criteria, err := h.store.Operator(ctx, OperatorInput{
		Kind: KindCriteria, Operator: "alex", Subject: proposalSubject(),
		Criteria: []Criterion{
			{ID: "crit_1", Description: "p99 drops below 100ms"},
			{ID: "crit_2", Description: "no error-rate regression"},
		},
	})
	if err != nil {
		t.Fatalf("record criteria: %v", err)
	}
	answer := func(id string) CriterionResult {
		return CriterionResult{CriterionID: id, Satisfied: true,
			Evidence: []frontier.Evidence{testEvidence(t)}}
	}
	for i, tc := range []struct {
		name       string
		assessment Assessment
	}{
		{
			name: "a criterion the operator never wrote",
			assessment: Assessment{Outcome: OutcomeVerified, CriteriaID: criteria.ID, Environment: "staging",
				AsOf: h.clock.at, Results: []CriterionResult{answer("crit_1"), answer("crit_99")}},
		},
		{
			name: "verified against a subset of the stated criteria",
			assessment: Assessment{Outcome: OutcomeVerified, CriteriaID: criteria.ID, Environment: "staging",
				AsOf: h.clock.at, Results: []CriterionResult{answer("crit_1")}},
		},
		{
			name: "one condition answered twice",
			assessment: Assessment{Outcome: OutcomeVerified, CriteriaID: criteria.ID, Environment: "staging",
				AsOf:    h.clock.at,
				Results: []CriterionResult{answer("crit_1"), answer("crit_1"), answer("crit_2")}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			granted := h.claim(t, fmt.Sprintf("asg_bind_%d", i), RoleOutcome, testRun)
			assessment := tc.assessment
			if _, err := h.store.Submit(ctx, Submission{AssignmentID: granted.ID, RunID: testRun,
				Fence: granted.Fence, Assessment: &assessment}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}

	// A partial outcome may answer a subset: saying that some conditions were
	// met and some were not is what it is for.
	partial := h.claim(t, "asg_bind_partial", RoleOutcome, testRun)
	if _, err := h.store.Submit(ctx, Submission{
		AssignmentID: partial.ID, RunID: testRun, Fence: partial.Fence,
		Assessment: &Assessment{Outcome: OutcomePartial, CriteriaID: criteria.ID, Environment: "staging",
			AsOf: h.clock.at, Uncertainty: "the second condition needs a production window",
			Results: []CriterionResult{answer("crit_1")}},
	}); err != nil {
		t.Fatalf("partial outcome over a subset: %v", err)
	}
	full := h.claim(t, "asg_bind_full", RoleOutcome, testRun)
	if _, err := h.store.Submit(ctx, Submission{
		AssignmentID: full.ID, RunID: testRun, Fence: full.Fence,
		Assessment: &Assessment{Outcome: OutcomeVerified, CriteriaID: criteria.ID, Environment: "staging",
			AsOf: h.clock.at, Results: []CriterionResult{answer("crit_1"), answer("crit_2")}},
	}); err != nil {
		t.Fatalf("verified outcome answering every stated condition: %v", err)
	}
}

// An authority another instance published passes the same gate a local one
// does. The local path comes back through Decode; a remote one arrives as a
// struct some other process filled in, so a resolver that answers with a
// malformed record - or with a different record than the one asked for - must
// not be able to grant an authority its own shape cannot support.
func TestRemoteAuthorityMustBeTheRecordItClaims(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	criteria := []Criterion{{ID: "crit_1", Description: "p99 drops below 100ms"}}
	for _, tc := range []struct {
		name   string
		record Record
	}{
		{
			name: "a record filed under another id",
			record: Record{ID: "evr_somethingelse", Kind: KindCriteria, Subject: proposalSubject(),
				ActorKind: ActorOperator, ActorID: "other-operator", CreatedAt: at, Criteria: criteria},
		},
		{
			name: "a criteria record with no criteria in it",
			record: Record{ID: "evr_remote", Kind: KindCriteria, Subject: proposalSubject(),
				ActorKind: ActorOperator, ActorID: "other-operator", CreatedAt: at},
		},
		{
			name: "a criteria record signed by nobody",
			record: Record{ID: "evr_remote", Kind: KindCriteria, Subject: proposalSubject(),
				ActorKind: ActorOperator, CreatedAt: at, Criteria: criteria},
		},
		{
			name: "a criteria record with no timestamp",
			record: Record{ID: "evr_remote", Kind: KindCriteria, Subject: proposalSubject(),
				ActorKind: ActorOperator, ActorID: "other-operator", Criteria: criteria},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.records.records["evr_remote"] = tc.record
			granted := h.claim(t, "asg_remote_authority", RoleOutcome, testRun)
			if _, err := h.store.Submit(ctx,
				outcomeSubmission(t, granted, "evr_remote")); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

// The grant carries the authority, so what a result may state is what its
// role admits. A reception vote satisfies reception and stands in for nothing
// else, a comparison is the comparison role's output - including on a subject
// kind that cannot carry one at all - and an outcome grant that reached no
// answer says so rather than completing with a comment.
func TestRolePayloadAuthorityFollowsTheGrant(t *testing.T) {
	ctx := context.Background()
	hypothesis := Subject{Kind: "hypothesis", ID: "hyp_0001"}
	alternative := Subject{Kind: "proposal", ID: "prop_0003"}
	newRoleHarness := func(t *testing.T) *harness {
		h := newHarness(t)
		h.resolver.artifacts[hypothesis] = Artifact{Subject: hypothesis, HeadID: hypothesis.ID,
			RunID: "run_author", ReviewStatus: "new", ContextVersion: "ctx-1"}
		h.resolver.artifacts[alternative] = Artifact{Subject: alternative, HeadID: alternative.ID,
			RunID: "run_other", ReviewStatus: "new", ContextVersion: "ctx-1"}
		return h
	}
	grant := func(t *testing.T, h *harness, id string, subject Subject, role string) Assignment {
		t.Helper()
		granted, err := h.store.Claim(ctx, Assignment{
			ID: id, Subject: subject, RunID: testRun, Role: role,
			PolicyVersion: testPolicy().Version, InputDigest: "digest-1", Lane: "coverage", ReservedCost: 1,
		}, testPolicy())
		if err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}
		return granted
	}

	t.Run("a comparison under a reception grant on a hypothesis", func(t *testing.T) {
		h := newRoleHarness(t)
		granted := grant(t, h, "asg_role_compare", hypothesis, RoleReception)
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Contributions: []Contribution{{
				Kind:         ContributionComparison,
				Text:         "the other remedy is cheaper here",
				Alternatives: []Subject{proposalSubject(), alternative},
			}}},
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("a bare vote under an evidence grant", func(t *testing.T) {
		h := newRoleHarness(t)
		granted := grant(t, h, "asg_role_vote", proposalSubject(), RoleEvidence)
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Vote: VoteSupport},
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("criterion results under a relevance grant", func(t *testing.T) {
		h := newRoleHarness(t)
		criteria, err := h.store.Operator(ctx, OperatorInput{
			Kind: KindCriteria, Operator: "alex", Subject: proposalSubject(),
			Criteria: []Criterion{{ID: "crit_1", Description: "p99 drops below 100ms"}},
		})
		if err != nil {
			t.Fatalf("record criteria: %v", err)
		}
		granted := grant(t, h, "asg_role_results", proposalSubject(), RoleRelevance)
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{CriteriaID: criteria.ID, Results: []CriterionResult{{
				CriterionID: "crit_1", Satisfied: true, Evidence: []frontier.Evidence{testEvidence(t)},
			}}},
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("an outcome grant that states no outcome", func(t *testing.T) {
		h := newRoleHarness(t)
		granted := grant(t, h, "asg_role_nooutcome", proposalSubject(), RoleOutcome)
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Contributions: []Contribution{{
				Kind: ContributionComment, Text: "it looks shipped",
			}}},
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("a comparison under a comparison grant mints no vote", func(t *testing.T) {
		h := newRoleHarness(t)
		granted := grant(t, h, "asg_role_ok", proposalSubject(), RoleComparison)
		preferred := alternative
		record, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Contributions: []Contribution{{
				Kind:         ContributionComparison,
				Text:         "the other remedy is cheaper for the indexing path",
				Alternatives: []Subject{proposalSubject(), alternative},
				Preferred:    &preferred,
			}}},
		})
		if err != nil {
			t.Fatalf("comparison under a comparison grant: %v", err)
		}
		if record.Assessment.Vote != "" {
			t.Fatalf("a comparison minted the vote %q", record.Assessment.Vote)
		}
	})
}

// A comparison compares records that exist and that are not each other. An
// alternative nothing can resolve is the assessment of an assumed artifact
// this package refuses everywhere, and [A, A] meets "at least two
// alternatives" while comparing one remedy with itself.
func TestComparisonAlternativesAreResolvedAndDistinct(t *testing.T) {
	ctx := context.Background()

	t.Run("the same alternative twice", func(t *testing.T) {
		h := newHarness(t)
		granted := h.claim(t, "asg_cmp_dup", RoleComparison, testRun)
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Contributions: []Contribution{{
				Kind:         ContributionComparison,
				Text:         "it beats itself",
				Alternatives: []Subject{proposalSubject(), proposalSubject()},
			}}},
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("an alternative nothing can resolve", func(t *testing.T) {
		h := newHarness(t)
		granted := h.claim(t, "asg_cmp_missing", RoleComparison, testRun)
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Contributions: []Contribution{{
				Kind:         ContributionComparison,
				Text:         "compared against a remedy I did not read",
				Alternatives: []Subject{proposalSubject(), {Kind: "proposal", ID: "prop_nowhere"}},
			}}},
		}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v, want ErrNotFound", err)
		}
	})
}
