package explore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/worker"
)

// stubBacklog records what the backlog pass asked the stores to do. It stands
// in for internal/frontier and internal/reality because what is under test is
// the mapping from one recipe result to one plan; the stores' own rules are
// theirs to test.
type stubBacklog struct {
	material BacklogMaterial
	proposed []BacklogPlan
	proposal string
	err      error
}

func (s *stubBacklog) Material(context.Context, evaluation.Subject) (BacklogMaterial, error) {
	return s.material, nil
}

func (s *stubBacklog) Propose(_ context.Context, plan BacklogPlan) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.proposed = append(s.proposed, plan)
	return s.proposal, nil
}

// backlogMaterial is what a pass is shown in these tests: one deferred
// candidate with two observations, two neighbours, one entity and the ledger's
// predicates.
func backlogMaterial() BacklogMaterial {
	return BacklogMaterial{
		Candidate: BacklogCandidate{
			ID:           "hyp_1",
			Statement:    "reconnects reset the retry backoff",
			Status:       string(frontier.StatusDeferred),
			DeferredAt:   time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC),
			Note:         "out of budget",
			Observations: 2,
			Topics:       []string{"manifold"},
		},
		Observations: []BacklogObservation{
			{ID: "obs_1", Claim: "the counter re-initializes on the reconnect path"},
			{ID: "obs_2", Claim: "the checkout lives at /home/alex/src/manifold"},
		},
		Siblings: []BacklogCandidate{
			{ID: "hyp_2", Statement: "the backoff is lost across reconnects",
				Status: string(frontier.StatusDeferred), Observations: 1},
			{ID: "hyp_3", Statement: "reconnect handling re-initializes the retry counter",
				Status: string(frontier.StatusQueued), Observations: 3},
		},
		Entities: []LedgerTopic{{
			ID: "ent_1", Name: "manifold", Kind: "repository",
			Aliases: []string{"manifold-cli"}, Binding: "repository github.com/atyrode/manifold",
		}},
		Predicates: []BacklogPredicate{
			{Name: "local-path", Kind: "text", Why: "checkouts move"},
			{Name: "lifecycle", Kind: "enum", Values: []string{"active", "dormant"}},
		},
	}
}

func backlogState(stub *stubBacklog) (*Reviewer, *reviewState) {
	if stub.material.Candidate.ID == "" {
		stub.material = backlogMaterial()
	}
	reviewer := &Reviewer{cfg: ReviewConfig{Backlog: stub}}
	material := stub.material
	st := &reviewState{
		commit:   context.Background(),
		backlog:  &material,
		evidence: material.evidence(),
		opt: ReviewOptions{Assignment: evaluation.Assignment{
			ID:      "asg_1",
			RunID:   "eval-run-1",
			Role:    evaluation.RoleBacklog,
			Subject: evaluation.Subject{Kind: "hypothesis", ID: "hyp_1"},
		}},
	}
	return reviewer, st
}

// Each of the recipe's five answers reaches exactly one plan, and the
// evaluation record says which answer it was.
func TestEachBacklogAnswerMintsItsPlan(t *testing.T) {
	t.Run("consolidate", func(t *testing.T) {
		stub := &stubBacklog{proposal: "prp_1"}
		reviewer, st := backlogState(stub)
		act, err := reviewer.settle(st, &ReviewResult{Consolidate: &BacklogConsolidation{
			Hypotheses: []string{"hyp_2"},
			Finding: FindingDraft{
				Title: "the backoff is reset on reconnect", Pattern: "three candidates observe it",
				WhyItMatters: "a failing endpoint is retried at full rate",
			},
		}})
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
		plan := stub.proposed[0]
		if plan.Operation != BacklogConsolidate {
			t.Fatalf("operation = %q", plan.Operation)
		}
		// The drawn candidate is folded whether or not the pass listed it:
		// a consolidation that settled every candidate but the one it was
		// drawn for would leave this backlog entry where it was.
		if len(plan.Hypotheses) != 2 || plan.Hypotheses[0].ID != "hyp_1" ||
			plan.Hypotheses[1].ID != "hyp_2" {
			t.Fatalf("folded %+v, want the drawn candidate and the one it named", plan.Hypotheses)
		}
		if plan.Finding == nil || plan.Finding.Title == "" {
			t.Fatal("the consolidation carries no finding")
		}
		if act.Outcome != evaluation.BacklogProposed || act.Proposal != "prp_1" ||
			act.Operation != string(BacklogConsolidate) {
			t.Fatalf("record = %+v", act)
		}
		if len(act.Hypotheses) != 2 {
			t.Fatalf("the record names %d candidates, want both", len(act.Hypotheses))
		}
	})

	t.Run("supersede", func(t *testing.T) {
		stub := &stubBacklog{proposal: "prp_2"}
		reviewer, st := backlogState(stub)
		act, err := reviewer.settle(st, &ReviewResult{Supersede: &Supersession{
			By: "hyp_3", Reason: "it names the counter and cites the path",
		}})
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
		plan := stub.proposed[0]
		if plan.Operation != BacklogSupersede || plan.By == nil || plan.By.ID != "hyp_3" {
			t.Fatalf("plan = %+v", plan)
		}
		if len(plan.Hypotheses) != 1 || plan.Hypotheses[0].ID != "hyp_1" {
			t.Fatalf("a supersession settles the drawn candidate alone: %+v", plan.Hypotheses)
		}
		if act.Operation != string(BacklogSupersede) || act.Reason == "" {
			t.Fatalf("record = %+v", act)
		}
	})

	t.Run("retire", func(t *testing.T) {
		stub := &stubBacklog{proposal: "prp_3"}
		reviewer, st := backlogState(stub)
		act, err := reviewer.settle(st, &ReviewResult{Retire: &Retirement{
			Reason: "the component it names was removed and nothing was ever observed",
		}})
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
		plan := stub.proposed[0]
		if plan.Operation != BacklogRetire || plan.By != nil || plan.Finding != nil {
			t.Fatalf("plan = %+v", plan)
		}
		if act.Operation != string(BacklogRetire) {
			t.Fatalf("record = %+v", act)
		}
	})

	t.Run("promote", func(t *testing.T) {
		stub := &stubBacklog{proposal: "prp_4"}
		reviewer, st := backlogState(stub)
		act, err := reviewer.settle(st, &ReviewResult{Promote: &Promotion{
			Observation: "obs_2", Entity: "manifold-cli", Predicate: "local-path",
			Value: "/home/alex/src/manifold", Reason: "it stays true until the checkout moves",
		}})
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
		plan := stub.proposed[0]
		// The alias resolves to the entity the ledger holds, and the
		// observation is the candidate's own.
		if plan.Entity == nil || plan.Entity.ID != "ent_1" {
			t.Fatalf("entity = %+v, want the entity the alias names", plan.Entity)
		}
		if plan.Observation == nil || plan.Observation.ID != "obs_2" {
			t.Fatalf("observation = %+v", plan.Observation)
		}
		if plan.Predicate != "local-path" || plan.Value != "/home/alex/src/manifold" {
			t.Fatalf("fact = %s %s", plan.Predicate, plan.Value)
		}
		if act.Operation != string(BacklogPromote) {
			t.Fatalf("record = %+v", act)
		}
	})

	t.Run("keep", func(t *testing.T) {
		stub := &stubBacklog{proposal: "prp_5"}
		reviewer, st := backlogState(stub)
		act, err := reviewer.settle(st, &ReviewResult{Keep: &Kept{
			Reason: "the question is open and nothing supersedes it",
		}})
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
		// Keeping a candidate writes nothing anywhere else: the assessment
		// is the whole record that a pass read it and left it alone.
		if len(stub.proposed) != 0 {
			t.Fatalf("keeping a candidate proposed %d acts", len(stub.proposed))
		}
		if act.Outcome != evaluation.BacklogKept || act.Proposal != "" {
			t.Fatalf("record = %+v", act)
		}
	})
}

// An identifier the material does not hold is refused as a malformed result
// rather than carried into a plan whose acceptance settles records.
func TestBacklogActsRefuseWhatTheyWereNotShown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result ReviewResult
	}{
		{"a candidate nobody showed it", ReviewResult{Consolidate: &BacklogConsolidation{
			Hypotheses: []string{"hyp_2", "hyp_absent"},
			Finding:    FindingDraft{Title: "t", Pattern: "p", WhyItMatters: "w"},
		}}},
		{"a successor nobody showed it", ReviewResult{Supersede: &Supersession{
			By: "hyp_absent", Reason: "r"}}},
		{"superseding itself", ReviewResult{Supersede: &Supersession{
			By: "hyp_1", Reason: "r"}}},
		{"an observation under another candidate", ReviewResult{Promote: &Promotion{
			Observation: "obs_absent", Entity: "manifold", Predicate: "local-path",
			Value: "/tmp/x", Reason: "r"}}},
		{"an entity the ledger does not hold", ReviewResult{Promote: &Promotion{
			Observation: "obs_1", Entity: "atlas", Predicate: "local-path",
			Value: "/tmp/x", Reason: "r"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubBacklog{proposal: "prp_x"}
			reviewer, st := backlogState(stub)
			result := tc.result
			if _, err := reviewer.settle(st, &result); !errors.Is(err, ErrBacklogResult) {
				t.Fatalf("error = %v, want ErrBacklogResult", err)
			}
			if len(stub.proposed) != 0 {
				t.Fatal("a refused act reached the store anyway")
			}
		})
	}
}

// The result contract: one of five answers, each carrying what an operator
// would need to rule on it.
func TestTheBacklogResultStatesOneAnswer(t *testing.T) {
	parse := func(t *testing.T, payload string) (*ReviewResult, error) {
		t.Helper()
		return parseReviewResult(&worker.ResultRecord{
			Schema:  ReviewResultSchema,
			Payload: json.RawMessage(payload),
		}, evaluation.RoleBacklog, reviewSelf{})
	}
	t.Run("two answers are refused", func(t *testing.T) {
		_, err := parse(t, `{"retire":{"reason":"r"},"keep":{"reason":"k"}}`)
		if err == nil || !strings.Contains(err.Error(), "not 2 of them") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("no answer is refused", func(t *testing.T) {
		if _, err := parse(t, `{}`); !errors.Is(err, ErrReviewEmpty) {
			t.Fatalf("error = %v, want ErrReviewEmpty", err)
		}
	})
	t.Run("a consolidation of one candidate is refused", func(t *testing.T) {
		_, err := parse(t, `{"consolidate":{"hypotheses":["hyp_2"],
			"finding":{"title":"t","pattern":"p","why_it_matters":"w"}}}`)
		if err == nil || !strings.Contains(err.Error(), "at least two candidates") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a predicate outside the ledger's vocabulary is refused", func(t *testing.T) {
		_, err := parse(t, `{"promote":{"observation":"obs_1","entity":"manifold",
			"predicate":"vibes","value":"good","reason":"r"}}`)
		if err == nil || !strings.Contains(err.Error(), "not a predicate the ledger admits") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a retirement with no reason is refused", func(t *testing.T) {
		_, err := parse(t, `{"retire":{"reason":"  "}}`)
		if err == nil || !strings.Contains(err.Error(), "reason a reader could check") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a backlog act from a reviewing role is refused", func(t *testing.T) {
		_, err := parseReviewResult(&worker.ResultRecord{
			Schema:  ReviewResultSchema,
			Payload: json.RawMessage(`{"keep":{"reason":"r"}}`),
		}, evaluation.RoleReception, reviewSelf{})
		if !errors.Is(err, ErrReviewRole) {
			t.Fatalf("error = %v, want ErrReviewRole", err)
		}
	})
	t.Run("each answer parses on its own", func(t *testing.T) {
		for _, payload := range []string{
			`{"consolidate":{"hypotheses":["hyp_1","hyp_2"],
				"finding":{"title":"t","pattern":"p","why_it_matters":"w","scope":["babel"]}}}`,
			`{"supersede":{"by":"hyp_3","reason":"it cites the path"}}`,
			`{"retire":{"reason":"the component was removed"}}`,
			`{"promote":{"observation":"obs_2","entity":"manifold","predicate":"local-path",
				"value":"/home/alex/src/manifold","reason":"it stays true until it moves"}}`,
			`{"keep":{"reason":"the question is still open"}}`,
		} {
			if _, err := parse(t, payload); err != nil {
				t.Fatalf("parse %s: %v", payload, err)
			}
		}
	})
}

// The prompt shows the candidate, its evidence, the neighbours an act may
// name and the ledger it may promote onto — under separate headings, because
// the difference between them is the authority boundary.
func TestTheBacklogPromptSeparatesTheCandidateFromItsNeighbours(t *testing.T) {
	contract, ok := ReviewOutputContract(evaluation.RoleBacklog)
	if !ok {
		t.Fatal("the backlog role has no contract")
	}
	material := backlogMaterial()
	prompt, err := composeReviewPrompt(contract, &cookbook.Recipe{
		ID: BacklogRecipeID, Version: 1, Body: "# Babel consolidates its backlog\n\nThe method.",
	}, reviewTarget{
		Kind: "hypothesis", ID: "hyp_1", Title: "reconnects reset the retry backoff",
	}, nil, nil, []worker.Source{{Selector: "claude/abc"}},
		map[string]string{"assignment": "asg_1"}, nil, false, nil, &material)
	if err != nil {
		t.Fatalf("composeReviewPrompt: %v", err)
	}
	t.Log("\n" + prompt)

	for _, want := range []string{
		"## The deferred candidate",
		"## Its observations",
		"## Candidates beside it",
		"## Entities a fact could be about",
		"## What a fact may say",
		"out of budget",
		"hyp_3",
		"`local-path` (text)",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not carry %q", want)
		}
	}
	// A backlog pass is not reviewing the candidate, so nothing about votes
	// or contributions reaches it.
	for _, forbidden := range []string{"support, oppose", "contributions"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("the prompt offers a backlog pass %q", forbidden)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(contract.JSONSchema, &schema); err != nil {
		t.Fatalf("decode the schema: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, key := range []string{"consolidate", "supersede", "retire", "promote", "keep"} {
		if _, ok := properties[key]; !ok {
			t.Errorf("the schema omits %q", key)
		}
	}
	for _, key := range []string{"vote", "contributions", "filing", "topic"} {
		if _, ok := properties[key]; ok {
			t.Errorf("the schema offers a backlog pass %q", key)
		}
	}
}

// BacklogRecipeID is the recipe a backlog pass runs under, named here so the
// prompt test does not depend on internal/cli.
const BacklogRecipeID = "babel-consolidates-its-backlog"
