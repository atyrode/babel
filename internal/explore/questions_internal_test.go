package explore

import (
	"context"
	"fmt"
	"testing"

	"github.com/atyrode/babel/internal/reality"
)

// The refusals below are unit-level because they are decisions Babel makes
// before the ledger is consulted at all, and each one is a boundary §4.8
// draws rather than an implementation detail: a run may not ask about nothing,
// and a run may not name a subject into existence.

// stubLedger answers subject resolution from a fixed table.
type stubLedger struct {
	entities map[string]string
}

func (s *stubLedger) ResolveSubject(_ context.Context, value string) (string, error) {
	if id, ok := s.entities[value]; ok {
		return id, nil
	}
	return "", fmt.Errorf("%w: no entity answers to that name", reality.ErrUnknownRecord)
}

func (s *stubLedger) Ask(context.Context, reality.QuestionInput) (reality.Question, error) {
	return reality.Question{}, nil
}

func TestAQuestionWithNoSubjectIsRefused(t *testing.T) {
	c := &Controller{cfg: Config{Questions: &stubLedger{}}}
	st := &state{ctx: context.Background(), hypotheses: map[string]string{}}
	if _, err := c.question(st, QuestionDraft{Ref: "q-1", Prompt: "which?", WhyAsked: "because"}); err == nil {
		t.Fatal("a question naming no subject was accepted; nothing in the ledger could answer it")
	}
}

func TestAQuestionNamingAnUnknownSubjectIsRefused(t *testing.T) {
	c := &Controller{cfg: Config{Questions: &stubLedger{entities: map[string]string{"dev-01": "ent-1"}}}}
	st := &state{ctx: context.Background(), hypotheses: map[string]string{}}
	_, err := c.question(st, QuestionDraft{
		Ref: "q-1", Subjects: []string{"a-machine-nobody-declared"},
		Prompt: "which?", WhyAsked: "because",
	})
	if err == nil {
		t.Fatal("a question minted its own subject; §4.8 puts entity creation behind an operator act")
	}
}

// A question that blocks a candidate is ranked above one that does not, and
// the ranking is derived from the work rather than declared by the model.
func TestABlockingQuestionIsClassedByTheWorkItHoldsUp(t *testing.T) {
	c := &Controller{cfg: Config{Questions: &stubLedger{entities: map[string]string{"dev-01": "ent-1"}}}}
	st := &state{ctx: context.Background(), hypotheses: map[string]string{"c-1": "hyp-9"}}

	curious, err := c.question(st, QuestionDraft{
		Ref: "q-1", Subjects: []string{"dev-01"}, Prompt: "what for?", WhyAsked: "unclear",
	})
	if err != nil {
		t.Fatalf("question: %v", err)
	}
	if curious.Class != reality.ClassCuriosity {
		t.Errorf("a question holding up nothing is %q, want curiosity", curious.Class)
	}
	if len(curious.MaterialEvidence) != 0 {
		t.Errorf("a question with no blocked record carries evidence %v, so a decline could be reopened by re-asking",
			curious.MaterialEvidence)
	}

	blocking, err := c.question(st, QuestionDraft{
		Ref: "q-2", Subjects: []string{"dev-01"}, Hypothesis: "c-1",
		Prompt: "which host?", WhyAsked: "the claim depends on it",
	})
	if err != nil {
		t.Fatalf("question: %v", err)
	}
	if blocking.Class != reality.ClassBlocking {
		t.Errorf("a question holding up a candidate is %q, want blocking", blocking.Class)
	}
	if len(blocking.DependentWork) != 1 || blocking.DependentWork[0].ID != "hyp-9" {
		t.Errorf("dependent work = %+v, want the durable id of the candidate the run wrote", blocking.DependentWork)
	}
	if !blocking.DependentWork[0].Blocking {
		t.Error("the blocked candidate is not marked blocking, so §4.8's first ranking factor sees nothing")
	}
}
