package web

import (
	"context"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/reality"
)

// LedgerTopics is §4.13's plan and stance surface over the Reality Ledger, in
// the shape the topics page and the ruling route consume. It is the one
// adapter between the ledger's own vocabulary — a TopicPlan carrying an
// EntityDraft whose binding is a list of facts — and the surface's, which
// wants an operation, a name and a list of target names, because the page
// renders and the ledger reasons.
//
// It satisfies TopicPlanService and TopicStanceReader together, since both
// read the same store with the same authority and a build wires them as one.
type LedgerTopics struct {
	Ledger *reality.Store
	// Filer is the frontier an application files the plan's records into.
	// A nil Filer applies a plan that names no records and refuses one
	// that does, rather than creating a topic with nothing in it.
	Filer reality.Filer
}

var (
	_ TopicPlanService  = LedgerTopics{}
	_ TopicStanceReader = LedgerTopics{}
)

// OpenTopicPlans lists the plans awaiting a ruling as the page views them.
func (t LedgerTopics) OpenTopicPlans(ctx context.Context) ([]TopicPlanView, error) {
	if t.Ledger == nil {
		return nil, nil
	}
	plans, err := t.Ledger.OpenTopicPlans(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]TopicPlanView, 0, len(plans))
	for _, plan := range plans {
		view, err := t.view(ctx, plan)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// TopicPlan reports the plan one proposal carries. Most proposals carry none
// — they are about the corpus rather than about the ledger's naming — and
// that is a false rather than an error.
func (t LedgerTopics) TopicPlan(ctx context.Context, proposalID string) (TopicPlanView, bool, error) {
	if t.Ledger == nil {
		return TopicPlanView{}, false, nil
	}
	plan, found, err := t.Ledger.TopicPlan(ctx, proposalID)
	if err != nil || !found {
		return TopicPlanView{}, false, err
	}
	view, err := t.view(ctx, plan)
	if err != nil {
		return TopicPlanView{}, false, err
	}
	return view, true, nil
}

// view states one plan in the page's vocabulary, resolving each target to the
// name a reader recognizes.
//
// A target whose entity this build cannot read keeps its id as its name
// rather than costing the whole rail: the ruling is addressed to the proposal
// and works either way, and an unnamed target is a worse row than a named one
// but a better answer than no row at all.
func (t LedgerTopics) view(ctx context.Context, plan reality.TopicPlan) (TopicPlanView, error) {
	view := TopicPlanView{
		ProposalID: plan.ProposalID,
		Operation:  string(plan.Operation),
		Name:       plan.Name(),
		Kind:       string(plan.Kind()),
		Why:        plan.Reasoning,
		RunID:      plan.By.RunID,
	}
	for _, target := range plan.Targets {
		name := target
		if entity, err := t.Ledger.Entity(ctx, target); err == nil &&
			entity.Payload.DisplayName != "" {
			name = entity.Payload.DisplayName
		}
		view.Targets = append(view.Targets, TopicTargetView{ID: target, Name: name})
	}
	for _, record := range plan.Records() {
		view.Records = append(view.Records, record.ID)
	}
	return view, nil
}

// ApplyTopicPlan performs what the operator accepted and files the records
// the plan named. A filing failure after the ledger's half committed is
// reported as an error and the outcome is still returned: the act is durable,
// the records it did not reach stay unfiled, and the ruling says so rather
// than hiding either.
func (t LedgerTopics) ApplyTopicPlan(ctx context.Context, proposalID, operator string) (
	TopicPlanOutcome, error) {
	if t.Ledger == nil {
		return TopicPlanOutcome{}, fmt.Errorf("the reality ledger is not available in this session")
	}
	acceptance, err := t.Ledger.ApplyTopicPlan(ctx, proposalID, operator, t.Filer)
	return TopicPlanOutcome{
		Operation: string(acceptance.Operation),
		EntityID:  acceptance.EntityID,
		Filed:     len(acceptance.Filings),
	}, err
}

// DeclineTopicPlan refuses a plan and keeps the reason verbatim.
func (t LedgerTopics) DeclineTopicPlan(ctx context.Context, proposalID, operator, reason string) error {
	if t.Ledger == nil {
		return fmt.Errorf("the reality ledger is not available in this session")
	}
	return t.Ledger.DeclineTopicPlan(ctx, proposalID, operator, reason)
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
