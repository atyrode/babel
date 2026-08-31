package review_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// harness is one throwaway deployment: a durable database in a temporary
// directory with all three components open on it, and the record builders the
// review tests need. Nothing here reads a configuration file, a real
// transcript, or the network; every fixture is written by the test that uses
// it.
type harness struct {
	t     *testing.T
	ctx   context.Context
	dir   string
	front *frontier.Store
	runs  *run.Store
	svc   *review.Service
	op    review.Authority
	agent review.Agent
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	front, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("frontier.Open: %v", err)
	}
	t.Cleanup(func() { front.Close() })
	runs, err := run.Open(dir)
	if err != nil {
		t.Fatalf("run.Open: %v", err)
	}
	t.Cleanup(func() { runs.Close() })
	svc, err := review.Open(dir, front, runs)
	if err != nil {
		t.Fatalf("review.Open: %v", err)
	}
	t.Cleanup(func() { svc.Close() })

	op, err := review.NewAuthority("operator-1")
	if err != nil {
		t.Fatalf("NewAuthority: %v", err)
	}
	agent, err := review.NewAgent("refinement-worker-1")
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	return &harness{t: t, ctx: context.Background(), dir: dir, front: front, runs: runs, svc: svc, op: op, agent: agent}
}

// locator builds a synthetic evidence locator. The digest is a real sha256 of
// a synthetic string, so a test that asserts a locator survived redaction is
// asserting about the shape a real locator has.
func locator(name string, line int) event.Locator {
	sum := sha256.Sum256([]byte(name + fmt.Sprint(line)))
	return event.Locator{
		Path:       "/synthetic/corpus/" + name + ".jsonl",
		Line:       line,
		ByteOffset: int64(line * 512),
		Digest:     hex.EncodeToString(sum[:]),
	}
}

func (h *harness) evidence(name string, line int, note string) frontier.Evidence {
	h.t.Helper()
	ev, err := frontier.NewEvidence(locator(name, line), note)
	if err != nil {
		h.t.Fatalf("frontier.NewEvidence: %v", err)
	}
	return ev
}

func (h *harness) hypothesis(statement string) frontier.Hypothesis {
	h.t.Helper()
	record, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
		RunID: "run-1",
		Payload: frontier.HypothesisPayload{
			Statement: statement,
			Novelty:   0.5,
			Priority:  0.5,
		},
	})
	if err != nil {
		h.t.Fatalf("CreateHypothesis: %v", err)
	}
	return record
}

func (h *harness) observation(hypothesisID, claim string, evidence ...frontier.Evidence) frontier.Observation {
	h.t.Helper()
	if len(evidence) == 0 {
		evidence = []frontier.Evidence{h.evidence("session-a", 12, "the build failed twice")}
	}
	record, err := h.front.CreateObservation(h.ctx, frontier.ObservationInput{
		HypothesisID:  hypothesisID,
		RunID:         "run-1",
		RecipeID:      "outcome-integrity",
		RecipeVersion: 1,
		Payload: frontier.ObservationPayload{
			Claim:                 claim,
			Category:              "outcome",
			Confidence:            frontier.ConfidenceModerate,
			Impact:                frontier.ImpactModerate,
			Evidence:              evidence,
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		h.t.Fatalf("CreateObservation: %v", err)
	}
	return record
}

func (h *harness) finding(observationIDs []string, title string) frontier.Finding {
	h.t.Helper()
	record, err := h.front.CreateFinding(h.ctx, frontier.FindingInput{
		RunID:          "run-1",
		ObservationIDs: observationIDs,
		Payload: frontier.FindingPayload{
			Title:                 title,
			Pattern:               "the same step is repeated after it reports success",
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		h.t.Fatalf("CreateFinding: %v", err)
	}
	return record
}

func (h *harness) proposal(findingIDs []string, ancestorID, title string) frontier.Proposal {
	h.t.Helper()
	// A descendant states why it supersedes its ancestor and an original has
	// nothing to supersede, so the reason follows the ancestor.
	reason := ""
	if ancestorID != "" {
		reason = "a refinement run reworked the rejected proposal"
	}
	record, err := h.front.CreateProposal(h.ctx, frontier.ProposalInput{
		RunID:      "run-1",
		AncestorID: ancestorID,
		Reason:     reason,
		FindingIDs: findingIDs,
		Payload: frontier.ProposalPayload{
			Title:          title,
			Problem:        "the verification step is skipped when the previous step reports success",
			Outcome:        "verify independently of the reported outcome",
			Impact:         frontier.ImpactModerate,
			Classification: frontier.ClassificationPrivate,
		},
	})
	if err != nil {
		h.t.Fatalf("CreateProposal: %v", err)
	}
	return record
}

// chain builds a whole §4.2 development path and returns its proposal, which
// is the canonical reviewable artifact.
func (h *harness) chain(title string) frontier.Proposal {
	h.t.Helper()
	hyp := h.hypothesis("verification may be reported rather than performed")
	obs := h.observation(hyp.ID, "the agent claimed the tests passed without running them")
	fnd := h.finding([]string{obs.ID}, "claimed verification")
	return h.proposal([]string{fnd.ID}, "", title)
}

// rejectAndRefine rejects a proposal and returns the authorized request.
func (h *harness) rejectAndRefine(subject frontier.Ref, guidance string) frontier.RefinementRequest {
	h.t.Helper()
	_, request, err := h.svc.RejectAndRefine(h.ctx, review.Decision{
		Subject: subject,
		By:      h.op,
		Note:    "the proposal overstates what the evidence shows",
	}, frontier.RefinementPayload{Guidance: guidance})
	if err != nil {
		h.t.Fatalf("RejectAndRefine: %v", err)
	}
	return request
}

func (h *harness) assessment(mode review.Mode, destination review.Destination) review.AssessmentPayload {
	h.t.Helper()
	payload := review.AssessmentPayload{
		Rationale:   "the same correction has been made in three unrelated runs",
		Scope:       "every run using the outcome-integrity lens",
		Sensitivity: frontier.ClassificationPrivate,
		Supporting:  []frontier.Evidence{h.evidence("session-b", 44, "the reviewer corrected the same claim")},
		Destination: destination,
	}
	return payload
}

func (h *harness) memory(title string) *review.MemoryInput {
	h.t.Helper()
	return &review.MemoryInput{
		Title:         title,
		Statement:     "treat a claimed verification as unverified until a command output is cited",
		Applicability: "the outcome-integrity lens, until the lens itself requires it",
		Supporting:    []frontier.Evidence{h.evidence("session-b", 44, "the reviewer corrected the same claim")},
	}
}

// receipt writes one minimal run receipt so the export tests have a real run
// record to read. The worker receipt is deliberately absent and a failure
// explains why, which is the shape internal/run requires of a run that never
// reached the worker.
func (h *harness) receipt(query, failure string) run.Receipt {
	h.t.Helper()
	prepared := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	prep, err := run.NewPreparation(prepared, []run.Selected{{
		Host:          "workstation",
		Harness:       "omp",
		SourceID:      "session-a",
		CaptureDigest: digest.Bytes([]byte("capture")),
		SourceDigest:  digest.Bytes([]byte("source")),
		Adapter:       run.AdapterRef{Schema: 1, Version: "1.0.0"},
	}})
	if err != nil {
		h.t.Fatalf("NewPreparation: %v", err)
	}
	origin, err := run.NewEvidence(locator("session-a", 12), "the candidate came from this record")
	if err != nil {
		h.t.Fatalf("run.NewEvidence: %v", err)
	}
	hit, err := run.NewEvidence(locator("session-c", 7), "the retrieval returned this record")
	if err != nil {
		h.t.Fatalf("run.NewEvidence: %v", err)
	}
	started := prepared.Add(time.Minute)
	body := run.Body{
		Cookbook: []run.CookbookAsset{{Kind: run.AssetLens, Ref: workerRecipe()}},
		Job:      run.JobVersions{Job: 1, Prompt: "p1", Schema: "s1"},
		Policy:   run.PolicyVersions{Redaction: "r1", Disclosure: "d1"},
		Timing:   run.Timing{StartedAt: started, FinishedAt: started.Add(time.Minute)},
		Retrieval: []run.RetrievalStep{{
			Index:   1,
			Tool:    "corpus-search",
			Query:   query,
			At:      started,
			Results: []run.RetrievalResult{{Rank: 1, Evidence: hit}},
		}},
		Deferred: []run.Candidate{{
			ID:     "cand-1",
			Reason: "the run's budget ended before this branch was explored",
			At:     started,
			Origin: []run.Evidence{origin},
		}},
		Failures: []run.Failure{{
			Stage:   "worker",
			Code:    "worker-unavailable",
			Message: failure,
			At:      started,
		}},
	}
	receipt, err := run.NewReceipt(run.NewReceiptID(), "run-1", prep,
		run.Authority{Kind: run.AuthorityOperator, Ref: "command:explore"}, body, started)
	if err != nil {
		h.t.Fatalf("NewReceipt: %v", err)
	}
	if err := h.runs.PutReceipt(h.ctx, receipt); err != nil {
		h.t.Fatalf("PutReceipt: %v", err)
	}
	stored, err := h.runs.Receipt(h.ctx, receipt.Header.ID)
	if err != nil {
		h.t.Fatalf("Receipt: %v", err)
	}
	return stored
}

// workerRecipe names the cookbook asset the fixture run applied.
func workerRecipe() worker.RecipeRef {
	return worker.RecipeRef{ID: "outcome-integrity", Version: 1}
}
