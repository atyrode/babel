package web

import (
	"context"
	"fmt"

	"github.com/atyrode/babel/internal/reality"
)

// LedgerBacklog adapts the Reality Ledger's backlog plans (§4.13's last
// paragraph) to the shape the ruling route consumes.
//
// It is topicwire.go's twin and exists for the same reason: the ledger
// reasons about fact inputs, provenance and candidate identifiers, and the
// surface wants an act, the candidates it settles and what it settles them
// to. The frontier half of an acceptance — the status events, and the
// supersession link — is performed through the injected writer, because the
// two components share one durable file and neither may hold the other's
// write lock.
type LedgerBacklog struct {
	Ledger   *reality.Store
	Frontier reality.BacklogFrontier
}

var _ BacklogPlanService = LedgerBacklog{}

// BacklogPlan reports the plan one proposal carries. Most proposals carry
// none — they are about the corpus rather than about the backlog — and that is
// a false rather than an error.
func (b LedgerBacklog) BacklogPlan(ctx context.Context, proposalID string) (
	BacklogPlanView, bool, error) {
	if b.Ledger == nil {
		return BacklogPlanView{}, false, nil
	}
	plan, found, err := b.Ledger.BacklogPlan(ctx, proposalID)
	if err != nil || !found {
		return BacklogPlanView{}, false, err
	}
	return BacklogPlanView{
		ProposalID: plan.ProposalID,
		Operation:  string(plan.Operation),
		Hypotheses: plan.Hypotheses,
		Status:     string(plan.Settles()),
		Reasoning:  plan.Reasoning,
	}, true, nil
}

// ApplyBacklogPlan performs what the operator accepted. A frontier write that
// failed after the ledger's half committed is reported as an error beside a
// populated outcome, because the ruling did happen and the candidates it did
// not reach are still deferred.
func (b LedgerBacklog) ApplyBacklogPlan(ctx context.Context, proposalID, operator string) (
	BacklogPlanOutcome, error) {
	if b.Ledger == nil {
		return BacklogPlanOutcome{}, fmt.Errorf("the reality ledger is not available in this session")
	}
	acceptance, err := b.Ledger.ApplyBacklogPlan(ctx, proposalID, operator, b.Frontier)
	outcome := BacklogPlanOutcome{
		Operation: string(acceptance.Operation),
		Settled:   acceptance.Settled,
		Status:    string(acceptance.Status),
		FactID:    acceptance.FactID,
	}
	return outcome, err
}

// DeclineBacklogPlan records the operator's refusal with his reason kept
// verbatim, which is what suppresses the same act until something materially
// new turns up.
func (b LedgerBacklog) DeclineBacklogPlan(ctx context.Context, proposalID, operator, reason string) error {
	if b.Ledger == nil {
		return fmt.Errorf("the reality ledger is not available in this session")
	}
	return b.Ledger.DeclineBacklogPlan(ctx, proposalID, operator, reason)
}
