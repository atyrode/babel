package cli

import (
	"context"
	"errors"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/explore"
	runstore "github.com/atyrode/babel/internal/run"
)

// Completed repairs the crash window after the exploration's terminal receipt
// but before the conductor journal's terminal cycle. It reads no corpus and
// launches nothing: receipt custody, not a fresh preparation, decides the result.
func (r *conductorRunner) Completed(ctx context.Context, runID string) (conductor.CompletedRun, bool, error) {
	receipt, err := r.state.runs.Latest(ctx, runID)
	if err != nil && !errors.Is(err, runstore.ErrNotFound) {
		return conductor.CompletedRun{}, false, err
	}
	if err == nil && (receipt.Body.Checkpoint == nil || receipt.Body.Checkpoint.State == runstore.Closed) {
		return completedReceipt(receipt), true, nil
	}
	owned, err := r.state.runs.AttemptOwned(ctx, runID)
	if err != nil {
		return conductor.CompletedRun{}, false, err
	}
	if owned {
		_, err := r.state.runs.Reconcile(ctx, time.Now().Add(-runstore.ReconcileStaleAfter), runstore.ReconcileOptions{
			RunID: runID,
			Publish: func(commit context.Context, id string) error {
				r.app.publishLifecycle(commit, id)
				return nil
			},
		})
		if err != nil {
			return conductor.CompletedRun{}, false, err
		}
		owned, err = r.state.runs.AttemptOwned(ctx, runID)
		if err != nil {
			return conductor.CompletedRun{}, false, err
		}
		if owned {
			return conductor.CompletedRun{}, false, runstore.ErrAttemptOwned
		}
	}
	receipt, err = r.state.runs.Latest(ctx, runID)
	if errors.Is(err, runstore.ErrNotFound) {
		return conductor.CompletedRun{}, false, nil
	}
	if err != nil {
		return conductor.CompletedRun{}, false, err
	}
	if cp := receipt.Body.Checkpoint; cp != nil && cp.State != runstore.Closed {
		return conductor.CompletedRun{}, false, nil
	}
	return completedReceipt(receipt), true, nil
}

func completedReceipt(receipt runstore.Receipt) conductor.CompletedRun {
	result := conductor.CompletedRun{
		Result: conductor.Result{
			PreparationID: string(receipt.Preparation.ID),
			ReceiptID:     string(receipt.Header.ID),
			Failures:      len(receipt.Body.Failures),
		},
		FinishedAt: receipt.Body.Timing.FinishedAt,
	}
	if worker := receipt.Body.Worker; worker != nil {
		result.Cost, result.Currency = worker.Cost.EstimatedRun, worker.Cost.Currency
	}
	if cp := receipt.Body.Checkpoint; cp != nil {
		if cp.Verdict != nil {
			result.Failure = cp.Verdict.Failure
			result.Cancelled = cp.Verdict.Cancelled
			return result
		}
		if cp.Historical {
			result.Failure = "run deliberately closed without a recorded execution verdict"
			return result
		}
	}
	// Older conductor receipts contain only the discovery job. Its reference
	// and publication warnings do not turn successful inference into failure.
	// Other recorded control-plane failures are that run's failed verdict.
	for _, failure := range receipt.Body.Failures {
		if failure.Code == explore.FailureCancelled {
			result.Cancelled = true
		}
		if failure.Code != explore.FailureReference && failure.Code != explore.FailureSyncPublish && result.Failure == "" {
			result.Failure = failure.Message
		}
	}
	if result.Failure == "" && receipt.Body.Worker != nil && receipt.Body.Worker.Failure != nil {
		result.Failure = receipt.Body.Worker.Failure.Message
	}
	return result
}
