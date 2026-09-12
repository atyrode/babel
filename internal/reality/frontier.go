package reality

import (
	"context"
	"fmt"

	"github.com/atyrode/babel/internal/frontier"
)

// FrontierSink retains a plan's candidate hypotheses in the durable hypothesis
// frontier.
//
// It exists so the seam HypothesisSink describes is a real one rather than a
// hole a caller has to fill. The frontier owns candidate hypotheses (§4.2,
// §5.2) and this package owns reality; a plan that produces a candidate hands
// it over instead of storing a second copy that could disagree.
//
// The adapter is deliberately thin and lossy in one direction only: it passes
// the statement, the origin cues, and the provisional labels, and it does not
// set novelty or priority. §5.2 confines those to ordering and an interpreter
// has no basis to estimate them, so leaving them at zero states that this
// candidate arrived unranked rather than ranked lowest.
type FrontierSink struct {
	// Store is the frontier this sink writes to. It may share the durable
	// file with the Reality Ledger's store, which is why RecordHypothesis is
	// called before the ledger's transaction opens.
	Store *frontier.Store
}

// RecordHypothesis persists the candidate and returns its frontier ID.
func (f FrontierSink) RecordHypothesis(ctx context.Context, draft HypothesisDraft) (string, error) {
	if f.Store == nil {
		return "", ErrNoHypothesisSink
	}
	record, err := f.Store.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID: draft.RunID,
		Payload: frontier.HypothesisPayload{
			Statement:         draft.Statement,
			OriginCues:        draft.OriginCues,
			ProvisionalLabels: draft.ProvisionalLabels,
		},
	})
	if err != nil {
		return "", fmt.Errorf("reality: create frontier hypothesis: %w", err)
	}
	return record.ID, nil
}

// Settle appends the status an accepted backlog plan calls for, attributed to
// the operator who accepted it, and the supersession link when there is one.
//
// The link goes first and the status second, which is §4.2's own separation
// read in the order that survives a failure between them: a link with no
// status event says two candidates are related and leaves the older one on the
// frontier, while a status with no link would say a candidate was replaced by
// nothing.
func (f FrontierSink) Settle(ctx context.Context, in Settlement) error {
	if f.Store == nil {
		return ErrNoHypothesisSink
	}
	if in.SupersededBy != "" {
		if _, err := f.Store.Link(ctx, frontier.LinkInput{
			FromID: in.SupersededBy,
			ToID:   in.HypothesisID,
			Type:   frontier.LinkSupersedes,
			Note:   in.Reason,
		}); err != nil {
			return fmt.Errorf("reality: link %s over %s: %w", in.SupersededBy, in.HypothesisID, err)
		}
	}
	if _, err := f.Store.SetStatus(ctx, frontier.StatusInput{
		HypothesisID: in.HypothesisID,
		Status:       in.Status,
		Actor:        frontier.Operator(in.Operator),
		Note:         in.Reason,
	}); err != nil {
		return fmt.Errorf("reality: settle hypothesis %s as %s: %w", in.HypothesisID, in.Status, err)
	}
	return nil
}

// Status reports where a candidate currently stands, which is what an
// acceptance checks before it settles one.
func (f FrontierSink) Status(ctx context.Context, hypothesisID string) (frontier.Status, error) {
	if f.Store == nil {
		return "", ErrNoHypothesisSink
	}
	record, err := f.Store.Hypothesis(ctx, hypothesisID)
	if err != nil {
		return "", err
	}
	return record.Status, nil
}
