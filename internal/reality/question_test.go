package reality

import (
	"context"
	"testing"
	"time"
)

func refreshQuestion(subject string, evidence []string) QuestionInput {
	return QuestionInput{
		Kind:              KindRefreshStale,
		Class:             ClassMaintenance,
		Sensitivity:       SensitivityRoutine,
		ExpectedAuthority: AuthorityOperator,
		TargetEntityIDs:   []string{subject},
		TargetPredicates:  []Predicate{PredicateDeploymentState},
		MaterialEvidence:  evidence,
		Payload: QuestionPayload{
			Prompt:   "is this service still deployed?",
			WhyAsked: "the recorded deployment state passed its refresh expectation",
		},
	}
}

// TestDeclinedQuestionIsSuppressedUntilMateriallyNewEvidence is §4.8's
// deduplication and suppression rule.
//
// Three distinct behaviours are checked, because conflating them is how an
// inbox becomes noise. An equivalent ask while a question is live is a
// duplicate: the operator already has it. An equivalent ask after a refusal is
// suppressed, and repeating the same evidence does not lift the suppression —
// that is exactly the repetition §4.8 exists to prevent. And an ask carrying
// evidence the refused question did not have is allowed, supersedes the refused
// one, and links back to it, so the refusal and the reason it was revisited stay
// connected.
func TestDeclinedQuestionIsSuppressedUntilMateriallyNewEvidence(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")

	first, err := store.Ask(ctx, refreshQuestion(service.ID, []string{"observation-1"}))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if first.State != QuestionOpen {
		t.Fatalf("a new question is %s, want %s", first.State, QuestionOpen)
	}

	// Live: a duplicate, not a suppression.
	if _, err := store.Ask(ctx, refreshQuestion(service.ID, []string{"observation-2"})); !isErr(err, ErrDuplicateQuestion) {
		t.Errorf("an equivalent ask while open returned %v, want ErrDuplicateQuestion", err)
	}

	// The operator declines it.
	if _, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: first.ID,
		Author:     "operator",
		At:         clock.now(),
		Outcome:    OutcomeDeclined,
		Text:       "not answering this one",
	}); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	declined, err := store.Question(ctx, first.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if declined.State != QuestionDeclined {
		t.Fatalf("the question is %s after a declined answer, want %s",
			declined.State, QuestionDeclined)
	}

	// Repetition is suppressed, whether it offers the same evidence or none.
	for _, evidence := range [][]string{{"observation-1"}, nil} {
		if _, err := store.Ask(ctx, refreshQuestion(service.ID, evidence)); !isErr(err, ErrSuppressed) {
			t.Errorf("an ask with evidence %v returned %v, want ErrSuppressed", evidence, err)
		}
	}

	// It is out of the inbox, too.
	inbox, err := store.Inbox(ctx, InboxQuery{})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(inbox) != 0 {
		t.Errorf("the inbox holds %d items, want none while the only question is declined", len(inbox))
	}

	// Materially new evidence lifts it.
	revived, err := store.Ask(ctx, refreshQuestion(service.ID,
		[]string{"observation-1", "observation-3"}))
	if err != nil {
		t.Fatalf("Ask with new evidence: %v", err)
	}
	if revived.PromptedByID != first.ID {
		t.Errorf("the new question links to %q, want the refused question %q",
			revived.PromptedByID, first.ID)
	}
	superseded, err := store.Question(ctx, first.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if superseded.State != QuestionSuperseded {
		t.Errorf("the refused question is %s, want %s", superseded.State, QuestionSuperseded)
	}
	// Its whole history is still there: asked, declined, superseded.
	history, err := store.QuestionHistory(ctx, first.ID)
	if err != nil {
		t.Fatalf("QuestionHistory: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("history has %d entries, want open/declined/superseded: %+v", len(history), history)
	}
	if history[0].State != QuestionOpen || history[1].State != QuestionDeclined ||
		history[2].State != QuestionSuperseded {
		t.Errorf("history is %s/%s/%s", history[0].State, history[1].State, history[2].State)
	}

	inbox, err = store.Inbox(ctx, InboxQuery{})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(inbox) != 1 || inbox[0].Question.ID != revived.ID {
		t.Errorf("the inbox holds %+v, want only the revived question", inbox)
	}
}

// TestUnknownOutcomeAlsoSuppressesRepeats covers the other outcome §4.8 names:
// an operator who does not know has answered, and re-asking immediately is the
// same repetition a refusal suppresses.
func TestUnknownOutcomeAlsoSuppressesRepeats(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")

	question, err := store.Ask(ctx, refreshQuestion(service.ID, []string{"observation-1"}))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if _, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID,
		Author:     "operator",
		At:         clock.now(),
		Outcome:    OutcomeUnknown,
		Text:       "no idea",
	}); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	// There is nothing to interpret, so the question is disposed of directly.
	answered, err := store.Question(ctx, question.ID)
	if err != nil {
		t.Fatalf("Question: %v", err)
	}
	if answered.State != QuestionAnswered {
		t.Errorf("an unknown answer left the question %s, want %s",
			answered.State, QuestionAnswered)
	}
	if _, err := store.Ask(ctx, refreshQuestion(service.ID, []string{"observation-1"})); !isErr(err, ErrSuppressed) {
		t.Errorf("re-asking after an unknown answer returned %v, want ErrSuppressed", err)
	}
	if _, err := store.Ask(ctx, refreshQuestion(service.ID, []string{"observation-9"})); err != nil {
		t.Errorf("re-asking with new evidence returned %v", err)
	}
}

// TestQuestionsAboutOneCanonicalIdentityDedupe checks that equivalence is
// computed over canonical entities: after a merge, a question about the folded
// name is the same question as one about the surviving name.
func TestQuestionsAboutOneCanonicalIdentityDedupe(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	folded := mustEntity(t, store, EntityService, "old service name")
	surviving := mustEntity(t, store, EntityService, "current service name")
	if _, err := store.MergeEntities(ctx, MergeInput{
		SourceIDs: []string{folded.ID},
		TargetID:  surviving.ID,
		Actor:     "operator",
		Reason:    "one service under two names",
	}); err != nil {
		t.Fatalf("MergeEntities: %v", err)
	}

	asked, err := store.Ask(ctx, refreshQuestion(folded.ID, []string{"observation-1"}))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(asked.TargetEntityIDs) != 1 || asked.TargetEntityIDs[0] != surviving.ID {
		t.Errorf("the question targets %v, want the canonical identity", asked.TargetEntityIDs)
	}
	if _, err := store.Ask(ctx, refreshQuestion(surviving.ID, []string{"observation-1"})); !isErr(err, ErrDuplicateQuestion) {
		t.Errorf("the same question about the surviving name returned %v, want a duplicate", err)
	}
}

// TestQuestionStateMachineRefusesIllegalTransitions pins §4.8's machine. The
// state is derived from an append-only history, so an illegal transition has to
// be refused at the write rather than recorded and ignored.
func TestQuestionStateMachineRefusesIllegalTransitions(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")

	question, err := store.Ask(ctx, refreshQuestion(service.ID, []string{"observation-1"}))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	// A plan cannot be interpreted before there is an answer.
	if err := store.BeginInterpretation(ctx, question.ID); !isErr(err, ErrInvalidTransition) {
		t.Errorf("interpreting an unanswered question returned %v, want ErrInvalidTransition", err)
	}

	// Snooze, then reopen: the one edge back into `open`.
	if err := store.SetQuestionState(ctx, QuestionStateInput{
		QuestionID: question.ID, State: QuestionSnoozed, Actor: "operator", Note: "later",
	}); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	snoozed, err := store.Inbox(ctx, InboxQuery{})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(snoozed) != 0 {
		t.Errorf("a snoozed question is in the inbox: %+v", snoozed)
	}
	if err := store.SetQuestionState(ctx, QuestionStateInput{
		QuestionID: question.ID, State: QuestionOpen, Actor: "operator", Note: "now",
	}); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	// Decline it, and check that a refusal cannot be quietly reopened: §4.8
	// lifts suppression by asking a new question backed by new evidence, not
	// by walking the refused one back to `open`.
	if err := store.SetQuestionState(ctx, QuestionStateInput{
		QuestionID: question.ID, State: QuestionDeclined, Actor: "operator", Note: "no",
	}); err != nil {
		t.Fatalf("decline: %v", err)
	}
	if err := store.SetQuestionState(ctx, QuestionStateInput{
		QuestionID: question.ID, State: QuestionOpen, Actor: "operator",
	}); !isErr(err, ErrInvalidTransition) {
		t.Errorf("reopening a declined question returned %v, want ErrInvalidTransition", err)
	}
	// And a declined question cannot be answered afterwards.
	if _, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID, Author: "operator", At: clock.now(),
		Outcome: OutcomeAnswered, Text: "actually, yes",
	}); !isErr(err, ErrInvalidTransition) {
		t.Errorf("answering a declined question returned %v, want ErrInvalidTransition", err)
	}

	// Obsolete is terminal.
	if err := store.SetQuestionState(ctx, QuestionStateInput{
		QuestionID: question.ID, State: QuestionObsolete, Actor: "operator", Note: "gone",
	}); err != nil {
		t.Fatalf("obsolete: %v", err)
	}
	for _, state := range []QuestionState{QuestionOpen, QuestionSnoozed, QuestionAnswered} {
		if err := store.SetQuestionState(ctx, QuestionStateInput{
			QuestionID: question.ID, State: state, Actor: "operator",
		}); !isErr(err, ErrInvalidTransition) {
			t.Errorf("obsolete -> %s returned %v, want ErrInvalidTransition", state, err)
		}
	}
}

// TestQuestionStateMachineCoversEverySpecifiedState checks that the machine
// carries all nine §4.8 states, so a state cannot be quietly dropped.
func TestQuestionStateMachineCoversEverySpecifiedState(t *testing.T) {
	specified := []QuestionState{
		QuestionOpen, QuestionAnsweredUninterpreted, QuestionInterpreting, QuestionPlanReady,
		QuestionAnswered, QuestionSnoozed, QuestionDeclined, QuestionObsolete, QuestionSuperseded,
	}
	if len(questionTransitions) != len(specified) {
		t.Errorf("the machine has %d states, want %d", len(questionTransitions), len(specified))
	}
	for _, state := range specified {
		if !state.valid() {
			t.Errorf("state %q is not in the machine", state)
		}
	}
	// Every terminal state is terminal on purpose, and every other state can
	// reach at least one of them, so no question can be stranded.
	for state, next := range questionTransitions {
		if len(next) == 0 && state != QuestionObsolete && state != QuestionSuperseded {
			t.Errorf("state %q has no outgoing transition", state)
		}
	}
}

// TestAnswerIsRetainedVerbatim is §4.8's retention rule. The text is stored
// exactly as the operator wrote it, including the whitespace and control bytes
// a terminal-safe renderer would have to handle later, and the row is
// immutable. Retention is not rendering: §9's terminal-safe renderer decides
// how this is displayed, and the ledger's job is to still have the bytes.
func TestAnswerIsRetainedVerbatim(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")
	question, err := store.Ask(ctx, refreshQuestion(service.ID, []string{"observation-1"}))
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	verbatim := "  it moved.\n\nsee the note\ttrailing spaces   \r\n\x1b[31mnot a colour instruction\x1b[0m"
	answer, err := store.RecordAnswer(ctx, AnswerInput{
		QuestionID: question.ID,
		Author:     "operator",
		At:         clock.now(),
		Outcome:    OutcomeAnswered,
		Text:       verbatim,
	})
	if err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
	if answer.Payload.Text != verbatim {
		t.Errorf("the returned answer was normalized:\n%q\nwant\n%q", answer.Payload.Text, verbatim)
	}

	stored, err := store.Answers(ctx, question.ID)
	if err != nil {
		t.Fatalf("Answers: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored %d answers, want 1", len(stored))
	}
	if stored[0].Payload.Text != verbatim {
		t.Errorf("the stored answer was normalized:\n%q\nwant\n%q", stored[0].Payload.Text, verbatim)
	}
	if stored[0].Author != "operator" || stored[0].At.IsZero() {
		t.Errorf("the answer lost its attribution: %+v", stored[0])
	}

	if _, err := store.db.Exec(`UPDATE reality_answer SET payload_json = '{}' WHERE id = ?`,
		answer.ID); err == nil {
		t.Error("an answer row accepted an update")
	}
	if _, err := store.db.Exec(`DELETE FROM reality_answer WHERE id = ?`, answer.ID); err == nil {
		t.Error("an answer row accepted a delete")
	}
}

// TestInboxRanksByTheFiveFactors pins §4.8's ranking. The ordering is the
// product here, so the test checks the order, the arithmetic behind it, and the
// class-blindness: a curiosity question with real security impact must be able
// to outrank a blocking one with none.
func TestInboxRanksByTheFiveFactors(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")
	project := mustEntity(t, store, EntityProject, "a project")
	machine := mustEntity(t, store, EntityMachine, "a machine")

	// A stale fact for the staleness term to measure.
	observed := clock.now()
	stale, _, err := store.AssertFact(ctx, operatorFact(service.ID, PredicateDeploymentState,
		enum(DeploymentDeployed), observed))
	if err != nil {
		t.Fatalf("AssertFact: %v", err)
	}
	asOf := observed.Add(20 * 24 * time.Hour)
	if _, err := store.ExpireStale(ctx, asOf); err != nil {
		t.Fatalf("ExpireStale: %v", err)
	}

	blocking := refreshQuestion(service.ID, []string{"observation-1"})
	blocking.Class = ClassBlocking
	blocking.DependentWork = []WorkRef{
		{Kind: WorkHypothesis, ID: "hyp-1", Blocking: true},
		{Kind: WorkHypothesis, ID: "hyp-2", Blocking: false},
	}
	blocking.ExistingFactIDs = []string{stale.ID}
	blockingQuestion, err := store.Ask(ctx, blocking)
	if err != nil {
		t.Fatalf("Ask(blocking): %v", err)
	}

	curiosity := refreshQuestion(project.ID, []string{"observation-2"})
	curiosity.Kind = KindAcquireContext
	curiosity.Class = ClassCuriosity
	curiosity.Sensitivity = SensitivityRestricted
	curiosity.AvoidedCost = 10
	curiosityQuestion, err := store.Ask(ctx, curiosity)
	if err != nil {
		t.Fatalf("Ask(curiosity): %v", err)
	}

	quiet := refreshQuestion(machine.ID, []string{"observation-3"})
	quiet.Kind = KindFactCheckDrift
	quiet.Class = ClassMaintenance
	quietQuestion, err := store.Ask(ctx, quiet)
	if err != nil {
		t.Fatalf("Ask(quiet): %v", err)
	}

	items, err := store.Inbox(ctx, InboxQuery{AsOf: asOf})
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("the inbox holds %d items, want 3", len(items))
	}

	wantOrder := []string{blockingQuestion.ID, curiosityQuestion.ID, quietQuestion.ID}
	for i, want := range wantOrder {
		if items[i].Question.ID != want {
			t.Errorf("inbox position %d is %q, want %q", i, items[i].Question.ID, want)
		}
	}

	// The arithmetic is the policy, so it is checked rather than trusted.
	first := items[0]
	wantTerms := map[string]int{
		"affected-work":    1 * weightBlockedWork,
		"security-impact":  0,
		"dependency-count": 2 * weightDependency,
		"staleness":        13 * weightStaleness,
		"avoided-cost":     0,
	}
	for name, want := range wantTerms {
		if first.Terms[name] != want {
			t.Errorf("blocking question term %q = %d, want %d", name, first.Terms[name], want)
		}
	}
	total := 0
	for _, value := range wantTerms {
		total += value
	}
	if first.Score != total {
		t.Errorf("blocking question score is %d, want %d", first.Score, total)
	}

	// A restricted curiosity question with no blocked work still outranks a
	// routine maintenance one, which is what class-blind ranking buys.
	if items[1].Terms["security-impact"] != SensitivityRestricted.weight()*weightSecurity {
		t.Errorf("curiosity security term is %d", items[1].Terms["security-impact"])
	}
	if items[1].Score <= items[2].Score {
		t.Errorf("a restricted question scored %d, not above the routine one's %d",
			items[1].Score, items[2].Score)
	}

	// The class filter narrows the view without changing the ranking policy.
	only, err := store.Inbox(ctx, InboxQuery{AsOf: asOf, Class: ClassCuriosity})
	if err != nil {
		t.Fatalf("Inbox(curiosity): %v", err)
	}
	if len(only) != 1 || only[0].Question.ID != curiosityQuestion.ID {
		t.Errorf("the class filter returned %+v", only)
	}
	limited, err := store.Inbox(ctx, InboxQuery{AsOf: asOf, Limit: 2})
	if err != nil {
		t.Fatalf("Inbox(limit): %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("the limit returned %d items", len(limited))
	}
}

// TestQuestionValidationRefusesAnUnanswerableQuestion checks the one authority
// rule a question carries: its expected authority must be able to authorize an
// answer, or asking it is pointless by §4.8's own rule.
func TestQuestionValidationRefusesAnUnanswerableQuestion(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	service := mustEntity(t, store, EntityService, "a service")

	unanswerable := refreshQuestion(service.ID, nil)
	unanswerable.ExpectedAuthority = AuthorityObservation
	if _, err := store.Ask(ctx, unanswerable); !isErr(err, ErrNotAuthoritative) {
		t.Errorf("got %v, want ErrNotAuthoritative", err)
	}

	noRationale := refreshQuestion(service.ID, nil)
	noRationale.Payload.WhyAsked = ""
	if _, err := store.Ask(ctx, noRationale); !isErr(err, ErrInvalidValue) {
		t.Errorf("a question with no stated reason returned %v", err)
	}

	noTarget := refreshQuestion(service.ID, nil)
	noTarget.TargetEntityIDs = nil
	if _, err := store.Ask(ctx, noTarget); !isErr(err, ErrInvalidValue) {
		t.Errorf("a question with no target returned %v", err)
	}
}
