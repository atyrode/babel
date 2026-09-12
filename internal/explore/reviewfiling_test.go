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
	"github.com/atyrode/babel/internal/worker"
)

// stubTopics records what the filing pass asked the stores to do. It stands in
// for internal/frontier, internal/reality and internal/complaint because what
// is under test here is the mapping from one recipe result to one store call,
// and the stores' own rules are theirs to test.
type stubTopics struct {
	ledger   TopicLedger
	resolve  map[string]string
	filed    []string
	proposed []TopicPlan
	noTopic  []string
	answered []string
	proposal string
	err      error
}

func (s *stubTopics) Ledger(context.Context, []string) (TopicLedger, error) {
	return s.ledger, nil
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

func (s *stubTopics) Propose(_ context.Context, plan TopicPlan) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.proposed = append(s.proposed, plan)
	return s.proposal, nil
}

func (s *stubTopics) AnswerAsk(_ context.Context, askID, runID, reason string) error {
	if s.err != nil {
		return s.err
	}
	s.answered = append(s.answered, strings.Join([]string{askID, runID, reason}, "|"))
	return nil
}

func (s *stubTopics) NoTopic(_ context.Context, record evaluation.Subject, runID, reason string) error {
	if s.err != nil {
		return s.err
	}
	s.noTopic = append(s.noTopic, strings.Join([]string{record.ID, runID, reason}, "|"))
	return nil
}

// filingLedger is the material a filing pass is shown in these tests: two live
// topics, two identities nothing names, and one ask the operator made.
func filingLedger() TopicLedger {
	return TopicLedger{
		Topics: []LedgerTopic{
			{ID: "ent_1", Name: "manifold", Kind: "repository",
				Aliases: []string{"manifold-cli"}, Binding: "repository github.com/atyrode/manifold"},
			{ID: "ent_2", Name: "manifold service", Kind: "service",
				Binding: "host hearth"},
		},
		Unbound: []TopicObservation{
			{Identity: "github.com/atyrode/atlas", Remote: "github.com/atyrode/atlas",
				Name: "atlas", Paths: []string{"/home/alex/src/atlas"},
				Sessions: 41, Checkouts: 3, Records: 7},
			{Identity: "/home/alex/scratch/probe", Name: "probe",
				Paths:    []string{"/home/alex/scratch/probe"},
				Sessions: 1, Checkouts: 1, Records: 0},
		},
		Asks: []TopicAsk{{
			ID:    "cmp_ask",
			Topic: "t/manifold",
			Text:  "topic t/manifold: the cli and the service are two things, split them",
			At:    time.Date(2026, 9, 11, 9, 30, 0, 0, time.UTC),
		}},
	}
}

func filingState(topics *stubTopics) (*Reviewer, *reviewState) {
	if topics.ledger.Topics == nil {
		topics.ledger = filingLedger()
	}
	reviewer := &Reviewer{cfg: ReviewConfig{Topics: topics}}
	ledger := topics.ledger
	st := &reviewState{
		commit: context.Background(),
		ledger: &ledger,
		opt: ReviewOptions{Assignment: evaluation.Assignment{
			ID:      "asg_1",
			RunID:   "eval-run-1",
			Role:    evaluation.RoleFiling,
			Subject: evaluation.Subject{Kind: "finding", ID: "f1"},
		}},
	}
	return reviewer, st
}

// Each of the recipe's answers reaches exactly one store call, and the
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

	t.Run("proposed a new topic", func(t *testing.T) {
		topics := &stubTopics{proposal: "pro_7"}
		reviewer, st := filingState(topics)
		filing, err := reviewer.file(st, &ReviewResult{Topic: &TopicProposal{
			Operation: TopicCreate, Name: "atlas", Kind: "repository",
			Identity: "github.com/atyrode/atlas", Remote: "github.com/atyrode/atlas",
			Reasoning: "the record is about the deployment tool in that repository",
		}})
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		if len(topics.proposed) != 1 {
			t.Fatalf("the proposal was not published: %+v", topics.proposed)
		}
		plan := topics.proposed[0]
		if plan.Operation != TopicCreate || plan.Entity == nil || plan.Entity.Name != "atlas" {
			t.Fatalf("plan = %+v, want a create carrying the entity it would bring into being", plan)
		}
		if plan.Observed == nil || plan.Observed.Sessions != 41 || plan.Observed.Records != 7 {
			t.Errorf("observed = %+v, want the scan evidence behind the identity", plan.Observed)
		}
		if plan.Record.ID != "f1" || plan.RunID != "eval-run-1" {
			t.Errorf("plan = %+v, want the record under review and the run that judged it", plan)
		}
		if filing.Outcome != evaluation.FilingProposed || filing.Proposal != "pro_7" ||
			filing.Operation != "create" {
			t.Errorf("outcome = %+v, want the proposal record and the operation it carries", filing)
		}
		if got, want := filingOutcome(filing), "topic create proposed as pro_7"; got != want {
			t.Errorf("receipt line = %q, want %q", got, want)
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
		if filing.Outcome != evaluation.FilingNone || filing.Entity != "" || filing.Proposal != "" {
			t.Errorf("outcome = %+v, want a no-topic naming neither entity nor proposal", filing)
		}
	})

	t.Run("answered the operator's ask", func(t *testing.T) {
		topics := &stubTopics{}
		reviewer, st := filingState(topics)
		filing, err := reviewer.file(st, &ReviewResult{NoChange: &NoChange{
			AskID:  "cmp_ask",
			Reason: "the cli and the service are one deployable and the records never separate them",
		}})
		if err != nil {
			t.Fatalf("file: %v", err)
		}
		want := "cmp_ask|eval-run-1|the cli and the service are one deployable and the records never separate them"
		if len(topics.answered) != 1 || topics.answered[0] != want {
			t.Fatalf("answered = %v, want the reason recorded against the ask", topics.answered)
		}
		if len(topics.proposed) != 0 {
			t.Error("an answered ask proposed a change to the ledger anyway")
		}
		if filing.Outcome != evaluation.FilingAnswered || filing.Ask != "cmp_ask" {
			t.Errorf("outcome = %+v, want the answered ask", filing)
		}
		if !strings.Contains(filingOutcome(filing), "cmp_ask") {
			t.Errorf("receipt line = %q, want it to name the ask", filingOutcome(filing))
		}
	})
}

// The four operations are one output kind, and each reaches the consumer with
// the targets resolved to the entity ids the ledger holds.
func TestEveryTopicOperationReachesTheConsumerResolved(t *testing.T) {
	split := TopicProposal{
		Operation: TopicSplit, Targets: []string{"manifold"},
		Name: "manifold service", Kind: "service", Identity: "hearth/manifold-service",
		Definition: "the service half of the manifold checkout",
		Reasoning:  "the record is about the deployed service and the topic also names the library",
		AskID:      "cmp_ask",
	}
	merge := TopicProposal{
		Operation: TopicMerge, Targets: []string{"manifold-cli", "manifold service"},
		Reasoning: "both names carry records about one deployable",
	}
	retire := TopicProposal{
		Operation: TopicRetire, Targets: []string{"ent_2"},
		Reasoning: "the service never existed outside one experiment",
	}
	for _, tc := range []struct {
		name      string
		proposal  TopicProposal
		operation TopicOperation
		targets   []string
		creates   bool
		ask       bool
	}{
		{"split", split, TopicSplit, []string{"ent_1"}, true, true},
		{"merge", merge, TopicMerge, []string{"ent_1", "ent_2"}, false, false},
		{"retire", retire, TopicRetire, []string{"ent_2"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			topics := &stubTopics{proposal: "pro_" + tc.name}
			reviewer, st := filingState(topics)
			filing, err := reviewer.file(st, &ReviewResult{Topic: &tc.proposal})
			if err != nil {
				t.Fatalf("file: %v", err)
			}
			plan := topics.proposed[0]
			if plan.Operation != tc.operation {
				t.Errorf("operation = %q, want %q", plan.Operation, tc.operation)
			}
			if len(plan.Targets) != len(tc.targets) {
				t.Fatalf("targets = %+v, want %v", plan.Targets, tc.targets)
			}
			for i, want := range tc.targets {
				if plan.Targets[i].ID != want {
					t.Errorf("target %d = %+v, want the entity id %s", i, plan.Targets[i], want)
				}
				if plan.Targets[i].Name == "" {
					t.Errorf("target %d carries no name for the proposal's title", i)
				}
			}
			if creates := plan.Entity != nil; creates != tc.creates {
				t.Errorf("entity draft present = %t, want %t", creates, tc.creates)
			}
			if asked := plan.Ask != nil; asked != tc.ask {
				t.Errorf("ask answered = %t, want %t", asked, tc.ask)
			}
			if filing.Operation != string(tc.operation) {
				t.Errorf("the evaluation record says %q, want %q", filing.Operation, tc.operation)
			}
		})
	}
}

// A target, or an answered ask, that the served material does not hold is a
// malformed result. It is deliberately not turned into a topic: an invented
// name that minted an entity would be the authority §4.8 reserves to the
// operator, taken by a typo.
func TestAnInventedTargetOrAskIsRefusedRatherThanProposed(t *testing.T) {
	for name, res := range map[string]*ReviewResult{
		"unknown merge target": {Topic: &TopicProposal{
			Operation: TopicMerge, Targets: []string{"manifold", "atlas"},
			Reasoning: "they look alike",
		}},
		"unknown retire target": {Topic: &TopicProposal{
			Operation: TopicRetire, Targets: []string{"tmp"}, Reasoning: "it is a folder",
		}},
		"unknown ask on a proposal": {Topic: &TopicProposal{
			Operation: TopicCreate, Name: "atlas", Kind: "repository",
			Identity: "github.com/atyrode/atlas", Remote: "github.com/atyrode/atlas",
			Reasoning: "why", AskID: "cmp_nobody",
		}},
		"unknown ask answered": {NoChange: &NoChange{AskID: "cmp_nobody", Reason: "I disagree"}},
	} {
		t.Run(name, func(t *testing.T) {
			topics := &stubTopics{proposal: "pro_1"}
			reviewer, st := filingState(topics)
			_, err := reviewer.file(st, res)
			if !errors.Is(err, ErrTopicResult) {
				t.Fatalf("error = %v, want the malformed-result sentinel", err)
			}
			if len(topics.proposed) != 0 || len(topics.answered) != 0 {
				t.Errorf("the invented name reached a store: %+v", topics)
			}
		})
	}
}

// §4.13: a name the ledger cannot resolve is a proposal rather than a new
// thing, and the refusal is what makes the run raise one. It is not a failure.
func TestAnUnresolvableNameBecomesACreateProposal(t *testing.T) {
	topics := &stubTopics{proposal: "pro_9"}
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
	plan := topics.proposed[0]
	if plan.Operation != TopicCreate || plan.Entity == nil {
		t.Fatalf("plan = %+v, want a create proposal", plan)
	}
	proposal := *plan.Entity
	if proposal.Name != "Atlas" || proposal.Identity != "atlas" {
		t.Errorf("proposal = %+v, want the name and a stable slug identity", proposal)
	}
	if proposal.Kind != "subject" {
		t.Errorf("kind = %q, want an operator-defined subject rather than a guessed kind", proposal.Kind)
	}
	if proposal.Definition == "" || !strings.Contains(proposal.Reasoning, "Atlas") {
		t.Errorf("the proposal must carry the run's rationale: %+v", proposal)
	}
	if filing.Outcome != evaluation.FilingProposed || filing.Proposal != "pro_9" {
		t.Errorf("outcome = %+v, want the published proposal", filing)
	}
}

// A topic the ledger already holds — a bound identity, an open plan, a decline
// nothing new has displaced — is not this pass's failure.
func TestAnAlreadyKnownTopicIsNotAFailure(t *testing.T) {
	topics := &stubTopics{err: ErrTopicKnown}
	reviewer, st := filingState(topics)
	_, err := reviewer.file(st, &ReviewResult{Topic: &TopicProposal{
		Operation: TopicCreate, Name: "hearth", Kind: "machine", Identity: "hearth",
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

// The result contract: one of four answers, each carrying what an operator
// would need to act on it, and no judgement about the record.
func TestFilingResultContract(t *testing.T) {
	for name, payload := range map[string]string{
		"filing": `{"filing":{"entity":"manifold","rationale":"about its test suite"}}`,
		"create": `{"topic":{"operation":"create","name":"hearth","kind":"machine","identity":"hearth",` +
			`"definition":"a host","reasoning":"why"}}`,
		"split": `{"topic":{"operation":"split","targets":["manifold"],"name":"manifold service",` +
			`"kind":"service","identity":"hearth/manifold-service","definition":"the service half",` +
			`"reasoning":"why"}}`,
		"merge":     `{"topic":{"operation":"merge","targets":["a","b"],"reasoning":"why"}}`,
		"retire":    `{"topic":{"operation":"retire","targets":["tmp"],"reasoning":"why"}}`,
		"no_topic":  `{"no_topic":{"reason":"about nothing in particular"}}`,
		"no_change": `{"no_change":{"ask_id":"cmp_1","reason":"the two topics are one thing"}}`,
		"skip":      `{"skip":"the record's payload does not decode"}`,
	} {
		if _, err := filingResult(t, payload); err != nil {
			t.Errorf("%s must be accepted: %v", name, err)
		}
	}

	for name, payload := range map[string]string{
		"two answers":        `{"filing":{"entity":"m","rationale":"r"},"no_topic":{"reason":"r"}}`,
		"no answer":          `{"uncertainty":"I could not decide"}`,
		"filing with no why": `{"filing":{"entity":"manifold"}}`,
		"no operation": `{"topic":{"name":"hearth","kind":"machine","identity":"hearth",` +
			`"definition":"d","reasoning":"why"}}`,
		"invented operation": `{"topic":{"operation":"rename","targets":["manifold"],"reasoning":"why"}}`,
		"unbound topic": `{"topic":{"operation":"create","name":"hearth","kind":"machine",` +
			`"identity":"hearth","reasoning":"why"}}`,
		"create with no identity": `{"topic":{"operation":"create","name":"hearth","kind":"machine",` +
			`"definition":"d","reasoning":"why"}}`,
		"invented kind": `{"topic":{"operation":"create","name":"hearth","kind":"folder",` +
			`"identity":"hearth","definition":"d","reasoning":"why"}}`,
		"create naming a target": `{"topic":{"operation":"create","targets":["manifold"],` +
			`"name":"hearth","kind":"machine","identity":"hearth","definition":"d","reasoning":"why"}}`,
		"merge of one":        `{"topic":{"operation":"merge","targets":["a"],"reasoning":"why"}}`,
		"retire of two":       `{"topic":{"operation":"retire","targets":["a","b"],"reasoning":"why"}}`,
		"retire that creates": `{"topic":{"operation":"retire","targets":["a"],"name":"a","reasoning":"why"}}`,
		"split of none": `{"topic":{"operation":"split","name":"a","kind":"service","identity":"i",` +
			`"definition":"d","reasoning":"why"}}`,
		"proposal with no why":     `{"topic":{"operation":"retire","targets":["a"]}}`,
		"no-topic with no reason":  `{"no_topic":{}}`,
		"no-change with no ask":    `{"no_change":{"reason":"I disagree"}}`,
		"no-change with no reason": `{"no_change":{"ask_id":"cmp_1"}}`,
		"a vote":                   `{"vote":"support"}`,
		"a contribution":           `{"contributions":[{"kind":"comment","text":"weak"}]}`,
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
	for _, absent := range []string{`"no_topic"`, `"no_change"`} {
		if strings.Contains(string(contract.JSONSchema), absent) {
			t.Errorf("the reception schema offers %s, a filing field it may not fill", absent)
		}
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
	// The four operations are a closed vocabulary on the wire, so a model
	// cannot invent a fifth change to the ledger and have it validated.
	for _, operation := range TopicOperations() {
		if !strings.Contains(string(filing.JSONSchema), `"`+string(operation)+`"`) {
			t.Errorf("the filing schema's operation enum omits %q", operation)
		}
	}
	for _, stated := range []string{"no_topic", "no_change", "ask_id", "targets"} {
		if !strings.Contains(filing.Instructions, stated) {
			t.Errorf("the filing instructions do not state %q", stated)
		}
	}
}

// The prompt shows the pass three different kinds of material about topics and
// keeps them apart: what exists, what was merely observed, and what the
// operator asked for. Collapsing any two would make an observation read as a
// topic, or an ask read as an instruction.
func TestTheFilingPromptSeparatesEntitiesObservationsAndAsks(t *testing.T) {
	contract, ok := ReviewOutputContract(evaluation.RoleFiling)
	if !ok {
		t.Fatal("the filing role has no contract")
	}
	ledger := filingLedger()
	prompt, err := composeReviewPrompt(contract, &cookbook.Recipe{
		ID: "babel-files-its-output", Version: 1, Body: "# Babel files its output\n\nThe method.",
	}, reviewTarget{
		Kind: "finding", ID: "f1", Title: "the deployment tool drops its retries",
	}, nil, nil, []worker.Source{{Selector: "claude/abc"}},
		map[string]string{"assignment": "asg_1"}, nil, false, &ledger)
	if err != nil {
		t.Fatalf("composeReviewPrompt: %v", err)
	}
	t.Log("\n" + prompt)

	for _, want := range []string{
		"## What the ledger already names",
		"## What the scan observed and nothing names",
		"- atlas (identity github.com/atyrode/atlas, remote github.com/atyrode/atlas): " +
			"41 sessions in 3 checkouts, 7 records cite it",
		"  seen at: /home/alex/src/atlas",
		"- probe (identity /home/alex/scratch/probe): 1 session in 1 checkout\n",
		"## What the operator asked",
		"- cmp_ask on 2026-09-11 about t/manifold: topic t/manifold: the cli and the service are two things",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt does not carry %q", want)
		}
	}

	// The observations and the asks are rendered under their own headings
	// and are not also inside the ledger's JSON, where they would read as
	// entities that already exist.
	ledgerBlock := prompt[strings.Index(prompt, "## What the ledger already names"):strings.Index(prompt, "## What the scan observed")]
	for _, absent := range []string{"unbound_identities", "operator_asks", "cmp_ask"} {
		if strings.Contains(ledgerBlock, absent) {
			t.Errorf("the ledger block carries %q, which belongs to its own section", absent)
		}
	}
}
