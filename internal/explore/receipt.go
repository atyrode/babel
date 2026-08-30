package explore

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// writeReceipt records one job's run receipt and stores it.
//
// A resumed attempt amends rather than replaces. §7 makes review decisions and
// prior revisions part of the answer, and internal/run enforces the chain: the
// amendment links back to the revision it extends, the prior receipt is left
// byte-identical, and a second amendment claiming the same predecessor loses
// rather than forking the history. That is also why the amendment reason is
// mandatory from revision 2 — a receipt that changed without saying why would
// be indistinguishable from one that was wrong the first time.
//
// The receipt is written on the detached context. It is the record of what
// happened, and a cancelled run is exactly when the record matters.
func (c *Controller) writeReceipt(st *state, runID string, workerReceipt *worker.Receipt,
	steps []run.RetrievalStep, failures []run.Failure, started time.Time) *run.Receipt {
	body := run.Body{
		Cookbook:     slices.Clone(c.assets),
		Frontier:     run.FrontierScope{Roots: slices.Clone(st.opt.Roots), Prior: slices.Clone(st.opt.Prior)},
		Capabilities: c.cfg.Capabilities,
		Job:          run.JobVersions{Job: JobVersion, Prompt: PromptVersion, Schema: worker.ResultSchema},
		Policy:       run.PolicyVersions{Redaction: RedactionPolicyVersion, Disclosure: DisclosurePolicyVersion},
		Worker:       workerReceipt,
		Retrieval:    steps,
		Failures:     failures,
		Timing:       run.Timing{StartedAt: started, FinishedAt: c.now()},
	}
	// The frontier checkpoint belongs to the exploration's own receipt: the
	// separate passes of §5.4 do not schedule the frontier, and recording
	// their deferral would claim they did.
	if runID == st.opt.RunID {
		body.Deferred = slices.Clone(st.deferredRecords)
		body.Rejected = slices.Clone(st.rejectedRecords)
	}
	if workerReceipt != nil {
		// Babel's own accounting, not the worker's: the number of tool
		// decisions this control plane made. CPU, memory and sandbox bytes
		// stay absent because Babel measured none of them, and a zero would
		// read as a measurement.
		calls := len(workerReceipt.ToolRequests)
		body.Resources = run.Resources{ToolCalls: &calls}
	}

	// A run with no receipt yet is the first attempt, which internal/run
	// reports as a missing record rather than an empty chain.
	prior, err := c.cfg.Runs.Revisions(st.commit, runID)
	if err != nil && !errors.Is(err, run.ErrNotFound) {
		st.fail(StageExplore, FailureStorage, c.now(),
			fmt.Errorf("explore: read the receipt chain for %s: %w", runID, err))
		return nil
	}
	var receipt run.Receipt
	if len(prior) == 0 {
		receipt, err = run.NewReceipt(run.NewReceiptID(), runID, c.cfg.Preparation, body, c.now())
	} else {
		body.AmendmentReason = "the run was resumed after an interruption"
		receipt, err = run.Amend(prior[len(prior)-1], run.NewReceiptID(), body, c.now())
	}
	if err != nil {
		st.fail(StageExplore, FailureStorage, c.now(),
			fmt.Errorf("explore: build the receipt for %s: %w", runID, err))
		return nil
	}
	if err := c.cfg.Runs.PutReceipt(st.commit, receipt); err != nil {
		st.fail(StageExplore, FailureStorage, c.now(),
			fmt.Errorf("explore: store the receipt for %s: %w", runID, err))
		return nil
	}
	return &receipt
}
