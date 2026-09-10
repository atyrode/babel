package explore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// TestARunRaisesAQuestionIntoTheInbox is §4.8's producer end to end: a real
// three-stage-capable run, a real ledger, and an open Question at the end of
// it that nobody typed.
//
// It is the half the Reality Ledger was missing. Every one of its tables was
// tested and none of them could be reached, because the only ways a Question
// could come into existence were an operator typing one and a stale-fact pass
// over facts nobody had imported. This is the third way, and the constraint
// that makes it safe is visible in the same test: the run cites a subject the
// operator declared, and the question it raises authorizes nothing.
func TestARunRaisesAQuestionIntoTheInbox(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	ledger, err := reality.Open(h.dir)
	if err != nil {
		t.Fatalf("open reality: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })

	// The operator's act, which is the only thing that can create a subject.
	machine, err := ledger.CreateEntity(ctx, reality.EntityInput{
		Kind:    reality.EntityMachine,
		Payload: reality.EntityPayload{DisplayName: "dev-01"},
	})
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}
	if _, err := ledger.AddAlias(ctx, reality.AliasInput{
		EntityID: machine.ID,
		Kind:     reality.AliasHostname,
		Payload:  reality.AliasPayload{Value: "dev-01"},
	}); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	result := h.discovery()
	result.Questions = []explore.QuestionDraft{
		{
			Ref:        "q-1",
			Subjects:   []string{"dev-01"},
			Hypothesis: "c-1",
			Prompt:     "Does the archive job still run on dev-01, or did it move?",
			WhyAsked:   "Two sessions three weeks apart name different hosts and neither says which is current.",
		},
		{
			// Refused: the ledger has never heard of this machine, and a
			// run does not get to mint one.
			Ref:      "q-2",
			Subjects: []string{"a-host-nobody-declared"},
			Prompt:   "What is this for?",
			WhyAsked: "It appears once and is never explained.",
		},
	}
	payload := h.writeResult("questions.json", result)
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		func(cfg *explore.Config) { cfg.Questions = ledger },
	)

	outcome, err := controller.Explore(ctx, explore.Options{Authority: testAuthority, RunID: "r-question"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Questions) != 1 {
		t.Fatalf("the run raised %d questions, want the one whose subject the ledger holds", len(outcome.Questions))
	}
	if !hasFailure(outcome.Failures, explore.FailureQuestion) {
		t.Error("the question naming an undeclared subject was not recorded as a refusal")
	}
	// The refusal is per item: the candidates beside it are durable.
	if len(outcome.Hypotheses) == 0 {
		t.Error("a refused question took the run's candidates down with it")
	}

	// The question is in the operator's inbox, ranked and answerable.
	inbox, err := ledger.Inbox(ctx, reality.InboxQuery{})
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	var found *reality.Question
	for i := range inbox {
		if inbox[i].Question.ID == outcome.Questions[0] {
			found = &inbox[i].Question
		}
	}
	if found == nil {
		t.Fatalf("the run's question is not in the inbox; inbox holds %d", len(inbox))
	}
	if found.Class != reality.ClassBlocking {
		t.Errorf("class = %q, want blocking: the question names the candidate it holds up", found.Class)
	}
	if found.ExpectedAuthority != reality.AuthorityOperator {
		t.Errorf("expected authority = %q, want the operator: no model may answer this", found.ExpectedAuthority)
	}
	if !strings.Contains(found.Payload.Prompt, "archive job") {
		t.Errorf("prompt = %q, which is not what the run asked", found.Payload.Prompt)
	}
	if len(found.TargetEntityIDs) != 1 || found.TargetEntityIDs[0] != machine.ID {
		t.Errorf("targets = %v, want the entity the alias resolved to (%s)", found.TargetEntityIDs, machine.ID)
	}
	if len(found.DependentWork) != 1 || !found.DependentWork[0].Blocking {
		t.Fatalf("dependent work = %+v, want the blocked candidate", found.DependentWork)
	}
	// The blocked record is the run's own durable hypothesis, not the ref.
	blocked := found.DependentWork[0].ID
	if _, err := h.frontier.Hypothesis(ctx, blocked); err != nil {
		t.Errorf("the question names %q as blocked work, which is not a durable record: %v", blocked, err)
	}
	var statements []string
	for _, id := range outcome.Hypotheses {
		hyp, err := h.frontier.Hypothesis(ctx, id)
		if err != nil {
			t.Fatalf("read hypothesis: %v", err)
		}
		statements = append(statements, hyp.Payload.Statement)
	}
	if blocked != outcome.Hypotheses[0] {
		t.Errorf("the question blocks %q, want the candidate it named (%q of %v)",
			blocked, outcome.Hypotheses[0], statements)
	}
	// And the ledger gained no facts: asking authorizes nothing.
	facts, err := ledger.Facts(ctx, reality.FactQuery{SubjectID: machine.ID})
	if err != nil {
		t.Fatalf("read facts: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("the run's question left %d facts behind; a question asserts nothing", len(facts))
	}
	_ = frontier.OutputHypothesis
}
