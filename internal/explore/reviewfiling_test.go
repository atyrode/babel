package explore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/worker"
)

// stubTopics records what the filing pass asked the two stores to do. It stands
// in for internal/frontier and internal/reality because what is under test here
// is the mapping from one recipe result to one store call, and the stores'
// own rules are theirs to test.
type stubTopics struct {
	resolve  map[string]string
	filed    []string
	proposed []TopicProposal
	noTopic  []string
	question string
	err      error
}

func (s *stubTopics) Ledger(context.Context, []string) (TopicLedger, error) {
	return TopicLedger{Topics: []LedgerTopic{{ID: "ent_1", Name: "manifold", Kind: "repository"}}}, nil
}

func (s *stubTopics) Resolve(_ context.Context, name string) (string, error) {
	if id, ok := s.resolve[name]; ok {
		return id, nil
	}
	return "", ErrUnknownTopic
}

func (s *stubTopics) File(_ context.Context, record evaluation.Subject, runID, entityID,
	rationale string) error {
	if s.err != nil {
		return s.err
	}
	s.filed = append(s.filed, strings.Join([]string{record.ID, runID, entityID, rationale}, "|"))
	return nil
}

func (s *stubTopics) Propose(_ context.Context, _ evaluation.Subject, _ string,
	proposal TopicProposal) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.proposed = append(s.proposed, proposal)
	return s.question, nil
}

func (s *stubTopics) NoTopic(_ context.Context, record evaluation.Subject, runID, reason string) error {
	if s.err != nil {
		return s.err
	}
	s.noTopic = append(s.noTopic, strings.Join([]string{record.ID, runID, reason}, "|"))
	return nil
}

func filingState(topics TopicService) (*Reviewer, *reviewState) {
	reviewer := &Reviewer{cfg: ReviewConfig{Topics: topics}}
	st := &reviewState{
		commit: context.Background(),
		opt: ReviewOptions{Assignment: evaluation.Assignment{
			ID:      "asg_1",
			RunID:   "eval-run-1",
			Role:    evaluation.RoleFiling,
			Subject: evaluation.Subject{Kind: "finding", ID: "f1"},
		}},
	}
	return reviewer, st
}

// Each of the recipe's three answers reaches exactly one store call, and the
// evaluation record says which answer it was.
func TestEachFilingAnswerReachesItsStore(t *testing.T) {
	t.Run("filed under an existing topic", func(t *testing.T) {
		topics := &stubTopics{resolve: map[string]string{"manifold": "ent_1"}}
		reviewer, st := filingState(topics)
		filing, err := reviewer.file(st, &ReviewResult{
			Filing: &FiledUnder{Entity: "manifold", Rationale: "the claim is about its test suite"},
		})
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		if len(topics.filed) != 1 {
			t.Fatalf("the about edge was not written: %+v", topics)
		}
		if got, want := topics.filed[0], "f1|eval-run-1|ent_1|the claim is about its test suite"; got != want {
			t.Errorf("filed %q, want %q", got, want)
		}
		if filing.Outcome != evaluation.FilingFiled || filing.Entity != "ent_1" {
			t.Errorf("outcome = %+v, want a filing under the resolved entity", filing)
		}
		if got, want := filingOutcome(filing), "filed under ent_1"; got != want {
			t.Errorf("receipt line = %q, want %q", got, want)
		}
	})

	t.Run("proposed a topic", func(t *testing.T) {
		topics := &stubTopics{question: "q_7"}
		reviewer, st := filingState(topics)
		filing, err := reviewer.file(st, &ReviewResult{Topic: &TopicProposal{
			Name: "hearth", Kind: "machine", Identity: "hearth",
			Definition: "the workstation the conductor runs on",
			Reasoning:  "three cited sessions name this host",
		}})
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		if len(topics.proposed) != 1 || topics.proposed[0].Name != "hearth" {
			t.Fatalf("the topic question was not raised: %+v", topics.proposed)
		}
		if filing.Outcome != evaluation.FilingProposed || filing.Question != "q_7" {
			t.Errorf("outcome = %+v, want the question that carries the proposal", filing)
		}
		if got := filingOutcome(filing); !strings.Contains(got, "q_7") {
			t.Errorf("receipt line = %q, want it to name the question", got)
		}
	})

	t.Run("about nothing in particular", func(t *testing.T) {
		topics := &stubTopics{}
		reviewer, st := filingState(topics)
		filing, err := reviewer.file(st, &ReviewResult{
			NoTopic: &NoTopic{Reason: "the record is about the timing of two runs"},
		})
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		if len(topics.noTopic) != 1 {
			t.Fatalf("no-topic was not recorded: %+v", topics)
		}
		if filing.Outcome != evaluation.FilingNone || filing.Entity != "" || filing.Question != "" {
			t.Errorf("outcome = %+v, want a no-topic naming neither entity nor question", filing)
		}
	})
}

// §4.13: a name the ledger cannot resolve is a question rather than a new
// thing, and the refusal is what makes the run raise one. It is not a failure.
func TestAnUnresolvableNameBecomesATopicQuestion(t *testing.T) {
	topics := &stubTopics{question: "q_9"}
	reviewer, st := filingState(topics)
	filing, err := reviewer.file(st, &ReviewResult{
		Filing: &FiledUnder{Entity: "Atlas", Rationale: "the deployment tool the claim is about"},
	})
	if err != nil {
		t.Fatalf("an unresolvable name must not fail the pass: %v", err)
	}
	if len(topics.filed) != 0 {
		t.Fatal("nothing may be filed under a name the ledger does not hold")
	}
	if len(topics.proposed) != 1 {
		t.Fatalf("the unresolvable name must be proposed: %+v", topics.proposed)
	}
	proposal := topics.proposed[0]
	if proposal.Name != "Atlas" || proposal.Identity != "atlas" {
		t.Errorf("proposal = %+v, want the name and a stable slug identity", proposal)
	}
	if proposal.Kind != "subject" {
		t.Errorf("kind = %q, want an operator-defined subject rather than a guessed kind", proposal.Kind)
	}
	if proposal.Definition == "" || !strings.Contains(proposal.Reasoning, "Atlas") {
		t.Errorf("the proposal must carry the run's rationale: %+v", proposal)
	}
	if filing.Outcome != evaluation.FilingProposed || filing.Question != "q_9" {
		t.Errorf("outcome = %+v, want the raised question", filing)
	}
}

// A topic the ledger already holds — a bound identity, an open question, a
// decline nothing new has displaced — is not this pass's failure.
func TestAnAlreadyKnownTopicIsNotAFailure(t *testing.T) {
	topics := &stubTopics{err: ErrTopicKnown}
	reviewer, st := filingState(topics)
	_, err := reviewer.file(st, &ReviewResult{Topic: &TopicProposal{
		Name: "hearth", Kind: "machine", Identity: "hearth",
		Definition: "the workstation", Reasoning: "why",
	}})
	if !errors.Is(err, ErrTopicKnown) {
		t.Fatalf("error = %v, want the already-known sentinel the runner turns into a skip", err)
	}
}

func filingResult(t *testing.T, payload string) (*ReviewResult, error) {
	t.Helper()
	return parseReviewResult(&worker.ResultRecord{
		Schema:  ReviewResultSchema,
		Payload: json.RawMessage(payload),
	}, evaluation.RoleFiling, reviewSelf{})
}

// The result contract: one of three answers, each carrying what an operator
// would need to act on it, and no judgement about the record.
func TestFilingResultContract(t *testing.T) {
	for name, payload := range map[string]string{
		"filing":   `{"filing":{"entity":"manifold","rationale":"about its test suite"}}`,
		"topic":    `{"topic":{"name":"hearth","kind":"machine","identity":"hearth","definition":"a host","reasoning":"why"}}`,
		"no_topic": `{"no_topic":{"reason":"about nothing in particular"}}`,
		"skip":     `{"skip":"the record's payload does not decode"}`,
	} {
		if _, err := filingResult(t, payload); err != nil {
			t.Errorf("%s must be accepted: %v", name, err)
		}
	}

	for name, payload := range map[string]string{
		"two answers":             `{"filing":{"entity":"m","rationale":"r"},"no_topic":{"reason":"r"}}`,
		"no answer":               `{"uncertainty":"I could not decide"}`,
		"filing with no why":      `{"filing":{"entity":"manifold"}}`,
		"unbound topic":           `{"topic":{"name":"hearth","kind":"machine","identity":"hearth","reasoning":"why"}}`,
		"invented kind":           `{"topic":{"name":"hearth","kind":"folder","identity":"hearth","definition":"d","reasoning":"why"}}`,
		"no-topic with no reason": `{"no_topic":{}}`,
		"a vote":                  `{"vote":"support"}`,
		"a contribution":          `{"contributions":[{"kind":"comment","text":"weak"}]}`,
	} {
		if _, err := filingResult(t, payload); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}

	// The other roles may not decide what a record is about, and their
	// schema does not offer the fields.
	_, err := parseReviewResult(&worker.ResultRecord{
		Schema:  ReviewResultSchema,
		Payload: json.RawMessage(`{"filing":{"entity":"manifold","rationale":"r"}}`),
	}, evaluation.RoleReception, reviewSelf{})
	if err == nil {
		t.Error("a reception review may not file a record")
	}
	contract, ok := ReviewOutputContract(evaluation.RoleReception)
	if !ok {
		t.Fatal("the reception role has no contract")
	}
	if strings.Contains(string(contract.JSONSchema), `"no_topic"`) {
		t.Error("the reception schema offers a filing field it may not fill")
	}
	filing, ok := ReviewOutputContract(evaluation.RoleFiling)
	if !ok {
		t.Fatal("the filing role has no contract")
	}
	for _, absent := range []string{`"vote"`, `"outcome"`, `"contributions"`} {
		if strings.Contains(string(filing.JSONSchema), absent) {
			t.Errorf("the filing schema offers %s, which is a judgement about the record", absent)
		}
	}
	if !strings.Contains(filing.Instructions, "no_topic") {
		t.Error("the filing instructions must state the third answer")
	}
}
