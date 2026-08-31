package reality

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordingSink stands in for the frontier where a test cares about what a plan
// handed over rather than about the frontier's own storage. The real adapter is
// exercised in TestPlanRetainsItsHypothesisInTheRealFrontier.
type recordingSink struct {
	drafts []HypothesisDraft
	err    error
}

func (r *recordingSink) RecordHypothesis(ctx context.Context, draft HypothesisDraft) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	r.drafts = append(r.drafts, draft)
	return "hyp_synthetic_" + draft.Statement[:4], nil
}

// planFixture builds the §4.8 case end to end: a question, an answer, and an
// interpretation whose plan mixes authoritative and non-authoritative actions.
type planFixture struct {
	store    *Store
	clock    *testClock
	sink     *recordingSink
	project  Entity
	folded   Entity
	question Question
	answer   Answer
	plan     Plan
	retained Retained
}

// The options are the caller's, so a publication test can attach a sync hook to
// this fixture rather than rebuilding a question, an answer and a plan beside
// it.
func newPlanFixture(t *testing.T, opts ...Option) *planFixture {
	t.Helper()
	ctx := context.Background()
	sink := &recordingSink{}
	store, clock := newStore(t, append([]Option{WithHypothesisSink(sink)}, opts...)...)
	fixture := &planFixture{store: store, clock: clock, sink: sink}

	fixture.project = mustEntity(t, store, EntityProject, "a project")
	fixture.folded = mustEntity(t, store, EntityProject, "the same project under another name")

	question, err := store.Ask(ctx, QuestionInput{
		Kind:              KindAcquireContext,
		Class:             ClassBlocking,
		Sensitivity:       SensitivityRoutine,
		ExpectedAuthority: AuthorityOperator,
		TargetEntityIDs:   []string{fixture.project.ID},
		TargetPredicates:  []Predicate{PredicateLifecycle},
		MaterialEvidence:  []string{"observation-1"},
		DependentWork:     []WorkRef{{Kind: WorkHypothesis, ID: "hyp-blocked", Blocking: true}},
		Payload: QuestionPayload{
			Prompt:   "is this project still active, and is the other name the same project?",
			WhyAsked: "a hypothesis about it cannot be scoped without knowing",
		},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	fixture.question = question

	answer, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID,
		Author:     "operator",
		At:         clock.now(),
		Outcome:    OutcomeAnswered,
		Text:       "it is dormant now, and yes, the other name is the same project",
	})
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	fixture.answer = answer

	if err := store.BeginInterpretation(ctx, question.ID); err != nil {
		t.Fatalf("BeginInterpretation: %v", err)
	}

	observed := clock.now()
	plan, retained, err := store.RecordPlan(ctx, PlanInput{
		QuestionID:         question.ID,
		AnswerID:           answer.ID,
		InterpreterVersion: 3,
		Summary:            "record dormancy, fold the duplicate identity, and follow up",
		Kinds: []ActionKind{
			ActionAssertFact,
			ActionMergeEntities,
			ActionCreateHypothesis,
			ActionAskFollowUp,
			ActionRequestInvestigation,
			ActionNone,
		},
		Actions: []ActionPayload{
			{
				Rationale: "the operator stated the project is dormant",
				Fact: &FactInput{
					SubjectID:   fixture.project.ID,
					Predicate:   PredicateLifecycle,
					Value:       enum(LifecycleDormant),
					ValidFrom:   observed,
					ObservedAt:  observed,
					Confidence:  ConfidenceHigh,
					Sensitivity: SensitivityRoutine,
				},
			},
			{
				Rationale: "the operator confirmed both names are one project",
				Merge: &MergeInput{
					SourceIDs: []string{fixture.folded.ID},
					TargetID:  fixture.project.ID,
					Reason:    "confirmed by the operator's answer",
				},
			},
			{
				Rationale: "dormancy raises a question worth investigating separately",
				Hypothesis: &HypothesisDraft{
					RunID:      "run-interpretation",
					Statement:  "dormant projects accumulate stale deployment facts",
					OriginCues: []string{"the operator's answer"},
				},
			},
			{
				Rationale: "the answer did not say who owns it now",
				FollowUp: &QuestionInput{
					Kind:              KindAcquireContext,
					Class:             ClassMaintenance,
					Sensitivity:       SensitivityRoutine,
					ExpectedAuthority: AuthorityOperator,
					TargetEntityIDs:   []string{fixture.project.ID},
					TargetPredicates:  []Predicate{PredicateOwnership},
					MaterialEvidence:  []string{"answer-" + answer.ID},
					Payload: QuestionPayload{
						Prompt:   "who owns this project now?",
						WhyAsked: "the answer settled lifecycle but not ownership",
					},
				},
			},
			{
				Rationale: "the stale-facts pattern is worth an evidence-backed investigation",
				Request: &RequestDraft{
					Kind:         RequestInvestigation,
					HypothesisID: "hyp_synthetic_dorm",
					Guidance:     "investigate through the normal evidence pipeline",
				},
			},
			{Rationale: "nothing else in the answer needs acting on"},
		},
	})
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	fixture.plan = plan
	fixture.retained = retained
	return fixture
}

// TestPlanRetainsDescendantsButWaitsToTouchReality is §4.8's central rule about
// interpretation: agent interpretation never silently becomes authoritative
// reality.
//
// The two halves are checked against one plan. Its non-authoritative
// descendants — the candidate hypothesis, the follow-up question, and the
// pipeline request — exist the moment the plan is recorded, because none of them
// asserts anything about reality and holding them hostage would lose ideas the
// interpretation produced. Its fact assertion and its entity merge have changed
// nothing at all: the ledger holds no fact, the folded identity still speaks for
// itself, and the actions sit as pending-acceptance.
func TestPlanRetainsDescendantsButWaitsToTouchReality(t *testing.T) {
	ctx := context.Background()
	fixture := newPlanFixture(t)
	store := fixture.store

	// Retained immediately.
	if len(fixture.retained.HypothesisIDs) != 1 {
		t.Errorf("retained %d hypotheses, want 1", len(fixture.retained.HypothesisIDs))
	}
	if len(fixture.sink.drafts) != 1 || fixture.sink.drafts[0].RunID != "run-interpretation" {
		t.Errorf("the frontier received %+v", fixture.sink.drafts)
	}
	if len(fixture.retained.QuestionIDs) != 1 {
		t.Fatalf("retained %d follow-up questions, want 1", len(fixture.retained.QuestionIDs))
	}
	followUp, err := store.Question(ctx, fixture.retained.QuestionIDs[0])
	if err != nil {
		t.Fatalf("the follow-up question was not retained: %v", err)
	}
	if followUp.State != QuestionOpen {
		t.Errorf("the follow-up question is %s, want %s", followUp.State, QuestionOpen)
	}
	if len(fixture.retained.RequestIDs) != 1 {
		t.Errorf("retained %d requests, want 1", len(fixture.retained.RequestIDs))
	}
	requests, err := store.Requests(ctx, fixture.plan.ID)
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}
	if len(requests) != 1 || requests[0].Kind != RequestInvestigation {
		t.Errorf("recorded requests are %+v", requests)
	}

	// Nothing authoritative applied.
	facts, err := store.Facts(ctx, FactQuery{SubjectID: fixture.project.ID})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("%d facts exist before acceptance", len(facts))
	}
	folded, err := store.Entity(ctx, fixture.folded.ID)
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if folded.Role != RoleSelf {
		t.Errorf("the merge applied before acceptance: the identity is %s", folded.Role)
	}
	if got := countRows(t, store, "reality_resolution"); got != 0 {
		t.Errorf("%d resolutions exist before acceptance", got)
	}

	// The action states say which half is which.
	stored, err := store.Plan(ctx, fixture.plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if stored.State != PlanProposed {
		t.Errorf("the plan is %s, want %s", stored.State, PlanProposed)
	}
	wantStates := map[ActionKind]ActionState{
		ActionAssertFact:           ActionPendingAcceptance,
		ActionMergeEntities:        ActionPendingAcceptance,
		ActionCreateHypothesis:     ActionRetained,
		ActionAskFollowUp:          ActionRetained,
		ActionRequestInvestigation: ActionRetained,
		ActionNone:                 ActionRetained,
	}
	if len(stored.Actions) != len(wantStates) {
		t.Fatalf("the plan has %d actions, want %d", len(stored.Actions), len(wantStates))
	}
	for _, action := range stored.Actions {
		if want := wantStates[action.Kind]; action.State != want {
			t.Errorf("action %s is %s, want %s", action.Kind, action.State, want)
		}
		if action.State == ActionRetained && action.Kind != ActionNone && action.ResultID == "" {
			t.Errorf("retained action %s has no result", action.Kind)
		}
	}

	// The question is waiting for a human, and is in the inbox for it.
	current, err := store.Question(ctx, fixture.question.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if current.State != QuestionPlanReady {
		t.Errorf("the question is %s, want %s", current.State, QuestionPlanReady)
	}
	inbox, err := store.Inbox(ctx, InboxQuery{})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	found := false
	for _, item := range inbox {
		if item.Question.ID == fixture.question.ID {
			found = true
		}
	}
	if !found {
		t.Error("a plan-ready question is not in the inbox")
	}
}

// TestAcceptancePutsRealityUnderTheOperatorsAuthority checks what one explicit
// acceptance does: it applies the authoritative actions, attributes them to the
// accepting operator rather than to the interpreter, and disposes of the
// question — and it can happen only once.
func TestAcceptancePutsRealityUnderTheOperatorsAuthority(t *testing.T) {
	ctx := context.Background()
	fixture := newPlanFixture(t)
	store := fixture.store

	guidance, err := store.AttachContext(ctx, ContextInput{
		Author: "operator",
		At:     fixture.clock.now(),
		Text:   "accepting; the dormancy is deliberate",
	})
	if err != nil {
		t.Fatalf("AttachContext: %v", err)
	}

	acceptance, application, err := store.AcceptPlan(ctx, AcceptanceInput{
		PlanID:    fixture.plan.ID,
		Actor:     "operator",
		ContextID: guidance.ID,
		Note:      "looks right",
	})
	if err != nil {
		t.Fatalf("AcceptPlan: %v", err)
	}
	if acceptance.Actor != "operator" || acceptance.ContextID != guidance.ID {
		t.Errorf("the acceptance lost its attribution: %+v", acceptance)
	}
	if len(application.FactIDs) != 1 || len(application.ResolutionIDs) != 1 {
		t.Fatalf("the application reports %+v, want one fact and one resolution", application)
	}
	if application.QuestionState != QuestionAnswered {
		t.Errorf("the application reports question state %s, want %s",
			application.QuestionState, QuestionAnswered)
	}

	fact, err := store.Fact(ctx, application.FactIDs[0])
	if err != nil {
		t.Fatalf("Fact: %v", err)
	}
	if fact.Status != FactActive {
		t.Errorf("the applied fact is %s, want %s", fact.Status, FactActive)
	}
	// §4.8: the authority behind an applied fact is the operator who
	// accepted it, never the interpretation that proposed it.
	if fact.Authority.Kind != AuthorityOperator || fact.Authority.ID != "operator" {
		t.Errorf("the applied fact is attributed to %+v, want the accepting operator", fact.Authority)
	}
	if fact.Payload.ContextID != guidance.ID {
		t.Errorf("the applied fact did not pick up the acceptance's guidance")
	}

	folded, err := store.Entity(ctx, fixture.folded.ID)
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if folded.Role != RoleMerged || folded.CanonicalID != fixture.project.ID {
		t.Errorf("the merge did not apply: %+v", folded)
	}
	resolution, err := store.Resolution(ctx, application.ResolutionIDs[0])
	if err != nil {
		t.Fatalf("Resolution: %v", err)
	}
	if resolution.Actor != "operator" {
		t.Errorf("the resolution is attributed to %q, want the accepting operator", resolution.Actor)
	}

	question, err := store.Question(ctx, fixture.question.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if question.State != QuestionAnswered {
		t.Errorf("the question is %s after acceptance, want %s", question.State, QuestionAnswered)
	}
	plan, err := store.Plan(ctx, fixture.plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.State != PlanAccepted {
		t.Errorf("the plan is %s, want %s", plan.State, PlanAccepted)
	}
	for _, action := range plan.Actions {
		if action.Kind.RequiresAcceptance() && action.State != ActionApplied {
			t.Errorf("action %s is %s after acceptance, want %s",
				action.Kind, action.State, ActionApplied)
		}
	}

	// Exactly one acceptance, enforced by the database rather than by a
	// check that a double-click could race.
	if _, _, err := store.AcceptPlan(ctx, AcceptanceInput{
		PlanID: fixture.plan.ID, Actor: "operator",
	}); !isErr(err, ErrAlreadyDecided) {
		t.Errorf("a second acceptance returned %v, want ErrAlreadyDecided", err)
	}
	if err := store.RejectPlan(ctx, AcceptanceInput{
		PlanID: fixture.plan.ID, Actor: "operator",
	}); !isErr(err, ErrAlreadyDecided) {
		t.Errorf("rejecting an accepted plan returned %v, want ErrAlreadyDecided", err)
	}
}

// TestAcceptanceAndDispositionCommitAtomically is the failure §4.8 cares about
// most: an acceptance that applied facts but did not dispose of its question, or
// a disposition with no facts behind it, would each leave the ledger asserting
// something nobody decided.
//
// The fault is injected inside the transaction, after the acceptance row and the
// authoritative actions are written and before the question's disposition is
// appended — the exact window where a partial commit would be possible.
func TestAcceptanceAndDispositionCommitAtomically(t *testing.T) {
	ctx := context.Background()
	fixture := newPlanFixture(t)
	store := fixture.store

	injected := errors.New("injected failure before the question's disposition")
	store.faultBeforeDisposition = func() error { return injected }

	if _, _, err := store.AcceptPlan(ctx, AcceptanceInput{
		PlanID: fixture.plan.ID, Actor: "operator", Note: "accepting",
	}); !errors.Is(err, injected) {
		t.Fatalf("AcceptPlan returned %v, want the injected failure", err)
	}

	// Neither half survived.
	if got := countRows(t, store, "reality_plan_acceptance"); got != 0 {
		t.Errorf("%d acceptances survived the failure", got)
	}
	if got := countRows(t, store, "reality_fact"); got != 0 {
		t.Errorf("%d facts survived the failure", got)
	}
	if got := countRows(t, store, "reality_resolution"); got != 0 {
		t.Errorf("%d resolutions survived the failure", got)
	}
	folded, err := store.Entity(ctx, fixture.folded.ID)
	if err != nil {
		t.Fatalf("Entity: %v", err)
	}
	if folded.Role != RoleSelf {
		t.Errorf("the merge survived the failure: the identity is %s", folded.Role)
	}
	question, err := store.Question(ctx, fixture.question.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if question.State != QuestionPlanReady {
		t.Errorf("the question is %s after a failed acceptance, want %s",
			question.State, QuestionPlanReady)
	}
	plan, err := store.Plan(ctx, fixture.plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.State != PlanProposed {
		t.Errorf("the plan is %s after a failed acceptance, want %s", plan.State, PlanProposed)
	}
	for _, action := range plan.Actions {
		if action.Kind.RequiresAcceptance() && action.State != ActionPendingAcceptance {
			t.Errorf("action %s is %s after a failed acceptance", action.Kind, action.State)
		}
	}

	// The retained descendants are untouched by the failure, because they were
	// never part of the acceptance.
	if _, err := store.Question(ctx, fixture.retained.QuestionIDs[0]); err != nil {
		t.Errorf("the retained follow-up question was lost: %v", err)
	}

	// With the fault cleared, the same acceptance succeeds and both halves
	// commit together.
	store.faultBeforeDisposition = nil
	_, application, err := store.AcceptPlan(ctx, AcceptanceInput{
		PlanID: fixture.plan.ID, Actor: "operator", Note: "accepting",
	})
	if err != nil {
		t.Fatalf("AcceptPlan after clearing the fault: %v", err)
	}
	if len(application.FactIDs) != 1 {
		t.Errorf("the retried acceptance applied %d facts", len(application.FactIDs))
	}
	question, err = store.Question(ctx, fixture.question.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if question.State != QuestionAnswered {
		t.Errorf("the question is %s after the retry, want %s", question.State, QuestionAnswered)
	}
}

// TestRejectingAPlanKeepsTheAnswerForAnotherInterpretation covers §4.8's retry
// path: the answer is still a good answer when the interpretation of it was
// wrong, so nothing is applied, nothing is deleted, and the question returns to
// `answered-uninterpreted`.
func TestRejectingAPlanKeepsTheAnswerForAnotherInterpretation(t *testing.T) {
	ctx := context.Background()
	fixture := newPlanFixture(t)
	store := fixture.store

	if err := store.RejectPlan(ctx, AcceptanceInput{
		PlanID: fixture.plan.ID,
		Actor:  "operator",
		Note:   "it read the answer backwards",
	}); err != nil {
		t.Fatalf("RejectPlan: %v", err)
	}

	question, err := store.Question(ctx, fixture.question.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if question.State != QuestionAnsweredUninterpreted {
		t.Errorf("the question is %s after a rejection, want %s",
			question.State, QuestionAnsweredUninterpreted)
	}
	plan, err := store.Plan(ctx, fixture.plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.State != PlanRejected {
		t.Errorf("the plan is %s, want %s", plan.State, PlanRejected)
	}
	for _, action := range plan.Actions {
		if action.Kind.RequiresAcceptance() && action.State != ActionRejected {
			t.Errorf("action %s is %s after a rejection", action.Kind, action.State)
		}
	}
	if got := countRows(t, store, "reality_fact"); got != 0 {
		t.Errorf("%d facts exist after a rejection", got)
	}
	// The answer survives verbatim for another interpretation.
	answers, err := store.Answers(ctx, fixture.question.ID)
	if err != nil {
		t.Fatalf("Answers: %v", err)
	}
	if len(answers) != 1 || answers[0].ID != fixture.answer.ID {
		t.Errorf("the answer did not survive the rejection: %+v", answers)
	}

	// And it can be interpreted again.
	if err := store.BeginInterpretation(ctx, fixture.question.ID); err != nil {
		t.Errorf("re-interpreting after a rejection: %v", err)
	}
}

// TestInterpretationFailureLeavesTheAnswerForRetry is §4.8's other retry rule:
// when interpretation is unavailable or fails, the raw answer remains
// `answered-uninterpreted`.
func TestInterpretationFailureLeavesTheAnswerForRetry(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")
	question, err := store.Ask(ctx, refreshQuestion(service.ID, []string{"observation-1"}))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID, Author: "operator", At: clock.now(),
		Outcome: OutcomeAnswered, Text: "still deployed",
	}); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if err := store.BeginInterpretation(ctx, question.ID); err != nil {
		t.Fatalf("BeginInterpretation: %v", err)
	}
	if err := store.FailInterpretation(ctx, question.ID, "the worker was unavailable"); err != nil {
		t.Fatalf("FailInterpretation: %v", err)
	}
	current, err := store.Question(ctx, question.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if current.State != QuestionAnsweredUninterpreted {
		t.Errorf("the question is %s, want %s", current.State, QuestionAnsweredUninterpreted)
	}
	if err := store.BeginInterpretation(ctx, question.ID); err != nil {
		t.Errorf("retrying interpretation: %v", err)
	}
}

// TestPlanRetainsItsHypothesisInTheRealFrontier exercises the actual adapter
// against the actual frontier, sharing one durable file. It is what makes the
// injected sink a seam rather than a fiction, and it is also where the ordering
// constraint shows: the frontier write happens before the ledger's transaction
// opens, because the two components hold separate connections to one file.
func TestPlanRetainsItsHypothesisInTheRealFrontier(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sink, cleanup := openFrontier(t, dir)
	defer cleanup()

	clock := newClock()
	store, err := Open(dir, WithClock(clock.now), WithHypothesisSink(sink))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	project := mustEntity(t, store, EntityProject, "a project")
	question, err := store.Ask(ctx, QuestionInput{
		Kind:              KindAcquireContext,
		Class:             ClassCuriosity,
		Sensitivity:       SensitivityRoutine,
		ExpectedAuthority: AuthorityOperator,
		TargetEntityIDs:   []string{project.ID},
		Payload: QuestionPayload{
			Prompt:   "anything else worth knowing about this project?",
			WhyAsked: "broad discovery turned it up with no context",
		},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	answer, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID, Author: "operator", At: clock.now(),
		Outcome: OutcomeAnswered, Text: "it shares a deployment pipeline with the others",
	})
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if err := store.BeginInterpretation(ctx, question.ID); err != nil {
		t.Fatalf("BeginInterpretation: %v", err)
	}
	_, retained, err := store.RecordPlan(ctx, PlanInput{
		QuestionID:         question.ID,
		AnswerID:           answer.ID,
		InterpreterVersion: 1,
		Summary:            "one candidate hypothesis",
		Kinds:              []ActionKind{ActionCreateHypothesis},
		Actions: []ActionPayload{{
			Rationale: "a shared pipeline is worth a hypothesis",
			Hypothesis: &HypothesisDraft{
				RunID:             "run-1",
				Statement:         "projects sharing a deployment pipeline share its failures",
				OriginCues:        []string{"the operator's answer"},
				ProvisionalLabels: []string{"deployment"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if len(retained.HypothesisIDs) != 1 {
		t.Fatalf("retained %d hypotheses", len(retained.HypothesisIDs))
	}
	stored, err := sink.Store.Hypothesis(ctx, retained.HypothesisIDs[0])
	if err != nil {
		t.Fatalf("the frontier does not hold the retained candidate: %v", err)
	}
	if stored.Payload.Statement == "" || stored.RunID != "run-1" {
		t.Errorf("the frontier stored %+v", stored.Payload)
	}
	// §5.2 confines novelty and priority to ordering, and an interpreter has
	// no basis to estimate them, so they arrive unranked rather than ranked
	// lowest-but-stated.
	if stored.Payload.Novelty != 0 || stored.Payload.Priority != 0 {
		t.Errorf("the adapter invented ordering signals: %+v", stored.Payload)
	}
}

// TestPlanWithoutAHypothesisSinkIsRefused checks that the retention promise is
// not kept halfway: a plan that would create a candidate with nowhere to put it
// is refused rather than silently dropping the candidate.
func TestPlanWithoutAHypothesisSinkIsRefused(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	project := mustEntity(t, store, EntityProject, "a project")
	question, err := store.Ask(ctx, QuestionInput{
		Kind:              KindAcquireContext,
		Class:             ClassCuriosity,
		Sensitivity:       SensitivityRoutine,
		ExpectedAuthority: AuthorityOperator,
		TargetEntityIDs:   []string{project.ID},
		Payload:           QuestionPayload{Prompt: "p", WhyAsked: "w"},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	answer, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID, Author: "operator", At: clock.now(),
		Outcome: OutcomeAnswered, Text: "something",
	})
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if err := store.BeginInterpretation(ctx, question.ID); err != nil {
		t.Fatalf("BeginInterpretation: %v", err)
	}
	if _, _, err := store.RecordPlan(ctx, PlanInput{
		QuestionID:         question.ID,
		AnswerID:           answer.ID,
		InterpreterVersion: 1,
		Summary:            "s",
		Kinds:              []ActionKind{ActionCreateHypothesis},
		Actions: []ActionPayload{{
			Rationale:  "r",
			Hypothesis: &HypothesisDraft{RunID: "run-1", Statement: "a candidate"},
		}},
	}); !isErr(err, ErrNoHypothesisSink) {
		t.Errorf("got %v, want ErrNoHypothesisSink", err)
	}
}

// TestPlanActionVocabularyCannotBypassThePipelineOrPublish is §4.8's absolute
// limit, and §4.6's and decision 13's: a plan can never create a proposal and
// can never publish anything.
//
// The closed action vocabulary is the primary guarantee, so the test enumerates
// it and fails if a proposal-creating or publishing action ever appears. The
// second guarantee is the investigation request's shape: §4.8 lets a plan
// request that an issue-shaped output be investigated through the normal
// evidence pipeline, and an investigation that starts from no hypothesis would
// be the bypass of hypothesis → observation → finding → proposal.
func TestPlanActionVocabularyCannotBypassThePipelineOrPublish(t *testing.T) {
	forbidden := []string{"proposal", "publish", "issue", "export", "finding", "observation"}
	for _, kind := range ActionKinds() {
		for _, word := range forbidden {
			if strings.Contains(string(kind), word) {
				t.Errorf("the action vocabulary contains %q, which names %q", kind, word)
			}
		}
	}
	if len(ActionKinds()) != 11 {
		t.Errorf("the vocabulary has %d actions, want §4.8's 11", len(ActionKinds()))
	}

	gated := map[ActionKind]bool{
		ActionAssertFact:    true,
		ActionSupersedeFact: true,
		ActionDisputeFact:   true,
		ActionMergeEntities: true,
		ActionSplitEntity:   true,
		ActionChangeFocus:   true,
	}
	for _, kind := range ActionKinds() {
		if got := kind.RequiresAcceptance(); got != gated[kind] {
			t.Errorf("%s.RequiresAcceptance() = %v, want %v", kind, got, gated[kind])
		}
	}

	cases := []struct {
		name    string
		kind    ActionKind
		payload ActionPayload
	}{
		{"an investigation with no hypothesis", ActionRequestInvestigation, ActionPayload{
			Rationale: "r",
			Request:   &RequestDraft{Kind: RequestInvestigation, Guidance: "g"},
		}},
		{"a refinement naming no work", ActionRequestRefinement, ActionPayload{
			Rationale: "r",
			Request:   &RequestDraft{Kind: RequestRefinement, Guidance: "g"},
		}},
		{"a request kind that contradicts its action", ActionRequestRefinement, ActionPayload{
			Rationale: "r",
			Request:   &RequestDraft{Kind: RequestInvestigation, HypothesisID: "hyp-1", Guidance: "g"},
		}},
		{"a follow-up smuggling a fact", ActionAskFollowUp, ActionPayload{
			Rationale: "r",
			Fact:      &FactInput{SubjectID: "ent-1"},
			FollowUp:  &QuestionInput{},
		}},
		{"an interpretation attributing its own fact", ActionAssertFact, ActionPayload{
			Rationale: "r",
			Fact: &FactInput{
				SubjectID:   "ent-1",
				Predicate:   PredicateLifecycle,
				Value:       enum(LifecycleActive),
				ValidFrom:   baseTime,
				ObservedAt:  baseTime,
				Confidence:  ConfidenceHigh,
				Sensitivity: SensitivityRoutine,
				Authority:   Authority{Kind: AuthorityOperator, ID: "operator", At: baseTime},
			},
		}},
		{"an action with no rationale", ActionNone, ActionPayload{}},
		{"a no-action carrying a merge", ActionNone, ActionPayload{
			Rationale: "r",
			Merge:     &MergeInput{TargetID: "ent-1"},
		}},
		{"an unknown action kind", ActionKind("publish-issue"), ActionPayload{Rationale: "r"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAction(tc.kind, tc.payload); err == nil {
				t.Error("the action was accepted")
			}
		})
	}
}

// TestSnapshotRecordsTheContextBehindADeferral is §4.8's context snapshot: a
// deterministic deferral has to record the context that caused it, so the
// snapshot freezes the resolved identities, the facts read, the policy version,
// and the decisions — and the hypothesis is never touched.
func TestSnapshotRecordsTheContextBehindADeferral(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	if _, err := store.PutFocusRules(ctx, dormantLifecycleRules(1)); err != nil {
		t.Fatalf("PutFocusRules: %v", err)
	}

	permissive := mustEntity(t, store, EntityProject, "an active project")
	restricted := mustEntity(t, store, EntityProject, "a retired project")
	folded := mustEntity(t, store, EntityProject, "the retired project's old name")
	if _, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{folded.ID},
		TargetID:  restricted.ID,
		Actor:     "operator",
		Reason:    "renamed before it was retired",
	}); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}

	if _, _, err := store.AssertFact(ctx, operatorFact(permissive.ID, PredicateLifecycle,
		enum(LifecycleActive), clock.now())); err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	retiredFact, _, err := store.AssertFact(ctx, operatorFact(restricted.ID, PredicateLifecycle,
		enum(LifecycleRetired), clock.now()))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}

	asOf := clock.now()
	snapshot, err := store.CaptureSnapshot(ctx, SnapshotInput{
		HypothesisID:   "hyp-emergent",
		EntityIDs:      []string{permissive.ID, folded.ID},
		RuleSetVersion: 1,
		AsOf:           asOf,
		Note:           "deferring repository work on the retired project",
	})
	if err != nil {
		t.Fatalf("CaptureSnapshot: %v", err)
	}

	// The combined decision is the most restrictive: one excluded entity is
	// not unlocked by an active one beside it.
	if snapshot.Allowance != AllowanceExcluded {
		t.Errorf("the combined allowance is %s, want %s", snapshot.Allowance, AllowanceExcluded)
	}
	if len(snapshot.Entities) != 2 {
		t.Fatalf("the snapshot resolved %d entities", len(snapshot.Entities))
	}
	// Both the name the hypothesis mentioned and the identity it resolved to
	// are recorded, or a later reader cannot tell what was actually said.
	var sawFolded bool
	for _, entity := range snapshot.Entities {
		if entity.EntityID == folded.ID {
			sawFolded = true
			if entity.CanonicalID != restricted.ID {
				t.Errorf("the folded name resolved to %q, want %q",
					entity.CanonicalID, restricted.ID)
			}
			if entity.Allowance != AllowanceExcluded {
				t.Errorf("the retired project's allowance is %s", entity.Allowance)
			}
		}
	}
	if !sawFolded {
		t.Error("the snapshot lost the name the hypothesis mentioned")
	}
	found := false
	for _, id := range snapshot.FactIDs {
		if id == retiredFact.ID {
			found = true
		}
	}
	if !found {
		t.Error("the snapshot does not name the fact that caused the deferral")
	}
	if len(snapshot.Payload.Decisions) != 2 {
		t.Errorf("the snapshot froze %d decisions", len(snapshot.Payload.Decisions))
	}

	// It is immutable and re-readable.
	round, err := store.Snapshot(ctx, snapshot.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if round.Allowance != snapshot.Allowance || len(round.Entities) != len(snapshot.Entities) ||
		len(round.FactIDs) != len(snapshot.FactIDs) || !round.AsOf.Equal(asOf) {
		t.Errorf("the snapshot round-tripped as %+v", round)
	}
	if _, err := store.db.Exec(`UPDATE reality_snapshot SET allowance = ? WHERE id = ?`,
		string(AllowanceFull), snapshot.ID); err == nil {
		t.Error("a context snapshot accepted an update")
	}
	if _, err := store.db.Exec(`DELETE FROM reality_snapshot WHERE id = ?`, snapshot.ID); err == nil {
		t.Error("a context snapshot accepted a delete")
	}

	// A later ledger change does not rewrite the frozen decision, which is
	// what makes the deferral explainable after the fact.
	if _, err := store.SupersedeFact(ctx, SupersedeInput{
		PriorID: retiredFact.ID,
		Fact: operatorFact(restricted.ID, PredicateLifecycle,
			enum(LifecycleActive), clock.now().Add(time.Hour)),
	}); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}
	after, err := store.Snapshot(ctx, snapshot.ID)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if after.Allowance != AllowanceExcluded {
		t.Errorf("the frozen snapshot changed to %s after the ledger moved on", after.Allowance)
	}
}

// TestAcceptanceAppliesEveryAuthoritativeActionKind covers the applications the
// first fixture does not: a supersession, a dispute, a split, and a focus-policy
// change. Each is gated on the same single acceptance, and each is attributed to
// the accepting operator.
//
// The focus-policy action is the one worth reading twice. §4.8 makes the mapping
// from ledger state to expenditure a versioned artifact, so a policy change
// installs a new version rather than editing one — and the decision over the
// same unchanged ledger differs before and after, which is the whole reason the
// version exists.
func TestAcceptanceAppliesEveryAuthoritativeActionKind(t *testing.T) {
	ctx := context.Background()
	sink := &recordingSink{}
	store, clock := newStore(t, WithHypothesisSink(sink))

	if _, err := store.PutFocusRules(ctx, DefaultFocusRules()); err != nil {
		t.Fatalf("PutFocusRules: %v", err)
	}
	project := mustEntity(t, store, EntityProject, "a project")
	service := mustEntity(t, store, EntityService, "one service, or so it seemed")

	observed := clock.now()
	prior, _, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateLifecycle,
		enum(LifecycleActive), observed))
	if err != nil {
		t.Fatalf("AssertFact(prior): %v", err)
	}
	firstOwnership, _, err := store.AssertFact(ctx, operatorFact(project.ID, PredicateOwnership,
		enum(OwnershipOwned), observed))
	if err != nil {
		t.Fatalf("AssertFact(ownership): %v", err)
	}
	// A second ownership fact over a disjoint valid time, so the two are a
	// history rather than an automatic dispute — the plan's dispute action is
	// what puts them in conflict.
	secondInput := operatorFact(project.ID, PredicateOwnership, enum(OwnershipExternal), observed)
	secondInput.ValidFrom = observed.Add(-48 * time.Hour)
	secondInput.ValidUntil = observed.Add(-24 * time.Hour)
	secondOwnership, _, err := store.AssertFact(ctx, secondInput)
	if err != nil {
		t.Fatalf("AssertFact(second ownership): %v", err)
	}

	before, err := store.EvaluateFocus(ctx, FocusQuery{EntityID: project.ID, RuleSetVersion: 1})
	if err != nil {
		t.Fatalf("EvaluateFocus(before): %v", err)
	}
	if before.Allowance != AllowanceFull {
		t.Fatalf("the ledger already decides %s before any policy change", before.Allowance)
	}

	question, err := store.Ask(ctx, QuestionInput{
		Kind:              KindSetFocus,
		Class:             ClassMaintenance,
		Sensitivity:       SensitivityRoutine,
		ExpectedAuthority: AuthorityOperator,
		TargetEntityIDs:   []string{project.ID},
		TargetPredicates:  []Predicate{PredicateLifecycle},
		Payload: QuestionPayload{
			Prompt:   "how should dormant projects be treated?",
			WhyAsked: "several dormant projects are consuming repository work",
		},
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	answer, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID, Author: "operator", At: clock.now(),
		Outcome: OutcomeAnswered,
		Text:    "dormant means learn-only from now on; this one is dormant; and the service is really two",
	})
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if err := store.BeginInterpretation(ctx, question.ID); err != nil {
		t.Fatalf("BeginInterpretation: %v", err)
	}

	revisionAt := clock.now()
	plan, _, err := store.RecordPlan(ctx, PlanInput{
		QuestionID:         question.ID,
		AnswerID:           answer.ID,
		InterpreterVersion: 4,
		Summary:            "supersede, dispute, split, and install a new focus policy",
		Kinds: []ActionKind{
			ActionSupersedeFact, ActionDisputeFact, ActionSplitEntity, ActionChangeFocus,
		},
		Actions: []ActionPayload{
			{
				Rationale: "the operator said this project is dormant",
				Fact: &FactInput{
					SubjectID:   project.ID,
					Predicate:   PredicateLifecycle,
					Value:       enum(LifecycleDormant),
					ValidFrom:   revisionAt,
					ObservedAt:  revisionAt,
					Confidence:  ConfidenceHigh,
					Sensitivity: SensitivityRoutine,
				},
				PriorFactID: prior.ID,
			},
			{
				Rationale:      "the two ownership records cannot both describe the same arrangement",
				DisputeFactIDs: []string{firstOwnership.ID, secondOwnership.ID},
			},
			{
				Rationale: "the operator said the service is really two",
				Split: &SplitInput{
					ParentID: service.ID,
					Parts: []EntityInput{
						{Kind: EntityService, Payload: EntityPayload{DisplayName: "first service"}},
						{Kind: EntityService, Payload: EntityPayload{DisplayName: "second service"}},
					},
					Reason: "confirmed by the operator's answer",
				},
			},
			{
				Rationale: "the operator set an expenditure policy for dormancy",
				FocusRules: &FocusRuleSet{
					Version: 2,
					Default: AllowanceFull,
					Note:    "installed from an operator answer",
					Rules: []FocusRule{{
						Name:    "dormant-is-learn-only",
						When:    []FocusCondition{{Predicate: PredicateLifecycle, Equals: LifecycleDormant}},
						Then:    AllowanceLearnOnly,
						Because: "the operator stated dormant projects are learn-only",
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}

	// Nothing applied yet, including the policy version.
	if _, err := store.FocusRules(ctx, 2); !isErr(err, ErrUnknownRecord) {
		t.Errorf("the policy version exists before acceptance: %v", err)
	}
	if got := countRows(t, store, "reality_dispute"); got != 0 {
		t.Errorf("%d disputes exist before acceptance", got)
	}

	_, application, err := store.AcceptPlan(ctx, AcceptanceInput{
		PlanID: plan.ID, Actor: "operator", Note: "yes",
	})
	if err != nil {
		t.Fatalf("AcceptPlan: %v", err)
	}
	if len(application.FactIDs) != 1 || len(application.DisputeIDs) != 1 ||
		len(application.ResolutionIDs) != 1 || len(application.FocusVersions) != 1 {
		t.Fatalf("the application reports %+v", application)
	}

	// The supersession left its ancestor byte-identical and in force no
	// longer.
	ancestor, err := store.Fact(ctx, prior.ID)
	if err != nil {
		t.Fatalf("Fact(ancestor): %v", err)
	}
	if ancestor.Status != FactSuperseded || ancestor.Value.Enum != LifecycleActive {
		t.Errorf("the ancestor is %s with value %q", ancestor.Status, ancestor.Value.Enum)
	}
	revision, err := store.Fact(ctx, application.FactIDs[0])
	if err != nil {
		t.Fatalf("Fact(revision): %v", err)
	}
	if revision.Supersedes != prior.ID || revision.Status != FactActive {
		t.Errorf("the revision is %+v", revision)
	}
	if revision.Authority.ID != "operator" {
		t.Errorf("the revision is attributed to %q", revision.Authority.ID)
	}

	// The dispute applied.
	dispute, err := store.Dispute(ctx, application.DisputeIDs[0])
	if err != nil {
		t.Fatalf("Dispute: %v", err)
	}
	if len(dispute.FactIDs) != 2 || dispute.State != DisputeOpen {
		t.Errorf("dispute is %+v", dispute)
	}

	// The split applied and its parts exist.
	resolution, err := store.Resolution(ctx, application.ResolutionIDs[0])
	if err != nil {
		t.Fatalf("Resolution: %v", err)
	}
	if resolution.Kind != ResolutionSplit || len(resolution.ResultIDs) != 2 {
		t.Errorf("resolution is %+v", resolution)
	}
	for _, id := range resolution.ResultIDs {
		if _, err := store.Entity(ctx, id); err != nil {
			t.Errorf("a split part is missing: %v", err)
		}
	}

	// The policy version installed, and the same ledger now decides
	// differently under it than under version 1.
	if _, err := store.FocusRules(ctx, 2); err != nil {
		t.Fatalf("FocusRules(2): %v", err)
	}
	asOf := clock.now()
	underOne, err := store.EvaluateFocus(ctx, FocusQuery{
		EntityID: project.ID, RuleSetVersion: 1, AsOf: asOf,
	})
	if err != nil {
		t.Fatalf("EvaluateFocus(v1): %v", err)
	}
	underTwo, err := store.EvaluateFocus(ctx, FocusQuery{
		EntityID: project.ID, RuleSetVersion: 2, AsOf: asOf,
	})
	if err != nil {
		t.Fatalf("EvaluateFocus(v2): %v", err)
	}
	if underOne.Allowance != AllowanceFull {
		t.Errorf("version 1 now decides %s over the same ledger", underOne.Allowance)
	}
	if underTwo.Allowance != AllowanceLearnOnly || underTwo.RuleName != "dormant-is-learn-only" {
		t.Errorf("version 2 decides %s via %q", underTwo.Allowance, underTwo.RuleName)
	}
}
