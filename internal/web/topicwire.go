package web

import (
	"context"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/reality"
)

// LedgerTopics is §4.13's topic-question and stance surface over the Reality
// Ledger, in the shape the topics page consumes. It is the one adapter between
// the ledger's own vocabulary — a TopicQuestion carrying a TopicProposal whose
// binding is a list of facts — and the page's, which wants a name, a kind, a
// remote and the paths as plain strings, because the page renders and the
// ledger reasons.
//
// It satisfies TopicQuestionService and TopicStanceReader together, since both
// read the same store with the same authority and a build wires them as one.
type LedgerTopics struct {
	Ledger *reality.Store
	// Filer is the frontier an acceptance files the proposal's records into.
	// A nil Filer accepts a proposal that names no records and refuses one
	// that does, rather than creating a topic with nothing in it.
	Filer reality.Filer
}

var (
	_ TopicQuestionService = LedgerTopics{}
	_ TopicStanceReader    = LedgerTopics{}
)

// TopicProposals lists the open topic questions as the page views them.
func (t LedgerTopics) TopicProposals(ctx context.Context) ([]TopicProposalView, error) {
	if t.Ledger == nil {
		return nil, nil
	}
	questions, err := t.Ledger.TopicProposals(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]TopicProposalView, 0, len(questions))
	for _, q := range questions {
		view := TopicProposalView{
			QuestionID: q.Question.ID,
			Name:       q.Proposal.Name,
			Kind:       string(q.Proposal.Kind),
			Identity:   q.Proposal.Identity,
			Why:        q.Proposal.Reasoning,
		}
		for _, fact := range q.Proposal.Binding {
			switch fact.Predicate {
			case reality.PredicateRepositoryRemote:
				view.Remote = fact.Value.Text
			case reality.PredicateLocalPath:
				if fact.Value.Text != "" {
					view.Paths = append(view.Paths, fact.Value.Text)
				}
			}
		}
		for _, record := range q.Proposal.Records {
			view.Records = append(view.Records, record.ID)
		}
		views = append(views, view)
	}
	return views, nil
}

// AcceptTopic creates the proposed entity and files the records the proposal
// named. A filing failure after the entity exists is reported as an error and
// the entity id is still returned: the topic is durable, the records it did
// not reach stay unfiled, and the page says so rather than hiding either.
func (t LedgerTopics) AcceptTopic(ctx context.Context, questionID, operator string) (string, error) {
	if t.Ledger == nil {
		return "", fmt.Errorf("the reality ledger is not available in this session")
	}
	acceptance, err := t.Ledger.AcceptTopic(ctx, questionID, operator, t.Filer)
	return acceptance.EntityID, err
}

// DeclineTopic refuses a proposal and keeps the reason verbatim.
func (t LedgerTopics) DeclineTopic(ctx context.Context, questionID, operator, reason string) error {
	if t.Ledger == nil {
		return fmt.Errorf("the reality ledger is not available in this session")
	}
	return t.Ledger.DeclineTopic(ctx, questionID, operator, reason)
}

// TopicInterest reads the operator's recorded stance toward a topic.
func (t LedgerTopics) TopicInterest(ctx context.Context, entityID string) (TopicInterestView, error) {
	if t.Ledger == nil {
		return TopicInterestView{}, nil
	}
	interest, err := t.Ledger.EntityInterest(ctx, entityID)
	if err != nil {
		return TopicInterestView{}, err
	}
	view := TopicInterestView{State: interest.State, Reason: interest.Reason, By: interest.By}
	if !interest.At.IsZero() {
		view.At = interest.At.UTC().Format(time.RFC3339)
	}
	return view, nil
}
