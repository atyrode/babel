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
