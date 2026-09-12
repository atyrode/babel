package reality

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
)

// stubFrontier stands in for internal/frontier: it records the settlements an
// acceptance performed and answers where a candidate currently stands. The
// frontier's own status rules are its to test; what is under test here is that
// an acceptance settles exactly what the plan named, and refuses when the
// frontier has moved past it.
type stubFrontier struct {
	status   map[string]frontier.Status
	settled  []Settlement
	failFrom string
}

func (s *stubFrontier) Settle(_ context.Context, in Settlement) error {
	if in.HypothesisID == s.failFrom {
		return errors.New("the frontier refused this status")
	}
	s.settled = append(s.settled, in)
	if s.status == nil {
		s.status = map[string]frontier.Status{}
	}
	s.status[in.HypothesisID] = in.Status
	return nil
}

func (s *stubFrontier) Status(_ context.Context, id string) (frontier.Status, error) {
	if status, ok := s.status[id]; ok {
		return status, nil
	}
	return "", errors.New("no such candidate")
}

func deferredFrontier(ids ...string) *stubFrontier {
	out := &stubFrontier{status: map[string]frontier.Status{}}
	for _, id := range ids {
		out.status[id] = frontier.StatusDeferred
	}
	return out
}

// Each of the four acts is applied by the operator's acceptance and by nothing
// else, and each settles exactly what it named.
func TestApplyingABacklogPlanPerformsItsAct(t *testing.T) {
	ctx := context.Background()

	t.Run("consolidate promotes every candidate it folds", func(t *testing.T) {
		store, _ := newStore(t)
		front := deferredFrontier("hyp_1", "hyp_2")
		plan := BacklogPlan{
			ProposalID: "prp_1", Operation: BacklogConsolidate,
			Hypotheses: []string{"hyp_1", "hyp_2"}, Finding: "fnd_1",
			Reasoning: "both observe the same reset", Evidence: 4,
		}
		if err := store.ProposeBacklog(ctx, plan); err != nil {
			t.Fatalf("ProposeBacklog: %v", err)
		}
		acceptance, err := store.ApplyBacklogPlan(ctx, "prp_1", "alex", front)
		if err != nil {
			t.Fatalf("ApplyBacklogPlan: %v", err)
		}
		if len(acceptance.Settled) != 2 || acceptance.Status != frontier.StatusPromoted {
			t.Fatalf("acceptance = %+v", acceptance)
		}
		for _, settlement := range front.settled {
			if settlement.Operator != "alex" {
				t.Errorf("the status event is attributed to %q, not the accepting operator",
					settlement.Operator)
			}
		}
		read, _, err := store.BacklogPlan(ctx, "prp_1")
		if err != nil {
			t.Fatalf("BacklogPlan: %v", err)
		}
		if read.State != RulingApplied || read.RuledBy != "alex" {
			t.Fatalf("plan state = %+v", read)
		}
	})

	t.Run("supersede links and settles the older candidate", func(t *testing.T) {
		store, _ := newStore(t)
		front := deferredFrontier("hyp_1")
		front.status["hyp_9"] = frontier.StatusQueued
		plan := BacklogPlan{
			ProposalID: "prp_2", Operation: BacklogSupersede,
			Hypotheses: []string{"hyp_1"}, SupersededBy: "hyp_9",
			Reasoning: "hyp_9 says it with the evidence", Evidence: 2,
		}
		if err := store.ProposeBacklog(ctx, plan); err != nil {
			t.Fatalf("ProposeBacklog: %v", err)
		}
		if _, err := store.ApplyBacklogPlan(ctx, "prp_2", "alex", front); err != nil {
			t.Fatalf("ApplyBacklogPlan: %v", err)
		}
		if len(front.settled) != 1 {
			t.Fatalf("settled %d candidates, want the older one alone", len(front.settled))
		}
		settlement := front.settled[0]
		if settlement.HypothesisID != "hyp_1" || settlement.Status != frontier.StatusSuperseded ||
			settlement.SupersededBy != "hyp_9" {
			t.Fatalf("settlement = %+v", settlement)
		}
	})

	t.Run("retire settles the candidate with the reason", func(t *testing.T) {
		store, _ := newStore(t)
		front := deferredFrontier("hyp_1")
		plan := BacklogPlan{
			ProposalID: "prp_3", Operation: BacklogRetire, Hypotheses: []string{"hyp_1"},
			Reasoning: "the component it names was removed", Evidence: 0,
		}
		if err := store.ProposeBacklog(ctx, plan); err != nil {
			t.Fatalf("ProposeBacklog: %v", err)
		}
		if _, err := store.ApplyBacklogPlan(ctx, "prp_3", "alex", front); err != nil {
			t.Fatalf("ApplyBacklogPlan: %v", err)
		}
		settlement := front.settled[0]
		if settlement.Status != frontier.StatusRetired ||
			settlement.Reason != "the component it names was removed" {
			t.Fatalf("settlement = %+v", settlement)
		}
	})

	t.Run("promote asserts the fact under the operator's authority", func(t *testing.T) {
		store, _ := newStore(t)
		entity := mustEntity(t, store, EntityRepository, "manifold")
		front := deferredFrontier("hyp_1")
		plan := BacklogPlan{
			ProposalID: "prp_4", Operation: BacklogPromote, Hypotheses: []string{"hyp_1"},
			Observation: "obs_1",
			Fact: &FactInput{
				SubjectID: entity.ID,
				Predicate: PredicateLocalPath,
				Value:     FactValue{Kind: ValueText, Text: "/home/alex/src/manifold"},
			},
			Reasoning: "it stays true until the checkout moves", Evidence: 1,
		}
		if err := store.ProposeBacklog(ctx, plan); err != nil {
			t.Fatalf("ProposeBacklog: %v", err)
		}
		acceptance, err := store.ApplyBacklogPlan(ctx, "prp_4", "alex", front)
		if err != nil {
			t.Fatalf("ApplyBacklogPlan: %v", err)
		}
		if acceptance.FactID == "" {
			t.Fatal("the acceptance recorded no fact")
		}
		if acceptance.Fact.Authority.Kind != AuthorityOperator || acceptance.Fact.Authority.ID != "alex" {
			t.Fatalf("the fact is attributed to %+v, not the accepting operator",
				acceptance.Fact.Authority)
		}
		facts, err := store.Facts(ctx, FactQuery{SubjectID: entity.ID, Predicate: PredicateLocalPath})
		if err != nil || len(facts) != 1 {
			t.Fatalf("the ledger holds %d facts (%v)", len(facts), err)
		}
	})
}

// A plan the frontier has moved past is refused, and the refusal names the
// state — which is what tells the operator to let Babel look again.
func TestApplyingABacklogPlanRefusesASettledCandidate(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	front := deferredFrontier("hyp_1")
	plan := BacklogPlan{
		ProposalID: "prp_1", Operation: BacklogRetire, Hypotheses: []string{"hyp_1"},
		Reasoning: "the component was removed", Evidence: 1,
	}
	if err := store.ProposeBacklog(ctx, plan); err != nil {
		t.Fatalf("ProposeBacklog: %v", err)
	}
	front.status["hyp_1"] = frontier.StatusPromoted

	_, err := store.ApplyBacklogPlan(ctx, "prp_1", "alex", front)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if err == nil || !strings.Contains(err.Error(), "already promoted") {
		t.Fatalf("the refusal does not name the state: %v", err)
	}
	if len(front.settled) != 0 {
		t.Fatal("a refused acceptance settled a candidate anyway")
	}
	read, _, _ := store.BacklogPlan(ctx, "prp_1")
	if read.State != RulingOpen {
		t.Fatalf("a refused acceptance ruled the plan %s", read.State)
	}
}

// Two runs proposing the same act are one decision, and a declined act stays
// declined until more evidence stands behind it.
func TestBacklogPlansDeduplicateAndSuppress(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t)
	first := BacklogPlan{
		ProposalID: "prp_1", Operation: BacklogRetire, Hypotheses: []string{"hyp_1"},
		Reasoning: "nothing was ever observed", Evidence: 2,
	}
	if err := store.ProposeBacklog(ctx, first); err != nil {
		t.Fatalf("ProposeBacklog: %v", err)
	}
	second := first
	second.ProposalID = "prp_2"
	second.Reasoning = "another run reached the same act"
	if err := store.ProposeBacklog(ctx, second); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second unruled plan for one act = %v, want ErrConflict", err)
	}

	if err := store.DeclineBacklogPlan(ctx, "prp_1", "alex", "the question is still open"); err != nil {
		t.Fatalf("DeclineBacklogPlan: %v", err)
	}
	if err := store.ProposeBacklog(ctx, second); !errors.Is(err, ErrSuppressed) {
		t.Fatalf("re-proposing a declined act with no new evidence = %v, want ErrSuppressed", err)
	}
	second.Evidence = 5
	if err := store.ProposeBacklog(ctx, second); err != nil {
		t.Fatalf("materially more evidence must lift the suppression: %v", err)
	}

	open, err := store.OpenBacklogPlans(ctx)
	if err != nil {
		t.Fatalf("OpenBacklogPlans: %v", err)
	}
	if len(open) != 1 || open[0].ProposalID != "prp_2" {
		t.Fatalf("open plans = %+v, want only the unruled one", open)
	}
	declined, err := store.DeclinedBacklogPlans(ctx, 0)
	if err != nil || len(declined) != 1 || declined[0].Reason != "the question is still open" {
		t.Fatalf("declined = %+v (%v), want the operator's reason verbatim", declined, err)
	}
}
