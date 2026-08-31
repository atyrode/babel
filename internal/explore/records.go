package explore

import (
	"fmt"

	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
)

// The writes below share one shape, and it is the shape resumption needs.
//
// Each consults the resume ledger for the reference the worker emitted the
// item under. A hit means a prior attempt at this run already wrote the
// record, so the durable identifier is reused and nothing is written: that is
// what makes a re-run after an interruption neither lose nor duplicate
// committed state (§6.5). A miss writes the record and then binds the
// reference to it, in that order, because a binding pointing at a record that
// does not exist would be worse than a second attempt discovering the record
// is missing and writing it.
//
// A reference already bound to a record of another kind is an error rather
// than an overwrite. Records are immutable revisions (§4.7); a reference that
// changed its mind about what it names is a corrupt ledger.

// reuse resolves a reference through the ledger, requiring the recorded kind.
func reuse(committed map[string]Commit, ref string, want frontier.EntityType) (string, bool, error) {
	prior, ok := committed[ref]
	if !ok {
		return "", false, nil
	}
	if prior.Type != want {
		return "", false, fmt.Errorf("%w: reference %q names a %s, not a %s",
			ErrLedgerConflict, ref, prior.Type, want)
	}
	return prior.ID, true, nil
}

func (c *Controller) putHypothesis(st *state, stage Stage, runID string, committed map[string]Commit, cand Candidate) (string, bool, error) {
	if id, reused, err := reuse(committed, cand.Ref, frontier.EntityHypothesis); reused || err != nil {
		return id, reused, err
	}
	record, err := c.cfg.Frontier.CreateHypothesis(st.commit, frontier.HypothesisInput{
		RunID:   runID,
		Payload: cand.Hypothesis,
		// The dedup check runs before the write and travels with it, so the
		// warning and the candidate land in one transaction: a candidate
		// stored without the warning computed for it would be a suspicion
		// that existed only in a log. The candidate is written whatever the
		// heuristic said — never dropped, never reworded (#87).
		NearDuplicates: c.nearDuplicates(st, cand.Hypothesis.Statement),
	})
	if err != nil {
		return "", false, err
	}
	st.out.Duplicates = append(st.out.Duplicates, record.Duplicates...)
	return record.ID, false, c.bind(st, stage, cand.Ref, frontier.EntityHypothesis, record.ID)
}

func (c *Controller) putObservation(st *state, stage Stage, runID string, committed map[string]Commit, hypothesisID string, obs Observation) (string, bool, error) {
	id, reused, err := reuse(committed, obs.Ref, frontier.EntityObservation)
	if err != nil {
		return "", false, err
	}
	if reused {
		// Re-emitted on the resume path too. The claim's locators are the
		// worker's own, so they are the same on the attempt that wrote the
		// record and on the one that recognized it, and an interruption
		// between the write and its edges is repaired rather than permanent.
		c.mintEvidence(st, stage, runID, frontier.EntityObservation, id, obs.Claim.Evidence)
		return id, true, nil
	}
	record, err := c.cfg.Frontier.CreateObservation(st.commit, frontier.ObservationInput{
		HypothesisID:  hypothesisID,
		RunID:         runID,
		RecipeID:      obs.Recipe.ID,
		RecipeVersion: obs.Recipe.Version,
		Payload:       obs.Claim,
	})
	if err != nil {
		return "", false, err
	}
	c.mintEvidence(st, stage, runID, frontier.EntityObservation, record.ID, obs.Claim.Evidence)
	return record.ID, false, c.bind(st, stage, obs.Ref, frontier.EntityObservation, record.ID)
}

// putObjection records one challenger criticism, as an observation when it
// carries locators and as a contradicting candidate when it does not.
//
// The branch is the challenger's authority made mechanical: §4.3 forbids an
// evidence-free observation, and §5.4 permits a challenger to emit a new
// hypothesis, so an ungrounded criticism becomes an idea to investigate rather
// than a claim that has been established by being asserted.
func (c *Controller) putObjection(st *state, stage Stage, runID string, committed map[string]Commit, target string, obj Objection) (string, frontier.EntityType, bool, error) {
	if prior, ok := committed[obj.Ref]; ok {
		// A criticism recognized from a prior attempt gets its evidence
		// edges re-emitted for the same reason putObservation's does; the
		// branch below chose the kind then, and the ledger remembers it.
		if prior.Type == frontier.EntityObservation {
			c.mintEvidence(st, stage, runID, prior.Type, prior.ID, obj.Claim.Evidence)
		}
		return prior.ID, prior.Type, true, nil
	}
	if len(obj.Claim.Evidence) > 0 {
		record, err := c.cfg.Frontier.CreateObservation(st.commit, frontier.ObservationInput{
			HypothesisID:  target,
			RunID:         runID,
			RecipeID:      obj.Recipe.ID,
			RecipeVersion: obj.Recipe.Version,
			Payload:       obj.Claim,
		})
		if err != nil {
			return "", "", false, err
		}
		c.mintEvidence(st, stage, runID, frontier.EntityObservation, record.ID, obj.Claim.Evidence)
		return record.ID, frontier.EntityObservation, false,
			c.bind(st, stage, obj.Ref, frontier.EntityObservation, record.ID)
	}

	record, err := c.cfg.Frontier.CreateHypothesis(st.commit, frontier.HypothesisInput{
		RunID: runID,
		Payload: frontier.HypothesisPayload{
			Statement:         obj.Claim.Claim,
			OriginCues:        []string{"challenger objection grounded in " + string(obj.Grounds)},
			ProvisionalLabels: []string{"objection"},
			Notes:             obj.Claim.Category,
		},
	})
	if err != nil {
		return "", "", false, err
	}
	if _, err := c.cfg.Frontier.Link(st.commit, frontier.LinkInput{
		FromID: record.ID,
		ToID:   target,
		Type:   frontier.LinkContradicts,
		Note:   "challenger objection resting on " + string(obj.Grounds),
	}); err != nil {
		return "", "", false, err
	}
	return record.ID, frontier.EntityHypothesis, false,
		c.bind(st, stage, obj.Ref, frontier.EntityHypothesis, record.ID)
}

func (c *Controller) putFinding(st *state, stage Stage, runID string, committed map[string]Commit, con Consolidation, observationIDs []string) (frontier.Finding, bool, error) {
	if id, reused, err := reuse(committed, con.Ref, frontier.EntityFinding); err != nil {
		return frontier.Finding{}, false, err
	} else if reused {
		record, err := c.cfg.Frontier.Finding(st.commit, id)
		return record, true, err
	}
	record, err := c.cfg.Frontier.CreateFinding(st.commit, frontier.FindingInput{
		RunID:          runID,
		ObservationIDs: observationIDs,
		Payload:        con.Finding,
	})
	if err != nil {
		return frontier.Finding{}, false, err
	}
	return record, false, c.bind(st, stage, con.Ref, frontier.EntityFinding, record.ID)
}

func (c *Controller) putProposal(st *state, stage Stage, runID string, committed map[string]Commit, con Consolidation, findingID string) (string, bool, error) {
	ref := con.Ref + "/proposal"
	if id, reused, err := reuse(committed, ref, frontier.EntityProposal); reused || err != nil {
		return id, reused, err
	}
	record, err := c.cfg.Frontier.CreateProposal(st.commit, frontier.ProposalInput{
		RunID:      runID,
		FindingIDs: []string{findingID},
		Payload:    *con.Proposal,
	})
	if err != nil {
		return "", false, err
	}
	return record.ID, false, c.bind(st, stage, ref, frontier.EntityProposal, record.ID)
}

// bind records the reference-to-record binding a resumed attempt reads.
func (c *Controller) bind(st *state, stage Stage, ref string, kind frontier.EntityType, id string) error {
	return c.cfg.Ledger.Record(st.commit, st.opt.RunID, stage, Commit{
		Ref:  ref,
		Type: kind,
		ID:   id,
		At:   c.now(),
	})
}

// putDispositions records the next actions a job proposed against one record.
//
// Idempotence is the disposition store's rather than the resume ledger's. The
// ledger binds a worker reference to a frontier record and its Commit names a
// frontier entity kind; widening that kind to cover a second table would make
// every resume read ambiguous about what it is resolving. The store keys a
// run's proposal by (run, ref) instead, which is the same guarantee held one
// table over: a replayed result finds its own prior proposal and adds nothing.
//
// Each action is recorded on its own. One refused action — an unknown kind, an
// unverifiable repository — is a recorded failure for that action and leaves
// the others, because a job that proposed four next steps and got one wrong
// proposed three good ones.
func (c *Controller) putDispositions(st *state, stage Stage, runID string, record frontier.Ref, actions []ProposedAction) {
	for _, action := range actions {
		payload := disposition.Payload{Summary: action.Summary, Rationale: action.Rationale}
		if action.Kind == disposition.KindDraftIssue {
			anchor, err := disposition.VerifyAnchor(action.Workspace)
			if err != nil {
				st.fail(stage, FailureDisposition, c.now(), fmt.Errorf(
					"explore: draft-issue disposition %q: %w", action.Ref, err))
				continue
			}
			payload.Anchor = &anchor
		}
		proposed, err := c.cfg.Dispositions.Propose(st.commit, disposition.ProposeInput{
			Record:     record,
			Kind:       action.Kind,
			ProposedBy: frontier.Run(runID),
			Ref:        action.Ref,
			Payload:    payload,
		})
		if err != nil {
			st.fail(stage, FailureDisposition, c.now(), fmt.Errorf(
				"explore: persist disposition %q: %w", action.Ref, err))
			continue
		}
		st.out.Dispositions = append(st.out.Dispositions, proposed.ID)
	}
}
